<script setup lang="ts">
// Install on my filex: the person picks one of their saved filex instances
// or types an address (and may save it), adds a license key for a paid app,
// and gets a one-time magic link to open on that filex. The store never
// talks to the filex: the link carries the token in its fragment, and that
// filex's server asks the store for the intent (api/openapi.yaml
// "install"). ActionDialog is the dialog; this fills it.
import { computed, ref, watch } from 'vue';
import { ExternalLink, TriangleAlert } from 'lucide-vue-next';
import { ApiError, createInstallIntent, listInstances, type AppDetail, type InstallIntentCreated, type SavedInstance } from '@/api/client';
import { S, fmt } from '@/strings';
import { text, when } from '@/lib/format';
import { looksLikeFilexUrl, openableLink } from '@/lib/install';
import ActionDialog from './ActionDialog.vue';
import CopyField from './CopyField.vue';
import FaSelect from './FaSelect.vue';

const props = defineProps<{ open: boolean; app: AppDetail; version: string }>();
const emit = defineEmits<{ (e: 'close'): void }>();

const instances = ref<SavedInstance[]>([]);
const mode = ref<'saved' | 'new'>('new');
const instanceId = ref('');
const url = ref('');
const save = ref(true);
const label = ref('');
const licenseKey = ref('');
const created = ref<InstallIntentCreated | null>(null);
const busy = ref(false);
const error = ref('');
const tried = ref(false);

const paid = computed(() => props.app.pricing?.model === 'paid');
const appLabel = computed(() => text(props.app.label) || props.app.name);
const link = computed(() => openableLink(created.value?.magic_link));
const urlBad = computed(() => mode.value === 'new' && !looksLikeFilexUrl(url.value));

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      // Nothing of the link or the key stays on the page once it closes.
      created.value = null;
      licenseKey.value = '';
      return;
    }
    created.value = null;
    error.value = '';
    tried.value = false;
    url.value = '';
    label.value = '';
    licenseKey.value = '';
    save.value = true;
    try {
      instances.value = (await listInstances()).items;
    } catch {
      instances.value = [];
    }
    mode.value = instances.value.length ? 'saved' : 'new';
    instanceId.value = instances.value[0]?.id ?? '';
  },
  { immediate: true },
);

async function confirm() {
  if (created.value) {
    emit('close');
    return;
  }
  tried.value = true;
  error.value = '';
  if (mode.value === 'new' && urlBad.value) return;
  if (mode.value === 'saved' && !instanceId.value) return;
  busy.value = true;
  try {
    created.value = await createInstallIntent({
      app: props.app.name,
      version: props.version || undefined,
      ...(mode.value === 'saved'
        ? { instance_id: instanceId.value, save_instance: false }
        : { filex_url: url.value.trim(), save_instance: save.value, instance_label: save.value ? label.value.trim() || undefined : undefined }),
      license_key: licenseKey.value.trim() || undefined,
    });
    licenseKey.value = '';
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <ActionDialog
    :open="open"
    :title="fmt(S.install.title, { app: appLabel })"
    :intro="created ? '' : S.install.intro"
    :confirm-label="created ? S.install.done : S.install.create"
    :busy="busy"
    :error="error"
    @confirm="confirm"
    @cancel="emit('close')"
  >
    <div v-if="created" class="fa-stack" data-testid="install-link">
      <p class="fa-callout__title">{{ S.install.linkTitle }}</p>
      <CopyField boxed :value="created.magic_link" :label="`${S.common.copy}: ${S.install.linkLabel}`" testid="magic-link" />
      <p class="fa-callout fa-callout--warn fa-callout--row" role="note">
        <TriangleAlert class="fa-callout__icon" aria-hidden="true" />
        <span class="fa-callout__body">{{ fmt(S.install.linkNote, { time: when(created.expires_at) }) }}</span>
      </p>
      <a v-if="link" :href="link" class="fe-btn fe-btn--primary" target="_blank" rel="noopener noreferrer" data-testid="install-link-open">
        <ExternalLink class="fa-icon" aria-hidden="true" />{{ S.install.openLink }}
      </a>
    </div>

    <template v-else>
      <p class="fa-note">{{ fmt(S.install.version, { version: version || app.latest }) }}</p>
      <p class="fa-note" data-testid="install-requires">{{ S.install.requires }}</p>
      <fieldset v-if="instances.length" class="fa-fieldset">
        <legend>{{ S.install.where }}</legend>
        <div class="fa-checks">
          <label class="fe-checkbox">
            <input v-model="mode" type="radio" name="fa-install-where" value="saved" data-testid="install-mode-saved" />
            <span>{{ S.install.saved }}</span>
          </label>
          <label class="fe-checkbox">
            <input v-model="mode" type="radio" name="fa-install-where" value="new" data-testid="install-mode-new" />
            <span>{{ S.install.other }}</span>
          </label>
        </div>
      </fieldset>

      <div v-if="mode === 'saved' && instances.length" class="fe-field">
        <label class="fe-field__label" for="fa-install-instance">{{ S.install.instance }}</label>
        <FaSelect
          id="fa-install-instance"
          v-model="instanceId"
          :options="instances.map((i) => ({ value: i.id, label: i.label, hint: i.url }))"
          testid="install-instance"
        />
      </div>

      <template v-else>
        <div class="fe-field">
          <label class="fe-field__label" for="fa-install-url">{{ S.install.url }}</label>
          <input
            id="fa-install-url"
            v-model="url"
            class="fe-input"
            type="url"
            inputmode="url"
            autocomplete="url"
            spellcheck="false"
            maxlength="300"
            required
            placeholder="https://files.example.com"
            aria-describedby="fa-install-url-h"
            :aria-invalid="tried && urlBad ? 'true' : undefined"
            data-testid="install-url"
          />
          <p id="fa-install-url-h" class="fe-form__hint">{{ S.install.urlHint }}</p>
          <p v-if="tried && urlBad" class="fe-form__error" role="alert">{{ S.install.urlInvalid }}</p>
        </div>
        <label class="fe-checkbox">
          <input v-model="save" type="checkbox" data-testid="install-save" />
          <span>{{ S.install.save }}</span>
        </label>
        <div v-if="save" class="fe-field">
          <label class="fe-field__label" for="fa-install-label">{{ S.install.label }}</label>
          <input id="fa-install-label" v-model="label" class="fe-input" maxlength="80" aria-describedby="fa-install-label-h" data-testid="install-label" />
          <p id="fa-install-label-h" class="fe-form__hint">{{ S.install.labelHint }}</p>
        </div>
      </template>

      <div v-if="paid" class="fe-field">
        <label class="fe-field__label" for="fa-install-key">{{ S.install.licenseKeyOptional }}</label>
        <input
          id="fa-install-key"
          v-model="licenseKey"
          class="fe-input fa-code"
          autocomplete="off"
          spellcheck="false"
          maxlength="64"
          placeholder="FXL-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XX"
          aria-describedby="fa-install-key-h"
          data-testid="install-license"
        />
        <p id="fa-install-key-h" class="fe-form__hint">{{ S.install.licenseKeyHint }}</p>
      </div>
    </template>
  </ActionDialog>
</template>
