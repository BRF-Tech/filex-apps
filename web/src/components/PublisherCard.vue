<script setup lang="ts">
// One publisher in the publishers' shop window: avatar (the store's mirrored
// image, else the initial), name (the card's link to the publisher's page),
// badges, GitHub account, how many apps, a line about them.
import { computed } from 'vue';
import { Github, LayoutGrid } from 'lucide-vue-next';
import type { PublisherListItem } from '@/api/client';
import { S, fmt } from '@/strings';
import { toneOf } from '@/lib/format';
import AppIcon from './AppIcon.vue';
import PublisherBadges from './PublisherBadges.vue';

const props = defineProps<{ publisher: PublisherListItem }>();
const apps = computed(() => {
  const n = props.publisher.apps;
  return n === 0 ? S.publishers.appsNone : n === 1 ? S.publishers.appsOne : fmt(S.publishers.apps, { count: n });
});
</script>

<template>
  <article class="fa-card fa-pubcard2" :class="`fa-tone-${toneOf(publisher.id)}`" :data-testid="`publisher-card-${publisher.id}`">
    <div class="fa-card__head">
      <AppIcon :url="publisher.avatar_url" :name="publisher.name" :size="52" />
      <div class="fa-card__titles">
        <h2 class="fa-card__title">
          <RouterLink :to="{ name: 'publisher', params: { id: publisher.id } }" :data-testid="`publisher-link-${publisher.id}`">{{ publisher.name }}</RouterLink>
        </h2>
        <span class="fa-card__sub"><Github class="fa-icon" aria-hidden="true" />{{ publisher.github_owner }}</span>
      </div>
    </div>
    <PublisherBadges :verified="publisher.verified" :official="publisher.official" />
    <p class="fa-card__summary">{{ publisher.description }}</p>
    <div class="fa-card__foot">
      <span class="fa-chip"><LayoutGrid aria-hidden="true" />{{ apps }}</span>
    </div>
  </article>
</template>
