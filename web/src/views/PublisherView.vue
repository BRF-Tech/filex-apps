<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { Building2, Calendar, Github, Globe, LayoutGrid, UserRound } from 'lucide-vue-next';
import { ApiError, getPublisher, listCategories, type AppSummary, type Category, type Publisher } from '@/api/client';
import { S, fmt } from '@/strings';
import { bareUrl, github, safeHref, text, toneOf, when } from '@/lib/format';
import { setTitle } from '@/router';
import AppCard from '@/components/AppCard.vue';
import AppIcon from '@/components/AppIcon.vue';
import EmptyState from '@/components/EmptyState.vue';
import PageState from '@/components/PageState.vue';
import PublisherBadges from '@/components/PublisherBadges.vue';

const route = useRoute();
const id = computed(() => String(route.params.id ?? ''));
const publisher = ref<Publisher | null>(null);
const apps = ref<AppSummary[]>([]);
const loading = ref(true);
const error = ref('');
const notFound = ref(false);
const categories = ref<Category[]>([]);

async function load() {
  loading.value = true;
  error.value = '';
  notFound.value = false;
  try {
    const r = await getPublisher(id.value);
    publisher.value = r.publisher;
    apps.value = r.apps;
    setTitle(r.publisher.name);
  } catch (e) {
    publisher.value = null;
    if (e instanceof ApiError && e.status === 404) notFound.value = true;
    else error.value = fmt(S.common.loadFailed, { message: e instanceof Error ? e.message : String(e) });
  } finally {
    loading.value = false;
  }
}
watch(id, () => void load(), { immediate: true });

void listCategories()
  .then((r) => (categories.value = r.items))
  .catch(() => undefined);
const categoryLabel = (c: string) => text(categories.value.find((x) => x.id === c)?.label) || c;

// Apps first, then language packs; each by name.
const sorted = computed(() =>
  [...apps.value].sort((a, b) => (a.kind === b.kind ? (text(a.label) || a.name).localeCompare(text(b.label) || b.name) : a.kind === 'app' ? -1 : 1)),
);
</script>

<template>
  <div class="fa-page">
    <section class="fa-hero" :class="`fa-tone-${toneOf(id)}`" aria-labelledby="pub-h">
      <div class="fa-pubhead">
        <AppIcon v-if="publisher" :url="publisher.avatar_url" :name="publisher.name" :size="72" />
        <div class="fa-pubhead__text">
          <span v-if="publisher" class="fa-eyebrow"><span class="fa-eyebrow__dot" aria-hidden="true"></span>{{ S.publisher.eyebrow }}</span>
          <h1 id="pub-h" data-testid="publisher-title">{{ publisher ? publisher.name : notFound ? S.common.notFoundTitle : id }}</h1>
          <template v-if="publisher">
            <PublisherBadges :verified="publisher.verified" :official="publisher.official" />
            <p v-if="publisher.description" class="fa-apphead__summary" data-testid="publisher-description">{{ publisher.description }}</p>
            <ul class="fa-pubhead__links">
              <li>
                <a :href="github.owner(publisher.github_owner)" rel="noopener noreferrer"><Github class="fa-icon" aria-hidden="true" />{{ publisher.github_owner }}</a>
              </li>
              <li v-if="safeHref(publisher.homepage)">
                <a :href="safeHref(publisher.homepage)" rel="noopener noreferrer"><Globe class="fa-icon" aria-hidden="true" />{{ bareUrl(publisher.homepage ?? '') }}</a>
              </li>
              <li class="fa-muted">
                <span>
                  <Building2 v-if="publisher.owner_type === 'Organization'" class="fa-icon" aria-hidden="true" />
                  <UserRound v-else class="fa-icon" aria-hidden="true" />
                  {{ publisher.owner_type === 'Organization' ? S.publisher.organization : S.publisher.user }}
                </span>
              </li>
              <li v-if="publisher.created_at" class="fa-muted">
                <span><Calendar class="fa-icon" aria-hidden="true" />{{ fmt(S.publisher.since, { date: when(publisher.created_at, false) }) }}</span>
              </li>
            </ul>
          </template>
        </div>
        <div v-if="publisher" class="fa-stat">
          <span class="fa-stat__value" data-testid="publisher-count">{{ apps.length }}</span>
          <span class="fa-stat__label">{{ S.publisher.appsCount }}</span>
        </div>
      </div>
    </section>

    <PageState :loading="loading && !publisher" :error="error" :not-found="notFound" @retry="load">
      <template v-if="publisher">
        <p v-if="publisher.status === 'suspended'" class="fa-callout fa-callout--danger">{{ S.publisher.suspended }}</p>
        <section class="fa-section" aria-labelledby="pub-apps-h">
          <div class="fa-section__head">
            <h2 id="pub-apps-h">{{ S.publisher.apps }}</h2>
            <RouterLink v-if="apps.length" :to="{ name: 'catalog', query: { publisher: publisher.id } }">{{ S.publisher.inCatalog }}</RouterLink>
          </div>
          <ul v-if="sorted.length" class="fa-cards" :aria-label="fmt(S.publisher.tableLabel, { name: publisher.name })" data-testid="publisher-apps">
            <li v-for="a in sorted" :key="a.name">
              <AppCard :app="a" :category-label="categoryLabel" />
            </li>
          </ul>
          <EmptyState v-else :title="S.publisher.appsEmpty" :icon="LayoutGrid" data-testid="publisher-apps" />
        </section>
      </template>
    </PageState>
  </div>
</template>
