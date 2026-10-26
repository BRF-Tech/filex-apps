<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { ApiError, createSubmission, listCategories, type Category, type SubmissionKind } from '@/api/client';
import { S, fmt } from '@/strings';
import { normalizeRepo } from '@/lib/format';
import { explainCode, knownCode } from '@/lib/checkCodes';
import { activePublishers, session } from '@/lib/session';
import { CircleAlert, ListChecks, Send, TriangleAlert } from 'lucide-vue-next';
import AccessGate from '@/components/AccessGate.vue';
import FaSegmented from '@/components/FaSegmented.vue';
import FaSelect from '@/components/FaSelect.vue';
import Stepper from '@/components/Stepper.vue';
import ConsentHelper from '@/components/ConsentHelper.vue';

const route = useRoute();
const router = useRouter();
const publishers = computed(() => activePublishers(session.me));

const form = reactive({ publisher: '', kind: 'app' as SubmissionKind, repo: '', ref: '', note: '' });
// What is submitted: an app (or a language pack), or a storage plugin, whose
// feed and builds the store reads from the tag's release.
const kindOptions = computed(() => [
  { value: 'app' as SubmissionKind, label: S.submit.kindApp },
  { value: 'storage' as SubmissionKind, label: S.submit.kindStorage },
]);
const checkLabel = (k: string) => (form.kind === 'storage' ? S.checkKindStorage[k] : undefined) ?? S.checkKind[k as keyof typeof S.checkKind];
watch(
  publishers,
  (ps) => {
    const wanted = typeof route.query.publisher === 'string' ? route.query.publisher : '';
    if (!form.publisher || !ps.some((p) => p.id === form.publisher)) {
      form.publisher = ps.find((p) => p.id === wanted)?.id ?? ps[0]?.id ?? '';
    }
  },
  { immediate: true },
);

const steps = S.publish.stepTitles.map((title, i) => ({ title, text: S.publish.steps[i] }));
// The pipeline's checks, in the order they run (what the submission page then shows).
const CHECKS = ['repo_access', 'tag', 'manifest', 'consent', 'policy', 'artifacts', 'media', 'dry_run'] as const;
const touched = ref(false);
const busy = ref(false);
const error = ref('');
const openId = ref<number | null>(null);
const categories = ref<Category[]>([]);
void listCategories()
  .then((r) => (categories.value = r.items))
  .catch(() => undefined);

const errors = computed(() => ({
  repo: normalizeRepo(form.repo) ? '' : S.submit.repoInvalid,
  ref: form.ref.trim() && form.ref.trim().length <= 255 ? '' : S.submit.refInvalid,
}));
const valid = computed(() => !!form.publisher && !errors.value.repo && !errors.value.ref);
const show = (k: 'repo' | 'ref') => touched.value && errors.value[k];

async function submit() {
  touched.value = true;
  error.value = '';
  openId.value = null;
  if (!valid.value) return;
  busy.value = true;
  try {
    const sub = await createSubmission({
      publisher: form.publisher,
      repo: normalizeRepo(form.repo),
      ref: form.ref.trim(),
      ...(form.kind === 'storage' ? { kind: form.kind } : {}),
      ...(form.note.trim() ? { note: form.note.trim() } : {}),
    });
    void router.push({ name: 'submission', params: { id: sub.id } });
  } catch (e) {
    if (e instanceof ApiError && e.code === 'submission_open') {
      error.value = S.submit.open;
      const s = e.body.submission as { id?: unknown } | undefined;
      const id = Number(s?.id ?? e.body.id);
      openId.value = Number.isFinite(id) && id > 0 ? id : null;
    } else if (e instanceof ApiError && e.status === 429) {
      error.value = fmt(S.submit.rateLimited, { seconds: e.retryAfter ?? 60 });
    } else if (e instanceof ApiError && knownCode(e.code)) {
      error.value = `${explainCode(e.code)} (${e.code})`;
    } else {
      error.value = e instanceof ApiError ? `${e.message} (${e.code})` : String(e);
    }
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="fa-page">
    <div class="fa-pagehead">
      <span class="fa-pagehead__icon" aria-hidden="true"><Send /></span>
      <div class="fa-pagehead__text">
        <h1>{{ S.submit.title }}</h1>
        <p class="fa-pagehead__lead">{{ S.submit.intro }}</p>
      </div>
    </div>
    <Stepper :steps="steps" :current="2" :done="publishers.length ? [0] : []" :label="S.publish.stepsLabel" />
    <AccessGate need="github">
      <div v-if="!publishers.length" class="fa-callout fa-callout--warn fa-callout--row" data-testid="submit-no-publisher">
        <TriangleAlert class="fa-callout__icon" aria-hidden="true" />
        <div class="fa-callout__body">
          <p>{{ S.submit.noPublisher }}</p>
          <RouterLink to="/publish/new-publisher">{{ S.publish.askPublisher }}</RouterLink>
        </div>
      </div>
      <div v-else class="fa-columns">
      <form class="fa-form fa-form--wide fa-panel" novalidate data-testid="submit-form" @submit.prevent="submit">
        <div class="fe-field">
          <label class="fe-field__label" for="sb-pub">{{ S.submit.publisher }}</label>
          <FaSelect
            id="sb-pub"
            v-model="form.publisher"
            :options="publishers.map((p) => ({ value: p.id, label: p.name, hint: p.id }))"
            required
            :required-message="S.common.required"
            testid="sb-publisher"
          />
        </div>
        <div class="fe-field">
          <span id="sb-kind-l" class="fe-field__label">{{ S.submit.kind }}</span>
          <FaSegmented v-model="form.kind" :options="kindOptions" aria-labelledby="sb-kind-l" testid="sb-kind" />
          <p class="fe-form__hint" data-testid="sb-kind-hint">{{ form.kind === 'storage' ? S.submit.kindHintStorage : S.submit.kindHintApp }}</p>
        </div>
        <div class="fe-field">
          <label class="fe-field__label" for="sb-repo">{{ S.submit.repo }}</label>
          <input
            id="sb-repo"
            v-model="form.repo"
            class="fe-input"
            required
            maxlength="300"
            autocomplete="off"
            spellcheck="false"
            placeholder="owner/name"
            :aria-describedby="show('repo') ? 'sb-repo-h sb-repo-e' : 'sb-repo-h'"
            :aria-invalid="show('repo') ? 'true' : undefined"
            data-testid="sb-repo"
          />
          <p id="sb-repo-h" class="fe-form__hint">{{ S.submit.repoHint }}</p>
          <p v-if="show('repo')" id="sb-repo-e" class="fe-form__error">{{ errors.repo }}</p>
        </div>
        <div class="fe-field">
          <label class="fe-field__label" for="sb-ref">{{ S.submit.ref }}</label>
          <input
            id="sb-ref"
            v-model="form.ref"
            class="fe-input"
            required
            maxlength="255"
            autocomplete="off"
            spellcheck="false"
            placeholder="v1.0.0"
            :aria-describedby="show('ref') ? 'sb-ref-h sb-ref-e' : 'sb-ref-h'"
            :aria-invalid="show('ref') ? 'true' : undefined"
            data-testid="sb-ref"
          />
          <p id="sb-ref-h" class="fe-form__hint">{{ S.submit.refHint }}</p>
          <p v-if="show('ref')" id="sb-ref-e" class="fe-form__error">{{ errors.ref }}</p>
        </div>
        <div class="fe-field">
          <label class="fe-field__label" for="sb-note">{{ S.submit.note }}</label>
          <textarea id="sb-note" v-model="form.note" class="fe-input" rows="3" maxlength="4000" data-testid="sb-note"></textarea>
        </div>
        <div v-if="error" class="fa-callout fa-callout--danger fa-callout--row" role="alert" data-testid="sb-error">
          <CircleAlert class="fa-callout__icon" aria-hidden="true" />
          <div class="fa-callout__body">
            <p>{{ error }}</p>
            <RouterLink v-if="openId" :to="{ name: 'submission', params: { id: openId } }">{{ fmt(S.submit.openLink, { id: openId }) }}</RouterLink>
          </div>
        </div>
        <div class="fa-actions">
          <button type="submit" class="fe-btn fe-btn--primary fe-btn--lg" :disabled="busy" data-testid="sb-send">
            <Send class="fa-icon" aria-hidden="true" />{{ busy ? S.submit.sending : S.submit.send }}
          </button>
        </div>
      </form>
      <section class="fa-panel fa-panel--soft" aria-labelledby="sb-checks-h">
        <h2 id="sb-checks-h" class="fa-panel__title"><ListChecks aria-hidden="true" />{{ S.submit.checksTitle }}</h2>
        <p class="fa-note">{{ S.submit.checksLead }}</p>
        <ol class="fa-steps">
          <li v-for="k in CHECKS" :key="k">{{ checkLabel(k) }}</li>
          <li>{{ S.submit.reviewStep }}</li>
        </ol>
      </section>
      </div>
      <ConsentHelper :publisher="form.publisher" :categories="categories" />
    </AccessGate>
  </div>
</template>
