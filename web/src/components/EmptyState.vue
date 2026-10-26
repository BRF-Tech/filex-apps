<script setup lang="ts">
// Nothing to show: say so, say why, and offer the way on (the slot).
import type { Component } from 'vue';
import { PackageSearch } from 'lucide-vue-next';

withDefaults(defineProps<{ title: string; text?: string; icon?: Component }>(), { text: '', icon: () => PackageSearch });
</script>

<template>
  <div class="fa-empty" data-testid="empty-state">
    <span class="fa-empty__icon" aria-hidden="true"><component :is="icon" /></span>
    <p class="fa-empty__title">{{ title }}</p>
    <p v-if="text" class="fa-note">{{ text }}</p>
    <slot />
  </div>
</template>
