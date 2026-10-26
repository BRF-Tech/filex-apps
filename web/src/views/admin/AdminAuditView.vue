<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { adminAudit, type AuditEntry } from '@/api/client';
import { S, fmt } from '@/strings';
import { emptyUnlessError, when } from '@/lib/format';

const draft = reactive({ action: '', target_type: '', target_id: '' });
const applied = ref({ action: '', target_type: '', target_id: '' });
const page = ref(1);
const rows = ref<AuditEntry[]>([]);
const total = ref(0);
const chainOk = ref<boolean | null>(null);
const loading = ref(false);
const error = ref('');
const PER_PAGE = 50;

async function load() {
  loading.value = true;
  error.value = '';
  try {
    const r = await adminAudit({ ...applied.value, page: page.value, per_page: PER_PAGE });
    rows.value = r.items;
    total.value = r.total;
    chainOk.value = r.chain_ok;
  } catch (e) {
    error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    loading.value = false;
  }
}
watch([applied, page], () => void load(), { immediate: true });
function apply() {
  page.value = 1;
  applied.value = { action: draft.action.trim(), target_type: draft.target_type.trim(), target_id: draft.target_id.trim() };
}

const columns = computed<DataColumn<AuditEntry>[]>(() => [
  { id: 'at', label: S.admin.colAt, width: 180, lead: true, format: (r) => when(r.at) },
  { id: 'actor', label: S.admin.colActor, width: 150, format: (r) => r.actor ?? '' },
  { id: 'role', label: S.admin.colRole, width: 110, format: (r) => r.actor_role },
  { id: 'action', label: S.admin.colAction, width: 190 },
  { id: 'target', label: S.admin.colTarget, width: 200, format: (r) => [r.target_type, r.target_id].filter(Boolean).join(': ') },
  { id: 'data', label: S.admin.colData, width: 360, format: (r) => (r.data ? JSON.stringify(r.data) : '') },
]);
const asRow = (r: unknown) => r as AuditEntry;
</script>

<template>
  <section class="fa-section" aria-labelledby="au-h">
    <h2 id="au-h">{{ S.admin.audit }}</h2>
    <p v-if="chainOk === true" class="fa-callout fa-callout--ok" data-testid="chain-ok">{{ S.admin.auditChainOk }}</p>
    <p v-else-if="chainOk === false" class="fa-callout fa-callout--danger" role="alert" data-testid="chain-broken">{{ S.admin.auditChainBroken }}</p>
    <p v-if="error" class="fa-callout fa-callout--danger" role="alert">{{ error }}</p>
    <DataTable
      table-id="store.admin.audit"
      :columns="columns"
      :rows="rows"
      :loading="loading"
      row-key="id"
      :empty="emptyUnlessError(S.admin.auditEmpty, error)"
      :aria-label="S.admin.audit"
      :page="page"
      :page-size="PER_PAGE"
      :total="total"
      data-testid="admin-audit"
      @page="(p: number) => (page = p)"
    >
      <template #toolbar>
        <form class="fa-actions" novalidate @submit.prevent="apply">
          <div class="fa-filter">
            <label for="au-action">{{ S.admin.auditFilterAction }}</label>
            <input id="au-action" v-model="draft.action" class="fe-input" spellcheck="false" />
          </div>
          <div class="fa-filter">
            <label for="au-type">{{ S.admin.auditFilterType }}</label>
            <input id="au-type" v-model="draft.target_type" class="fe-input" spellcheck="false" />
          </div>
          <div class="fa-filter">
            <label for="au-id">{{ S.admin.auditFilterId }}</label>
            <input id="au-id" v-model="draft.target_id" class="fe-input" spellcheck="false" />
          </div>
          <button type="submit" class="fe-btn">{{ S.admin.auditApply }}</button>
        </form>
      </template>
      <template #cell-data="{ row }">
        <span class="fa-code tbl-clamp" :title="asRow(row).data ? JSON.stringify(asRow(row).data, null, 2) : undefined">
          {{ asRow(row).data ? JSON.stringify(asRow(row).data) : '' }}
        </span>
      </template>
    </DataTable>
  </section>
</template>
