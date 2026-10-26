<script setup lang="ts">
// The three answers a page can give before its content: still loading, could
// not load (with a way to try again), or nothing here.
import { CircleAlert, SearchX } from 'lucide-vue-next';
import { S } from '@/strings';

defineProps<{ loading?: boolean; error?: string; notFound?: boolean }>();
defineEmits<{ (e: 'retry'): void }>();
</script>

<template>
  <div v-if="loading" class="fa-columns" data-testid="page-loading">
    <span class="fe-sr-only" role="status">{{ S.common.loading }}</span>
    <div class="fa-stack" aria-hidden="true">
      <span class="fa-skel fa-skel--block"></span>
      <span class="fa-skel fa-skel--line"></span>
      <span class="fa-skel fa-skel--line fa-skel--short"></span>
    </div>
    <div class="fa-panel" aria-hidden="true">
      <span class="fa-skel fa-skel--line fa-skel--short"></span>
      <span class="fa-skel fa-skel--line"></span>
      <span class="fa-skel fa-skel--line"></span>
    </div>
  </div>
  <div v-else-if="notFound" class="fa-empty" data-testid="page-not-found">
    <span class="fa-empty__icon" aria-hidden="true"><SearchX /></span>
    <p class="fa-empty__title">{{ S.common.notFound }}</p>
    <RouterLink to="/" class="fe-btn">{{ S.common.backToCatalog }}</RouterLink>
  </div>
  <div v-else-if="error" class="fa-callout fa-callout--danger fa-callout--row" role="alert" data-testid="page-error">
    <CircleAlert class="fa-callout__icon" aria-hidden="true" />
    <div class="fa-callout__body">
      <p>{{ error }}</p>
      <button type="button" class="fe-btn" @click="$emit('retry')">{{ S.common.retry }}</button>
    </div>
  </div>
  <slot v-else />
</template>
