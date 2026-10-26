<script setup lang="ts">
// Official (the store operator's own publisher) and Verified (the store confirmed who this is):
// a word each, with a mark beside it. The word is the meaning; the mark and
// its colour only add to it.
import { BadgeCheck, ShieldCheck } from 'lucide-vue-next';
import { S } from '@/strings';

defineProps<{ verified?: boolean; official?: boolean }>();
</script>

<template>
  <span v-if="verified || official" class="fa-badges">
    <span v-if="official" class="fa-badge fa-badge--info" :title="S.common.officialHint"><BadgeCheck aria-hidden="true" />{{ S.common.official }}</span>
    <span v-if="verified" class="fa-badge fa-badge--ok" :title="S.common.verifiedHint"><ShieldCheck aria-hidden="true" />{{ S.common.verified }}</span>
  </span>
</template>
