<script setup lang="ts">
// One app in the shop window: its icon, name (the card's one link: the whole
// card opens the app), publisher with badges, a line of what it does, and
// what kind of thing it is. A card, not a table row: tables are DataTable.
import { computed } from 'vue';
import { HardDrive, Languages, Puzzle, Tag } from 'lucide-vue-next';
import type { AppSummary, FileTypeUse } from '@/api/client';
import { S, fmt } from '@/strings';
import { text, toneOf, when } from '@/lib/format';
import { cardPhrases, useKey, useKind, usePhrases } from '@/lib/fileTypes';
import AppIcon from './AppIcon.vue';
import PublisherBadges from './PublisherBadges.vue';

const props = withDefaults(
  defineProps<{
    app: AppSummary;
    categoryLabel?: (id: string) => string;
    feature?: boolean;
    /** "Works with filex X" is set: the card names the version for it. */
    filex?: string;
    headingLevel?: 2 | 3;
    /** A file-type search: what the app does with each kind (the server's match). */
    uses?: FileTypeUse[];
  }>(),
  { categoryLabel: (id: string) => id, feature: false, filex: '', headingLevel: 3, uses: () => [] },
);

const name = computed(() => text(props.app.label) || props.app.name);
const summary = computed(() => text(props.app.summary));
const cats = computed(() => props.app.categories.slice(0, props.feature ? 3 : 2).map((c) => props.categoryLabel(c)));
const version = computed(() => {
  if (props.filex && props.app.compatible) return fmt(S.catalog.forFilex, { version: props.app.compatible, filex: props.filex });
  return props.app.latest ? fmt(S.catalog.latestShort, { version: props.app.latest }) : '';
});
const updated = computed(() => fmt(S.catalog.updatedOn, { date: when(props.app.updated_at, false) }));
// What the card says it does with each kind: the conversions summed up
// (cardPhrases); the whole of it on hover when that is shorter.
const useLines = computed(() =>
  props.uses.map((u) => {
    const text = cardPhrases(u).join(', ');
    const full = usePhrases(u).join('; ');
    return { u, text, full: full !== text ? full : undefined };
  }),
);
// A paid app says so on its card, with the publisher's price text.
const paid = computed(() => props.app.pricing?.model === 'paid');
</script>

<template>
  <article class="fa-card" :class="[`fa-tone-${toneOf(app.name)}`, { 'fa-card--feature': feature }]" :data-testid="`app-card-${app.name}`">
    <div class="fa-card__head">
      <AppIcon :url="app.icon_url" :name="name" :size="feature ? 60 : 48" />
      <div class="fa-card__titles">
        <component :is="`h${headingLevel}`" class="fa-card__title">
          <RouterLink :to="{ name: 'app', params: { name: app.name } }" :data-testid="`app-link-${app.name}`">{{ name }}</RouterLink>
        </component>
        <div class="fa-card__pub">
          <RouterLink :to="{ name: 'publisher', params: { id: app.publisher.id } }">{{ fmt(S.catalog.by, { publisher: app.publisher.name }) }}</RouterLink>
          <PublisherBadges :verified="app.publisher.verified" :official="app.publisher.official" />
        </div>
      </div>
    </div>
    <p class="fa-card__summary">{{ summary }}</p>
    <ul v-if="uses.length" class="fa-card__uses" :data-testid="`app-uses-${app.name}`">
      <li v-for="l in useLines" :key="useKey(l.u)" :data-testid="`app-use-${app.name}-${l.u.ext ?? (l.u.any_file ? 'any' : l.u.mime)}`">
        <span class="fa-chip fa-chip--kind">{{ useKind(l.u) }}</span>
        <span class="fa-card__usetext" :title="l.full">{{ l.text }}</span>
      </li>
    </ul>
    <div class="fa-card__foot">
      <span class="fa-chip">
        <Languages v-if="app.kind === 'language_pack'" aria-hidden="true" />
        <HardDrive v-else-if="app.kind === 'storage'" aria-hidden="true" />
        <Puzzle v-else aria-hidden="true" />
        {{ S.kind[app.kind] }}
      </span>
      <span v-if="paid" class="fa-chip" :title="app.pricing?.price_text || S.pricing.paidHint" :data-testid="`app-paid-${app.name}`">
        <Tag aria-hidden="true" />{{ app.pricing?.price_text ? `${S.pricing.paid}: ${app.pricing.price_text}` : S.pricing.paid }}
      </span>
      <span v-for="c in cats" :key="c" class="fa-chip">{{ c }}</span>
      <span class="fa-card__meta" :title="updated">{{ version }}</span>
    </div>
  </article>
</template>
