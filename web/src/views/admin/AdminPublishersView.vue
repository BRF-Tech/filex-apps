<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { ApiError, adminListPublishers, adminUpdatePublisher, type Publisher, type PublisherStatus } from '@/api/client';
import { S, fmt } from '@/strings';
import { emptyUnlessError, github, publisherTone, when } from '@/lib/format';
import ActionDialog from '@/components/ActionDialog.vue';
import FaSegmented from '@/components/FaSegmented.vue';
import StatusBadge from '@/components/StatusBadge.vue';

const status = ref<PublisherStatus | ''>('');
const page = ref(1);
const rows = ref<Publisher[]>([]);
const total = ref(0);
const loading = ref(false);
const error = ref('');
const done = ref('');
const PER_PAGE = 50;

async function load() {
  loading.value = true;
  error.value = '';
  try {
    const r = await adminListPublishers({ status: status.value, page: page.value, per_page: PER_PAGE });
    rows.value = r.items;
    total.value = r.total;
  } catch (e) {
    error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    loading.value = false;
  }
}
watch([status, page], () => void load(), { immediate: true });
watch(status, () => (page.value = 1));

const columns = computed<DataColumn<Publisher>[]>(() => [
  { id: 'name', label: S.admin.colPublisher, width: 220, lead: true },
  { id: 'owner', label: S.admin.colOwner, width: 200, format: (r) => `${r.github_owner} (${r.owner_type})` },
  { id: 'status', label: S.admin.colStatus, width: 200, min: 110, format: (r) => S.publisherStatus[r.status] },
  { id: 'verified', label: S.admin.colVerified, width: 100, format: (r) => (r.verified ? S.common.yes : S.common.no) },
  { id: 'official', label: S.admin.colOfficial, width: 100, format: (r) => (r.official ? S.common.yes : S.common.no) },
  { id: 'created', label: S.admin.colCreated, width: 170, format: (r) => when(r.created_at) },
]);

function actions(r: Publisher) {
  return [
    r.status === 'active'
      ? { key: 'suspend', label: S.admin.suspend, danger: true }
      : { key: 'activate', label: S.admin.activate },
    r.verified ? { key: 'unverify', label: S.admin.unverify } : { key: 'verify', label: S.admin.verify },
    r.official ? { key: 'unofficial', label: S.admin.unmarkOfficial } : { key: 'official', label: S.admin.markOfficial },
  ];
}

const suspending = ref<Publisher | null>(null);
const busy = ref(false);
const dialogError = ref('');

async function patch(p: Publisher, body: Parameters<typeof adminUpdatePublisher>[1]) {
  busy.value = true;
  error.value = '';
  done.value = '';
  dialogError.value = '';
  try {
    await adminUpdatePublisher(p.id, body);
    suspending.value = null;
    done.value = S.admin.saved;
    await load();
  } catch (e) {
    const msg = e instanceof ApiError ? `${e.message} (${e.code})` : String(e);
    if (suspending.value) dialogError.value = msg;
    else error.value = msg;
  } finally {
    busy.value = false;
  }
}

function onAction(key: string, p: Publisher) {
  if (key === 'suspend') {
    dialogError.value = '';
    suspending.value = p;
  } else if (key === 'activate') void patch(p, { status: 'active' });
  else if (key === 'verify') void patch(p, { verified: true });
  else if (key === 'unverify') void patch(p, { verified: false });
  else if (key === 'official') void patch(p, { official: true });
  else if (key === 'unofficial') void patch(p, { official: false });
}
const asPub = (r: unknown) => r as Publisher;
</script>

<template>
  <section class="fa-section" aria-labelledby="ap-h">
    <h2 id="ap-h">{{ S.admin.publishers }}</h2>
    <p v-if="error" class="fa-callout fa-callout--danger" role="alert">{{ error }}</p>
    <p v-if="done" class="fa-note" role="status">{{ done }}</p>
    <DataTable
      table-id="store.admin.publishers"
      :columns="columns"
      :rows="rows"
      :loading="loading"
      row-key="id"
      :empty="emptyUnlessError(S.admin.publishersEmpty, error)"
      :aria-label="S.admin.publishers"
      :page="page"
      :page-size="PER_PAGE"
      :total="total"
      :row-actions="actions"
      :row-actions-test-id="(r: Publisher) => `pub-actions-${r.id}`"
      data-testid="admin-publishers"
      @page="(p: number) => (page = p)"
      @row-action="(key: string, row: Publisher) => onAction(key, row)"
    >
      <template #toolbar>
        <div class="fa-filter fa-filter--auto">
          <span id="ap-status-label" class="fa-filter__label">{{ S.admin.statusFilter }}</span>
          <FaSegmented
            v-model="status"
            :options="[
              { value: '', label: S.common.all },
              { value: 'pending', label: S.publisherStatus.pending },
              { value: 'active', label: S.publisherStatus.active },
              { value: 'suspended', label: S.publisherStatus.suspended },
            ]"
            aria-labelledby="ap-status-label"
            testid="ap-status"
          />
        </div>
      </template>
      <template #cell-name="{ row }">
        <div class="fa-cell fa-cell--stack">
          <RouterLink v-if="asPub(row).status !== 'pending'" :to="{ name: 'publisher', params: { id: asPub(row).id } }">{{ asPub(row).name }}</RouterLink>
          <span v-else>{{ asPub(row).name }}</span>
          <span class="tbl-sub fa-code">{{ asPub(row).id }}</span>
        </div>
      </template>
      <template #cell-owner="{ row }">
        <div class="fa-cell">
          <a :href="github.owner(asPub(row).github_owner)" rel="noopener noreferrer">{{ asPub(row).github_owner }}</a>
          <span class="fa-note">{{ asPub(row).owner_type }}</span>
        </div>
      </template>
      <template #cell-status="{ row }">
        <div class="fa-cell fa-cell--stack">
          <StatusBadge :label="S.publisherStatus[asPub(row).status]" :tone="publisherTone(asPub(row).status)" />
          <span v-if="asPub(row).status_reason" class="tbl-sub">{{ asPub(row).status_reason }}</span>
        </div>
      </template>
    </DataTable>
    <ActionDialog
      :open="!!suspending"
      :title="fmt(S.admin.suspendTitle, { name: suspending?.name ?? '' })"
      :intro="S.admin.suspendIntro"
      :confirm-label="S.admin.suspend.replace('…', '')"
      reason
      danger
      :busy="busy"
      :error="dialogError"
      @confirm="(reason: string) => suspending && patch(suspending, { status: 'suspended', status_reason: reason })"
      @cancel="suspending = null"
    />
  </section>
</template>
