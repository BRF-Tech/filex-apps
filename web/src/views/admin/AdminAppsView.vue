<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { ApiError, adminListApps, adminUpdateApp, revokeApp, unrevokeApp, type AdminApp, type AppStatus } from '@/api/client';
import { S, fmt } from '@/strings';
import { appStatusTone, emptyUnlessError, text, when } from '@/lib/format';
import StatusBadge from '@/components/StatusBadge.vue';
import FaSegmented from '@/components/FaSegmented.vue';
import ActionDialog from '@/components/ActionDialog.vue';

const router = useRouter();
const q = ref('');
const status = ref<AppStatus | ''>('');
const page = ref(1);
const rows = ref<AdminApp[]>([]);
const total = ref(0);
const loading = ref(false);
const error = ref('');
const done = ref('');
const PER_PAGE = 50;

async function load() {
  loading.value = true;
  error.value = '';
  try {
    const r = await adminListApps({ status: status.value || undefined, q: q.value.trim() || undefined, page: page.value, per_page: PER_PAGE, sort: 'name' });
    rows.value = r.items;
    total.value = r.total;
  } catch (e) {
    error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    loading.value = false;
  }
}
watch(page, () => void load(), { immediate: true });
watch(status, () => {
  if (page.value === 1) void load();
  else page.value = 1;
});
let t: ReturnType<typeof setTimeout> | undefined;
function onSearch() {
  clearTimeout(t);
  t = setTimeout(() => {
    page.value = 1;
    void load();
  }, 300);
}

const columns = computed<DataColumn<AdminApp>[]>(() => [
  { id: 'name', label: S.catalog.colApp, width: 220, lead: true, format: (r) => text(r.label) || r.name },
  { id: 'publisher', label: S.catalog.colPublisher, width: 180, format: (r) => r.publisher.name },
  { id: 'status', label: S.admin.colAppStatus, width: 130, format: (r) => S.admin.appStatus[r.status] },
  { id: 'kind', label: S.catalog.colKind, width: 130, format: (r) => S.kind[r.kind] },
  { id: 'latest', label: S.catalog.colLatest, width: 110, format: (r) => r.latest },
  { id: 'updated', label: S.catalog.colUpdated, width: 140, format: (r) => when(r.updated_at, false) },
]);
const actions = (r: AdminApp) => [
  { key: 'open', label: S.common.open },
  { key: 'licences', label: S.licensing.manage },
  ...(r.status === 'listed' ? [{ key: 'unlist', label: S.admin.unlist }] : []),
  ...(r.status === 'unlisted' ? [{ key: 'relist', label: S.admin.relist }] : []),
  ...(r.status === 'revoked' ? [{ key: 'unrevoke', label: S.admin.unrevoke }] : [{ key: 'revoke', label: S.admin.revoke, danger: true }]),
];

const revoking = ref<AdminApp | null>(null);
const busy = ref(false);
const dialogError = ref('');
async function onAction(key: string, r: AdminApp) {
  done.value = '';
  if (key === 'open') void router.push({ name: 'app', params: { name: r.name } });
  else if (key === 'licences') void router.push({ name: 'manage-app', params: { name: r.name } });
  else if (key === 'revoke') {
    dialogError.value = '';
    revoking.value = r;
  } else if (key === 'unlist' || key === 'relist' || key === 'unrevoke') {
    try {
      if (key === 'unrevoke') await unrevokeApp(r.name);
      else await adminUpdateApp(r.name, { status: key === 'unlist' ? 'unlisted' : 'listed' });
      done.value = S.app.adminDone;
      await load();
    } catch (e) {
      error.value = e instanceof ApiError ? `${e.message} (${e.code})` : String(e);
    }
  }
}
async function doRevoke(reason: string) {
  if (!revoking.value) return;
  busy.value = true;
  dialogError.value = '';
  try {
    await revokeApp(revoking.value.name, reason);
    revoking.value = null;
    done.value = S.app.adminDone;
    await load();
  } catch (e) {
    dialogError.value = e instanceof ApiError ? `${e.message} (${e.code})` : String(e);
  } finally {
    busy.value = false;
  }
}

const byName = ref('');
function openByName() {
  const n = byName.value.trim().toLowerCase();
  if (/^[a-z0-9][a-z0-9_-]{0,31}$/.test(n)) void router.push({ name: 'app', params: { name: n } });
}
const asApp = (r: unknown) => r as AdminApp;
</script>

<template>
  <section class="fa-section" aria-labelledby="aa-h">
    <h2 id="aa-h">{{ S.admin.apps }}</h2>
    <p class="fa-note">{{ S.admin.appsIntro }}</p>
    <form class="fa-actions" novalidate @submit.prevent="openByName">
      <div class="fa-filter">
        <label for="aa-name">{{ S.admin.openByName }}</label>
        <input id="aa-name" v-model="byName" class="fe-input" spellcheck="false" autocomplete="off" data-testid="aa-name" />
      </div>
      <button type="submit" class="fe-btn" data-testid="aa-open">{{ S.admin.openByNameButton }}</button>
    </form>
    <p v-if="error" class="fa-callout fa-callout--danger" role="alert">{{ error }}</p>
    <p v-if="done" class="fa-note" role="status">{{ done }}</p>
    <DataTable
      table-id="store.admin.apps"
      :columns="columns"
      :rows="rows"
      :loading="loading"
      row-key="name"
      :empty="emptyUnlessError(S.admin.appsEmpty, error)"
      :aria-label="S.admin.apps"
      :page="page"
      :page-size="PER_PAGE"
      :total="total"
      :row-actions="actions"
      :row-actions-test-id="(r: AdminApp) => `app-actions-${r.name}`"
      data-testid="admin-apps"
      @page="(p: number) => (page = p)"
      @row-action="(key: string, row: AdminApp) => onAction(key, row)"
    >
      <template #toolbar>
        <div class="fa-filter fa-filter--auto">
          <span id="aa-status-label" class="fa-filter__label">{{ S.admin.statusFilter }}</span>
          <FaSegmented
            v-model="status"
            :options="[
              { value: '', label: S.common.all },
              { value: 'listed', label: S.admin.appStatus.listed },
              { value: 'unlisted', label: S.admin.appStatus.unlisted },
              { value: 'revoked', label: S.admin.appStatus.revoked },
            ]"
            aria-labelledby="aa-status-label"
            testid="aa-status"
          />
        </div>
        <div class="fa-filter fa-filter--wide">
          <label for="aa-q">{{ S.catalog.search }}</label>
          <input id="aa-q" v-model="q" class="fe-input" type="search" maxlength="100" @input="onSearch" />
        </div>
      </template>
      <template #cell-status="{ row }">
        <div class="fa-cell">
          <StatusBadge :label="S.admin.appStatus[asApp(row).status]" :tone="appStatusTone(asApp(row).status)" />
        </div>
      </template>
      <template #cell-name="{ row }">
        <div class="fa-cell">
          <RouterLink :to="{ name: 'app', params: { name: asApp(row).name } }">{{ text(asApp(row).label) || asApp(row).name }}</RouterLink>
        </div>
      </template>
    </DataTable>
    <ActionDialog
      :open="!!revoking"
      :title="fmt(S.app.revokeTitle, { app: revoking ? text(revoking.label) || revoking.name : '' })"
      :intro="S.app.revokeIntro"
      :confirm-label="S.app.adminRevoke.replace('…', '')"
      reason
      danger
      :busy="busy"
      :error="dialogError"
      @confirm="doRevoke"
      @cancel="revoking = null"
    />
  </section>
</template>
