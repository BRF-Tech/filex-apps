<script setup lang="ts">
import { computed, ref } from 'vue';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { ApiError, adminListKeys, indexHead, publishIndex, type IndexRelease, type SigningKey } from '@/api/client';
import { S, fmt } from '@/strings';
import { emptyUnlessError, when } from '@/lib/format';
import ActionDialog from '@/components/ActionDialog.vue';
import CopyButton from '@/components/CopyButton.vue';
import CopyField from '@/components/CopyField.vue';
import StatusBadge from '@/components/StatusBadge.vue';

const head = ref<{ serial: number | null; etag: string | null; ready: boolean } | null>(null);
const keys = ref<SigningKey[]>([]);
const loading = ref(true);
const error = ref('');

async function load() {
  loading.value = true;
  error.value = '';
  try {
    const [h, k] = await Promise.all([indexHead(), adminListKeys()]);
    head.value = h;
    keys.value = k.items;
  } catch (e) {
    error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    loading.value = false;
  }
}
void load();

// One label per key use: the index key, the artifact key and the license key
// that signs license verdicts (it is in /v1/keys.json, not in the index).
const USE_LABEL: Record<SigningKey['use'], string> = { index: S.admin.useIndex, artifact: S.admin.useArtifact, license: S.admin.useLicense };

const columns = computed<DataColumn<SigningKey>[]>(() => [
  { id: 'id', label: S.admin.colKey, width: 180, lead: true },
  { id: 'use', label: S.admin.colUse, width: 100, format: (r) => USE_LABEL[r.use] ?? r.use },
  { id: 'public', label: S.admin.colPublic, width: 300, min: 160, format: (r) => r.public_hex },
  { id: 'status', label: S.admin.colKeyStatus, width: 110, min: 100, format: (r) => S.admin.keyStatus[r.status] },
  { id: 'demo', label: S.admin.colDemo, width: 90, format: (r) => (r.demo ? S.common.yes : S.common.no) },
  { id: 'created', label: S.admin.colKeyCreated, width: 150, format: (r) => when(r.created_at, false) },
  { id: 'activated', label: S.admin.colActivated, width: 150, format: (r) => when(r.activated_at, false) },
  { id: 'retired', label: S.admin.colRetired, width: 150, format: (r) => when(r.retired_at, false) },
]);

const confirm = ref(false);
const busy = ref(false);
const dialogError = ref('');
const release = ref<IndexRelease | null>(null);
async function resign() {
  busy.value = true;
  dialogError.value = '';
  try {
    release.value = await publishIndex();
    confirm.value = false;
    head.value = await indexHead();
  } catch (e) {
    dialogError.value = e instanceof ApiError ? `${e.message} (${e.code})` : String(e);
  } finally {
    busy.value = false;
  }
}
const asKey = (r: unknown) => r as SigningKey;
</script>

<template>
  <div class="fa-page">
    <section class="fa-panel" aria-labelledby="ai-h">
      <h2 id="ai-h">{{ S.admin.indexTitle }}</h2>
      <p v-if="error" class="fa-callout fa-callout--danger" role="alert">{{ error }}</p>
      <p v-if="loading" class="fa-note" role="status">{{ S.common.loading }}</p>
      <template v-else-if="head">
        <p v-if="!head.ready" class="fa-callout fa-callout--warn" data-testid="index-not-ready">{{ S.admin.indexNotReady }}</p>
        <dl v-else class="fa-dl" data-testid="index-head">
          <dt>{{ S.admin.indexSerial }}</dt>
          <dd data-testid="index-serial">{{ head.serial ?? S.common.unknown }}</dd>
          <template v-if="head.etag">
            <dt>{{ S.admin.indexSha }}</dt>
            <dd><CopyField :value="head.etag" :label="`${S.common.copy}: ${S.admin.indexSha}`" /></dd>
          </template>
          <dt>{{ S.admin.indexFiles }}</dt>
          <dd class="fa-actions">
            <a href="/v1/index.json">index.json</a>
            <a href="/v1/index.json.sig">index.json.sig</a>
            <a href="/v1/keys.json">keys.json</a>
          </dd>
        </dl>
      </template>
      <div class="fa-actions">
        <button type="button" class="fe-btn" data-testid="resign" @click="(dialogError = ''), (confirm = true)">{{ S.admin.resign }}</button>
      </div>
      <p v-if="release" class="fa-callout fa-callout--ok" role="status" data-testid="resigned">
        {{ fmt(S.admin.resigned, { serial: release.serial, expires: when(release.expires_at) }) }}
      </p>
    </section>

    <section class="fa-section" aria-labelledby="ak-h">
      <h2 id="ak-h">{{ S.admin.keys }}</h2>
      <DataTable
        table-id="store.admin.keys"
        :columns="columns"
        :rows="keys"
        :loading="loading"
        row-key="id"
        :empty="emptyUnlessError(S.admin.keysEmpty, error)"
        :aria-label="S.admin.keys"
        data-testid="admin-keys"
      >
        <template #cell-public="{ row }">
          <div class="fa-cell">
            <span class="fa-code" :title="asKey(row).public_hex">{{ asKey(row).public_hex.slice(0, 16) }}…</span>
            <CopyButton :value="asKey(row).public_hex" :label="`${S.common.copy}: ${asKey(row).id}`" />
          </div>
        </template>
        <template #cell-status="{ row }">
          <div class="fa-cell">
            <StatusBadge :label="S.admin.keyStatus[asKey(row).status]" :tone="asKey(row).status === 'active' ? 'ok' : asKey(row).status === 'next' ? 'info' : 'muted'" />
          </div>
        </template>
        <template #cell-demo="{ row }">
          <div class="fa-cell">
            <StatusBadge v-if="asKey(row).demo" :label="S.admin.colDemo" tone="warn" :title="S.admin.demoKey" />
            <span v-else>{{ S.common.no }}</span>
          </div>
        </template>
      </DataTable>
    </section>

    <ActionDialog
      :open="confirm"
      :title="S.admin.resign"
      :intro="S.admin.resignConfirm"
      :confirm-label="S.admin.resign"
      :busy="busy"
      :error="dialogError"
      @confirm="resign"
      @cancel="confirm = false"
    />
  </div>
</template>
