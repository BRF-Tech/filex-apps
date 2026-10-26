<script setup lang="ts">
// What a version may do, in filex's own words (the label filex's install
// review shows), each with a picture of what it touches and the publisher's
// reason under it. The permission's id stays visible, small, for the reader
// who compares it with the manifest.
import type { Component } from 'vue';
import { AppWindow, FileText, Globe, Image, KeyRound, Repeat } from 'lucide-vue-next';
import type { PermissionRow } from '@/api/client';
import { S, fmt } from '@/strings';
import { permissionKind, text, type PermissionKind } from '@/lib/format';

defineProps<{ rows: PermissionRow[] }>();

const LOOK: Record<PermissionKind, { icon: Component; tone: number }> = {
  files: { icon: FileText, tone: 0 },
  ui: { icon: AppWindow, tone: 1 },
  net: { icon: Globe, tone: 5 },
  convert: { icon: Repeat, tone: 2 },
  thumbnail: { icon: Image, tone: 4 },
  other: { icon: KeyRound, tone: 6 },
};
const look = (id: string) => LOOK[permissionKind(id)];
</script>

<template>
  <ul class="fa-perms" data-testid="permissions">
    <li v-for="p in rows" :key="p.id" class="fa-perm" :class="`fa-tone-${look(p.id).tone}`">
      <span class="fa-perm__icon" aria-hidden="true"><component :is="look(p.id).icon" /></span>
      <span class="fa-perm__text">
        <span class="fa-perm__label">{{ p.label || p.id }}</span>
        <p v-if="text(p.reason)" class="fa-perm__why">{{ fmt(S.app.permissionWhy, { reason: text(p.reason) }) }}</p>
        <code class="fa-perm__id">{{ p.id }}</code>
      </span>
    </li>
  </ul>
</template>
