<script setup lang="ts">
import { computed, nextTick, ref } from 'vue';
import { START_LOCATION, useRoute, useRouter } from 'vue-router';
import { LogoMark, provideTableEnv, type LocaleCode } from '@brftech/filex-core';
import { Bell, ClipboardCheck, LayoutGrid, LogIn, LogOut, Server, Settings, Upload, Users } from 'lucide-vue-next';
import { S, fmt } from './strings';
import { session, signOut, hasRole } from './lib/session';
import { brand, brandParts, hasOperatorInfo, operatorLabel } from './lib/brand';
import { startTheme, tableTheme } from './lib/theme';
import ThemeSwitch from './components/ThemeSwitch.vue';

// Every DataTable on the page takes its language and light/dark mode from
// here (filex's lib/tableEnv), once. The store is English (an open-source
// surface); the mode follows the theme choice below.
startTheme();
provideTableEnv({ locale: ref<LocaleCode>('en'), theme: tableTheme });

const route = useRoute();
const router = useRouter();
const me = computed(() => session.me);
const isReviewer = computed(() => hasRole(me.value, 'reviewer'));
const isAdmin = computed(() => hasRole(me.value, 'admin'));
const signInTo = computed(() => ({ name: 'sign-in', query: route.name === 'sign-in' ? route.query : { return_to: route.fullPath } }));
const who = computed(() => (me.value ? fmt(S.nav.signedInAs, { login: me.value.identity.login }) : ''));

// Publish and its own pages (ask for a publisher, submit), not a publisher's
// page: /publishers/... starts with "/publish" too.
const publishActive = computed(() => route.path === '/publish' || route.path.startsWith('/publish/') || route.name === 'submit');
const initial = computed(() => (me.value?.identity.login.trim()[0] ?? '?').toUpperCase());
// The store's own name (FAPPS_STORE_NAME) in the brand's two weights, its
// logo or filex's mark, and who runs it (lib/brand.ts).
const name = computed(() => brandParts(brand.name));
const operator = computed(() => operatorLabel(brand));
const showAbout = computed(() => hasOperatorInfo(brand));

async function doSignOut() {
  await signOut();
  void router.push({ name: 'catalog' });
}

// A new page moves the keyboard and the screen reader to its heading, the way
// a full page load would; a change of query on the same page (a filter) does
// not steal focus from the field being typed in.
// ⚠ Not "skip the first change of route.path": the first page load does not
// change the path (it starts at "/" too), so that skipped the first click.
const main = ref<HTMLElement | null>(null);
router.afterEach(async (to, from, failure) => {
  if (failure || from === START_LOCATION || to.path === from.path) return;
  await nextTick();
  requestAnimationFrame(() => {
    const h = main.value?.querySelector<HTMLElement>('h1');
    if (h) {
      h.setAttribute('tabindex', '-1');
      h.focus({ preventScroll: true });
    } else main.value?.focus({ preventScroll: true });
  });
});
</script>

<template>
  <a class="fa-skip" href="#main">{{ S.skipToContent }}</a>
  <header class="fa-topbar">
    <div class="fa-wrap fa-topbar__inner">
      <RouterLink to="/" class="fa-brand" data-testid="brand">
        <img v-if="brand.logoUrl" :src="brand.logoUrl" alt="" class="fa-brand__logo" data-testid="brand-logo" />
        <LogoMark v-else />
        <span>{{ name.head }}{{ name.tag ? ' ' : '' }}<span v-if="name.tag" class="fa-brand__tag">{{ name.tag }}</span></span>
      </RouterLink>
      <nav class="fa-nav" :aria-label="S.nav.label">
        <RouterLink :to="{ name: 'catalog' }" exact-active-class="is-active" data-testid="nav-catalog"><LayoutGrid class="fa-icon" aria-hidden="true" />{{ S.nav.catalog }}</RouterLink>
        <RouterLink :to="{ name: 'publishers' }" :class="{ 'is-active': route.path === '/publishers' || route.path.startsWith('/publishers/') }" data-testid="nav-publishers"><Users class="fa-icon" aria-hidden="true" />{{ S.nav.publishers }}</RouterLink>
        <RouterLink to="/publish" :class="{ 'is-active': publishActive }" data-testid="nav-publish"><Upload class="fa-icon" aria-hidden="true" />{{ S.nav.publish }}</RouterLink>
        <RouterLink v-if="isReviewer" to="/review" active-class="is-active" data-testid="nav-review"><ClipboardCheck class="fa-icon" aria-hidden="true" />{{ S.nav.review }}</RouterLink>
        <RouterLink v-if="isAdmin" to="/admin" active-class="is-active" data-testid="nav-admin"><Settings class="fa-icon" aria-hidden="true" />{{ S.nav.admin }}</RouterLink>
      </nav>
      <div class="fa-account">
        <ThemeSwitch />
        <template v-if="me">
          <span class="fa-who" data-testid="who" :title="who">
            <span class="fa-avatar" aria-hidden="true">{{ initial }}</span>
            <span class="fa-who__name">{{ me.identity.login }}</span>
            <span class="fe-sr-only">{{ who }}</span>
          </span>
          <RouterLink :to="{ name: 'my-instances' }" class="fe-btn fe-btn--sm" :title="S.instances.title" :aria-label="S.instances.title" data-testid="nav-instances">
            <Server class="fa-icon" aria-hidden="true" /><span class="fa-signout__label">{{ S.instances.menu }}</span>
          </RouterLink>
          <RouterLink :to="{ name: 'my-notifications' }" class="fe-btn fe-btn--sm" :title="S.notifications.title" :aria-label="S.notifications.title" data-testid="nav-notifications">
            <Bell class="fa-icon" aria-hidden="true" /><span class="fa-signout__label">{{ S.notifications.menu }}</span>
          </RouterLink>
          <button type="button" class="fe-btn fe-btn--sm" data-testid="sign-out" @click="doSignOut"><LogOut class="fa-icon" aria-hidden="true" /><span class="fa-signout__label">{{ S.nav.signOut }}</span></button>
        </template>
        <RouterLink v-else-if="session.loaded" :to="signInTo" class="fe-btn fe-btn--sm fe-btn--primary" data-testid="sign-in"><LogIn class="fa-icon" aria-hidden="true" />{{ S.nav.signIn }}</RouterLink>
      </div>
    </div>
  </header>
  <main id="main" ref="main" class="fa-wrap fa-main" tabindex="-1">
    <RouterView />
  </main>
  <footer class="fa-footer">
    <div class="fa-wrap fa-footer__inner">
      <div class="fa-footer__brand">
        <!-- Not a link: the header's brand is the one link to the home page with the store's name. -->
        <span class="fa-brand">
          <img v-if="brand.logoUrl" :src="brand.logoUrl" alt="" class="fa-brand__logo" />
          <LogoMark v-else />
          <span>{{ name.head }}{{ name.tag ? ' ' : '' }}<span v-if="name.tag" class="fa-brand__tag">{{ name.tag }}</span></span>
        </span>
        <p>{{ S.footer.note }}</p>
      </div>
      <nav :aria-label="S.footer.store">
        <h2>{{ S.footer.store }}</h2>
        <ul>
          <li><RouterLink :to="{ name: 'catalog' }">{{ S.nav.catalog }}</RouterLink></li>
          <li><RouterLink :to="{ name: 'publishers' }">{{ S.nav.publishers }}</RouterLink></li>
          <li><RouterLink to="/publish">{{ S.nav.publish }}</RouterLink></li>
          <li><a href="https://filex.sh" rel="noopener noreferrer">{{ S.footer.whatIsFilex }}</a></li>
        </ul>
      </nav>
      <nav :aria-label="S.footer.verify">
        <h2>{{ S.footer.verify }}</h2>
        <ul class="fa-footer__links">
          <li><a href="/v1/index.json">{{ S.footer.index }}</a></li>
          <li><a href="/v1/index.json.sig">{{ S.footer.signature }}</a></li>
          <li><a href="/v1/keys.json">{{ S.footer.keys }}</a></li>
        </ul>
      </nav>
    </div>
    <!-- Who runs this store (FAPPS_OPERATOR_*, FAPPS_CONTACT, FAPPS_TERMS_URL, FAPPS_PRIVACY_URL): only what is set. -->
    <nav v-if="showAbout" class="fa-wrap fa-footer__about" :aria-label="S.footer.about" data-testid="footer-about">
      <ul>
        <li v-if="operator" data-testid="footer-operator">
          {{ S.footer.operatedBy }}
          <a v-if="brand.operatorUrl" :href="brand.operatorUrl" rel="noopener noreferrer">{{ operator }}</a>
          <template v-else>{{ operator }}</template>
        </li>
        <li v-if="brand.contact"><a :href="brand.contact" rel="noopener noreferrer" data-testid="footer-contact">{{ S.footer.contact }}</a></li>
        <li v-if="brand.termsUrl"><a :href="brand.termsUrl" rel="noopener noreferrer" data-testid="footer-terms">{{ S.footer.terms }}</a></li>
        <li v-if="brand.privacyUrl"><a :href="brand.privacyUrl" rel="noopener noreferrer" data-testid="footer-privacy">{{ S.footer.privacy }}</a></li>
      </ul>
    </nav>
  </footer>
</template>
