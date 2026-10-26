<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import {
  ApiError,
  approveSubmission,
  commentSubmission,
  getApp,
  getSubmission,
  recheckSubmission,
  rejectSubmission,
  retrySubmission,
  withdrawSubmission,
  type Check,
  type DryRunResult,
  type PermissionRow,
  type Review,
  type SubmissionDetail,
} from '@/api/client';
import { S, fmt } from '@/strings';
import { bytes, checkTone, github, inFlight, safeHref, shortHash, submissionTone, text, took, undecided, when } from '@/lib/format';
import { hasRole, isGithub, isMemberOf, session } from '@/lib/session';
import { explainCode } from '@/lib/checkCodes';
import { declarationLine } from '@/lib/fileTypes';
import { setTitle } from '@/router';
import ActionDialog from '@/components/ActionDialog.vue';
import CopyField from '@/components/CopyField.vue';
import PageState from '@/components/PageState.vue';
import StatusBadge from '@/components/StatusBadge.vue';
import { Check as CheckIcon, ChevronDown, CircleAlert, CircleCheck, CircleDashed, CircleX, ClipboardList, LoaderCircle } from 'lucide-vue-next';

const route = useRoute();
const id = computed(() => Number(route.params.id));
const sub = ref<SubmissionDetail | null>(null);
const loading = ref(true);
const error = ref('');
const notFound = ref(false);
const lastRead = ref('');

const POLL_MS = 3000;
let timer: ReturnType<typeof setTimeout> | undefined;
let alive = true;
onBeforeUnmount(() => {
  alive = false;
  clearTimeout(timer);
});

async function load(quiet = false) {
  clearTimeout(timer);
  if (!quiet) {
    loading.value = true;
    error.value = '';
    notFound.value = false;
  }
  try {
    const s = await getSubmission(id.value);
    if (!alive) return;
    sub.value = s;
    error.value = '';
    lastRead.value = new Date().toISOString();
    setTitle(fmt(S.submission.title, { id: s.id }));
    // The pipeline runs in the background: follow it until it settles.
    if (inFlight(s.status)) timer = setTimeout(() => void load(true), POLL_MS);
  } catch (e) {
    if (!alive) return;
    if (e instanceof ApiError && e.status === 404) notFound.value = true;
    else if (!quiet || !sub.value) error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
    else timer = setTimeout(() => void load(true), POLL_MS * 2);
  } finally {
    loading.value = false;
  }
}
watch(id, () => void load(), { immediate: true });

const what = computed(() => {
  const s = sub.value;
  if (!s) return '';
  return s.app ? `${s.app} ${s.version ?? ''}`.trim() : `${s.repo}@${s.ref}`;
});
const res = computed(() => sub.value?.resolution ?? null);
const consent = computed(() => (res.value?.consent ?? null) as Record<string, unknown> | null);
const consentSummary = computed(() => text((consent.value?.summary ?? null) as Record<string, string> | null));
const consentCategories = computed(() => (Array.isArray(consent.value?.categories) ? (consent.value?.categories as string[]) : []));
// The conversions the listing declares (held to the schema and to the
// manifest's engines by the consent check); the reviewer compares them with
// the code.
interface Declared {
  from: string[];
  to: string[];
  needs?: string[];
}
const consentConversions = computed(() => (Array.isArray(consent.value?.conversions) ? (consent.value?.conversions as Declared[]) : []));
const conversionPairs = computed(() => consentConversions.value.reduce((n, c) => n + c.from.length * c.to.length, 0));
const consentShots = computed(() =>
  Array.isArray(consent.value?.screenshots) ? (consent.value?.screenshots as { path?: string }[]).map((s) => s.path ?? '') : [],
);
// The description's Markdown source (contract: Resolution.description_md),
// shown as text: the listed app page carries the server's sanitized HTML.
const descriptionMd = computed(() => res.value?.description_md ?? '');

// ---------------------------------------------------------------- tables
const checkColumns = computed<DataColumn<Check>[]>(() => [
  {
    id: 'kind',
    label: S.submission.colStep,
    width: 170,
    lead: true,
    // A storage plugin's steps read its feed, its builds, its conformance run.
    format: (r) => (sub.value?.kind === 'storage' ? S.checkKindStorage[r.kind] : undefined) ?? S.checkKind[r.kind] ?? r.kind,
  },
  { id: 'validator', label: S.submission.colValidator, width: 120, format: (r) => r.validator ?? '' },
  { id: 'status', label: S.submission.colResult, width: 130, min: 110, format: (r) => S.checkStatus[r.status] },
  { id: 'code', label: S.submission.colCode, width: 170, format: (r) => r.code ?? '' },
  { id: 'message', label: S.submission.colDetail, width: 460, format: (r) => [explainCode(r.code), r.message].filter(Boolean).join(' ') },
  { id: 'took', label: S.submission.colTook, width: 90, align: 'right', format: (r) => took(r.started_at, r.ended_at) },
]);
const dryColumns = computed<DataColumn<DryRunResult>[]>(() => [
  { id: 'validator', label: S.submission.colValidator, width: 130, lead: true },
  { id: 'filex', label: S.submission.colFilex, width: 90, format: (r) => r.filex_version },
  { id: 'ok', label: S.submission.colResult, width: 150, min: 110, format: (r) => (r.ok ? S.checkStatus.passed : S.checkStatus.failed) },
  { id: 'kind', label: S.submission.colKind, width: 120, format: (r) => S.kind[r.kind] ?? r.kind },
  { id: 'manifest', label: S.submission.colManifest, width: 200, format: (r) => r.manifest_sha256 },
  { id: 'wasm', label: S.submission.colModule, width: 200, format: (r) => r.wasm_sha256 ?? '' },
  { id: 'ui', label: S.submission.colBundle, width: 200, format: (r) => r.ui_sha256 ?? '' },
  { id: 'perms', label: S.submission.colPermissions, width: 110, align: 'right', format: (r) => r.permissions.length },
  {
    id: 'languages',
    label: S.submission.colLanguages,
    width: 200,
    format: (r) => (r.languages ?? []).map((l) => `${l.code} ${l.percent}%`).join(', '),
  },
  { id: 'engines', label: S.submission.colEngines, width: 150, format: (r) => (r.engines_missing ?? []).join(', ') },
  { id: 'code', label: S.submission.colDetail, width: 380, format: (r) => [explainCode(r.code), r.message].filter(Boolean).join(' ') },
]);
const permissions = computed<PermissionRow[]>(() => {
  const runs = sub.value?.dry_runs ?? [];
  return (runs.find((r) => r.ok) ?? runs[0])?.permissions ?? [];
});
const permColumns = computed<DataColumn<PermissionRow>[]>(() => [
  { id: 'id', label: S.app.colPermission, width: 200, lead: true },
  { id: 'label', label: S.app.colWhat, width: 340 },
  { id: 'reason', label: S.app.colWhy, width: 340, format: (r) => text(r.reason) },
]);
const reviewColumns = computed<DataColumn<Review>[]>(() => [
  { id: 'reviewer', label: S.submission.colReviewer, width: 160, lead: true },
  { id: 'decision', label: S.submission.colDecision, width: 130, format: (r) => S.submission.decision[r.decision] },
  { id: 'note', label: S.submission.colNote, width: 420, format: (r) => r.note ?? '' },
  { id: 'when', label: S.submission.colWhen, width: 170, format: (r) => when(r.created_at) },
]);

// ---------------------------------------------------------------- diff + links
const fromRef = ref('');
watch(
  () => [sub.value?.app, sub.value?.diff?.from] as const,
  async ([app, from]) => {
    fromRef.value = '';
    if (!app || !from) return;
    try {
      const a = await getApp(app);
      fromRef.value = a.versions.find((v) => v.version === from)?.ref ?? '';
    } catch {
      fromRef.value = '';
    }
  },
);

// ---------------------------------------------------------------- actions
const me = computed(() => session.me);
const isReviewer = computed(() => hasRole(me.value, 'reviewer'));
const isAdmin = computed(() => hasRole(me.value, 'admin'));
const isOwnPublisher = computed(() => !!sub.value && isGithub(me.value) && isMemberOf(me.value, sub.value.publisher.id));
const checksDone = computed(
  () => !!sub.value && sub.value.checks.length > 0 && sub.value.checks.every((c) => c.status === 'passed' || c.status === 'warned' || c.status === 'skipped'),
);
// Four eyes (docs/ARCHITECTURE.md Q9): a reviewer does not decide on their
// own publisher's submission; an administrator may in phase 1, and the audit
// log says so.
const fourEyes = computed(() => !!sub.value && isMemberOf(me.value, sub.value.publisher.id) && !isAdmin.value);
const canDecide = computed(() => sub.value?.status === 'needs_review' && checksDone.value && !fourEyes.value);

const dialog = ref<'' | 'approve' | 'reject' | 'comment' | 'withdraw'>('');
const busy = ref(false);
const actionError = ref('');
const actionDone = ref('');
const approveNote = ref('');
const approveSecurity = ref(false);

async function act(fn: () => Promise<unknown>, done = '') {
  busy.value = true;
  actionError.value = '';
  actionDone.value = '';
  try {
    await fn();
    dialog.value = '';
    if (done) actionDone.value = done;
    await load(true);
  } catch (e) {
    actionError.value = e instanceof ApiError ? `${e.message} (${e.code})` : String(e);
  } finally {
    busy.value = false;
  }
}
function open(d: typeof dialog.value) {
  actionError.value = '';
  approveNote.value = '';
  approveSecurity.value = false;
  dialog.value = d;
}
const doWithdraw = () => act(() => withdrawSubmission(id.value));
const doRetry = () => act(() => retrySubmission(id.value));
const doRecheck = () => act(() => recheckSubmission(id.value));
const doReject = (reason: string) => act(() => rejectSubmission(id.value, reason));
const doComment = (reason: string) => act(() => commentSubmission(id.value, reason));
async function doApprove() {
  let serial = 0;
  await act(async () => {
    const r = await approveSubmission(id.value, { note: approveNote.value.trim() || undefined, security: approveSecurity.value });
    serial = r.index_serial;
  });
  if (serial) actionDone.value = fmt(S.submission.approved, { serial });
}
// Where the submission is on its way: queued, reading the repository, dry
// runs, review, decided. A failure is drawn on the stage that failed.
const stage = computed(() => {
  const s = sub.value;
  if (!s) return { at: 0, failed: false };
  switch (s.status) {
    case 'queued':
      return { at: 0, failed: false };
    case 'resolving':
      return { at: 1, failed: false };
    case 'validating':
      return { at: 2, failed: false };
    case 'needs_review':
      return { at: 3, failed: false };
    case 'failed':
      return { at: s.checks.some((c) => c.kind === 'dry_run' && c.status === 'failed') ? 2 : 1, failed: true };
    default:
      return { at: 4, failed: false };
  }
});
const stageState = (i: number) =>
  i < stage.value.at || (i === 4 && stage.value.at === 4) ? 'is-done' : i === stage.value.at ? (stage.value.failed ? 'is-failed' : 'is-current') : '';
const asCheck = (r: unknown) => r as Check;
const asRun = (r: unknown) => r as DryRunResult;
</script>

<template>
  <div class="fa-page">
    <div class="fa-pagehead">
      <span class="fa-pagehead__icon" aria-hidden="true"><ClipboardList /></span>
      <div class="fa-pagehead__text">
        <h1 data-testid="submission-title">{{ notFound ? S.common.notFoundTitle : fmt(S.submission.title, { id }) }}</h1>
        <p v-if="sub" class="fa-pagehead__lead">{{ what }} · {{ fmt(S.submission.attempt, { n: sub.attempt }) }}</p>
      </div>
      <div v-if="sub" class="fa-pagehead__actions">
        <StatusBadge :label="S.submissionStatus[sub.status]" :tone="submissionTone(sub.status)" data-testid="submission-status" />
      </div>
    </div>
    <PageState :loading="loading && !sub" :error="error" :not-found="notFound" @retry="load()">
      <template v-if="sub">
        <div class="fa-panel fa-panel--soft">
          <ol class="fa-progress" :aria-label="S.submission.progressLabel" data-testid="submission-progress">
            <li v-for="(name, i) in S.submission.stages" :key="name" :class="stageState(i)" :aria-current="stageState(i) === 'is-current' || stageState(i) === 'is-failed' ? 'step' : undefined">
              <CheckIcon v-if="stageState(i) === 'is-done'" aria-hidden="true" />
              <CircleX v-else-if="stageState(i) === 'is-failed'" aria-hidden="true" />
              <LoaderCircle v-else-if="stageState(i) === 'is-current' && inFlight(sub.status)" aria-hidden="true" />
              <CircleCheck v-else-if="stageState(i) === 'is-current'" aria-hidden="true" />
              <CircleDashed v-else aria-hidden="true" />
              {{ name }}
            </li>
          </ol>
          <p class="fa-lead">{{ S.submissionStatusHelp[sub.status] }}</p>
          <p v-if="inFlight(sub.status)" class="fa-note" role="status" data-testid="submission-live">
            {{ S.submission.live }} {{ fmt(S.submission.liveUpdated, { time: when(lastRead) }) }}
          </p>
        </div>

        <div v-if="sub.failure" class="fa-callout fa-callout--danger fa-callout--row fa-callout--strong" role="alert" data-testid="submission-failure">
          <CircleAlert class="fa-callout__icon" aria-hidden="true" />
          <div class="fa-callout__body">
            <p class="fa-callout__title">{{ S.submission.failure }}</p>
            <p v-if="sub.failure.code" data-testid="failure-explained">{{ explainCode(sub.failure.code) }}</p>
            <p class="fa-note"><span class="fa-code">{{ sub.failure.code }}</span> {{ sub.failure.message }}</p>
          </div>
        </div>
        <p v-if="actionDone" class="fa-callout fa-callout--ok" role="status" data-testid="action-done">{{ actionDone }}</p>
        <p v-if="actionError && !dialog" class="fa-callout fa-callout--danger" role="alert" data-testid="action-error">{{ actionError }}</p>

        <section
          v-if="(isOwnPublisher && undecided(sub.status)) || isReviewer"
          class="fa-panel"
          aria-labelledby="act-h"
          data-testid="submission-actions"
        >
          <h2 id="act-h">{{ S.submission.actions }}</h2>
          <div class="fa-actions">
            <template v-if="isOwnPublisher">
              <button v-if="sub.status === 'failed'" type="button" class="fe-btn fe-btn--primary" :disabled="busy" data-testid="retry" @click="doRetry">
                {{ S.submission.retry }}
              </button>
              <button v-if="undecided(sub.status)" type="button" class="fe-btn" :disabled="busy" data-testid="withdraw" @click="open('withdraw')">
                {{ S.submission.withdraw }}
              </button>
            </template>
            <template v-if="isReviewer">
              <button type="button" class="fe-btn fe-btn--primary" :disabled="busy || !canDecide" data-testid="approve" @click="open('approve')">
                {{ S.submission.approve }}
              </button>
              <button type="button" class="fe-btn fe-btn--danger" :disabled="busy || sub.status !== 'needs_review' || fourEyes" data-testid="reject" @click="open('reject')">
                {{ S.submission.reject }}
              </button>
              <button type="button" class="fe-btn" :disabled="busy" data-testid="comment" @click="open('comment')">{{ S.submission.comment }}</button>
              <button v-if="sub.status === 'needs_review'" type="button" class="fe-btn" :disabled="busy" data-testid="recheck" @click="doRecheck">
                {{ S.submission.recheck }}
              </button>
            </template>
          </div>
          <p v-if="isReviewer && fourEyes" class="fa-note">{{ S.submission.fourEyes }}</p>
          <p v-else-if="isReviewer && sub.status === 'needs_review' && !checksDone" class="fa-note">{{ S.submission.notReady }}</p>
        </section>

        <section class="fa-panel" aria-labelledby="det-h">
        <h2 id="det-h">{{ S.submission.details }}</h2>
        <dl class="fa-dl">
          <dt>{{ S.submission.publisher }}</dt>
          <dd><RouterLink :to="{ name: 'publisher', params: { id: sub.publisher.id } }">{{ sub.publisher.name }}</RouterLink></dd>
          <dt>{{ S.submission.repository }}</dt>
          <dd><a :href="github.repo(sub.repo)" rel="noopener noreferrer">{{ sub.repo }}</a></dd>
          <dt>{{ S.submission.tag }}</dt>
          <dd><a :href="github.tag(sub.repo, sub.ref)" rel="noopener noreferrer">{{ sub.ref }}</a></dd>
          <template v-if="sub.submitted_by">
            <dt>{{ S.submission.submittedBy }}</dt>
            <dd>{{ sub.submitted_by }}</dd>
          </template>
          <dt>{{ S.submission.created }}</dt>
          <dd>{{ when(sub.created_at) }}</dd>
          <dt>{{ S.submission.updated }}</dt>
          <dd>{{ when(sub.updated_at) }}</dd>
          <template v-if="sub.decided_at">
            <dt>{{ S.submission.decided }}</dt>
            <dd>{{ when(sub.decided_at) }}</dd>
          </template>
          <template v-if="sub.note">
            <dt>{{ S.submission.note }}</dt>
            <dd class="fa-prose">{{ sub.note }}</dd>
          </template>
        </dl>
        </section>

        <section class="fa-section" aria-labelledby="checks-h">
          <h2 id="checks-h">{{ S.submission.checks }}</h2>
          <DataTable
            table-id="store.submission.checks"
            :columns="checkColumns"
            :rows="sub.checks"
            :row-key="(r: Check) => `${r.kind}:${r.validator ?? ''}:${r.started_at}`"
            :empty="S.submission.checksEmpty"
            :aria-label="S.submission.checks"
            data-testid="checks-table"
          >
            <template #cell-status="{ row }">
              <div class="fa-cell">
                <StatusBadge :label="S.checkStatus[asCheck(row).status]" :tone="checkTone(asCheck(row).status)" />
              </div>
            </template>
            <template #cell-code="{ row }">
              <span class="fa-code">{{ asCheck(row).code ?? '' }}</span>
            </template>
            <template #cell-message="{ row }">
              <div class="fa-cell fa-cell--stack">
                <span v-if="asCheck(row).code">{{ explainCode(asCheck(row).code) }}</span>
                <span v-if="asCheck(row).message" :class="{ 'tbl-sub': !!asCheck(row).code }">{{ asCheck(row).message }}</span>
              </div>
            </template>
          </DataTable>
        </section>

        <section class="fa-section" aria-labelledby="dry-h">
          <h2 id="dry-h">{{ S.submission.dryRuns }}</h2>
          <p class="fa-note">{{ S.submission.dryRunsIntro }}</p>
          <DataTable
            table-id="store.submission.dryruns"
            :columns="dryColumns"
            :rows="sub.dry_runs"
            row-key="validator"
            :empty="S.submission.dryRunsEmpty"
            :aria-label="S.submission.dryRuns"
            data-testid="dryruns-table"
          >
            <template #cell-ok="{ row }">
              <div class="fa-cell">
                <StatusBadge
                  :label="(asRun(row).ok ? S.checkStatus.passed : S.checkStatus.failed) + (asRun(row).compat_ok === false ? ` (${S.submission.compatNo})` : '')"
                  :tone="asRun(row).ok ? 'ok' : 'danger'"
                />
              </div>
            </template>
            <template #cell-manifest="{ row }">
              <span class="fa-code" :title="asRun(row).manifest_sha256">{{ shortHash(asRun(row).manifest_sha256, 8) }}</span>
            </template>
            <template #cell-wasm="{ row }">
              <span class="fa-code" :title="asRun(row).wasm_sha256">{{ shortHash(asRun(row).wasm_sha256, 8) }}</span>
            </template>
            <template #cell-code="{ row }">
              <div class="fa-cell fa-cell--stack">
                <span v-if="asRun(row).code">{{ explainCode(asRun(row).code) }}</span>
                <span v-if="asRun(row).message || asRun(row).code" class="tbl-sub">
                  <span v-if="asRun(row).code" class="fa-code">{{ asRun(row).code }}</span> {{ asRun(row).message }}
                </span>
              </div>
            </template>
            <template #cell-ui="{ row }">
              <span class="fa-code" :title="asRun(row).ui_sha256">{{ shortHash(asRun(row).ui_sha256, 8) }}</span>
            </template>
          </DataTable>
        </section>

        <section v-if="permissions.length" class="fa-section" aria-labelledby="sperm-h">
          <h2 id="sperm-h">{{ S.submission.permissions }}</h2>
          <DataTable
            table-id="store.submission.permissions"
            :columns="permColumns"
            :rows="permissions"
            row-key="id"
            :aria-label="S.submission.permissions"
          >
            <template #cell-id="{ row }">
              <span class="fa-code">{{ (row as PermissionRow).id }}</span>
            </template>
          </DataTable>
        </section>

        <section v-if="sub.diff || (res && sub.status !== 'queued')" class="fa-panel" aria-labelledby="diff-h">
          <h2 id="diff-h">{{ S.submission.diffTitle }}</h2>
          <p class="fa-note">{{ sub.diff?.from ? fmt(S.submission.diff, { version: sub.diff.from }) : S.submission.diffFirst }}</p>
          <dl v-if="sub.diff" class="fa-dl" data-testid="diff">
            <dt>{{ S.submission.diffAdded }}</dt>
            <dd>
              <ul v-if="sub.diff.permissions_added?.length" class="fa-chips">
                <li v-for="p in sub.diff.permissions_added" :key="p" class="fa-chip fa-code">+ {{ p }}</li>
              </ul>
              <span v-else>{{ S.common.none }}</span>
            </dd>
            <dt>{{ S.submission.diffRemoved }}</dt>
            <dd>
              <ul v-if="sub.diff.permissions_removed?.length" class="fa-chips">
                <li v-for="p in sub.diff.permissions_removed" :key="p" class="fa-chip fa-code">− {{ p }}</li>
              </ul>
              <span v-else>{{ S.common.none }}</span>
            </dd>
            <dt>{{ S.submission.diffRange }}</dt>
            <dd>{{ sub.diff.filex_from || S.common.any }} → {{ sub.diff.filex_to || S.common.any }}</dd>
            <dt>{{ S.submission.diffModule }}</dt>
            <dd>{{ sub.diff.module_changed ? S.common.yes : S.common.no }}</dd>
            <dt>{{ S.submission.diffBundle }}</dt>
            <dd>{{ sub.diff.ui_changed ? S.common.yes : S.common.no }}</dd>
          </dl>
          <p class="fa-actions">
            <a :href="github.tag(sub.repo, sub.ref)" rel="noopener noreferrer">{{ S.submission.tagOnGithub }}</a>
            <a v-if="fromRef" :href="github.compare(sub.repo, fromRef, sub.ref)" rel="noopener noreferrer" data-testid="compare-link">{{ S.submission.compareOnGithub }}</a>
          </p>
        </section>

        <section v-if="res" class="fa-panel" aria-labelledby="res-h">
          <h2 id="res-h">{{ S.submission.resolution }}</h2>
          <dl class="fa-dl" data-testid="resolution">
            <template v-if="res.commit_sha">
              <dt>{{ S.submission.commit }}</dt>
              <dd><a :href="github.commit(sub.repo, res.commit_sha)" rel="noopener noreferrer" class="fa-code">{{ res.commit_sha }}</a></dd>
              <dt>{{ S.submission.tagVerified }}</dt>
              <dd>{{ res.tag_verified ? S.submission.tagVerifiedYes : S.submission.tagVerifiedNo }}</dd>
            </template>
            <template v-if="res.name">
              <dt>{{ S.submission.name }}</dt>
              <dd class="fa-code">{{ res.name }}</dd>
            </template>
            <template v-if="res.version">
              <dt>{{ S.submission.version }}</dt>
              <dd>{{ res.version }}</dd>
            </template>
            <template v-if="res.kind">
              <dt>{{ S.submission.kind }}</dt>
              <dd>{{ S.kind[res.kind] }}</dd>
            </template>
            <dt>{{ S.submission.range }}</dt>
            <dd>{{ res.filex_range || S.common.any }}</dd>
            <template v-if="res.manifest_sha256">
              <dt>{{ S.submission.manifestSha }}</dt>
              <dd><CopyField :value="res.manifest_sha256" :label="`${S.common.copy}: ${S.submission.manifestSha}`" /></dd>
            </template>
            <template v-if="res.wasm">
              <dt>{{ S.submission.module }}</dt>
              <dd><CopyField :value="res.wasm.sha256" :label="`${S.common.copy}: ${S.submission.module}`" /> · {{ bytes(res.wasm.size) }}</dd>
            </template>
            <template v-if="res.ui">
              <dt>{{ S.submission.bundle }}</dt>
              <dd><CopyField :value="res.ui.sha256" :label="`${S.common.copy}: ${S.submission.bundle}`" /> · {{ bytes(res.ui.size) }}</dd>
            </template>
            <!-- A storage plugin's builds, each held to the feed's sha256. -->
            <template v-for="(b, plat) in res.binaries ?? {}" :key="plat">
              <dt>{{ fmt(S.app.build, { platform: String(plat) }) }}</dt>
              <dd :data-testid="`res-build-${plat}`"><CopyField :value="b.sha256" :label="`${S.common.copy}: ${fmt(S.app.build, { platform: String(plat) })}`" /> · {{ bytes(b.size) }}</dd>
            </template>
            <template v-if="res.feed_notes">
              <dt>{{ S.submission.feedNotes }}</dt>
              <dd><pre class="fa-pre" data-testid="res-feed-notes">{{ res.feed_notes }}</pre></dd>
            </template>
            <template v-if="res.license">
              <dt>{{ S.submission.license }}</dt>
              <dd>{{ res.license }}</dd>
            </template>
            <template v-if="safeHref(res.homepage)">
              <dt>{{ S.submission.homepage }}</dt>
              <dd><a :href="safeHref(res.homepage)" rel="noopener noreferrer">{{ res.homepage }}</a></dd>
            </template>
          </dl>
          <template v-if="consent">
            <h3>{{ S.submission.listing }}</h3>
            <dl class="fa-dl" data-testid="listing">
              <template v-if="consentSummary">
                <dt>{{ S.submission.listingSummary }}</dt>
                <dd>{{ consentSummary }}</dd>
              </template>
              <template v-if="consentCategories.length">
                <dt>{{ S.submission.listingCategories }}</dt>
                <dd>{{ consentCategories.join(', ') }}</dd>
              </template>
              <template v-if="typeof consent.icon === 'string'">
                <dt>{{ S.submission.listingIcon }}</dt>
                <dd class="fa-code">{{ consent.icon }}</dd>
              </template>
              <template v-if="consentShots.length">
                <dt>{{ S.submission.listingScreenshots }}</dt>
                <dd class="fa-code">{{ consentShots.join(', ') }}</dd>
              </template>
              <template v-if="typeof consent.description === 'string'">
                <dt>{{ S.submission.listingDescription }}</dt>
                <dd class="fa-code">{{ consent.description }}</dd>
              </template>
              <template v-if="consentConversions.length">
                <dt>{{ S.submission.listingConversions }}</dt>
                <dd data-testid="listing-conversions">
                  <p>{{ fmt(S.submission.listingConversionsCount, { entries: consentConversions.length, pairs: conversionPairs }) }}</p>
                  <details class="fa-disclosure">
                    <summary>
                      <ChevronDown class="fa-disclosure__chev" aria-hidden="true" />
                      <span class="fa-disclosure__title">{{ S.submission.listingConversionsShow }}</span>
                    </summary>
                    <div class="fa-disclosure__body">
                      <ul class="fa-declared">
                        <li v-for="(c, i) in consentConversions" :key="i">{{ declarationLine(c) }}</li>
                      </ul>
                    </div>
                  </details>
                </dd>
              </template>
            </dl>
          </template>
          <template v-if="descriptionMd">
            <h3>{{ S.submission.descriptionText }}</h3>
            <pre class="fa-pre" tabindex="0" data-testid="description-md">{{ descriptionMd }}</pre>
          </template>
        </section>

        <section class="fa-section" aria-labelledby="rev-h">
          <h2 id="rev-h">{{ S.submission.reviews }}</h2>
          <DataTable
            table-id="store.submission.reviews"
            :columns="reviewColumns"
            :rows="sub.reviews"
            row-key="id"
            :empty="S.submission.reviewsEmpty"
            :aria-label="S.submission.reviews"
            data-testid="reviews-table"
          />
        </section>

        <ActionDialog
          :open="dialog === 'approve'"
          :title="fmt(S.submission.approveTitle, { what })"
          :intro="S.submission.approveIntro"
          :confirm-label="S.submission.approve.replace('…', '')"
          :busy="busy"
          :error="actionError"
          @confirm="doApprove"
          @cancel="dialog = ''"
        >
          <div class="fe-field">
            <label class="fe-field__label" for="ap-note">{{ S.submission.approveNote }}</label>
            <textarea id="ap-note" v-model="approveNote" class="fe-input" rows="3" maxlength="8000" data-testid="approve-note"></textarea>
          </div>
          <label class="fe-checkbox">
            <input v-model="approveSecurity" type="checkbox" data-testid="approve-security" />
            <span>{{ S.submission.approveSecurity }}</span>
          </label>
        </ActionDialog>
        <ActionDialog
          :open="dialog === 'reject'"
          :title="fmt(S.submission.rejectTitle, { what })"
          :intro="S.submission.rejectIntro"
          :confirm-label="S.submission.reject.replace('…', '')"
          reason
          danger
          :busy="busy"
          :error="actionError"
          @confirm="doReject"
          @cancel="dialog = ''"
        />
        <ActionDialog
          :open="dialog === 'comment'"
          :title="fmt(S.submission.commentTitle, { what })"
          :intro="S.submission.commentIntro"
          :confirm-label="S.submission.comment.replace('…', '')"
          :reason-label="S.submission.colNote"
          reason
          :busy="busy"
          :error="actionError"
          @confirm="doComment"
          @cancel="dialog = ''"
        />
        <ActionDialog
          :open="dialog === 'withdraw'"
          :title="S.submission.withdraw"
          :intro="S.submission.withdrawConfirm"
          :confirm-label="S.submission.withdraw"
          danger
          :busy="busy"
          :error="actionError"
          @confirm="doWithdraw"
          @cancel="dialog = ''"
        />
      </template>
    </PageState>
  </div>
</template>
