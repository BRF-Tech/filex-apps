<script setup lang="ts">
// The filex instances a person installs apps on. The store keeps them
// encrypted and shows them to their owner only; the Install dialog offers
// them. The store never connects to these addresses.
//
// Instance connections: a row's "Connect a filex server" makes a
// one-time code its filex administrator pastes into filex; that filex then
// asks for install links in its own name, signing its requests with a key
// only it holds. "Disconnect" takes the key away.
import { computed, ref, watch } from 'vue';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { Server } from 'lucide-vue-next';
import {
  ApiError,
  createConnectCode,
  deleteInstance,
  disconnectInstance,
  listInstances,
  saveInstance,
  type ConnectCode,
  type SavedInstance,
} from '@/api/client';
import { S, fmt } from '@/strings';
import { emptyUnlessError, when } from '@/lib/format';
import { looksLikeFilexUrl } from '@/lib/install';
import { session } from '@/lib/session';
import AccessGate from '@/components/AccessGate.vue';
import ActionDialog from '@/components/ActionDialog.vue';
import CopyField from '@/components/CopyField.vue';

const items = ref<SavedInstance[]>([]);
const loading = ref(false);
const error = ref('');
const done = ref('');

async function load() {
  if (!session.me) return;
  loading.value = true;
  error.value = '';
  try {
    items.value = (await listInstances()).items;
  } catch (e) {
    error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    loading.value = false;
  }
}
watch(() => session.me, () => void load(), { immediate: true });

const url = ref('');
const label = ref('');
const tried = ref(false);
const busy = ref(false);
const addError = ref('');
const urlBad = computed(() => !looksLikeFilexUrl(url.value));
async function add() {
  tried.value = true;
  addError.value = '';
  done.value = '';
  if (urlBad.value) return;
  busy.value = true;
  try {
    const before = items.value.length;
    await saveInstance({ url: url.value.trim(), label: label.value.trim() || undefined });
    await load();
    done.value = items.value.length > before ? S.instances.added : S.instances.already;
    url.value = '';
    label.value = '';
    tried.value = false;
  } catch (e) {
    addError.value = e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

const columns = computed<DataColumn<SavedInstance>[]>(() => [
  { id: 'label', label: S.instances.colLabel, width: 220, lead: true },
  { id: 'url', label: S.instances.colUrl, width: 320 },
  { id: 'saved', label: S.instances.colSaved, width: 160, format: (r) => when(r.created_at, false) },
  { id: 'server', label: S.instances.colServer, width: 200, format: (r) => serverText(r) },
]);
function serverText(r: SavedInstance): string {
  if (!r.connected) return S.instances.notConnected;
  return r.connected_at ? fmt(S.instances.connectedSince, { when: when(r.connected_at, false) }) : S.instances.connected;
}
const actions = (r: SavedInstance) => [
  { key: 'connect', label: r.connected ? S.instances.reconnect : S.instances.connect },
  ...(r.connected ? [{ key: 'disconnect', label: S.instances.disconnect, danger: true }] : []),
  { key: 'forget', label: S.instances.forget, danger: true },
];
function onAction(key: string, row: SavedInstance) {
  if (key === 'forget') {
    forgetError.value = '';
    forgetting.value = row;
  } else if (key === 'connect') {
    void startConnect(row);
  } else if (key === 'disconnect') {
    disconnectError.value = '';
    disconnecting.value = row;
  }
}

// A connection code: made when the row asks, shown once in the dialog.
const connecting = ref<SavedInstance | null>(null);
const code = ref<ConnectCode | null>(null);
const codeBusy = ref(false);
const codeError = ref('');
async function startConnect(row: SavedInstance) {
  connecting.value = row;
  code.value = null;
  codeError.value = '';
  codeBusy.value = true;
  try {
    code.value = await createConnectCode(row.id);
  } catch (e) {
    codeError.value = e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
  } finally {
    codeBusy.value = false;
  }
}
function endConnect() {
  connecting.value = null;
  code.value = null;
}

const disconnecting = ref<SavedInstance | null>(null);
const disconnectBusy = ref(false);
const disconnectError = ref('');
async function doDisconnect() {
  if (!disconnecting.value) return;
  disconnectBusy.value = true;
  disconnectError.value = '';
  try {
    await disconnectInstance(disconnecting.value.id);
    disconnecting.value = null;
    await load();
  } catch (e) {
    disconnectError.value = e instanceof Error ? e.message : String(e);
  } finally {
    disconnectBusy.value = false;
  }
}

const forgetting = ref<SavedInstance | null>(null);
const forgetBusy = ref(false);
const forgetError = ref('');
async function doForget() {
  if (!forgetting.value) return;
  forgetBusy.value = true;
  forgetError.value = '';
  try {
    await deleteInstance(forgetting.value.id);
    forgetting.value = null;
    await load();
  } catch (e) {
    forgetError.value = e instanceof Error ? e.message : String(e);
  } finally {
    forgetBusy.value = false;
  }
}
const asInst = (r: unknown) => r as SavedInstance;
</script>

<template>
  <div class="fa-page">
    <div class="fa-pagehead">
      <span class="fa-pagehead__icon" aria-hidden="true"><Server /></span>
      <div class="fa-pagehead__text">
        <h1>{{ S.instances.title }}</h1>
        <p class="fa-pagehead__lead">{{ S.instances.intro }}</p>
      </div>
    </div>
    <AccessGate need="github">
      <section class="fa-panel" aria-labelledby="inst-add-h">
        <h2 id="inst-add-h" class="fa-panel__title">{{ S.instances.add }}</h2>
        <form class="fa-form fe-form" novalidate data-testid="instance-form" @submit.prevent="add">
          <div class="fe-field">
            <label class="fe-field__label" for="fa-inst-url">{{ S.instances.url }}</label>
            <input
              id="fa-inst-url"
              v-model="url"
              class="fe-input"
              type="url"
              inputmode="url"
              maxlength="300"
              spellcheck="false"
              required
              placeholder="https://files.example.com"
              aria-describedby="fa-inst-url-h"
              :aria-invalid="tried && urlBad ? 'true' : undefined"
              data-testid="instance-url"
            />
            <p id="fa-inst-url-h" class="fe-form__hint">{{ S.install.urlHint }}</p>
            <p v-if="tried && urlBad" class="fe-form__error" role="alert">{{ S.install.urlInvalid }}</p>
          </div>
          <div class="fe-field">
            <label class="fe-field__label" for="fa-inst-label">{{ S.instances.label }}</label>
            <input id="fa-inst-label" v-model="label" class="fe-input" maxlength="80" aria-describedby="fa-inst-label-h" data-testid="instance-label" />
            <p id="fa-inst-label-h" class="fe-form__hint">{{ S.install.labelHint }}</p>
          </div>
          <div class="fa-actions">
            <button type="submit" class="fe-btn fe-btn--primary" :disabled="busy" data-testid="instance-save">{{ S.instances.add }}</button>
          </div>
          <p v-if="done" class="fa-note" role="status" data-testid="instance-done">{{ done }}</p>
          <p v-if="addError" class="fe-form__error" role="alert">{{ addError }}</p>
        </form>
      </section>

      <section class="fa-section" aria-labelledby="inst-h">
        <h2 id="inst-h">{{ S.instances.title }}</h2>
        <p v-if="error" class="fa-callout fa-callout--danger" role="alert">{{ error }}</p>
        <DataTable
          table-id="store.me.instances"
          :columns="columns"
          :rows="items"
          :loading="loading"
          row-key="id"
          :empty="emptyUnlessError(S.instances.empty, error)"
          :aria-label="S.instances.title"
          :row-actions="actions"
          data-testid="instances"
          :row-actions-test-id="(r: SavedInstance) => `instance-actions-${r.id}`"
          @row-action="onAction"
        >
          <template #cell-url="{ row }">
            <div class="fa-cell"><code class="fa-code">{{ asInst(row).url }}</code></div>
          </template>
        </DataTable>
      </section>

      <ActionDialog
        :open="!!connecting"
        :title="fmt(S.instances.connectTitle, { label: connecting?.label ?? '' })"
        :intro="S.instances.connectIntro"
        :confirm-label="S.instances.codeDone"
        :busy="codeBusy"
        :error="codeError"
        @confirm="endConnect"
        @cancel="endConnect"
      >
        <p v-if="connecting?.connected" class="fa-note">{{ S.instances.connectReplaces }}</p>
        <div v-if="code" class="fe-field" data-testid="instance-code">
          <span class="fe-field__label">{{ S.instances.codeLabel }}</span>
          <CopyField :value="code.code" :label="S.instances.codeLabel" testid="instance-code-value" boxed />
          <p class="fe-form__hint">{{ fmt(S.instances.codeExpires, { when: when(code.expires_at) }) }}</p>
        </div>
      </ActionDialog>

      <ActionDialog
        :open="!!disconnecting"
        :title="fmt(S.instances.disconnectTitle, { label: disconnecting?.label ?? '' })"
        :intro="S.instances.disconnectIntro"
        :confirm-label="S.instances.disconnectConfirm"
        danger
        :busy="disconnectBusy"
        :error="disconnectError"
        @confirm="doDisconnect"
        @cancel="disconnecting = null"
      />

      <ActionDialog
        :open="!!forgetting"
        :title="fmt(S.instances.forgetTitle, { label: forgetting?.label ?? '' })"
        :intro="S.instances.forgetIntro"
        :confirm-label="S.instances.forgetConfirm"
        danger
        :busy="forgetBusy"
        :error="forgetError"
        @confirm="doForget"
        @cancel="forgetting = null"
      />
    </AccessGate>
  </div>
</template>
