<script setup lang="ts">
// A decision that deserves a second look (revoke, yank, reject, suspend,
// approve): the browser's own modal <dialog>, so focus stays inside it,
// Escape closes it and the page behind is inert. With `reason` it asks for
// the sentence the contract requires (ReasonRequest: 1 to 2000 characters).
import { nextTick, ref, watch } from 'vue';
import { S } from '@/strings';

const props = withDefaults(
  defineProps<{
    open: boolean;
    title: string;
    intro?: string;
    confirmLabel: string;
    reason?: boolean;
    reasonLabel?: string;
    danger?: boolean;
    busy?: boolean;
    error?: string;
  }>(),
  { intro: '', reason: false, reasonLabel: '', danger: false, busy: false, error: '' },
);
const emit = defineEmits<{ (e: 'confirm', reason: string): void; (e: 'cancel'): void }>();

const el = ref<HTMLDialogElement | null>(null);
const text = ref('');
const missing = ref(false);
const uid = 'fa-dlg-' + Math.random().toString(36).slice(2, 8);

watch(
  () => props.open,
  async (open) => {
    await nextTick();
    const d = el.value;
    if (!d) return;
    if (open && !d.open) {
      text.value = '';
      missing.value = false;
      if (typeof d.showModal === 'function') d.showModal();
      else d.setAttribute('open', '');
    } else if (!open && d.open) {
      d.close();
    }
  },
  { immediate: true },
);

function submit() {
  if (props.reason && !text.value.trim()) {
    missing.value = true;
    return;
  }
  emit('confirm', text.value.trim());
}
</script>

<template>
  <dialog ref="el" class="fa-dialog" :aria-labelledby="uid + '-t'" @cancel.prevent="emit('cancel')">
    <form class="fe-form" novalidate @submit.prevent="submit">
      <h2 :id="uid + '-t'" class="fa-dialog__title">{{ title }}</h2>
      <p v-if="intro" class="fa-note">{{ intro }}</p>
      <slot />
      <div v-if="reason" class="fe-field">
        <label class="fe-field__label" :for="uid + '-r'">{{ reasonLabel || S.common.reasonLabel }}</label>
        <textarea
          :id="uid + '-r'"
          v-model="text"
          class="fe-input"
          rows="4"
          maxlength="2000"
          required
          :aria-invalid="missing ? 'true' : undefined"
          :aria-describedby="uid + '-h'"
          data-testid="dialog-reason"
        ></textarea>
        <p :id="uid + '-h'" class="fe-form__hint">{{ S.common.reasonHint }}</p>
        <p v-if="missing" class="fe-form__error" role="alert">{{ S.common.required }}</p>
      </div>
      <p v-if="error" class="fe-form__error" role="alert" data-testid="dialog-error">{{ error }}</p>
      <div class="fa-dialog__actions">
        <button type="button" class="fe-btn" :disabled="busy" @click="emit('cancel')">{{ S.common.cancel }}</button>
        <button type="submit" class="fe-btn" :class="danger ? 'fe-btn--danger' : 'fe-btn--primary'" :disabled="busy" data-testid="dialog-confirm">
          {{ confirmLabel }}
        </button>
      </div>
    </form>
  </dialog>
</template>
