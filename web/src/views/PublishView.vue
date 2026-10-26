<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { listSubmissions, myApps, type AdminApp, type MePublisher, type Submission, type SubmissionPage, type SubmissionStatus } from '@/api/client';
import { S, fmt } from '@/strings';
import { appStatusTone, emptyUnlessError, publisherTone, submissionTone, text, when } from '@/lib/format';
import { activePublishers, session } from '@/lib/session';
import { Plus, Send, Upload } from 'lucide-vue-next';
import AccessGate from '@/components/AccessGate.vue';
import FaSelect from '@/components/FaSelect.vue';
import Stepper from '@/components/Stepper.vue';
import PublisherBadges from '@/components/PublisherBadges.vue';
import StatusBadge from '@/components/StatusBadge.vue';

const router = useRouter();
const me = computed(() => session.me);
const publishers = computed<MePublisher[]>(() => me.value?.publishers ?? []);
const canSubmit = computed(() => activePublishers(me.value).length > 0);

const status = ref<SubmissionStatus | ''>('');
const page = ref(1);
const subs = ref<SubmissionPage | null>(null);
const loading = ref(false);
const error = ref('');
const PER_PAGE = 25;

async function load() {
  if (me.value?.identity.provider !== 'github') return;
  loading.value = true;
  error.value = '';
  try {
    subs.value = await listSubmissions({ status: status.value, page: page.value, per_page: PER_PAGE });
  } catch (e) {
    error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    loading.value = false;
  }
}
watch([me, status, page], () => void load(), { immediate: true });

// The apps of the person's publishers, to set a price and manage licenses.
const apps = ref<AdminApp[]>([]);
const appsError = ref('');
const appsLoading = ref(false);
async function loadApps() {
  if (me.value?.identity.provider !== 'github') return;
  appsLoading.value = true;
  appsError.value = '';
  try {
    apps.value = (await myApps()).items;
  } catch (e) {
    appsError.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    appsLoading.value = false;
  }
}
watch(me, () => void loadApps(), { immediate: true });
const appColumns = computed<DataColumn<AdminApp>[]>(() => [
  { id: 'name', label: S.licensing.colApp, width: 220, lead: true, format: (r) => text(r.label) || r.name },
  { id: 'status', label: S.admin.colAppStatus, width: 130, format: (r) => S.admin.appStatus[r.status] },
  {
    id: 'price',
    label: S.licensing.colPrice,
    width: 200,
    format: (r) => (r.pricing?.model === 'paid' ? [S.pricing.paid, r.pricing.price_text].filter(Boolean).join(': ') : S.pricing.free),
  },
  { id: 'latest', label: S.catalog.colLatest, width: 110, format: (r) => r.latest },
]);
const appActions = () => [{ key: 'manage', label: S.licensing.manage }];
function onAppAction(key: string, r: AdminApp) {
  if (key === 'manage') void router.push({ name: 'manage-app', params: { name: r.name } });
}
const asApp = (r: unknown) => r as AdminApp;
watch(status, () => (page.value = 1));

const pubColumns = computed<DataColumn<MePublisher>[]>(() => [
  { id: 'name', label: S.publish.colPublisher, width: 220, lead: true },
  { id: 'role', label: S.publish.colRole, width: 130, format: (r) => (r.role === 'maintainer' ? S.publish.roleMaintainer : S.publish.roleOwner) },
  { id: 'status', label: S.publish.colStatus, width: 140, min: 110, format: (r) => S.publisherStatus[r.status ?? 'active'] },
  { id: 'badges', label: S.publish.colBadges, width: 180, format: (r) => [r.official ? S.common.official : '', r.verified ? S.common.verified : ''].filter(Boolean).join(', ') },
]);

const subColumns = computed<DataColumn<Submission>[]>(() => [
  { id: 'id', label: S.review.colSubmission, width: 110, lead: true, format: (r) => '#' + r.id },
  { id: 'app', label: S.review.colApp, width: 200, format: (r) => (r.app ? `${r.app} ${r.version ?? ''}`.trim() : `${r.repo}@${r.ref}`) },
  { id: 'publisher', label: S.review.colPublisher, width: 160, format: (r) => r.publisher.name },
  { id: 'status', label: S.review.colStatus, width: 150, min: 130, format: (r) => S.submissionStatus[r.status] },
  { id: 'attempt', label: S.review.colAttempt, width: 90, align: 'right', format: (r) => r.attempt },
  { id: 'updated', label: S.review.colUpdated, width: 170, format: (r) => when(r.updated_at) },
]);
function onSubRow(row: Submission, ev: MouseEvent) {
  if ((ev.target as HTMLElement | null)?.closest('a, button')) return;
  void router.push({ name: 'submission', params: { id: row.id } });
}
const statuses = Object.keys(S.submissionStatus) as SubmissionStatus[];

// Where this person is in publishing: a publisher first, then the listing
// file and a tag, then submissions. Signed out, everything is ahead.
const steps = S.publish.stepTitles.map((title, i) => ({ title, text: S.publish.steps[i] }));
const hasSubmitted = computed(() => (subs.value?.total ?? 0) > 0);
const doneSteps = computed(() => [...(canSubmit.value ? [0] : []), ...(hasSubmitted.value ? [1] : [])]);
const currentStep = computed(() => (!canSubmit.value ? 0 : hasSubmitted.value ? 2 : 1));
const asSub = (r: unknown) => r as Submission;
const asPub = (r: unknown) => r as MePublisher;
</script>

<template>
  <div class="fa-page">
    <div class="fa-pagehead">
      <span class="fa-pagehead__icon" aria-hidden="true"><Upload /></span>
      <div class="fa-pagehead__text">
        <h1>{{ S.publish.title }}</h1>
        <p class="fa-pagehead__lead">{{ S.publish.intro }}</p>
      </div>
    </div>
    <Stepper :steps="steps" :current="currentStep" :done="doneSteps" :label="S.publish.stepsLabel" />
    <AccessGate need="github">
      <section class="fa-section" aria-labelledby="pub-h">
        <div class="fa-section__head">
          <h2 id="pub-h">{{ S.publish.publishers }}</h2>
          <div class="fa-actions">
            <RouterLink to="/publish/new-publisher" class="fe-btn" data-testid="ask-publisher"><Plus class="fa-icon" aria-hidden="true" />{{ S.publish.askPublisher }}</RouterLink>
            <RouterLink v-if="canSubmit" to="/submit" class="fe-btn fe-btn--primary" data-testid="go-submit"><Send class="fa-icon" aria-hidden="true" />{{ S.publish.submit }}</RouterLink>
            <span v-else class="fa-note">{{ S.publish.submitDisabled }}</span>
          </div>
        </div>
        <DataTable
          table-id="store.publish.publishers"
          :columns="pubColumns"
          :rows="publishers"
          row-key="id"
          :empty="S.publish.publishersEmpty"
          :aria-label="S.publish.publishers"
          data-testid="my-publishers"
        >
          <template #cell-name="{ row }">
            <div class="fa-cell">
              <RouterLink v-if="asPub(row).status !== 'pending'" :to="{ name: 'publisher', params: { id: asPub(row).id } }">{{ asPub(row).name }}</RouterLink>
              <span v-else>{{ asPub(row).name }}</span>
            </div>
          </template>
          <template #cell-status="{ row }">
            <div class="fa-cell">
              <StatusBadge :label="S.publisherStatus[asPub(row).status ?? 'active']" :tone="publisherTone(asPub(row).status)" />
            </div>
          </template>
          <template #cell-badges="{ row }">
            <div class="fa-cell">
              <PublisherBadges :verified="asPub(row).verified" :official="asPub(row).official" />
            </div>
          </template>
        </DataTable>
      </section>

      <section v-if="publishers.length" class="fa-section" aria-labelledby="apps-h" data-testid="my-apps-section">
        <h2 id="apps-h">{{ S.licensing.yourApps }}</h2>
        <p class="fa-note">{{ S.licensing.yourAppsIntro }}</p>
        <p v-if="appsError" class="fa-callout fa-callout--danger" role="alert">{{ appsError }}</p>
        <DataTable
          table-id="store.publish.apps"
          :columns="appColumns"
          :rows="apps"
          :loading="appsLoading"
          row-key="name"
          :empty="emptyUnlessError(S.licensing.yourAppsEmpty, appsError)"
          :aria-label="S.licensing.yourApps"
          :row-actions="appActions"
          :row-actions-test-id="(r: AdminApp) => `my-app-actions-${r.name}`"
          data-testid="my-apps"
          @row-action="(key: string, row: AdminApp) => onAppAction(key, row)"
        >
          <template #cell-name="{ row }">
            <div class="fa-cell">
              <RouterLink :to="{ name: 'manage-app', params: { name: asApp(row).name } }" :data-testid="`manage-${asApp(row).name}`">{{ text(asApp(row).label) || asApp(row).name }}</RouterLink>
            </div>
          </template>
          <template #cell-status="{ row }">
            <div class="fa-cell">
              <StatusBadge :label="S.admin.appStatus[asApp(row).status]" :tone="appStatusTone(asApp(row).status)" />
            </div>
          </template>
        </DataTable>
      </section>

      <section class="fa-section" aria-labelledby="subs-h">
        <h2 id="subs-h">{{ S.publish.submissions }}</h2>
        <p v-if="error" class="fa-callout fa-callout--danger" role="alert">{{ error }}</p>
        <DataTable
          table-id="store.publish.submissions"
          :columns="subColumns"
          :rows="subs?.items ?? []"
          :loading="loading"
          row-key="id"
          :empty="emptyUnlessError(S.publish.submissionsEmpty, error)"
          :aria-label="S.publish.submissions"
          :page="page"
          :page-size="PER_PAGE"
          :total="subs?.total"
          data-testid="my-submissions"
          @row-click="onSubRow"
          @page="(p: number) => (page = p)"
        >
          <template #toolbar>
            <div class="fa-filter">
              <label for="sub-status">{{ S.publish.statusFilter }}</label>
              <FaSelect
                id="sub-status"
                v-model="status"
                :options="[{ value: '', label: S.common.all }, ...statuses.map((s) => ({ value: s, label: S.submissionStatus[s] }))]"
                testid="sub-status"
              />
            </div>
          </template>
          <template #cell-id="{ row }">
            <div class="fa-cell">
              <RouterLink :to="{ name: 'submission', params: { id: asSub(row).id } }">#{{ asSub(row).id }}</RouterLink>
            </div>
          </template>
          <template #cell-status="{ row }">
            <div class="fa-cell">
              <StatusBadge :label="S.submissionStatus[asSub(row).status]" :tone="submissionTone(asSub(row).status)" />
            </div>
          </template>
        </DataTable>
      </section>
    </AccessGate>
  </div>
</template>
