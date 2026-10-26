<script setup lang="ts" generic="T extends string">
// A select, without the browser's native dropdown: a button-like combobox
// and a listbox panel (the WAI-ARIA "select-only combobox" pattern).
//
// Written to move into @brftech/filex-core as is: no store strings, no
// store modules, only Vue, lucide icons (core ships them) and filex's CSS
// variables (--fe-*). Every text comes in through props.
//
// - v-model (a string), `options` [{ value, label, hint?, disabled? }];
// - `id` lands on the combobox, so <label for="id"> names it and focuses it;
// - `required`: an empty value makes a surrounding form invalid (a hidden
//   input carries the native constraint, the browser's own validation stops
//   on it and the combobox is focused and marked), `disabled`, `name`;
// - keyboard: ArrowDown/ArrowUp/Enter/Space/Alt+ArrowDown open, arrows,
//   Home/End, PageUp/PageDown move, typing jumps to the next match, Enter or
//   Space choose, Escape closes without a change, Tab chooses and moves on;
// - the panel is teleported to <body> (or to the open <dialog> the combobox
//   is in: a modal dialog sits above everything else), placed under the
//   combobox or above it when there is no room below, and it follows scroll
//   and resize; a pointer press outside closes it.
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue';
import { Check, ChevronDown } from 'lucide-vue-next';

interface FaSelectOption {
  value: T;
  label: string;
  hint?: string;
  disabled?: boolean;
}

const props = withDefaults(
  defineProps<{
    modelValue: T;
    options: FaSelectOption[];
    id?: string;
    name?: string;
    placeholder?: string;
    required?: boolean;
    disabled?: boolean;
    invalid?: boolean;
    ariaLabel?: string;
    ariaLabelledby?: string;
    ariaDescribedby?: string;
    /** The message a form's own validation shows when a required value is missing. */
    requiredMessage?: string;
    testid?: string;
  }>(),
  {
    id: undefined,
    name: undefined,
    placeholder: '',
    required: false,
    disabled: false,
    invalid: false,
    ariaLabel: undefined,
    ariaLabelledby: undefined,
    ariaDescribedby: undefined,
    requiredMessage: '',
    testid: undefined,
  },
);
const emit = defineEmits<{ (e: 'update:modelValue', v: T): void; (e: 'change', v: T): void }>();

const uid = 'fa-sel-' + Math.random().toString(36).slice(2, 9);
const comboId = computed(() => props.id ?? uid);
const listId = uid + '-list';
const optId = (i: number) => `${uid}-opt-${i}`;

const open = ref(false);
const active = ref(-1);
const trigger = ref<HTMLButtonElement | null>(null);
const panel = ref<HTMLElement | null>(null);
const hidden = ref<HTMLInputElement | null>(null);
const target = ref<HTMLElement | null>(null);
const place = ref<{ top: number; bottom: number; left: number; width: number; maxHeight: number; up: boolean }>({
  top: 0,
  bottom: 0,
  left: 0,
  width: 0,
  maxHeight: 280,
  up: false,
});
const flagged = ref(false);
// What was typed for the jump to a matching option, and when.
let typed = '';
let typedAt = 0;

const selectedIndex = computed(() => props.options.findIndex((o) => o.value === props.modelValue));
const selected = computed(() => props.options[selectedIndex.value]);
const shown = computed(() => selected.value?.label ?? props.placeholder);
const isInvalid = computed(() => props.invalid || flagged.value);

watch(
  () => props.modelValue,
  (v) => {
    if (v !== '') flagged.value = false;
    void nextTick(() => hidden.value?.setCustomValidity(props.required && !v ? props.requiredMessage || ' ' : ''));
  },
  { immediate: true },
);
watch(
  () => props.required,
  () => hidden.value?.setCustomValidity(props.required && !props.modelValue ? props.requiredMessage || ' ' : ''),
);

function enabledFrom(start: number, step: number): number {
  const n = props.options.length;
  for (let k = 0, i = start; k < n; k++, i += step) {
    if (i < 0 || i >= n) return -1;
    if (!props.options[i].disabled) return i;
  }
  return -1;
}

function place_() {
  const t = trigger.value;
  if (!t) return;
  const r = t.getBoundingClientRect();
  const vh = window.innerHeight;
  const vw = window.innerWidth;
  const below = vh - r.bottom - 8;
  const above = r.top - 8;
  const want = Math.min(320, props.options.length * 44 + 8);
  const up = below < Math.min(want, 160) && above > below;
  const width = Math.min(Math.max(r.width, 180), vw - 16);
  const left = Math.min(Math.max(8, r.left), vw - width - 8);
  place.value = {
    top: r.bottom + 4,
    bottom: vh - r.top + 4,
    left,
    width,
    maxHeight: Math.max(120, Math.min(320, up ? above : below)),
    up,
  };
}

function onOutside(e: Event) {
  const n = e.target as Node | null;
  if (!n || trigger.value?.contains(n) || panel.value?.contains(n)) return;
  close(false);
}

function listen(on: boolean) {
  const fn = on ? window.addEventListener : window.removeEventListener;
  fn('resize', place_);
  fn('scroll', place_, true);
  const doc = on ? document.addEventListener : document.removeEventListener;
  doc.call(document, 'pointerdown', onOutside, true);
}

async function show(at?: number) {
  if (props.disabled || open.value) return;
  target.value = (trigger.value?.closest('dialog[open]') as HTMLElement | null) ?? document.body;
  place_();
  open.value = true;
  active.value = at ?? (selectedIndex.value >= 0 ? selectedIndex.value : enabledFrom(0, 1));
  listen(true);
  await nextTick();
  scrollActive();
}

function close(refocus = true) {
  if (!open.value) return;
  open.value = false;
  typed = '';
  listen(false);
  if (refocus) trigger.value?.focus();
}

function choose(i: number, refocus = true) {
  const o = props.options[i];
  if (!o || o.disabled) return;
  if (o.value !== props.modelValue) {
    emit('update:modelValue', o.value);
    emit('change', o.value);
  }
  close(refocus);
}

function scrollActive() {
  const el = document.getElementById(optId(active.value));
  if (el && typeof el.scrollIntoView === 'function') el.scrollIntoView({ block: 'nearest' });
}

function move(to: number) {
  if (to < 0) return;
  active.value = to;
  void nextTick(scrollActive);
}

// Typing jumps to the next option whose label starts with what was typed.
function typeahead(ch: string) {
  const now = Date.now();
  typed = now - typedAt > 600 ? ch : typed + ch;
  typedAt = now;
  const n = props.options.length;
  const from = typed.length > 1 ? Math.max(active.value, 0) : active.value + 1;
  for (let k = 0; k < n; k++) {
    const i = (from + k + n) % n;
    const o = props.options[i];
    if (!o.disabled && o.label.toLocaleLowerCase().startsWith(typed.toLocaleLowerCase())) return i;
  }
  return -1;
}

function onKey(e: KeyboardEvent) {
  if (props.disabled) return;
  const k = e.key;
  if (!open.value) {
    if (k === 'ArrowDown' || k === 'ArrowUp' || k === 'Enter' || k === ' ') {
      e.preventDefault();
      void show();
    } else if (k === 'Home' || k === 'End') {
      e.preventDefault();
      void show(k === 'Home' ? enabledFrom(0, 1) : enabledFrom(props.options.length - 1, -1));
    } else if (k.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey && k !== ' ') {
      const i = typeahead(k);
      if (i >= 0) void show(i);
    }
    return;
  }
  switch (k) {
    case 'ArrowDown':
      e.preventDefault();
      if (e.altKey) return;
      move(enabledFrom(active.value + 1, 1));
      return;
    case 'ArrowUp':
      e.preventDefault();
      if (e.altKey) return choose(active.value);
      move(enabledFrom(active.value - 1, -1));
      return;
    case 'Home':
      e.preventDefault();
      move(enabledFrom(0, 1));
      return;
    case 'End':
      e.preventDefault();
      move(enabledFrom(props.options.length - 1, -1));
      return;
    case 'PageDown':
      e.preventDefault();
      move(enabledFrom(Math.min(active.value + 10, props.options.length - 1), -1));
      return;
    case 'PageUp':
      e.preventDefault();
      move(enabledFrom(Math.max(active.value - 10, 0), 1));
      return;
    case 'Enter':
    case ' ':
      e.preventDefault();
      choose(active.value);
      return;
    case 'Escape':
      e.preventDefault();
      e.stopPropagation();
      close();
      return;
    case 'Tab':
      choose(active.value, false);
      return;
  }
  if (k.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
    const i = typeahead(k);
    if (i >= 0) move(i);
  }
}

function onTrigger() {
  if (open.value) close();
  else void show();
}

// The form's own validation found the required value missing.
function onInvalid(e: Event) {
  e.preventDefault();
  flagged.value = true;
  trigger.value?.focus();
}

onBeforeUnmount(() => listen(false));
defineExpose({ focus: () => trigger.value?.focus(), open: show, close });
</script>

<template>
  <span class="fa-select" :class="{ 'is-open': open, 'is-disabled': disabled, 'is-invalid': isInvalid }">
    <button
      :id="comboId"
      ref="trigger"
      type="button"
      class="fa-select__trigger"
      role="combobox"
      :disabled="disabled"
      aria-haspopup="listbox"
      :aria-expanded="open ? 'true' : 'false'"
      :aria-controls="listId"
      :aria-activedescendant="open && active >= 0 ? optId(active) : undefined"
      :aria-label="ariaLabel"
      :aria-labelledby="ariaLabelledby"
      :aria-describedby="ariaDescribedby"
      :aria-required="required ? 'true' : undefined"
      :aria-invalid="isInvalid ? 'true' : undefined"
      :data-testid="testid"
      :data-value="modelValue"
      @click="onTrigger"
      @keydown="onKey"
      @keyup.space.prevent
    >
      <span class="fa-select__value" :class="{ 'is-placeholder': !selected }">{{ shown }}</span>
      <ChevronDown class="fa-select__chev" aria-hidden="true" />
    </button>
    <input
      ref="hidden"
      class="fa-select__native"
      tabindex="-1"
      aria-hidden="true"
      :name="name"
      :value="modelValue"
      :required="required"
      :disabled="disabled"
      @invalid="onInvalid"
    />
    <Teleport v-if="open && target" :to="target">
      <ul
        :id="listId"
        ref="panel"
        class="fa-select__panel"
        :class="{ 'is-up': place.up }"
        role="listbox"
        :aria-labelledby="ariaLabelledby ?? comboId"
        :style="{
          left: place.left + 'px',
          width: place.width + 'px',
          maxHeight: place.maxHeight + 'px',
          top: place.up ? undefined : place.top + 'px',
          bottom: place.up ? place.bottom + 'px' : undefined,
        }"
        :data-testid="testid ? `${testid}-list` : undefined"
        @mousedown.prevent
      >
        <li
          v-for="(o, i) in options"
          :id="optId(i)"
          :key="o.value"
          class="fa-select__option"
          :class="{ 'is-active': i === active, 'is-selected': o.value === modelValue }"
          role="option"
          :aria-selected="o.value === modelValue ? 'true' : 'false'"
          :aria-disabled="o.disabled ? 'true' : undefined"
          :data-value="o.value"
          :data-testid="testid ? `${testid}-option-${o.value || 'none'}` : undefined"
          @mousemove="!o.disabled && (active = i)"
          @click="choose(i)"
        >
          <span class="fa-select__label">
            <span>{{ o.label }}</span>
            <span v-if="o.hint" class="fa-select__hint">{{ o.hint }}</span>
          </span>
          <Check v-if="o.value === modelValue" class="fa-select__check" aria-hidden="true" />
        </li>
      </ul>
    </Teleport>
  </span>
</template>
