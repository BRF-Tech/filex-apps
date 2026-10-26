<script setup lang="ts">
// Writes the publisher's `.filex-apps.json` from a form and shows it live.
// The store reads the listing only from the repository at the reviewed
// commit (docs/ARCHITECTURE.md Q10); nothing typed here is sent anywhere.
import { computed, reactive, watch } from 'vue';
import type { Category } from '@/api/client';
import { S, fmt } from '@/strings';
import { text } from '@/lib/format';
import { MAX_CATEGORIES, MAX_SCREENSHOTS, consentJSON, emptyConsent, validateConsent } from '@/lib/consent';
import { FileJson } from 'lucide-vue-next';
import CopyButton from './CopyButton.vue';

const props = defineProps<{ publisher: string; categories: Category[] }>();

const form = reactive(emptyConsent(props.publisher));
watch(
  () => props.publisher,
  (p) => (form.publisher = p),
);

const json = computed(() => consentJSON(form));
const errors = computed(() => validateConsent(form));

function addShot() {
  if (form.screenshots.length < MAX_SCREENSHOTS) form.screenshots.push({ path: '', caption: '' });
}
function removeShot(i: number) {
  form.screenshots.splice(i, 1);
}
</script>

<template>
  <section class="fa-panel" aria-labelledby="consent-h" data-testid="consent-helper">
    <h2 id="consent-h" class="fa-panel__title"><FileJson aria-hidden="true" />{{ S.consent.title }}</h2>
    <p class="fa-note fa-lead">{{ S.consent.intro }}</p>
    <div class="fa-columns">
      <div class="fa-form">
        <div class="fe-field">
          <label class="fe-field__label" for="cs-pub">{{ S.consent.publisher }}</label>
          <input id="cs-pub" v-model="form.publisher" class="fe-input" spellcheck="false" data-testid="cs-publisher" />
        </div>
        <fieldset class="fa-fieldset">
          <legend>{{ S.consent.categories }}</legend>
          <div class="fa-checks">
            <label v-for="c in categories" :key="c.id" class="fe-checkbox">
              <input
                v-model="form.categories"
                type="checkbox"
                :value="c.id"
                :disabled="!form.categories.includes(c.id) && form.categories.length >= MAX_CATEGORIES"
              />
              <span>{{ text(c.label) }}</span>
            </label>
          </div>
        </fieldset>
        <div class="fe-field">
          <label class="fe-field__label" for="cs-summary">{{ S.consent.summary }}</label>
          <input id="cs-summary" v-model="form.summary" class="fe-input" maxlength="300" data-testid="cs-summary" />
        </div>
        <div class="fe-field">
          <label class="fe-field__label" for="cs-desc">{{ S.consent.description }}</label>
          <input id="cs-desc" v-model="form.description" class="fe-input" placeholder="docs/STORE.md" spellcheck="false" data-testid="cs-description" />
        </div>
        <div class="fe-field">
          <label class="fe-field__label" for="cs-icon">{{ S.consent.icon }}</label>
          <input
            id="cs-icon"
            v-model="form.icon"
            class="fe-input"
            placeholder="assets/icon.png"
            spellcheck="false"
            aria-describedby="cs-icon-h"
            data-testid="cs-icon"
          />
          <p id="cs-icon-h" class="fe-form__hint">{{ S.consent.iconHint }}</p>
        </div>
        <fieldset class="fa-fieldset">
          <legend>{{ S.consent.screenshots }}</legend>
          <p class="fe-form__hint" data-testid="cs-shots-hint">{{ S.consent.screenshotsHint }}</p>
          <div v-for="(s, i) in form.screenshots" :key="i" class="fa-shotrow">
            <div class="fe-field">
              <label class="fe-field__label" :for="`cs-shot-${i}`">{{ fmt(S.consent.screenshotPath, { n: i + 1 }) }}</label>
              <input :id="`cs-shot-${i}`" v-model="s.path" class="fe-input" spellcheck="false" />
            </div>
            <div class="fe-field">
              <label class="fe-field__label" :for="`cs-cap-${i}`">{{ fmt(S.consent.screenshotCaption, { n: i + 1 }) }}</label>
              <input :id="`cs-cap-${i}`" v-model="s.caption" class="fe-input" maxlength="300" />
            </div>
            <button type="button" class="fe-btn fe-btn--sm" :aria-label="fmt(S.consent.removeScreenshot, { n: i + 1 })" @click="removeShot(i)">×</button>
          </div>
          <div>
            <button type="button" class="fe-btn fe-btn--sm" :disabled="form.screenshots.length >= MAX_SCREENSHOTS" @click="addShot">
              {{ S.consent.addScreenshot }}
            </button>
          </div>
        </fieldset>
      </div>
      <div class="fa-section">
        <div class="fa-section__head">
          <h3>{{ S.consent.preview }}</h3>
          <CopyButton :value="json" :label="`${S.common.copy}: .filex-apps.json`" />
        </div>
        <pre class="fa-pre" data-testid="cs-json" tabindex="0" aria-label=".filex-apps.json">{{ json }}</pre>
        <p class="fa-note">{{ S.consent.previewHint }}</p>
        <p class="fa-note" data-testid="consent-conversions-hint">{{ S.consent.conversionsHint }}</p>
        <div aria-live="polite">
          <ul v-if="errors.length" class="fe-form__error" data-testid="cs-errors">
            <li v-for="e in errors" :key="e">{{ e }}</li>
          </ul>
          <p v-else class="fa-note" data-testid="cs-valid">{{ S.consent.valid }}</p>
        </div>
      </div>
    </div>
  </section>
</template>
