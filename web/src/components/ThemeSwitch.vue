<script setup lang="ts">
// Light, system or dark: three icons in one pill (FaSegmented). The choice
// itself lives in lib/theme (localStorage, guarded).
import { Monitor, Moon, Sun } from 'lucide-vue-next';
import { S } from '@/strings';
import { setThemePref, themePref, type ThemePref } from '@/lib/theme';
import FaSegmented from './FaSegmented.vue';

const OPTIONS: { value: ThemePref; label: string; icon: typeof Sun }[] = [
  { value: 'light', label: S.theme.light, icon: Sun },
  { value: 'system', label: S.theme.system, icon: Monitor },
  { value: 'dark', label: S.theme.dark, icon: Moon },
];
</script>

<template>
  <FaSegmented
    :model-value="themePref"
    :options="OPTIONS"
    :aria-label="S.theme.label"
    icons
    testid="theme"
    @update:model-value="setThemePref"
  />
</template>
