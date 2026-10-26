<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import DOMPurify from 'dompurify';
import { Ban, ChevronDown, Clock, Download, Github, HardDrive, KeyRound, Languages, OctagonAlert, Puzzle, Scale, Settings, ShieldCheck, ShoppingCart, Tag, TriangleAlert, Wrench } from 'lucide-vue-next';
import { DataTable, type DataColumn } from '@brftech/filex-core';
import {
  ApiError,
  adminUpdateApp,
  getApp,
  getAppVersion,
  listCategories,
  revokeApp,
  unrevokeApp,
  unyankVersion,
  yankVersion,
  type AppDetail,
  type Category,
  type ConversionRow,
  type IndexArtifact,
  type VersionDetail,
  type VersionSummary,
} from '@/api/client';
import { S, fmt } from '@/strings';
import { bareUrl, bytes, github, languageName, mediaPath, safeHref, shortHash, text, toneOf, when } from '@/lib/format';
import { hasRole, isMemberOf, session } from '@/lib/session';
import { conversionCells, dotted, useKey, useKind, usePhrases } from '@/lib/fileTypes';
import { setTitle } from '@/router';
import AppIcon from '@/components/AppIcon.vue';
import ActionDialog from '@/components/ActionDialog.vue';
import InstallDialog from '@/components/InstallDialog.vue';
import CopyButton from '@/components/CopyButton.vue';
import CopyField from '@/components/CopyField.vue';
import PageState from '@/components/PageState.vue';
import PermissionList from '@/components/PermissionList.vue';
import PublisherBadges from '@/components/PublisherBadges.vue';
import ScreenshotGallery from '@/components/ScreenshotGallery.vue';
import StatusBadge from '@/components/StatusBadge.vue';

const route = useRoute();
const router = useRouter();
const name = computed(() => String(route.params.name ?? ''));
const wanted = computed(() => (route.params.version ? String(route.params.version) : ''));

const app = ref<AppDetail | null>(null);
const version = ref<VersionDetail | null>(null);
const loading = ref(true);
const error = ref('');
const notFound = ref(false);
const versionMissing = ref(false);
const categories = ref<Category[]>([]);
const isAdmin = computed(() => hasRole(session.me, 'admin'));

/** The version this page shows: the address's, else the newest one not yanked. */
const selected = computed(() => {
  const a = app.value;
  if (!a) return '';
  if (wanted.value) return wanted.value;
  return a.latest || a.versions.find((v) => !v.yanked)?.version || a.versions[0]?.version || '';
});

let seq = 0;
async function load() {
  const mine = ++seq;
  loading.value = true;
  error.value = '';
  notFound.value = false;
  versionMissing.value = false;
  try {
    const a = await getApp(name.value);
    if (mine !== seq) return;
    app.value = a;
    setTitle(text(a.label) || a.name);
    await loadVersion(mine);
  } catch (e) {
    if (mine !== seq) return;
    app.value = null;
    if (e instanceof ApiError && e.status === 404) notFound.value = true;
    else error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    if (mine === seq) loading.value = false;
  }
}

async function loadVersion(mine = seq) {
  version.value = null;
  versionMissing.value = false;
  if (!selected.value) return;
  try {
    const v = await getAppVersion(name.value, selected.value);
    if (mine === seq) version.value = v;
  } catch (e) {
    if (mine !== seq) return;
    if (e instanceof ApiError && e.status === 404) versionMissing.value = true;
    else error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  }
}

watch(name, () => void load(), { immediate: true });
watch(wanted, () => {
  if (app.value && app.value.name === name.value) void loadVersion();
});

void listCategories()
  .then((r) => (categories.value = r.items))
  .catch(() => undefined);
const categoryLabel = (id: string) => text(categories.value.find((c) => c.id === id)?.label) || id;

const label = computed(() => (app.value ? text(app.value.label) || app.value.name : ''));
// The server renders and sanitizes the description (no script, forms or
// frames); it is sanitized once more here before it touches the page.
const descriptionHtml = computed(() =>
  app.value?.description_html ? DOMPurify.sanitize(app.value.description_html, { USE_PROFILES: { html: true } }) : '',
);
const screenshots = computed(() =>
  (app.value?.screenshots ?? [])
    .map((m, i) => ({ src: mediaPath(m.url), caption: text(m.caption), alt: text(m.caption) || fmt(S.app.screenshotAlt, { n: i + 1, app: label.value }) }))
    .filter((m) => m.src),
);
const revoked = computed(() => app.value?.revoked ?? null);
const yanked = computed(() => version.value?.yanked ?? null);
const installable = computed(() => !!version.value && !revoked.value && !yanked.value);
const notLatest = computed(() => !!app.value && !!wanted.value && !!app.value.latest && wanted.value !== app.value.latest);
const paid = computed(() => app.value?.pricing?.model === 'paid');
const purchaseHref = computed(() => (paid.value ? safeHref(app.value?.pricing?.purchase_url) : ''));
// The price and the license keys are managed by the app's publisher's
// members and by administrators (the server checks it again).
const canManage = computed(() => !!app.value && (isAdmin.value || isMemberOf(session.me, app.value.publisher.id)));

// ---------------------------------------------------------------- install on my filex
// Signed out, Install goes to the sign-in and comes back with ?install=1,
// which opens the dialog once the session and the app are known.
const installOpen = ref(false);
function openInstall() {
  if (!session.me) {
    void router.push({ name: 'sign-in', query: { return_to: `${route.path}?install=1` } });
    return;
  }
  installOpen.value = true;
}
watch(
  [() => route.query.install, () => session.me, installable],
  ([want, me, can]) => {
    if (want !== '1' || !me || !can) return;
    installOpen.value = true;
    const query = { ...route.query };
    delete query.install;
    void router.replace({ query });
  },
  { immediate: true },
);

// The kinds of file the shown version works with (the server's reading of
// its manifest); an older server's answer without them reads as none.
const worksWith = computed(() => version.value?.file_types ?? []);
// What it converts, kind by kind (its listing's declaration, read by the
// server); an older server's answer without it reads as none.
const conversions = computed<ConversionRow[]>(() => version.value?.conversions ?? []);
const conversionsTitle = computed(() =>
  conversions.value.length === 1 ? S.fileTypes.conversionsShowOne : fmt(S.fileTypes.conversionsShow, { count: conversions.value.length }),
);
const convColumns = computed<DataColumn<ConversionRow>[]>(() => [
  { id: 'from', label: S.fileTypes.colFrom, width: 170, min: 110, lead: true, sortable: true, format: (r) => conversionCells(r).from },
  { id: 'to', label: S.fileTypes.colTo, width: 360, min: 140, format: (r) => conversionCells(r).to },
  { id: 'engine', label: S.fileTypes.colWithEngine, width: 520, min: 160, format: (r) => conversionCells(r).withEngine },
]);
const asConv = (r: unknown) => r as ConversionRow;
const convKey = (r: unknown) => asConv(r).from.join(',');

const permCount = computed(() => {
  const n = version.value?.permissions.length ?? 0;
  return n === 0 ? S.app.noPermissions : n === 1 ? S.app.permissionsOne : fmt(S.app.permissionsCount, { count: n });
});

// A storage plugin: a program filex runs outside any sandbox. Its page
// shows its builds and what the store's plugin validator measured where an
// app's shows its permissions.
const isStorage = computed(() => app.value?.kind === 'storage');
const conformance = computed(() => version.value?.conformance ?? null);
const conformanceLine = computed(() => {
  const c = conformance.value;
  return c ? fmt(S.app.conformanceLine, { filex: c.filex, platform: c.platform, passed: c.passed, skipped: c.skipped }) : '';
});
const proved = computed(() => (conformance.value?.capabilities ?? []).map((c) => ({ id: c, label: S.app.capability[c] ?? c })));
const platforms = computed(() => (version.value?.builds ?? []).map((b) => b.platform));
// The store ran one build; the others are pinned and signed only.
const othersNotRun = computed(() => {
  const c = conformance.value;
  return !!c && platforms.value.some((p) => p !== c.platform);
});

// ---------------------------------------------------------------- tables
interface ArtifactRow {
  key: string;
  what: string;
  a: IndexArtifact;
}
const artifacts = computed<ArtifactRow[]>(() => {
  const v = version.value;
  if (!v) return [];
  if (v.builds) {
    // A storage plugin: its release's feed, then every build it names.
    const rows: ArtifactRow[] = [{ key: 'manifest', what: S.app.feed, a: v.manifest }];
    for (const b of v.builds) rows.push({ key: `build-${b.platform}`, what: fmt(S.app.build, { platform: b.platform }), a: b });
    return rows;
  }
  const rows: ArtifactRow[] = [{ key: 'manifest', what: S.app.manifest, a: v.manifest }];
  if (v.wasm) rows.push({ key: 'wasm', what: S.app.module, a: v.wasm });
  if (v.ui) rows.push({ key: 'ui', what: S.app.bundle, a: v.ui });
  return rows;
});
const artColumns = computed<DataColumn<ArtifactRow>[]>(() => [
  { id: 'what', label: S.app.colArtifact, width: 150, lead: true },
  { id: 'sha', label: S.app.colSha, width: 260, min: 150, format: (r) => r.a.sha256 },
  { id: 'size', label: S.app.colSize, width: 100, align: 'right', format: (r) => (r.a.size != null ? bytes(r.a.size) : '') },
  { id: 'sig', label: S.app.colSig, width: 220, min: 140, format: (r) => r.a.sig },
  { id: 'url', label: S.app.colAddress, width: 280, format: (r) => r.a.url },
]);

const verColumns = computed<DataColumn<VersionSummary>[]>(() => [
  { id: 'version', label: S.app.colVersion, width: 140, lead: true },
  { id: 'published', label: S.app.colPublished, width: 150, format: (r) => when(r.published_at, false) },
  { id: 'filex', label: S.app.colRange, width: 160, format: (r) => r.filex || S.common.any },
  {
    id: 'state',
    label: S.app.colState,
    width: 200,
    format: (r) => [r.yanked ? S.app.stateYanked : r.version === app.value?.latest ? S.app.stateCurrent : '', r.security ? S.app.stateSecurity : ''].filter(Boolean).join(', '),
  },
]);
const versionActions = computed(() =>
  isAdmin.value
    ? (r: VersionSummary) => [
        r.yanked
          ? { key: 'unyank', label: S.app.adminUnyank }
          : { key: 'yank', label: S.app.adminYank, danger: true },
      ]
    : undefined,
);

// ---------------------------------------------------------------- admin
const dialog = ref<'' | 'revoke' | 'yank'>('');
const dialogVersion = ref('');
const busy = ref(false);
const actionError = ref('');
const actionDone = ref('');
const pickedCategories = ref<string[]>([]);
watch(app, (a) => (pickedCategories.value = a ? [...a.categories] : []));

async function act(fn: () => Promise<unknown>) {
  busy.value = true;
  actionError.value = '';
  actionDone.value = '';
  try {
    await fn();
    dialog.value = '';
    actionDone.value = S.app.adminDone;
    await load();
  } catch (e) {
    actionError.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
const toggleListed = () =>
  act(() => adminUpdateApp(name.value, { status: app.value?.status === 'unlisted' ? 'listed' : 'unlisted' }));
const saveCategories = () => act(() => adminUpdateApp(name.value, { categories: pickedCategories.value.slice(0, 3) }));
const doRevoke = (reason: string) => act(() => revokeApp(name.value, reason));
const doUnrevoke = () => act(() => unrevokeApp(name.value));
const doYank = (reason: string) => act(() => yankVersion(name.value, dialogVersion.value, reason));
function onVersionAction(key: string, row: VersionSummary) {
  if (key === 'yank') {
    dialogVersion.value = row.version;
    actionError.value = '';
    dialog.value = 'yank';
  } else if (key === 'unyank') void act(() => unyankVersion(name.value, row.version));
}

function openVersion(row: VersionSummary, ev: MouseEvent) {
  if ((ev.target as HTMLElement | null)?.closest('a, button')) return;
  void router.push({ name: 'app-version', params: { name: name.value, version: row.version } });
}
const asVer = (r: unknown) => r as VersionSummary;
// The page always has its heading (the keyboard and screen readers land on
// it), even while it loads or when there is nothing here.
const heading = computed(() => (app.value ? label.value : notFound.value ? S.common.notFoundTitle : name.value));
const asArt = (r: unknown) => r as ArtifactRow;
</script>

<template>
  <div class="fa-page">
    <section class="fa-hero" :class="`fa-tone-${toneOf(name)}`" aria-labelledby="app-h">
      <div class="fa-apphead">
        <AppIcon v-if="app" :url="app.icon_url" :name="label" :size="88" />
        <div class="fa-apphead__text">
          <h1 id="app-h" data-testid="app-title">{{ heading }}</h1>
          <template v-if="app">
            <p class="fa-apphead__by">
              <span>
                {{ S.app.by }}
                <RouterLink :to="{ name: 'publisher', params: { id: app.publisher.id } }">{{ app.publisher.name }}</RouterLink>
              </span>
              <PublisherBadges :verified="app.publisher.verified" :official="app.publisher.official" />
              <code class="fa-code fa-muted">{{ app.name }}</code>
            </p>
            <p v-if="text(app.summary)" class="fa-apphead__summary">{{ text(app.summary) }}</p>
            <ul class="fa-apphead__meta">
              <li class="fa-chip">
                <Languages v-if="app.kind === 'language_pack'" aria-hidden="true" />
                <HardDrive v-else-if="app.kind === 'storage'" aria-hidden="true" />
                <Puzzle v-else aria-hidden="true" />
                {{ S.kind[app.kind] }}
              </li>
              <li v-if="app.kind === 'language_pack' && version && version.languages.length" class="fa-chip" data-testid="app-provides">
                {{ fmt(S.app.provides, { languages: version.languages.map((l) => languageName(l.code)).join(', ') }) }}
              </li>
              <li v-if="app.latest" class="fa-chip">{{ fmt(S.catalog.latestShort, { version: app.latest }) }}</li>
              <li v-if="paid" class="fa-chip" data-testid="app-paid">
                <Tag aria-hidden="true" />{{ app.pricing?.price_text ? `${S.pricing.paid}: ${app.pricing.price_text}` : S.pricing.paid }}
              </li>
              <li v-if="app.license" class="fa-chip"><Scale aria-hidden="true" />{{ app.license }}</li>
              <li class="fa-chip"><Clock aria-hidden="true" />{{ fmt(S.catalog.updatedOn, { date: when(app.updated_at, false) }) }}</li>
              <li v-for="c in app.categories" :key="c" class="fa-chip">{{ categoryLabel(c) }}</li>
            </ul>
            <div class="fa-actions">
              <button v-if="installable" type="button" class="fe-btn fe-btn--primary fe-btn--lg" :title="S.install.openHint" data-testid="install-open" @click="openInstall">
                <Download class="fa-icon" aria-hidden="true" />{{ S.install.open }}
              </button>
              <a v-if="purchaseHref" :href="purchaseHref" class="fe-btn fe-btn--lg" rel="noopener noreferrer" data-testid="app-buy">
                <ShoppingCart class="fa-icon" aria-hidden="true" />{{ S.pricing.buy }}
              </a>
              <a v-if="installable" href="#install" class="fe-btn fe-btn--lg" data-testid="install-jump">
                <Wrench class="fa-icon" aria-hidden="true" />{{ S.install.byHand }}
              </a>
              <a :href="github.repo(app.repo)" class="fe-btn fe-btn--lg" rel="noopener noreferrer">
                <Github class="fa-icon" aria-hidden="true" />{{ S.app.viewRepo }}
              </a>
              <RouterLink v-if="canManage" :to="{ name: 'manage-app', params: { name: app.name } }" class="fe-btn fe-btn--lg" data-testid="app-manage">
                <KeyRound class="fa-icon" aria-hidden="true" />{{ S.licensing.manage }}
              </RouterLink>
            </div>
            <p v-if="paid" class="fa-note" data-testid="app-paid-note">{{ S.pricing.paidHint }}</p>
          </template>
        </div>
      </div>
    </section>

    <PageState :loading="loading && !app" :error="error" :not-found="notFound" @retry="load">
      <template v-if="app">
        <div v-if="revoked" class="fa-callout fa-callout--danger fa-callout--row fa-callout--strong" role="alert" data-testid="app-revoked">
          <Ban class="fa-callout__icon" aria-hidden="true" />
          <div class="fa-callout__body">
            <p class="fa-callout__title">{{ S.app.revokedTitle }}</p>
            <p>{{ fmt(S.app.revokedBody, { date: when(revoked.at, false), reason: revoked.reason }) }}</p>
          </div>
        </div>
        <div v-else-if="app.status === 'unlisted'" class="fa-callout fa-callout--warn fa-callout--row" data-testid="app-unlisted">
          <TriangleAlert class="fa-callout__icon" aria-hidden="true" />
          <div class="fa-callout__body"><p>{{ S.app.unlisted }}</p></div>
        </div>
        <div v-if="yanked" class="fa-callout fa-callout--danger fa-callout--row fa-callout--strong" role="alert" data-testid="version-yanked">
          <OctagonAlert class="fa-callout__icon" aria-hidden="true" />
          <div class="fa-callout__body">
            <p class="fa-callout__title">{{ S.app.yankedTitle }}</p>
            <p>{{ fmt(S.app.yankedBody, { date: when(yanked.at, false), reason: yanked.reason }) }}</p>
          </div>
        </div>
        <div v-if="versionMissing" class="fa-callout fa-callout--warn fa-callout--row">
          <TriangleAlert class="fa-callout__icon" aria-hidden="true" />
          <div class="fa-callout__body"><p>{{ fmt(S.app.versionMissing, { version: selected }) }}</p></div>
        </div>
        <div v-else-if="notLatest && !yanked" class="fa-callout fa-callout--info fa-callout--row" data-testid="app-not-latest">
          <div class="fa-callout__body">
            <p>{{ fmt(S.app.viewingOld, { version: wanted, latest: app.latest }) }}</p>
            <RouterLink :to="{ name: 'app', params: { name: app.name } }">{{ S.app.seeLatest }}</RouterLink>
          </div>
        </div>

        <!-- The side column comes first in the reading order: what to do
             with the app (install it) before everything about it. -->
        <div class="fa-columns fa-appcols">
          <div class="fa-stack fa-appside">
            <section v-if="installable && version" id="install" class="fa-panel fa-install" aria-labelledby="install-h" data-testid="install">
              <h2 id="install-h" class="fa-panel__title"><Download aria-hidden="true" />{{ S.app.install }}</h2>
              <template v-if="isStorage">
                <ol class="fa-steps" data-testid="install-steps-storage">
                  <li v-for="(step, i) in S.app.installStepsStorage" :key="i">{{ step }}</li>
                </ol>
                <div class="fa-copyrow">
                  <span class="fa-copyrow__label">{{ S.app.installSource }}</span>
                  <CopyField boxed :value="version.install.github_repo" :label="`${S.common.copy}: ${S.app.installSource}`" testid="install-source" />
                </div>
              </template>
              <template v-else>
                <ol class="fa-steps">
                  <li v-for="(step, i) in S.app.installSteps" :key="i">{{ step }}</li>
                </ol>
                <div class="fa-copyrow">
                  <span class="fa-copyrow__label">{{ S.app.installRepo }}</span>
                  <CopyField boxed :value="version.install.github_repo" :label="`${S.common.copy}: ${S.app.installRepo}`" testid="install-repo" />
                </div>
                <div class="fa-copyrow">
                  <span class="fa-copyrow__label">{{ S.app.installRef }}</span>
                  <CopyField boxed :value="version.install.ref" :label="`${S.common.copy}: ${S.app.installRef}`" testid="install-ref" />
                </div>
              </template>
            </section>
            <section v-else-if="version || revoked" class="fa-panel fa-panel--soft" aria-labelledby="noinstall-h" data-testid="not-installable">
              <h2 id="noinstall-h" class="fa-panel__title"><Ban aria-hidden="true" />{{ S.app.installNot }}</h2>
              <p class="fa-note">{{ revoked ? S.app.installNotRevoked : S.app.installNotYanked }}</p>
            </section>

            <section v-if="version" class="fa-panel" aria-labelledby="ver-h" data-testid="version-section">
              <div class="fa-panel__head">
                <h2 id="ver-h">{{ fmt(S.app.versionHeading, { version: version.version }) }}</h2>
                <span class="fa-badges">
                  <StatusBadge v-if="version.security" :label="S.app.securityFix" tone="warn" />
                  <StatusBadge v-if="yanked" :label="S.app.stateYanked" tone="danger" />
                </span>
              </div>
              <dl class="fa-dl fa-dl--card">
                <dt>{{ S.app.colPublished }}</dt>
                <dd>{{ when(version.published_at, false) }}</dd>
                <dt>{{ S.app.worksWith }}</dt>
                <dd>{{ version.filex ? `filex ${version.filex}` : S.app.anyFilex }}</dd>
                <template v-if="safeHref(version.notes_url)">
                  <dt>{{ S.app.notes }}</dt>
                  <dd><a :href="safeHref(version.notes_url)" rel="noopener noreferrer">{{ version.ref }}</a></dd>
                </template>
                <template v-if="app.license">
                  <dt>{{ S.app.license }}</dt>
                  <dd>{{ app.license }}</dd>
                </template>
                <dt>{{ S.app.repository }}</dt>
                <dd><a :href="github.repo(app.repo)" rel="noopener noreferrer">{{ app.repo }}</a></dd>
                <template v-if="safeHref(app.homepage)">
                  <dt>{{ S.app.homepage }}</dt>
                  <dd><a :href="safeHref(app.homepage)" rel="noopener noreferrer">{{ bareUrl(app.homepage ?? '') }}</a></dd>
                </template>
                <template v-if="version.languages.length">
                  <dt>{{ S.app.languages }}</dt>
                  <dd>
                    <ul class="fa-chips">
                      <li v-for="l in version.languages" :key="l.code" class="fa-chip" :lang="l.code">{{ languageName(l.code) }}</li>
                    </ul>
                  </dd>
                </template>
                <template v-if="version.engines.length">
                  <dt>{{ S.app.engines }}</dt>
                  <dd>{{ version.engines.join(', ') }}</dd>
                </template>
                <template v-if="platforms.length">
                  <dt>{{ S.app.platforms }}</dt>
                  <dd>
                    <ul class="fa-chips" data-testid="app-platforms">
                      <li v-for="p in platforms" :key="p" class="fa-chip">{{ p }}</li>
                    </ul>
                  </dd>
                </template>
              </dl>
            </section>

            <!-- The publisher as on its own page: its mirrored logo, else the
                 initial. An inline "by <publisher>" (the head, the cards)
                 stays text: there the app's icon is the picture. -->
            <section class="fa-panel" aria-labelledby="pub-h" data-testid="app-publisher">
              <h2 id="pub-h" class="fe-sr-only">{{ S.app.publisherCard }}</h2>
              <div class="fa-pubcard">
                <AppIcon :url="app.publisher.avatar_url" :name="app.publisher.name" :size="40" />
                <div class="fa-pubcard__text">
                  <RouterLink class="fa-pubcard__name" :to="{ name: 'publisher', params: { id: app.publisher.id } }">{{ app.publisher.name }}</RouterLink>
                  <PublisherBadges :verified="app.publisher.verified" :official="app.publisher.official" />
                </div>
              </div>
              <RouterLink :to="{ name: 'catalog', query: { publisher: app.publisher.id } }">{{ fmt(S.app.moreFrom, { name: app.publisher.name }) }}</RouterLink>
            </section>

            <section v-if="isAdmin" class="fa-panel fa-panel--soft" aria-labelledby="adm-h" data-testid="app-admin">
              <h2 id="adm-h" class="fa-panel__title"><Settings aria-hidden="true" />{{ S.app.adminTitle }}</h2>
              <div class="fa-actions">
                <button v-if="app.status !== 'revoked'" type="button" class="fe-btn" :disabled="busy" data-testid="app-toggle-listed" @click="toggleListed">
                  {{ app.status === 'unlisted' ? S.app.adminList : S.app.adminUnlist }}
                </button>
                <button v-if="!revoked" type="button" class="fe-btn fe-btn--danger" :disabled="busy" data-testid="app-revoke" @click="(actionError = ''), (dialog = 'revoke')">
                  {{ S.app.adminRevoke }}
                </button>
                <button v-else type="button" class="fe-btn" :disabled="busy" data-testid="app-unrevoke" @click="doUnrevoke">{{ S.app.adminUnrevoke }}</button>
              </div>
              <fieldset class="fa-fieldset">
                <legend>{{ S.app.adminCategories }}</legend>
                <div class="fa-checks">
                  <label v-for="c in categories" :key="c.id" class="fe-checkbox">
                    <input
                      v-model="pickedCategories"
                      type="checkbox"
                      :value="c.id"
                      :disabled="!pickedCategories.includes(c.id) && pickedCategories.length >= 3"
                    />
                    <span>{{ text(c.label) }}</span>
                  </label>
                </div>
                <div>
                  <button type="button" class="fe-btn fe-btn--sm" :disabled="busy" @click="saveCategories">{{ S.app.adminSaveCategories }}</button>
                </div>
              </fieldset>
              <p v-if="actionDone" class="fa-note" role="status">{{ actionDone }}</p>
              <p v-if="actionError && !dialog" class="fe-form__error" role="alert">{{ actionError }}</p>
            </section>
          </div>

          <div class="fa-page fa-appmain">
            <section v-if="screenshots.length" class="fa-section" aria-labelledby="shots-h">
              <h2 id="shots-h">{{ S.app.screenshots }}</h2>
              <ScreenshotGallery :shots="screenshots" :label="fmt(S.app.gallery, { app: label })" />
            </section>

            <section v-if="descriptionHtml" class="fa-section" aria-labelledby="about-h">
              <h2 id="about-h">{{ S.app.description }}</h2>
              <!-- eslint-disable-next-line vue/no-v-html -- server-sanitized, then DOMPurify -->
              <div class="fa-prose" data-testid="app-description" v-html="descriptionHtml"></div>
            </section>

            <!-- The kinds of file the version works with, read by the server
                 from its manifest; each kind links to the catalog's search
                 for it. A language pack names none, so it has no section. -->
            <section v-if="version && app.kind === 'app'" class="fa-section" aria-labelledby="ft-h" data-testid="works-with">
              <h2 id="ft-h">{{ S.fileTypes.worksWithTitle }}</h2>
              <p class="fa-note">{{ S.fileTypes.worksWithIntro }}</p>
              <ul v-if="worksWith.length" class="fa-uses">
                <li v-for="u in worksWith" :key="useKey(u)" :data-testid="`works-with-${u.ext ?? (u.any_file ? 'any' : u.mime)}`">
                  <RouterLink
                    v-if="u.ext"
                    class="fa-chip fa-chip--kind"
                    :to="{ name: 'catalog', query: { q: dotted(u.ext) } }"
                    :title="fmt(S.fileTypes.searchFor, { ext: dotted(u.ext) })"
                    :aria-label="fmt(S.fileTypes.searchFor, { ext: dotted(u.ext) })"
                  >{{ useKind(u) }}</RouterLink>
                  <span v-else class="fa-chip fa-chip--kind">{{ useKind(u) }}</span>
                  <span>{{ usePhrases(u).join(', ') }}</span>
                </li>
              </ul>
              <p v-else-if="!conversions.length" class="fa-note" data-testid="works-with-none">{{ S.fileTypes.worksWithNone }}</p>
              <template v-if="conversions.length">
                <h3 id="conv-h">{{ S.fileTypes.conversionsTitle }}</h3>
                <p class="fa-note">{{ S.fileTypes.conversionsIntro }}</p>
                <details class="fa-disclosure" data-testid="conversions">
                  <summary>
                    <ChevronDown class="fa-disclosure__chev" aria-hidden="true" />
                    <span class="fa-disclosure__title">{{ conversionsTitle }}</span>
                    <span class="fa-disclosure__hint">{{ S.fileTypes.conversionsHint }}</span>
                  </summary>
                  <div class="fa-disclosure__body">
                    <DataTable
                      table-id="store.app.conversions"
                      :columns="convColumns"
                      :rows="conversions"
                      :row-key="convKey"
                      :aria-label="S.fileTypes.conversionsTitle"
                      data-testid="conversions-table"
                    >
                      <template #cell-from="{ row }">
                        <div class="fa-cell">
                          <RouterLink
                            v-for="e in asConv(row).from"
                            :key="e"
                            class="fa-chip fa-chip--kind"
                            :to="{ name: 'catalog', query: { q: dotted(e) } }"
                            :title="fmt(S.fileTypes.searchFor, { ext: dotted(e) })"
                            :aria-label="fmt(S.fileTypes.searchFor, { ext: dotted(e) })"
                          >{{ dotted(e) }}</RouterLink>
                        </div>
                      </template>
                    </DataTable>
                  </div>
                </details>
              </template>
            </section>

            <section v-if="version && isStorage" class="fa-section" aria-labelledby="storage-h" data-testid="storage-section">
              <h2 id="storage-h">{{ S.app.storageTitle }}</h2>
              <div class="fa-callout fa-callout--warn fa-callout--row" data-testid="storage-warning">
                <TriangleAlert class="fa-callout__icon" aria-hidden="true" />
                <div class="fa-callout__body">
                  <p>{{ S.app.storageWarning }}</p>
                </div>
              </div>
              <template v-if="conformance">
                <h3>{{ S.app.conformanceTitle }}</h3>
                <p data-testid="storage-conformance">{{ conformanceLine }}</p>
                <p v-if="conformance.driver" class="fa-note">{{ fmt(S.app.conformanceDriver, { driver: conformance.driver }) }}</p>
                <dl v-if="proved.length" class="fa-dl">
                  <dt>{{ S.app.conformanceProved }}</dt>
                  <dd>
                    <ul class="fa-chips">
                      <li v-for="c in proved" :key="c.id" class="fa-chip" :data-testid="`storage-cap-${c.id}`">{{ c.label }}</li>
                    </ul>
                  </dd>
                </dl>
                <p v-if="othersNotRun" class="fa-note" data-testid="storage-others">{{ fmt(S.app.conformanceOthers, { platform: conformance.platform }) }}</p>
              </template>
            </section>

            <section v-if="version && !isStorage" class="fa-section" aria-labelledby="perm-h">
              <div class="fa-section__head">
                <h2 id="perm-h">{{ S.app.permissions }}</h2>
                <span class="fa-badge fa-badge--info"><ShieldCheck aria-hidden="true" />{{ permCount }}</span>
              </div>
              <p class="fa-note">{{ S.app.permissionsIntro }}</p>
              <PermissionList v-if="version.permissions.length" :rows="version.permissions" />
              <p v-else class="fa-callout fa-callout--ok">{{ S.app.permissionsNone }}</p>
            </section>

            <section class="fa-section" aria-labelledby="vers-h">
              <h2 id="vers-h">{{ S.app.versions }}</h2>
              <DataTable
                table-id="store.app.versions"
                :columns="verColumns"
                :rows="app.versions"
                row-key="version"
                :aria-label="S.app.versions"
                :row-actions="versionActions"
                :row-class="(r: VersionSummary) => (r.version === selected ? 'is-selected' : undefined)"
                data-testid="versions-table"
                @row-click="openVersion"
                @row-action="(key: string, row: VersionSummary) => onVersionAction(key, row)"
              >
                <template #cell-version="{ row }">
                  <div class="fa-cell">
                    <RouterLink
                      :to="{ name: 'app-version', params: { name: app.name, version: asVer(row).version } }"
                      :aria-current="asVer(row).version === selected ? 'page' : undefined"
                    >{{ asVer(row).version }}</RouterLink>
                  </div>
                </template>
              </DataTable>
            </section>

            <details v-if="version" class="fa-disclosure" data-testid="integrity">
              <summary>
                <ChevronDown class="fa-disclosure__chev" aria-hidden="true" />
                <span class="fa-disclosure__title">{{ S.app.advanced }}</span>
                <span class="fa-disclosure__hint">{{ S.app.advancedHint }}</span>
              </summary>
              <div class="fa-disclosure__body">
                <p class="fa-note">{{ S.app.integrityIntro }}</p>
                <dl class="fa-dl">
                  <dt>{{ S.app.commit }}</dt>
                  <dd>
                    <a :href="github.commit(app.repo, version.commit)" rel="noopener noreferrer" class="fa-code">{{ version.commit }}</a>
                  </dd>
                </dl>
                <DataTable
                  table-id="store.app.artifacts"
                  :columns="artColumns"
                  :rows="artifacts"
                  row-key="key"
                  :aria-label="S.app.integrity"
                  data-testid="artifacts-table"
                >
                  <template #cell-sha="{ row }">
                    <div class="fa-cell">
                      <span class="fa-code" :title="asArt(row).a.sha256">{{ shortHash(asArt(row).a.sha256) }}</span>
                      <CopyButton :value="asArt(row).a.sha256" :label="`${S.common.copy}: ${asArt(row).what} ${S.app.colSha}`" />
                    </div>
                  </template>
                  <template #cell-sig="{ row }">
                    <div class="fa-cell">
                      <span class="fa-code" :title="asArt(row).a.sig">{{ shortHash(asArt(row).a.sig, 8) }}</span>
                      <CopyButton :value="asArt(row).a.sig" :label="`${S.common.copy}: ${asArt(row).what} ${S.app.colSig}`" />
                    </div>
                  </template>
                  <template #cell-url="{ row }">
                    <div class="fa-cell">
                      <a v-if="safeHref(asArt(row).a.url)" :href="safeHref(asArt(row).a.url)" rel="noopener noreferrer" :title="asArt(row).a.url">{{ asArt(row).a.url }}</a>
                      <span v-else>{{ asArt(row).a.url }}</span>
                    </div>
                  </template>
                </DataTable>
              </div>
            </details>
          </div>
        </div>

        <InstallDialog v-if="app && installable" :open="installOpen" :app="app" :version="version?.version ?? ''" @close="installOpen = false" />
        <ActionDialog
          :open="dialog === 'revoke'"
          :title="fmt(S.app.revokeTitle, { app: label })"
          :intro="S.app.revokeIntro"
          :confirm-label="S.app.adminRevoke.replace('…', '')"
          reason
          danger
          :busy="busy"
          :error="actionError"
          @confirm="doRevoke"
          @cancel="dialog = ''"
        />
        <ActionDialog
          :open="dialog === 'yank'"
          :title="fmt(S.app.yankTitle, { app: label, version: dialogVersion })"
          :intro="S.app.yankIntro"
          :confirm-label="S.app.adminYank.replace('…', '')"
          reason
          danger
          :busy="busy"
          :error="actionError"
          @confirm="doYank"
          @cancel="dialog = ''"
        />
      </template>
    </PageState>
  </div>
</template>
