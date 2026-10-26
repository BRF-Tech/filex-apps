<script setup lang="ts">
// A state in a word. The meaning is the label; the tone only adds a coloured
// mark (a filex token), so the text keeps the palette's full contrast and
// nobody has to tell green from red to read it.
import type { Tone } from '@/lib/format';

withDefaults(defineProps<{ label: string; tone?: Tone }>(), { tone: 'muted' });
</script>

<template>
  <span class="fa-badge" :class="`fa-badge--${tone}`"><span class="fa-badge__dot" aria-hidden="true"></span>{{ label }}</span>
</template>
