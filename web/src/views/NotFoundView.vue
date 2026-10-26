<script setup lang="ts">
import { SearchX } from 'lucide-vue-next';
import { S } from '@/strings';
</script>

<template>
  <div class="fa-page">
    <h1>{{ S.common.notFoundTitle }}</h1>
    <div class="fa-empty" data-testid="not-found">
      <span class="fa-empty__icon" aria-hidden="true"><SearchX /></span>
      <p class="fa-empty__title">{{ S.common.notFound }}</p>
      <RouterLink to="/" class="fe-btn fe-btn--primary">{{ S.common.backToCatalog }}</RouterLink>
    </div>
  </div>
</template>
