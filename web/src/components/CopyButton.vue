<script setup lang="ts">
// Copies a value and says so (to the eye and to a screen reader). Falls back
// to selecting a hidden field when the Clipboard API is refused.
import { ref } from 'vue';
import { Check, Copy } from 'lucide-vue-next';
import { S } from '@/strings';

const props = defineProps<{ value: string; label: string }>();
const said = ref('');
let timer: ReturnType<typeof setTimeout> | undefined;

async function copy() {
  let ok = false;
  try {
    await navigator.clipboard.writeText(props.value);
    ok = true;
  } catch {
    ok = legacyCopy(props.value);
  }
  said.value = ok ? S.common.copied : S.common.copyFailed;
  clearTimeout(timer);
  timer = setTimeout(() => (said.value = ''), 2500);
}

function legacyCopy(v: string): boolean {
  const ta = document.createElement('textarea');
  ta.value = v;
  ta.setAttribute('readonly', '');
  ta.className = 'fe-sr-only';
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  ta.remove();
  return ok;
}
</script>

<template>
  <span class="fa-copy">
    <button type="button" class="fe-btn fe-btn--sm" :aria-label="label" :title="label" data-testid="copy" @click="copy">
      <Check v-if="said === S.common.copied" class="fa-icon" aria-hidden="true" />
      <Copy v-else class="fa-icon" aria-hidden="true" />
      {{ said === S.common.copied ? S.common.copied : S.common.copy }}
    </button>
    <span class="fe-sr-only" role="status" aria-live="polite">{{ said }}</span>
  </span>
</template>
