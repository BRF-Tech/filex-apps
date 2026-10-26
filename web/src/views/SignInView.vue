<script setup lang="ts">
// One door: GitHub, for everyone. Publishing needs it (it proves who may list
// a repository), and the reviewer and administrator roles come from the
// account's permission on the store's administration repository, which the
// server reads at every sign-in. The link goes to the store's own /auth/*
// route, which redirects to GitHub; the interface never talks to GitHub
// itself, and never decides a role.
//
// ⚠ Whether the door is open comes from /api/v1/me (`providers`, on 200 and
// on 401), read once by the session. The page never probes /auth/*/login: that
// route counts against the sign-in rate limit, so a probe made every sign-in
// cost two (e2e lane, 2026-10-04).
import { computed } from 'vue';
import { useRoute } from 'vue-router';
import { LogoMark } from '@brftech/filex-core';
import { CircleCheck, Github, ShieldCheck } from 'lucide-vue-next';
import { S, fmt } from '@/strings';
import { returnTo, session } from '@/lib/session';

const route = useRoute();
const back = computed(() => {
  const r = typeof route.query.return_to === 'string' ? route.query.return_to : '/';
  return returnTo(r.startsWith('/sign-in') ? '/' : r);
});
const githubUrl = computed(() => '/auth/github/login?return_to=' + encodeURIComponent(back.value));

/** Unknown (an older server, /me unreachable) keeps the link: the server decides. */
const githubOn = computed(() => session.providers?.github !== false);
const provider = S.nav.viaGithub;
</script>

<template>
  <div class="fa-signin">
    <section class="fa-panel fa-signin__card" aria-labelledby="si-h">
      <div class="fa-signin__head">
        <LogoMark />
        <h1 id="si-h">{{ S.signIn.title }}</h1>
        <p>{{ S.signIn.lead }}</p>
      </div>
      <div v-if="session.me" class="fa-callout fa-callout--info fa-callout--row" data-testid="signed-in">
        <CircleCheck class="fa-callout__icon" aria-hidden="true" />
        <div class="fa-callout__body"><p>{{ fmt(S.signIn.already, { login: session.me.identity.login, provider }) }}</p></div>
      </div>
      <div class="fa-door">
        <h2 id="si-pub" class="fa-door__title">{{ S.signIn.publishers }}</h2>
        <a v-if="githubOn" :href="githubUrl" class="fe-btn fe-btn--primary fe-btn--lg" data-testid="sign-in-github">
          <Github class="fa-icon" aria-hidden="true" />{{ S.signIn.github }}
        </a>
        <a v-else class="fe-btn fe-btn--primary fe-btn--lg fa-btn-off" role="link" aria-disabled="true" aria-describedby="si-gh-note" data-testid="sign-in-github">
          <Github class="fa-icon" aria-hidden="true" />{{ S.signIn.github }}
        </a>
        <p v-if="!githubOn" id="si-gh-note" class="fa-note" data-testid="sign-in-github-note">{{ S.signIn.notConfigured }}</p>
        <p class="fa-door__note"><ShieldCheck class="fa-icon" aria-hidden="true" />{{ S.signIn.publishersBody }}</p>
      </div>
    </section>
  </div>
</template>
