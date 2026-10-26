<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue';
import { ApiError, requestPublisher, type Publisher } from '@/api/client';
import { S, fmt } from '@/strings';
import { PUBLISHER_ID } from '@/lib/consent';
import { loadSession, session } from '@/lib/session';
import { CircleCheck, Info, UserPlus } from 'lucide-vue-next';
import AccessGate from '@/components/AccessGate.vue';
import Stepper from '@/components/Stepper.vue';

const form = reactive({ id: '', name: '', github_owner: session.me?.identity.login ?? '', homepage: '' });
const touched = ref(false);
const busy = ref(false);
const error = ref('');
const done = ref<Publisher | null>(null);

const errors = computed(() => ({
  id: PUBLISHER_ID.test(form.id.trim()) ? '' : S.newPublisher.idInvalid,
  name: form.name.trim().length >= 1 && form.name.trim().length <= 80 ? '' : S.newPublisher.nameInvalid,
  github_owner: /^[A-Za-z0-9-]{1,39}$/.test(form.github_owner.trim()) ? '' : S.newPublisher.ownerInvalid,
  homepage: !form.homepage.trim() || /^https:\/\/\S+$/.test(form.homepage.trim()) ? '' : S.newPublisher.homepageInvalid,
}));
const valid = computed(() => Object.values(errors.value).every((e) => !e));

async function submit() {
  touched.value = true;
  error.value = '';
  if (!valid.value) return;
  busy.value = true;
  try {
    done.value = await requestPublisher({
      id: form.id.trim(),
      name: form.name.trim(),
      github_owner: form.github_owner.trim(),
      ...(form.homepage.trim() ? { homepage: form.homepage.trim() } : {}),
    });
    await loadSession(true);
  } catch (e) {
    error.value = e instanceof ApiError ? `${e.message} (${e.code})` : String(e);
  } finally {
    busy.value = false;
  }
}
const steps = S.publish.stepTitles.map((title, i) => ({ title, text: S.publish.steps[i] }));
const show = (k: keyof typeof errors.value) => touched.value && errors.value[k];
// A deep link can open this page before the session has answered.
watch(
  () => session.me?.identity.login,
  (login) => {
    if (login && !form.github_owner) form.github_owner = login;
  },
);
</script>

<template>
  <div class="fa-page">
    <div class="fa-pagehead">
      <span class="fa-pagehead__icon" aria-hidden="true"><UserPlus /></span>
      <div class="fa-pagehead__text">
        <h1>{{ S.newPublisher.title }}</h1>
        <p class="fa-pagehead__lead">{{ S.newPublisher.intro }}</p>
      </div>
    </div>
    <Stepper :steps="steps" :current="0" :label="S.publish.stepsLabel" />
    <AccessGate need="github">
      <div v-if="done" class="fa-callout fa-callout--ok fa-callout--row" role="status" data-testid="publisher-requested">
        <CircleCheck class="fa-callout__icon" aria-hidden="true" />
        <div class="fa-callout__body">
          <p>{{ fmt(S.newPublisher.sent, { name: done.name }) }}</p>
          <RouterLink to="/publish">{{ S.newPublisher.toPublish }}</RouterLink>
        </div>
      </div>
      <div v-else class="fa-columns">
      <form class="fa-form fa-form--wide fa-panel" novalidate data-testid="publisher-form" @submit.prevent="submit">
        <div class="fe-field">
          <label class="fe-field__label" for="np-id">{{ S.newPublisher.id }}</label>
          <input
            id="np-id"
            v-model="form.id"
            class="fe-input"
            required
            maxlength="40"
            autocomplete="off"
            spellcheck="false"
            :aria-describedby="show('id') ? 'np-id-h np-id-e' : 'np-id-h'"
            :aria-invalid="show('id') ? 'true' : undefined"
            data-testid="np-id"
          />
          <p id="np-id-h" class="fe-form__hint">{{ S.newPublisher.idHint }}</p>
          <p v-if="show('id')" id="np-id-e" class="fe-form__error">{{ errors.id }}</p>
        </div>
        <div class="fe-field">
          <label class="fe-field__label" for="np-name">{{ S.newPublisher.name }}</label>
          <input
            id="np-name"
            v-model="form.name"
            class="fe-input"
            required
            maxlength="80"
            :aria-describedby="show('name') ? 'np-name-h np-name-e' : 'np-name-h'"
            :aria-invalid="show('name') ? 'true' : undefined"
            data-testid="np-name"
          />
          <p id="np-name-h" class="fe-form__hint">{{ S.newPublisher.nameHint }}</p>
          <p v-if="show('name')" id="np-name-e" class="fe-form__error">{{ errors.name }}</p>
        </div>
        <div class="fe-field">
          <label class="fe-field__label" for="np-owner">{{ S.newPublisher.owner }}</label>
          <input
            id="np-owner"
            v-model="form.github_owner"
            class="fe-input"
            required
            maxlength="39"
            autocomplete="off"
            spellcheck="false"
            :aria-describedby="show('github_owner') ? 'np-owner-h np-owner-e' : 'np-owner-h'"
            :aria-invalid="show('github_owner') ? 'true' : undefined"
            data-testid="np-owner"
          />
          <p id="np-owner-h" class="fe-form__hint">{{ S.newPublisher.ownerHint }}</p>
          <p v-if="show('github_owner')" id="np-owner-e" class="fe-form__error">{{ errors.github_owner }}</p>
        </div>
        <div class="fe-field">
          <label class="fe-field__label" for="np-home">{{ S.newPublisher.homepage }}</label>
          <input
            id="np-home"
            v-model="form.homepage"
            class="fe-input"
            type="url"
            inputmode="url"
            :aria-describedby="show('homepage') ? 'np-home-e' : undefined"
            :aria-invalid="show('homepage') ? 'true' : undefined"
            data-testid="np-homepage"
          />
          <p v-if="show('homepage')" id="np-home-e" class="fe-form__error">{{ errors.homepage }}</p>
        </div>
        <p v-if="error" class="fe-form__error" role="alert" data-testid="np-error">{{ error }}</p>
        <div class="fa-actions">
          <button type="submit" class="fe-btn fe-btn--primary fe-btn--lg" :disabled="busy" data-testid="np-send">{{ S.newPublisher.send }}</button>
          <RouterLink to="/publish">{{ S.common.cancel }}</RouterLink>
        </div>
      </form>
      <section class="fa-panel fa-panel--soft" aria-labelledby="np-aside-h">
        <h2 id="np-aside-h" class="fa-panel__title"><Info aria-hidden="true" />{{ S.newPublisher.asideTitle }}</h2>
        <ul class="fa-facts">
          <li v-for="f in S.newPublisher.asideFacts" :key="f"><CircleCheck aria-hidden="true" />{{ f }}</li>
        </ul>
      </section>
      </div>
    </AccessGate>
  </div>
</template>
