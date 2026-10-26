<script setup lang="ts">
// An app's screenshots: a strip that scrolls sideways inside itself, each
// picture a button that opens the gallery. The gallery is the browser's own
// modal <dialog>, full screen: focus stays inside it (the page behind is
// inert), Escape closes it and focus goes back to the picture that opened
// it. Previous and next by the side buttons, the arrow keys, a swipe on a
// touch screen, or the thumbnails along the bottom.
import { computed, nextTick, onBeforeUnmount, ref } from 'vue';
import { ChevronLeft, ChevronRight, Maximize2, X } from 'lucide-vue-next';
import { S, fmt } from '@/strings';

export interface Shot {
  src: string;
  caption: string;
  alt: string;
}
const props = defineProps<{ shots: Shot[]; label: string }>();

const dlg = ref<HTMLDialogElement | null>(null);
const at = ref(0);
const current = computed(() => props.shots[at.value]);
const many = computed(() => props.shots.length > 1);
const counter = computed(() => fmt(S.app.lightbox, { n: at.value + 1, count: props.shots.length }));
let opener: HTMLElement | null = null;

// The page behind does not scroll while the gallery is open.
function lockScroll(on: boolean) {
  document.documentElement.classList.toggle('fa-noscroll', on);
}

async function open(i: number, ev: Event) {
  opener = ev.currentTarget as HTMLElement;
  at.value = i;
  await nextTick();
  const d = dlg.value;
  if (!d) return;
  if (typeof d.showModal === 'function') d.showModal();
  else d.setAttribute('open', '');
  lockScroll(true);
  await nextTick();
  d.querySelector<HTMLElement>('[data-testid="lightbox-close"]')?.focus();
}
function close() {
  const d = dlg.value;
  if (d?.open) d.close();
}
function onClose() {
  lockScroll(false);
  opener?.focus();
  opener = null;
}
onBeforeUnmount(() => lockScroll(false));

async function go(i: number) {
  const n = props.shots.length;
  at.value = (i + n) % n;
  await nextTick();
  const thumb = dlg.value?.querySelector<HTMLElement>('.fa-lightbox__thumbs [aria-current="true"]');
  if (thumb && typeof thumb.scrollIntoView === 'function') thumb.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}
const step = (by: number) => go(at.value + by);

function onKey(e: KeyboardEvent) {
  if (!many.value) return;
  if (e.key === 'ArrowRight') {
    e.preventDefault();
    void step(1);
  } else if (e.key === 'ArrowLeft') {
    e.preventDefault();
    void step(-1);
  } else if (e.key === 'Home') {
    e.preventDefault();
    void go(0);
  } else if (e.key === 'End') {
    e.preventDefault();
    void go(props.shots.length - 1);
  }
}

// A swipe on a touch screen: a mostly sideways move of 40 px or more.
let touch: { x: number; y: number } | null = null;
function onPointerDown(e: PointerEvent) {
  touch = e.pointerType === 'mouse' ? null : { x: e.clientX, y: e.clientY };
}
function onPointerUp(e: PointerEvent) {
  if (!touch || !many.value) return;
  const dx = e.clientX - touch.x;
  const dy = e.clientY - touch.y;
  touch = null;
  if (Math.abs(dx) >= 40 && Math.abs(dx) > Math.abs(dy) * 1.5) void step(dx < 0 ? 1 : -1);
}
// A click on the empty stage around the picture closes, as a person expects
// of a picture viewer; a click on the picture or a button does not.
function onStageClick(e: MouseEvent) {
  if (e.target === e.currentTarget) close();
}
</script>

<template>
  <div>
    <ul class="fa-gallery" :aria-label="label" data-testid="gallery">
      <li v-for="(s, i) in shots" :key="s.src">
        <button
          type="button"
          class="fa-shot"
          :aria-label="fmt(S.app.openShot, { n: i + 1, count: shots.length }) + (s.caption ? `: ${s.caption}` : '')"
          :data-testid="`shot-${i + 1}`"
          @click="open(i, $event)"
        >
          <span class="fa-shot__frame">
            <img :src="s.src" :alt="s.alt" loading="lazy" />
            <span class="fa-shot__zoom" aria-hidden="true"><Maximize2 /></span>
          </span>
          <span v-if="s.caption" class="fa-shot__caption" aria-hidden="true">{{ s.caption }}</span>
        </button>
      </li>
    </ul>
    <dialog ref="dlg" class="fa-lightbox" :aria-label="label" data-testid="lightbox" @close="onClose" @keydown="onKey">
      <div v-if="current" class="fa-lightbox__frame">
        <div class="fa-lightbox__bar">
          <span class="fa-lightbox__count" aria-hidden="true">{{ at + 1 }} / {{ shots.length }}</span>
          <p class="fa-lightbox__caption" aria-live="polite" data-testid="lightbox-caption">
            <span class="fe-sr-only">{{ counter }}: </span>{{ current.caption || current.alt }}
          </p>
          <button type="button" class="fa-lightbox__btn" :aria-label="S.app.closeShot" :title="S.app.closeShot" data-testid="lightbox-close" @click="close">
            <X aria-hidden="true" />
          </button>
        </div>
        <div class="fa-lightbox__stage" data-testid="lightbox-stage" @click="onStageClick" @pointerdown="onPointerDown" @pointerup="onPointerUp">
          <img :key="current.src" class="fa-lightbox__img" :src="current.src" :alt="current.alt" draggable="false" />
          <template v-if="many">
            <button
              type="button"
              class="fa-lightbox__btn fa-lightbox__nav fa-lightbox__nav--prev"
              :aria-label="S.app.prevShot"
              :title="S.app.prevShot"
              data-testid="lightbox-prev"
              @click="step(-1)"
            >
              <ChevronLeft aria-hidden="true" />
            </button>
            <button
              type="button"
              class="fa-lightbox__btn fa-lightbox__nav fa-lightbox__nav--next"
              :aria-label="S.app.nextShot"
              :title="S.app.nextShot"
              data-testid="lightbox-next"
              @click="step(1)"
            >
              <ChevronRight aria-hidden="true" />
            </button>
          </template>
        </div>
        <ul v-if="many" class="fa-lightbox__thumbs" :aria-label="S.app.thumbs">
          <li v-for="(s, i) in shots" :key="s.src">
            <button
              type="button"
              :aria-label="fmt(S.app.lightbox, { n: i + 1, count: shots.length }) + (s.caption ? `: ${s.caption}` : '')"
              :aria-current="i === at ? 'true' : undefined"
              :data-testid="`lightbox-thumb-${i + 1}`"
              @click="go(i)"
            >
              <img :src="s.src" alt="" />
            </button>
          </li>
        </ul>
      </div>
    </dialog>
  </div>
</template>
