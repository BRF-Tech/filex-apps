<script setup lang="ts">
// Where a person is in a process of a few steps (publishing): done steps
// carry a check, the current one is marked for the eye and for a screen
// reader (aria-current="step").
import { Check } from 'lucide-vue-next';
import { S } from '@/strings';

defineProps<{ steps: { title: string; text?: string }[]; current: number; label: string; done?: number[] }>();
</script>

<template>
  <ol class="fa-stepper" :aria-label="label" data-testid="stepper">
    <li
      v-for="(s, i) in steps"
      :key="i"
      class="fa-step"
      :class="{ 'fa-step--done': done?.includes(i), 'fa-step--current': i === current }"
      :aria-current="i === current ? 'step' : undefined"
    >
      <span class="fa-step__num" aria-hidden="true">
        <Check v-if="done?.includes(i)" />
        <template v-else>{{ i + 1 }}</template>
      </span>
      <span class="fa-step__title">
        {{ s.title }}<span v-if="done?.includes(i)" class="fe-sr-only"> ({{ S.publish.stepDone }})</span>
      </span>
      <p v-if="s.text" class="fa-step__text">{{ s.text }}</p>
    </li>
  </ol>
</template>
