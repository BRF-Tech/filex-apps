<script setup lang="ts">
// A file-type search's first answer: what filex itself does with each kind
// searched for, before any app (from the filex release the server's data
// describes). A kind filex does nothing with is said plainly, unless the
// page says it in its empty state already (`missing: false`).
import { computed } from 'vue';
import { FileCheck, FileQuestion } from 'lucide-vue-next';
import type { FileTypeSearch } from '@/api/client';
import { S, fmt } from '@/strings';
import { builtinPhrase, builtinTitle, dotted, engineNotes } from '@/lib/fileTypes';

const props = withDefaults(defineProps<{ search: FileTypeSearch; missing?: boolean }>(), { missing: true });

const rows = computed(() =>
  props.search.extensions.map((ext) => ({ ext, builtin: props.search.builtin.find((b) => b.ext === ext) ?? null })),
);
const notes = computed(() => engineNotes(props.search));
const shown = computed(() => rows.value.filter((r) => r.builtin || props.missing));
</script>

<template>
  <section v-if="shown.length" class="fa-ftpanel" :aria-label="S.fileTypes.panelLabel" data-testid="ft-panel">
    <template v-for="r in shown" :key="r.ext">
      <div v-if="r.builtin" class="fa-callout fa-callout--info fa-callout--row" :data-testid="`ft-builtin-${r.ext}`">
        <FileCheck class="fa-callout__icon" aria-hidden="true" />
        <div class="fa-callout__body">
          <p class="fa-callout__title">{{ builtinTitle(r.builtin, search) }}</p>
          <p>
            {{ fmt(S.fileTypes.builtinLead, { version: search.filex }) }}
            <strong>{{ builtinPhrase(r.builtin, search) }}</strong>
          </p>
        </div>
      </div>
      <div v-else class="fa-callout fa-callout--row" :data-testid="`ft-notbuiltin-${r.ext}`">
        <FileQuestion class="fa-callout__icon" aria-hidden="true" />
        <div class="fa-callout__body">
          <p>{{ fmt(S.fileTypes.notBuiltin, { ext: dotted(r.ext) }) }}</p>
        </div>
      </div>
    </template>
    <p v-for="n in notes" :key="n" class="fa-note" data-testid="ft-engine-note">{{ n }}</p>
  </section>
</template>
