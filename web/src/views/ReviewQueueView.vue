<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { reviewQueue, type Submission, type SubmissionPage } from '@/api/client';
import { S, fmt } from '@/strings';
import { emptyUnlessError, when } from '@/lib/format';
import { hasRole, session } from '@/lib/session';
import { ClipboardCheck } from 'lucide-vue-next';
import AccessGate from '@/components/AccessGate.vue';

const router = useRouter();
const page = ref(1);
const result = ref<SubmissionPage | null>(null);
const loading = ref(false);
const error = ref('');
const PER_PAGE = 50;
const allowed = computed(() => hasRole(session.me, 'reviewer'));

async function load() {
  if (!allowed.value) return;
  loading.value = true;
  error.value = '';
  try {
    result.value = await reviewQueue({ page: page.value, per_page: PER_PAGE });
  } catch (e) {
    error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    loading.value = false;
  }
}
watch([allowed, page], () => void load(), { immediate: true });

// Oldest first, as the server answers: the queue is worked from the top.
const columns = computed<DataColumn<Submission>[]>(() => [
  { id: 'id', label: S.review.colSubmission, width: 110, lead: true, format: (r) => '#' + r.id },
  { id: 'app', label: S.review.colApp, width: 180, format: (r) => r.app ?? '' },
  { id: 'version', label: S.review.colVersion, width: 110, format: (r) => r.version ?? '' },
  { id: 'publisher', label: S.review.colPublisher, width: 160, format: (r) => r.publisher.name },
  { id: 'repo', label: S.review.colRepo, width: 240, format: (r) => `${r.repo}@${r.ref}` },
  { id: 'by', label: S.review.colBy, width: 140, format: (r) => r.submitted_by ?? '' },
  { id: 'waiting', label: S.review.colWaiting, width: 170, format: (r) => when(r.updated_at) },
  { id: 'attempt', label: S.review.colAttempt, width: 90, align: 'right', format: (r) => r.attempt },
]);
function onRow(row: Submission, ev: MouseEvent) {
  if ((ev.target as HTMLElement | null)?.closest('a, button')) return;
  void router.push({ name: 'submission', params: { id: row.id } });
}
const asSub = (r: unknown) => r as Submission;
</script>

<template>
  <div class="fa-page">
    <div class="fa-pagehead">
      <span class="fa-pagehead__icon" aria-hidden="true"><ClipboardCheck /></span>
      <div class="fa-pagehead__text">
        <h1>{{ S.review.title }}</h1>
        <p class="fa-pagehead__lead">{{ S.review.intro }}</p>
      </div>
      <div v-if="result" class="fa-pagehead__actions">
        <span class="fa-badge" :class="result.total ? 'fa-badge--warn' : 'fa-badge--ok'" data-testid="queue-count">
          <span class="fa-badge__dot" aria-hidden="true"></span>{{ fmt(S.review.waiting, { count: result.total }) }}
        </span>
      </div>
    </div>
    <AccessGate need="reviewer">
      <p v-if="error" class="fa-callout fa-callout--danger" role="alert">{{ error }}</p>
      <DataTable
        table-id="store.review.queue"
        :columns="columns"
        :rows="result?.items ?? []"
        :loading="loading"
        row-key="id"
        :empty="emptyUnlessError(S.review.empty, error)"
        :aria-label="S.review.tableLabel"
        :page="page"
        :page-size="PER_PAGE"
        :total="result?.total"
        data-testid="review-queue"
        @row-click="onRow"
        @page="(p: number) => (page = p)"
      >
        <template #cell-id="{ row }">
          <div class="fa-cell">
            <RouterLink :to="{ name: 'submission', params: { id: asSub(row).id } }" :data-testid="`queue-${asSub(row).id}`">#{{ asSub(row).id }}</RouterLink>
          </div>
        </template>
      </DataTable>
    </AccessGate>
  </div>
</template>
