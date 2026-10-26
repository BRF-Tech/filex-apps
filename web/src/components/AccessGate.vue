<script setup lang="ts">
// What a page needs from the session, said plainly instead of a blank page or
// a bounce. The server enforces every right again; this only explains.
import { computed } from 'vue';
import { useRoute } from 'vue-router';
import { Lock, LogIn } from 'lucide-vue-next';
import { S, fmt } from '@/strings';
import { hasRole, isGithub, session } from '@/lib/session';

const props = defineProps<{ need: 'signed-in' | 'github' | 'reviewer' | 'admin' }>();
const route = useRoute();

const state = computed<'wait' | 'ok' | 'sign-in' | 'github' | 'role'>(() => {
  if (!session.loaded) return 'wait';
  const me = session.me;
  if (!me) return 'sign-in';
  if (props.need === 'github') return isGithub(me) ? 'ok' : 'github';
  if (props.need === 'reviewer' || props.need === 'admin') return hasRole(me, props.need) ? 'ok' : 'role';
  return 'ok';
});
const signInTo = computed(() => ({ name: 'sign-in', query: { return_to: route.fullPath } }));
const roleName = computed(() => (props.need === 'admin' ? S.gate.admin : S.gate.reviewer));
</script>

<template>
  <p v-if="state === 'wait'" class="fa-note" role="status">{{ S.common.loading }}</p>
  <slot v-else-if="state === 'ok'" />
  <div v-else class="fa-empty" data-testid="access-gate" :data-state="state">
    <span class="fa-empty__icon" aria-hidden="true"><Lock /></span>
    <p v-if="state === 'sign-in'" class="fa-empty__title">{{ S.gate.signInNeeded }}</p>
    <p v-else-if="state === 'github'" class="fa-empty__title">{{ S.gate.githubNeeded }}</p>
    <p v-else class="fa-empty__title">{{ fmt(S.gate.roleNeeded, { role: roleName }) }}</p>
    <RouterLink v-if="state !== 'role'" class="fe-btn fe-btn--primary" :to="signInTo"><LogIn class="fa-icon" aria-hidden="true" />{{ S.nav.signIn }}</RouterLink>
  </div>
</template>
