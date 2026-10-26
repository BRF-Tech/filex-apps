<script setup lang="ts" generic="T extends string">
// A few choices side by side (a filter of two to four values, a view, the
// theme): a radio group of buttons in one strip, the chosen one filled.
// The chosen button is the only tab stop; the arrow keys, Home and End move
// the choice. With `icons`, the buttons show only their icon (the label is
// the accessible name and the tooltip).
//
// Like FaSelect, written to move into @brftech/filex-core as is: Vue, the
// icons the caller passes and filex's CSS variables, nothing of the store.
import { nextTick, ref, type Component } from 'vue';

interface FaSegmentedOption {
  value: T;
  label: string;
  icon?: Component;
}

const props = withDefaults(
  defineProps<{
    modelValue: T;
    options: FaSegmentedOption[];
    ariaLabel?: string;
    ariaLabelledby?: string;
    icons?: boolean;
    testid?: string;
    /** The buttons' test ids: `<testid>-<value>` (an empty value is `all`) unless given. */
    optionTestid?: (value: T) => string;
  }>(),
  { ariaLabel: undefined, ariaLabelledby: undefined, icons: false, testid: undefined, optionTestid: undefined },
);
const emit = defineEmits<{ (e: 'update:modelValue', v: T): void; (e: 'change', v: T): void }>();
const buttons = ref<HTMLButtonElement[]>([]);

function tid(v: T): string | undefined {
  if (props.optionTestid) return props.optionTestid(v);
  return props.testid ? `${props.testid}-${v || 'all'}` : undefined;
}

function choose(v: T) {
  if (v === props.modelValue) return;
  emit('update:modelValue', v);
  emit('change', v);
}

async function onKey(e: KeyboardEvent, i: number) {
  const n = props.options.length;
  const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
  let next = step === undefined ? -1 : (i + step + n) % n;
  if (e.key === 'Home') next = 0;
  if (e.key === 'End') next = n - 1;
  if (next < 0) return;
  e.preventDefault();
  choose(props.options[next].value);
  await nextTick();
  buttons.value[next]?.focus();
}

// With no choice among the options, the first is the tab stop.
const isStop = (i: number) =>
  props.options[i].value === props.modelValue || (i === 0 && !props.options.some((o) => o.value === props.modelValue));
</script>

<template>
  <div
    class="fa-segmented"
    :class="{ 'fa-segmented--icons': icons }"
    role="radiogroup"
    :aria-label="ariaLabel"
    :aria-labelledby="ariaLabelledby"
    :data-testid="testid"
  >
    <button
      v-for="(o, i) in options"
      :key="o.value"
      ref="buttons"
      type="button"
      role="radio"
      class="fa-segmented__btn"
      :aria-checked="o.value === modelValue ? 'true' : 'false'"
      :aria-label="icons ? o.label : undefined"
      :title="icons ? o.label : undefined"
      :tabindex="isStop(i) ? 0 : -1"
      :data-testid="tid(o.value)"
      @click="choose(o.value)"
      @keydown="onKey($event, i)"
    >
      <component :is="o.icon" v-if="o.icon" class="fa-icon" aria-hidden="true" />
      <span v-if="!icons">{{ o.label }}</span>
    </button>
  </div>
</template>
