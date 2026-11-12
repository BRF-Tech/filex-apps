<script setup lang="ts">
// What setting a version up asks, read by the server from its manifest
// (filex 0.60.0 `routes`, `setup`, `link_shortener`; `user_permissions`):
// the app's own setup step, its own addresses, the short-link role and the
// warning before its data is erased, each a row like a permission's; then
// what an administrator grants inside it, with who holds each until then.
import { computed } from 'vue';
import { Eraser, Globe, Link2, Users, Wand2 } from 'lucide-vue-next';
import type { AppSetup } from '@/api/client';
import { S, fmt } from '@/strings';
import { text } from '@/lib/format';

const props = defineProps<{ setup: AppSetup }>();

const routeLabel = (id: string) => {
  const r = props.setup.routes.find((x) => x.id === id);
  return (r && text(r.label)) || id;
};
const requiresLabel = computed(() => {
  const id = props.setup.link_shortener?.requires;
  if (!id) return '';
  const p = props.setup.user_permissions.find((x) => x.id === id);
  return (p && text(p.label)) || id;
});
const hasRows = computed(
  () => props.setup.wizard || props.setup.routes.length > 0 || !!props.setup.link_shortener || !!text(props.setup.reset_warning),
);
const who = (d: string) => fmt(S.setup.peopleDefault, { who: S.setup.defaultWho[d] ?? d });
</script>

<template>
  <section class="fa-section" aria-labelledby="setup-h" data-testid="app-setup">
    <h2 id="setup-h">{{ S.setup.title }}</h2>
    <p v-if="setup.needs_setup" class="fa-note" data-testid="setup-intro">{{ S.setup.intro }}</p>
    <ul v-if="hasRows" class="fa-perms" data-testid="setup-rows">
      <li v-if="setup.wizard" class="fa-perm fa-tone-1" data-testid="setup-wizard">
        <span class="fa-perm__icon" aria-hidden="true"><Wand2 /></span>
        <span class="fa-perm__text">
          <span class="fa-perm__label">{{ S.setup.wizard }}</span>
          <p class="fa-perm__why">{{ S.setup.wizardText }}</p>
        </span>
      </li>
      <li v-for="r in setup.routes" :key="r.id" class="fa-perm fa-tone-5" :data-testid="`setup-route-${r.id}`">
        <span class="fa-perm__icon" aria-hidden="true"><Globe /></span>
        <span class="fa-perm__text">
          <span class="fa-perm__label">{{ fmt(S.setup.route, { label: text(r.label) || r.id }) }}</span>
          <p class="fa-perm__why">{{ S.setup.routeText }}</p>
          <p v-if="r.default_prefix" class="fa-perm__why">{{ fmt(S.setup.routeSuggested, { prefix: r.default_prefix }) }}</p>
        </span>
      </li>
      <li v-if="setup.link_shortener" class="fa-perm fa-tone-2" data-testid="setup-shortener">
        <span class="fa-perm__icon" aria-hidden="true"><Link2 /></span>
        <span class="fa-perm__text">
          <span class="fa-perm__label">{{ S.setup.shortener }}</span>
          <p class="fa-perm__why">{{ fmt(S.setup.shortenerText, { route: routeLabel(setup.link_shortener.route) }) }}</p>
          <p v-if="requiresLabel" class="fa-perm__why">{{ fmt(S.setup.shortenerRequires, { permission: requiresLabel }) }}</p>
        </span>
      </li>
      <li v-if="text(setup.reset_warning)" class="fa-perm fa-tone-0" data-testid="setup-reset">
        <span class="fa-perm__icon" aria-hidden="true"><Eraser /></span>
        <span class="fa-perm__text">
          <span class="fa-perm__label">{{ S.setup.reset }}</span>
          <p class="fa-perm__why">{{ fmt(S.setup.resetText, { warning: text(setup.reset_warning) }) }}</p>
        </span>
      </li>
    </ul>
    <template v-if="setup.user_permissions.length">
      <h3 id="setup-people-h">{{ S.setup.peopleTitle }}</h3>
      <p class="fa-note">{{ S.setup.peopleIntro }}</p>
      <ul class="fa-perms" aria-labelledby="setup-people-h" data-testid="setup-people">
        <li v-for="p in setup.user_permissions" :key="p.id" class="fa-perm fa-tone-4" :data-testid="`setup-people-${p.id}`">
          <span class="fa-perm__icon" aria-hidden="true"><Users /></span>
          <span class="fa-perm__text">
            <span class="fa-perm__label">{{ text(p.label) || p.id }}</span>
            <p v-if="text(p.description)" class="fa-perm__why">{{ text(p.description) }}</p>
            <p class="fa-perm__why">{{ who(p.default) }}</p>
            <code class="fa-perm__id">{{ p.id }}</code>
          </span>
        </li>
      </ul>
    </template>
  </section>
</template>
