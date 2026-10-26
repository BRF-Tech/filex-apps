<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch, type Component } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import {
  BadgeCheck,
  CircleAlert,
  CircleCheck,
  Code,
  FileQuestion,
  Files,
  FileText,
  HardDrive,
  Image,
  Languages,
  LayoutGrid,
  List,
  Plug,
  Puzzle,
  Repeat,
  Search,
  ShieldCheck,
  Sparkles,
  Tag,
  Users,
  X,
  Zap,
} from 'lucide-vue-next';
// The list view is THE table: filex's DataTable (the explorer's list view),
// never a <table> or a look-alike. Resizing, sorting, the column menu and the
// frozen Actions column come with it. The default view is the shop window,
// one card per app, which is not a table.
import { DataTable, type DataColumn } from '@brftech/filex-core';
import { ApiError, listApps, listCategories, type AppPage, type AppSummary, type Category } from '@/api/client';
import FaSelect from '@/components/FaSelect.vue';
import FaSegmented from '@/components/FaSegmented.vue';
import { S, fmt } from '@/strings';
import { emptyUnlessError, text, when } from '@/lib/format';
import {
  FILEX_VERSION,
  PER_PAGE,
  catalogApiQuery,
  catalogRouteQuery,
  filtersActive,
  parseCatalogQuery,
  parseCatalogView,
  withView,
  type CatalogFilters,
  type CatalogView,
} from '@/lib/catalogQuery';
import AppCard from '@/components/AppCard.vue';
import AppIcon from '@/components/AppIcon.vue';
import EmptyState from '@/components/EmptyState.vue';
import FileTypePanel from '@/components/FileTypePanel.vue';
import { cardPhrases, extList, namesAKind, useKind, usePhrases } from '@/lib/fileTypes';
import PublisherBadges from '@/components/PublisherBadges.vue';

const route = useRoute();
const router = useRouter();
const filters = computed(() => parseCatalogQuery(route.query));
const view = computed<CatalogView>(() => parseCatalogView(route.query));

const result = ref<AppPage | null>(null);
const loading = ref(true);
const error = ref('');
const categories = ref<Category[]>([]);

// The two typed fields keep their own text while it is being written; the
// address follows once it means something.
const qInput = ref(filters.value.q);
const filexInput = ref(filters.value.filex);
const filexBad = computed(() => filexInput.value.trim() !== '' && !FILEX_VERSION.test(filexInput.value.trim()));

let seq = 0;
async function load() {
  const mine = ++seq;
  loading.value = true;
  error.value = '';
  try {
    const page = await listApps(catalogApiQuery(filters.value));
    if (mine !== seq) return;
    result.value = page;
  } catch (e) {
    if (mine !== seq) return;
    result.value = null;
    error.value =
      e instanceof ApiError && (e.status === 0 || e.status >= 500) ? S.catalog.unavailable : e instanceof Error ? e.message : String(e);
  } finally {
    if (mine === seq) loading.value = false;
  }
}

// A change of view alone redraws what is already here; a change of filters
// asks the server again.
const apiKey = computed(() => JSON.stringify(catalogApiQuery(filters.value)));
watch(
  apiKey,
  () => {
    if (route.name !== 'catalog') return;
    qInput.value = filters.value.q;
    if (!filexBad.value) filexInput.value = filters.value.filex;
    void load();
  },
  { immediate: true },
);

void listCategories()
  .then((r) => (categories.value = r.items))
  .catch(() => (categories.value = []));

const categoryLabels = computed(() => {
  const m = new Map<string, string>();
  for (const c of categories.value) m.set(c.id, text(c.label));
  return m;
});
const categoryLabel = (id: string) => categoryLabels.value.get(id) ?? id;

const CATEGORY_ICONS: Record<string, Component> = {
  documents: FileText,
  conversion: Repeat,
  media: Image,
  productivity: Zap,
  security: ShieldCheck,
  developer: Code,
  integration: Plug,
  integrations: Plug,
  fun: Sparkles,
  storage: HardDrive,
};
const categoryIcon = (id: string) => CATEGORY_ICONS[id] ?? Tag;

function go(next: CatalogFilters, nextView: CatalogView = view.value, push = false) {
  const to = { name: 'catalog', query: withView(catalogRouteQuery(next), nextView) };
  void (push ? router.push(to) : router.replace(to));
}
function update(patch: Partial<CatalogFilters>) {
  go({ ...filters.value, page: 1, ...patch });
}
function setView(v: CatalogView) {
  if (v !== view.value) go(filters.value, v);
}

let qTimer: ReturnType<typeof setTimeout> | undefined;
function onSearch() {
  clearTimeout(qTimer);
  qTimer = setTimeout(() => update({ q: qInput.value }), 300);
}
function onFilex() {
  const v = filexInput.value.trim();
  if (v === '' || FILEX_VERSION.test(v)) update({ filex: v });
}
onBeforeUnmount(() => clearTimeout(qTimer));

function clearAll() {
  qInput.value = '';
  filexInput.value = '';
  go({ ...parseCatalogQuery({}) });
}

const items = computed(() => result.value?.items ?? []);
const total = computed(() => result.value?.total ?? 0);
const pages = computed(() => Math.max(1, Math.ceil(total.value / (result.value?.per_page ?? PER_PAGE))));

// A file-type search (".docx", "pdf docx"): the server read the words as
// extensions and matched the apps (matched_extensions); the page groups them
// by what the server says - the apps that name the kind, then the ones that
// offer something on every file - and says what filex does by itself.
const ft = computed(() => result.value?.file_types ?? null);
const ftExts = computed(() => extList(ft.value?.extensions ?? []));
const namedApps = computed(() => (ft.value ? items.value.filter(namesAKind) : []));
const everyFileApps = computed(() => (ft.value ? items.value.filter((a) => !namesAKind(a)) : []));
// Nothing at all: filex does nothing with any of the kinds, no app names one.
const ftEmpty = computed(() => !!ft.value && namedApps.value.length === 0 && ft.value.builtin.length === 0);
// The list's cell says what a card says (conversions summed up); its hover
// text says all of it.
const usesText = (a: AppSummary) =>
  (a.matched_extensions ?? []).map((u) => `${useKind(u)}: ${cardPhrases(u).join(', ')}`).join('; ');
const usesTitle = (a: AppSummary) =>
  (a.matched_extensions ?? []).map((u) => `${useKind(u)}: ${usePhrases(u).join('; ')}`).join(' | ');

const footNote = computed(() => {
  const n = result.value?.total;
  if (n == null) return '';
  if (ft.value) {
    // The apps that name the kind, and apart the ones only offered on every
    // file: "No app works with .xyz; 1 offered on every file".
    const exts = ftExts.value;
    const named = ft.value.named;
    const line = named === 0 ? fmt(S.fileTypes.countNone, { exts }) : named === 1 ? fmt(S.fileTypes.countOne, { exts }) : fmt(S.fileTypes.count, { count: named, exts });
    return n > named ? line + fmt(S.fileTypes.countEvery, { count: n - named }) : line;
  }
  return n === 1 ? S.catalog.countOne : fmt(S.catalog.count, { count: n });
});
const countLine = computed(() => (loading.value && !result.value ? S.catalog.loadingApps : footNote.value));

// The plain first page is a shop front: the official publisher's apps first, then
// everyone else's, then the language packs, then the storage plugins. Any
// filter, search or other order shows one grid of results.
const shelves = computed(() => {
  const f = filters.value;
  if (filtersActive(f) || f.page > 1 || f.sort !== 'name') return null;
  const all = items.value;
  return [
    { id: 'official', icon: BadgeCheck, tone: 0, title: S.catalog.shelfOfficial, sub: S.catalog.shelfOfficialSub, feature: true, apps: all.filter((a) => a.kind === 'app' && a.publisher.official) },
    { id: 'community', icon: Users, tone: 4, title: S.catalog.shelfCommunity, sub: S.catalog.shelfCommunitySub, feature: false, apps: all.filter((a) => a.kind === 'app' && !a.publisher.official) },
    { id: 'languages', icon: Languages, tone: 1, title: S.catalog.shelfLanguages, sub: S.catalog.shelfLanguagesSub, feature: false, apps: all.filter((a) => a.kind === 'language_pack') },
    { id: 'storage', icon: HardDrive, tone: 2, title: S.catalog.shelfStorage, sub: S.catalog.shelfStorageSub, feature: false, apps: all.filter((a) => a.kind === 'storage') },
  ].filter((s) => s.apps.length > 0);
});

// The band's picture: the icons of the first page's apps, those with an
// icon of their own first; nothing until there are three to show.
const heroApps = ref<AppSummary[]>([]);
watch(result, (r) => {
  if (!r || filtersActive(filters.value) || heroApps.value.length) return;
  heroApps.value = [...r.items].sort((a, b) => Number(!!b.icon_url) - Number(!!a.icon_url)).slice(0, 6);
});

const emptyTitle = computed(() =>
  emptyUnlessError(
    ft.value
      ? fmt(ftEmpty.value ? S.fileTypes.emptyTitle : S.fileTypes.noApps, { exts: ftExts.value })
      : filtersActive(filters.value)
        ? S.catalog.emptyFiltered
        : S.catalog.emptyNone,
    error.value,
  ),
);
const showEmpty = computed(() => !loading.value && !error.value && result.value !== null && items.value.length === 0);

function onPage(p: number) {
  go({ ...filters.value, page: p }, view.value, true);
}

// ---------------------------------------------------------------- list view
const sort = computed(() =>
  filters.value.sort === 'updated' ? { key: 'updated', dir: 'desc' as const } : { key: 'name', dir: 'asc' as const },
);
// The server orders the rows (name A-Z, or most recently updated first); a
// second click on the same header keeps that order rather than pretending to
// reverse a list the table holds only one page of.
function onSort(p: { key: string }) {
  update({ sort: p.key === 'updated' ? 'updated' : 'name', page: filters.value.page });
}
const columns = computed<DataColumn<AppSummary>[]>(() => [
  { id: 'name', label: S.catalog.colApp, width: 240, min: 150, lead: true, sortable: true, format: (r) => text(r.label) || r.name },
  ...(ft.value ? [{ id: 'uses', label: fmt(S.fileTypes.colWith, { exts: ftExts.value }), width: 340, min: 160, format: usesText, title: usesTitle }] : []),
  { id: 'summary', label: S.catalog.colSummary, width: 340, format: (r) => text(r.summary) },
  { id: 'publisher', label: S.catalog.colPublisher, width: 270, min: 120, format: (r) => r.publisher.name },
  { id: 'kind', label: S.catalog.colKind, width: 130, format: (r) => S.kind[r.kind] },
  { id: 'categories', label: S.catalog.colCategories, width: 180, format: (r) => r.categories.map(categoryLabel).join(', ') },
  filters.value.filex
    ? { id: 'compatible', label: fmt(S.catalog.colCompatible, { version: filters.value.filex }), width: 130, format: (r) => r.compatible ?? '' }
    : { id: 'latest', label: S.catalog.colLatest, width: 110, format: (r) => r.latest },
  { id: 'updated', label: S.catalog.colUpdated, width: 130, sortable: true, sortDir: 'desc', format: (r) => when(r.updated_at, false) },
]);
function onRow(row: AppSummary, ev: MouseEvent) {
  if ((ev.target as HTMLElement | null)?.closest('a, button, input, select, label')) return;
  void router.push({ name: 'app', params: { name: row.name } });
}
const asApp = (r: unknown) => r as AppSummary;
</script>

<template>
  <div class="fa-page">
    <section class="fa-hero fa-cathero" :class="{ 'fa-cathero--art': heroApps.length >= 3 }" aria-labelledby="cat-h">
      <span class="fa-eyebrow"><span class="fa-eyebrow__dot" aria-hidden="true"></span>{{ S.catalog.eyebrow }}</span>
      <h1 id="cat-h">{{ S.catalog.heroTitle }} <span class="fa-cathero__accent">{{ S.catalog.heroAccent }}</span></h1>
      <p class="fa-cathero__lead">{{ S.catalog.heroLead }}</p>
      <div class="fa-search" role="search">
        <Search class="fa-icon" aria-hidden="true" />
        <label class="fe-sr-only" for="cat-q">{{ S.catalog.search }}</label>
        <input
          id="cat-q"
          v-model="qInput"
          class="fe-input fa-search__input"
          type="search"
          maxlength="100"
          autocomplete="off"
          :placeholder="S.catalog.searchPlaceholder"
          data-testid="catalog-search"
          @input="onSearch"
        />
      </div>
      <ul class="fa-trust">
        <li v-for="t in S.catalog.trust" :key="t"><CircleCheck aria-hidden="true" />{{ t }}</li>
      </ul>
      <div v-if="heroApps.length >= 3" class="fa-cathero__art" aria-hidden="true" data-testid="catalog-hero-art">
        <AppIcon v-for="a in heroApps" :key="a.name" :url="a.icon_url" :name="text(a.label) || a.name" :size="76" />
      </div>
    </section>

    <div class="fa-stack">
      <ul v-if="categories.length" class="fa-filterchips" :aria-label="S.catalog.categoriesLabel" data-testid="catalog-categories">
        <li>
          <button type="button" class="fa-filterchip" :aria-pressed="!filters.category" data-testid="catalog-cat-all" @click="update({ category: '' })">
            <LayoutGrid aria-hidden="true" />{{ S.catalog.allChip }}
          </button>
        </li>
        <li v-for="c in categories" :key="c.id">
          <button
            type="button"
            class="fa-filterchip"
            :aria-pressed="filters.category === c.id"
            :data-testid="`catalog-cat-${c.id}`"
            @click="update({ category: filters.category === c.id ? '' : c.id })"
          >
            <component :is="categoryIcon(c.id)" aria-hidden="true" />{{ text(c.label) }}
          </button>
        </li>
      </ul>

      <div class="fa-toolbar">
        <p class="fa-toolbar__count" role="status" aria-live="polite" data-testid="catalog-count">{{ countLine }}</p>
        <div class="fa-toolbar__controls">
          <div class="fa-filter">
            <label for="cat-kind">{{ S.catalog.kind }}</label>
            <FaSelect
              id="cat-kind"
              :model-value="filters.kind"
              :options="[
                { value: '', label: S.catalog.allKinds },
                { value: 'app', label: S.kind.app },
                { value: 'language_pack', label: S.kind.language_pack },
                { value: 'storage', label: S.kind.storage },
              ]"
              testid="catalog-kind"
              @update:model-value="(v: string) => update({ kind: v as CatalogFilters['kind'] })"
            />
          </div>
          <div class="fa-filter">
            <label for="cat-filex">{{ S.catalog.worksWith }}</label>
            <input
              id="cat-filex"
              v-model="filexInput"
              class="fe-input"
              inputmode="decimal"
              :placeholder="S.catalog.worksWithPlaceholder"
              :aria-invalid="filexBad ? 'true' : undefined"
              :aria-describedby="filexBad ? 'cat-filex-err' : undefined"
              data-testid="catalog-filex"
              @change="onFilex"
              @keydown.enter="onFilex"
            />
            <span v-if="filexBad" id="cat-filex-err" class="fa-inline-error">{{ S.catalog.worksWithInvalid }}</span>
          </div>
          <div class="fa-filter">
            <label for="cat-sort">{{ S.catalog.sort }}</label>
            <FaSelect
              id="cat-sort"
              :model-value="filters.sort"
              :options="[
                { value: 'name', label: S.catalog.sortName },
                { value: 'updated', label: S.catalog.sortUpdated },
              ]"
              testid="catalog-sort"
              @update:model-value="(v: string) => update({ sort: v === 'updated' ? 'updated' : 'name' })"
            />
          </div>
          <div class="fa-filter fa-filter--check">
            <label class="fe-checkbox">
              <input
                type="checkbox"
                :checked="filters.verified"
                data-testid="catalog-verified"
                @change="update({ verified: ($event.target as HTMLInputElement).checked })"
              />
              <span>{{ S.catalog.verifiedOnly }}</span>
            </label>
          </div>
          <FaSegmented
            :model-value="view"
            :options="[
              { value: 'grid', label: S.catalog.viewGrid, icon: LayoutGrid },
              { value: 'list', label: S.catalog.viewList, icon: List },
            ]"
            :aria-label="S.catalog.view"
            :option-testid="(v: string) => `catalog-view-${v}`"
            @update:model-value="(v: string) => setView(v as CatalogView)"
          />
        </div>
      </div>

      <div v-if="filtersActive(filters)" class="fa-actions">
        <button v-if="filters.publisher" type="button" class="fa-filterchip" aria-pressed="true" data-testid="catalog-clear-publisher" @click="update({ publisher: '' })">
          <span aria-hidden="true">{{ fmt(S.catalog.publisherFilter, { id: filters.publisher }) }}</span>
          <X aria-hidden="true" />
          <span class="fe-sr-only">{{ S.catalog.clearPublisher }}</span>
        </button>
        <button type="button" class="fe-btn fe-btn--sm" data-testid="catalog-clear" @click="clearAll"><X class="fa-icon" aria-hidden="true" />{{ S.catalog.clearFilters }}</button>
      </div>
    </div>

    <FileTypePanel v-if="ft && !error" :search="ft" :missing="!ftEmpty" />

    <div v-if="error" class="fa-callout fa-callout--danger fa-callout--row" role="alert" data-testid="catalog-note">
      <CircleAlert class="fa-callout__icon" aria-hidden="true" />
      <div class="fa-callout__body">
        <p>{{ error }}</p>
        <button type="button" class="fe-btn fe-btn--sm" @click="load">{{ S.common.retry }}</button>
      </div>
    </div>

    <DataTable
      v-if="view === 'list'"
      table-id="store.catalog"
      :columns="columns"
      :rows="items"
      :loading="loading"
      row-key="name"
      :empty="emptyTitle"
      :aria-label="S.catalog.tableLabel"
      :sort="sort"
      :page="filters.page"
      :page-size="result?.per_page ?? PER_PAGE"
      :total="result?.total"
      :foot-note="footNote"
      data-testid="catalog-table"
      @sort="onSort"
      @page="onPage"
      @row-click="onRow"
    >
      <template #cell-name="{ row }">
        <div class="fa-cell">
          <AppIcon :url="asApp(row).icon_url" :name="text(asApp(row).label) || asApp(row).name" :size="24" />
          <RouterLink :to="{ name: 'app', params: { name: asApp(row).name } }" :data-testid="`app-link-${asApp(row).name}`">
            {{ text(asApp(row).label) || asApp(row).name }}
          </RouterLink>
        </div>
      </template>
      <template #cell-publisher="{ row }">
        <div class="fa-cell">
          <RouterLink :to="{ name: 'publisher', params: { id: asApp(row).publisher.id } }">{{ asApp(row).publisher.name }}</RouterLink>
          <PublisherBadges :verified="asApp(row).publisher.verified" :official="asApp(row).publisher.official" />
        </div>
      </template>
    </DataTable>
    <p v-if="view === 'list' && ft && !loading && namedApps.length === 0" class="fa-note" data-testid="ft-noapps">
      {{ items.length ? `${emptyTitle}.` : S.fileTypes.emptyHint }}
      <RouterLink to="/publish" data-testid="ft-publish">{{ S.fileTypes.publish }}</RouterLink>
    </p>

    <!-- Labelled "Apps" like the list view's table, whatever it holds (the
         e2e suite and a screen reader find the results by that name). -->
    <section v-else class="fa-stack" :aria-label="S.catalog.tableLabel" :aria-busy="loading ? 'true' : undefined" data-testid="catalog-results">
      <ul v-if="loading && !result && !error" class="fa-cards" aria-hidden="true" data-testid="catalog-skeleton">
        <li v-for="n in 6" :key="n">
          <div class="fa-card fa-card--skeleton">
            <div class="fa-card__head">
              <span class="fa-skel fa-skel--icon"></span>
              <div class="fa-card__titles">
                <span class="fa-skel fa-skel--line"></span>
                <span class="fa-skel fa-skel--line fa-skel--short"></span>
              </div>
            </div>
            <span class="fa-skel fa-skel--line"></span>
            <span class="fa-skel fa-skel--line fa-skel--short"></span>
          </div>
        </li>
      </ul>

      <template v-else-if="ft">
        <EmptyState v-if="ftEmpty" :icon="FileQuestion" :title="emptyTitle" :text="S.fileTypes.emptyHint" data-testid="ft-empty">
          <RouterLink to="/publish" class="fe-btn fe-btn--primary" data-testid="ft-publish">{{ S.fileTypes.publish }}</RouterLink>
        </EmptyState>
        <section v-if="namedApps.length" class="fa-section" aria-labelledby="ft-apps-h" data-testid="ft-apps">
          <div class="fa-section__head">
            <h2 id="ft-apps-h" class="fa-heading fa-tone-0">
              <span class="fa-heading__icon" aria-hidden="true"><Puzzle /></span>{{ fmt(S.fileTypes.appsTitle, { exts: ftExts }) }}
            </h2>
            <p class="fa-section__sub">{{ S.fileTypes.appsSub }}</p>
          </div>
          <ul class="fa-cards">
            <li v-for="a in namedApps" :key="a.name">
              <AppCard :app="a" :category-label="categoryLabel" :filex="filters.filex" :uses="a.matched_extensions" />
            </li>
          </ul>
        </section>
        <p v-else-if="!ftEmpty" class="fa-note" data-testid="ft-noapps">
          {{ emptyTitle }}.
          <RouterLink to="/publish" data-testid="ft-publish">{{ S.fileTypes.publish }}</RouterLink>
        </p>
        <section v-if="everyFileApps.length" class="fa-section" aria-labelledby="ft-any-h" data-testid="ft-any">
          <div class="fa-section__head">
            <h2 id="ft-any-h" class="fa-heading fa-tone-6">
              <span class="fa-heading__icon" aria-hidden="true"><Files /></span>{{ S.fileTypes.anyTitle }}
            </h2>
            <p class="fa-section__sub">{{ fmt(S.fileTypes.anySub, { exts: ftExts }) }}</p>
          </div>
          <ul class="fa-cards">
            <li v-for="a in everyFileApps" :key="a.name">
              <AppCard :app="a" :category-label="categoryLabel" :filex="filters.filex" :uses="a.matched_extensions" />
            </li>
          </ul>
        </section>
      </template>

      <EmptyState v-else-if="showEmpty" :title="emptyTitle" :text="filtersActive(filters) ? S.catalog.emptyFilteredHint : S.catalog.emptyNoneHint">
        <button v-if="filtersActive(filters)" type="button" class="fe-btn" data-testid="catalog-empty-clear" @click="clearAll">{{ S.catalog.clearFilters }}</button>
        <RouterLink v-else to="/publish" class="fe-btn fe-btn--primary">{{ S.nav.publish }}</RouterLink>
      </EmptyState>

      <template v-else-if="items.length && shelves">
        <section v-for="s in shelves" :key="s.id" class="fa-section fa-shelf" :aria-labelledby="`shelf-${s.id}`" :data-testid="`shelf-${s.id}`">
          <div class="fa-section__head">
            <h2 :id="`shelf-${s.id}`" class="fa-heading" :class="`fa-tone-${s.tone}`">
              <span class="fa-heading__icon" aria-hidden="true"><component :is="s.icon" /></span>{{ s.title }}
            </h2>
            <p class="fa-section__sub">{{ s.sub }}</p>
          </div>
          <ul class="fa-cards" :class="{ 'fa-cards--feature': s.feature }" :aria-label="s.title">
            <li v-for="a in s.apps" :key="a.name">
              <AppCard :app="a" :category-label="categoryLabel" :feature="s.feature" />
            </li>
          </ul>
        </section>
      </template>

      <template v-else-if="items.length">
        <h2 class="fe-sr-only">{{ S.catalog.results }}</h2>
        <ul class="fa-cards" data-testid="catalog-grid">
          <li v-for="a in items" :key="a.name">
            <AppCard :app="a" :category-label="categoryLabel" :filex="filters.filex" />
          </li>
        </ul>
      </template>

      <nav v-if="pages > 1" class="fa-pager" :aria-label="S.catalog.pagerLabel" data-testid="catalog-pager">
        <button type="button" class="fe-btn" :disabled="filters.page <= 1" @click="onPage(filters.page - 1)">{{ S.catalog.prev }}</button>
        <span class="fa-note">{{ fmt(S.catalog.pageOf, { page: filters.page, pages }) }}</span>
        <button type="button" class="fe-btn" :disabled="filters.page >= pages" @click="onPage(filters.page + 1)">{{ S.catalog.next }}</button>
      </nav>
    </section>

    <!-- Apps whose text names the kind but whose manifest does not: the
         nearest results, in both views. -->
    <section v-if="ft && ft.near.length && !loading" class="fa-section" aria-labelledby="ft-near-h" data-testid="ft-near">
      <h2 id="ft-near-h">{{ fmt(S.fileTypes.nearTitle, { words: ft.extensions.join(' ') }) }}</h2>
      <ul class="fa-cards">
        <li v-for="a in ft.near" :key="a.name">
          <AppCard :app="a" :category-label="categoryLabel" :filex="filters.filex" />
        </li>
      </ul>
    </section>
  </div>
</template>
