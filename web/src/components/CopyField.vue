<script setup lang="ts">
// A value a person copies into filex (a repository, a tag) or checks by eye
// (a hash): monospace, wraps anywhere, with its own Copy button. `boxed`
// draws it as a field with the button inside (the install steps).
import CopyButton from './CopyButton.vue';

defineProps<{ value: string; label: string; testid?: string; boxed?: boolean }>();
</script>

<template>
  <span v-if="boxed" class="fa-copybox">
    <code :data-testid="testid">{{ value }}</code>
    <CopyButton :value="value" :label="label" />
  </span>
  <span v-else class="fa-copyfield">
    <code class="fa-code" :data-testid="testid">{{ value }}</code>
    <CopyButton :value="value" :label="label" />
  </span>
</template>
