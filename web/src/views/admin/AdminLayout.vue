<script setup lang="ts">
// Administration: one page per section, each with its own address, under the
// administrator role. The server checks the role on every call.
import { KeyRound, LayoutGrid, ScrollText, Server, Settings, Users } from 'lucide-vue-next';
import { S } from '@/strings';
import AccessGate from '@/components/AccessGate.vue';

const tabs = [
  { name: 'admin-publishers', label: S.admin.publishers, icon: Users },
  { name: 'admin-apps', label: S.admin.apps, icon: LayoutGrid },
  { name: 'admin-index', label: S.admin.index, icon: KeyRound },
  { name: 'admin-validators', label: S.admin.validators, icon: Server },
  { name: 'admin-audit', label: S.admin.audit, icon: ScrollText },
];
</script>

<template>
  <div class="fa-page">
    <div class="fa-pagehead">
      <span class="fa-pagehead__icon" aria-hidden="true"><Settings /></span>
      <div class="fa-pagehead__text">
        <h1>{{ S.admin.title }}</h1>
        <p class="fa-pagehead__lead">{{ S.admin.intro }}</p>
      </div>
    </div>
    <AccessGate need="admin">
      <nav class="fa-tabs" :aria-label="S.admin.tabsLabel">
        <RouterLink v-for="t in tabs" :key="t.name" :to="{ name: t.name }" active-class="is-active" :data-testid="`tab-${t.name}`">
          <component :is="t.icon" aria-hidden="true" />{{ t.label }}
        </RouterLink>
      </nav>
      <RouterView />
    </AccessGate>
  </div>
</template>
