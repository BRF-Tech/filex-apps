<script setup lang="ts">
import { computed, ref } from 'vue';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { adminListValidators, type PluginValidatorStatus, type ValidatorStatus } from '@/api/client';
import { S, fmt } from '@/strings';
import { emptyUnlessError } from '@/lib/format';
import StatusBadge from '@/components/StatusBadge.vue';

const rows = ref<ValidatorStatus[]>([]);
// The plugin validators storage plugin submissions run on.
const pluginRows = ref<PluginValidatorStatus[]>([]);
const loading = ref(true);
const error = ref('');

async function load() {
  loading.value = true;
  error.value = '';
  try {
    const r = await adminListValidators();
    rows.value = r.items;
    pluginRows.value = r.plugin_validators ?? [];
  } catch (e) {
    error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    loading.value = false;
  }
}
void load();

const columns = computed<DataColumn<ValidatorStatus>[]>(() => [
  { id: 'label', label: S.admin.colLabel, width: 140, lead: true },
  { id: 'base_url', label: S.admin.colAddress, width: 260 },
  { id: 'reachable', label: S.admin.colReachable, width: 140, min: 120, format: (r) => (r.reachable ? S.admin.reachable : S.admin.unreachable) },
  { id: 'filex_version', label: S.admin.colVersion, width: 110, format: (r) => r.filex_version ?? '' },
  { id: 'error', label: S.admin.colError, width: 360, format: (r) => r.error ?? '' },
]);
const pluginColumns = computed<DataColumn<PluginValidatorStatus>[]>(() => [
  { id: 'label', label: S.admin.colLabel, width: 140, lead: true },
  { id: 'platform', label: S.admin.colPlatform, width: 160, format: (r) => r.platform ?? '' },
  { id: 'reachable', label: S.admin.colReachable, width: 140, min: 120, format: (r) => (r.reachable ? S.admin.reachable : S.admin.unreachable) },
  { id: 'filex_version', label: S.admin.colVersion, width: 110, format: (r) => r.filex_version ?? '' },
  { id: 'error', label: S.admin.colError, width: 360, format: (r) => r.error ?? '' },
]);
const reachable = (r: unknown) => (r as { reachable: boolean }).reachable;
</script>

<template>
  <section class="fa-section" aria-labelledby="av-h">
    <div class="fa-section__head">
      <h2 id="av-h">{{ S.admin.validators }}</h2>
      <button type="button" class="fe-btn fe-btn--sm" :disabled="loading" data-testid="validators-refresh" @click="load">{{ S.admin.refresh }}</button>
    </div>
    <p class="fa-note">{{ S.admin.validatorsIntro }}</p>
    <p v-if="error" class="fa-callout fa-callout--danger" role="alert">{{ error }}</p>
    <DataTable
      table-id="store.admin.validators"
      :columns="columns"
      :rows="rows"
      :loading="loading"
      row-key="label"
      :empty="emptyUnlessError(S.admin.validatorsEmpty, error)"
      :aria-label="S.admin.validators"
      data-testid="admin-validators"
    >
      <template #cell-reachable="{ row }">
        <div class="fa-cell">
          <StatusBadge :label="reachable(row) ? S.admin.reachable : S.admin.unreachable" :tone="reachable(row) ? 'ok' : 'danger'" />
        </div>
      </template>
    </DataTable>
    <h3 id="apv-h">{{ S.admin.pluginValidators }}</h3>
    <p class="fa-note">{{ S.admin.pluginValidatorsIntro }}</p>
    <DataTable
      table-id="store.admin.plugin-validators"
      :columns="pluginColumns"
      :rows="pluginRows"
      :loading="loading"
      row-key="label"
      :empty="emptyUnlessError(S.admin.pluginValidatorsEmpty, error)"
      :aria-label="S.admin.pluginValidators"
      data-testid="admin-plugin-validators"
    >
      <template #cell-reachable="{ row }">
        <div class="fa-cell">
          <StatusBadge :label="reachable(row) ? S.admin.reachable : S.admin.unreachable" :tone="reachable(row) ? 'ok' : 'danger'" />
        </div>
      </template>
    </DataTable>
  </section>
</template>
