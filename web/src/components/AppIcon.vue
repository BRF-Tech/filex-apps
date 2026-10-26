<script setup lang="ts">
// An app's icon: the store's mirrored image, or its initial on a tone picked
// from its name (always the same tone for the same app) when it has none.
// Decorative: the name always stands next to it.
import { computed } from 'vue';
import { initialOf, mediaPath, toneOf } from '@/lib/format';

const props = withDefaults(defineProps<{ url?: string; name: string; size?: number }>(), { url: '', size: 32 });
const src = computed(() => mediaPath(props.url));
const initial = computed(() => initialOf(props.name));
// A large tile carries the full tone (its initial is large text); a small one
// the soft ground with dark ink, which reads at any size.
const solid = computed(() => props.size >= 40);
const box = computed(() => ({ width: props.size + 'px', height: props.size + 'px', fontSize: Math.round(props.size * 0.44) + 'px' }));
</script>

<template>
  <img v-if="src" class="fa-appicon" :src="src" alt="" :width="size" :height="size" loading="lazy" />
  <span
    v-else
    class="fa-appicon fa-appicon--letter"
    :class="[`fa-tone-${toneOf(name)}`, { 'fa-appicon--solid': solid }]"
    :style="box"
    aria-hidden="true"
    data-testid="app-icon-placeholder"
  >{{ initial }}</span>
</template>
