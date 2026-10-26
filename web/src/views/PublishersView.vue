<script setup lang="ts">
// Who makes filex apps: one card per publisher (a shop window, not a table),
// searchable, sorted by name or by how many apps. The search, the order and
// the page live in the address, like the catalog's.
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { CircleAlert, Search, Users } from 'lucide-vue-next';
import { ApiError, listPublishers, type PublisherPage } from '@/api/client';
import { S, fmt } from '@/strings';
import EmptyState from '@/components/EmptyState.vue';
import FaSegmented from '@/components/FaSegmented.vue';
import PublisherCard from '@/components/PublisherCard.vue';

const PER_PAGE = 48;
const route = useRoute();
const router = useRouter();
const one = (v: unknown) => (typeof v === 'string' ? v : Array.isArray(v) && typeof v[0] === 'string' ? v[0] : '');
const query = computed(() => {
  const page = Number.parseInt(one(route.query.page), 10);
  return {
    q: one(route.query.q).slice(0, 100),
    sort: one(route.query.sort) === 'apps' ? ('apps' as const) : ('name' as const),
    page: Number.isFinite(page) && page > 1 ? page : 1,
  };
});

const result = ref<PublisherPage | null>(null);
const loading = ref(true);
const error = ref('');
const qInput = ref(query.value.q);

let seq = 0;
async function load() {
  const mine = ++seq;
  loading.value = true;
  error.value = '';
  try {
    const r = await listPublishers({ q: query.value.q || undefined, sort: query.value.sort, page: query.value.page, per_page: PER_PAGE });
    if (mine === seq) result.value = r;
  } catch (e) {
    if (mine !== seq) return;
    result.value = null;
    error.value = e instanceof ApiError && (e.status === 0 || e.status >= 500) ? S.publishers.unavailable : e instanceof Error ? e.message : String(e);
  } finally {
    if (mine === seq) loading.value = false;
  }
}
watch(
  () => JSON.stringify(query.value),
  () => {
    if (route.name !== 'publishers') return;
    qInput.value = query.value.q;
    void load();
  },
  { immediate: true },
);

function go(next: { q?: string; sort?: 'name' | 'apps'; page?: number }, push = false) {
  const n = { ...query.value, page: 1, ...next };
  const q: Record<string, string> = {};
  if (n.q.trim()) q.q = n.q.trim();
  if (n.sort !== 'name') q.sort = n.sort;
  if (n.page > 1) q.page = String(n.page);
  void (push ? router.push({ name: 'publishers', query: q }) : router.replace({ name: 'publishers', query: q }));
}
let timer: ReturnType<typeof setTimeout> | undefined;
function onSearch() {
  clearTimeout(timer);
  timer = setTimeout(() => go({ q: qInput.value }), 300);
}
onBeforeUnmount(() => clearTimeout(timer));
function clear() {
  qInput.value = '';
  go({ q: '' });
}

const items = computed(() => result.value?.items ?? []);
const pages = computed(() => Math.max(1, Math.ceil((result.value?.total ?? 0) / (result.value?.per_page ?? PER_PAGE))));
const countLine = computed(() => {
  if (loading.value && !result.value) return S.publishers.loading;
  const n = result.value?.total;
  if (n == null) return '';
  return n === 1 ? S.publishers.countOne : fmt(S.publishers.count, { count: n });
});
const showEmpty = computed(() => !loading.value && !error.value && result.value !== null && items.value.length === 0);
</script>

<template>
  <div class="fa-page">
    <section class="fa-hero fa-cathero fa-hero--slim" aria-labelledby="pubs-h">
      <span class="fa-eyebrow"><Users class="fa-icon" aria-hidden="true" />{{ S.publishers.eyebrow }}</span>
      <h1 id="pubs-h">{{ S.publishers.heroTitle }}</h1>
      <p class="fa-cathero__lead">{{ S.publishers.lead }}</p>
      <div class="fa-search" role="search">
        <Search class="fa-icon" aria-hidden="true" />
        <label class="fe-sr-only" for="pubs-q">{{ S.publishers.search }}</label>
        <input
          id="pubs-q"
          v-model="qInput"
          class="fe-input fa-search__input"
          type="search"
          maxlength="100"
          autocomplete="off"
          :placeholder="S.publishers.searchPlaceholder"
          data-testid="publishers-search"
          @input="onSearch"
        />
      </div>
    </section>

    <div class="fa-toolbar">
      <p class="fa-toolbar__count" role="status" aria-live="polite" data-testid="publishers-count">{{ countLine }}</p>
      <div class="fa-toolbar__controls">
        <div class="fa-filter fa-filter--auto">
          <span id="pubs-sort" class="fa-filter__label">{{ S.publishers.sort }}</span>
          <FaSegmented
            :model-value="query.sort"
            :options="[
              { value: 'name', label: S.publishers.sortName },
              { value: 'apps', label: S.publishers.sortApps },
            ]"
            aria-labelledby="pubs-sort"
            testid="publishers-sort"
            @update:model-value="(v: string) => go({ sort: v === 'apps' ? 'apps' : 'name' })"
          />
        </div>
      </div>
    </div>

    <div v-if="error" class="fa-callout fa-callout--danger fa-callout--row" role="alert" data-testid="publishers-error">
      <CircleAlert class="fa-callout__icon" aria-hidden="true" />
      <div class="fa-callout__body">
        <p>{{ error }}</p>
        <button type="button" class="fe-btn fe-btn--sm" @click="load">{{ S.common.retry }}</button>
      </div>
    </div>

    <section class="fa-stack" :aria-label="S.publishers.listLabel" :aria-busy="loading ? 'true' : undefined" data-testid="publishers-results">
      <ul v-if="loading && !result && !error" class="fa-cards" aria-hidden="true" data-testid="publishers-skeleton">
        <li v-for="n in 4" :key="n">
          <div class="fa-card fa-card--skeleton">
            <div class="fa-card__head">
              <span class="fa-skel fa-skel--icon"></span>
              <div class="fa-card__titles">
                <span class="fa-skel fa-skel--line"></span>
                <span class="fa-skel fa-skel--line fa-skel--short"></span>
              </div>
            </div>
            <span class="fa-skel fa-skel--line"></span>
          </div>
        </li>
      </ul>
      <EmptyState v-else-if="showEmpty" :title="query.q ? S.publishers.emptyFiltered : S.publishers.empty" :text="query.q ? S.publishers.emptyHint : ''" :icon="Users">
        <button v-if="query.q" type="button" class="fe-btn" data-testid="publishers-clear" @click="clear">{{ S.publishers.clear }}</button>
      </EmptyState>
      <ul v-else-if="items.length" class="fa-cards" data-testid="publishers-grid">
        <li v-for="p in items" :key="p.id"><PublisherCard :publisher="p" /></li>
      </ul>
      <nav v-if="pages > 1" class="fa-pager" :aria-label="S.catalog.pagerLabel">
        <button type="button" class="fe-btn" :disabled="query.page <= 1" @click="go({ page: query.page - 1 }, true)">{{ S.catalog.prev }}</button>
        <span class="fa-note">{{ fmt(S.catalog.pageOf, { page: query.page, pages }) }}</span>
        <button type="button" class="fe-btn" :disabled="query.page >= pages" @click="go({ page: query.page + 1 }, true)">{{ S.catalog.next }}</button>
      </nav>
    </section>
  </div>
</template>
