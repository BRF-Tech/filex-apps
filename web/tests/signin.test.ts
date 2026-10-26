import { afterEach, describe, expect, it } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h } from 'vue';
import { RouterView } from 'vue-router';
import { loadSession, session } from '@/lib/session';
import { mockFetch, settle, testRouter } from './helpers';

// The sign-in page reads which sign-ins exist from /api/v1/me (`providers`,
// on 200 and on 401) and never probes /auth/*/login: a probe counts against
// the sign-in rate limit, so every sign-in cost two (measured by the e2e
// lane, 2026-10-04).

const Host = defineComponent({ setup: () => () => h(RouterView) });
const mounted: VueWrapper[] = [];

// A browser would leave the page on the link; the test stays and only
// watches what the page itself requests.
function blockNavigation(e: Event) {
  if ((e.target as Element | null)?.closest?.('a[href^="/auth/"]')) e.preventDefault();
}

async function open(providers: { github: boolean }) {
  const { calls } = mockFetch({
    'GET /api/v1/me': { status: 401, body: { error: 'unauthorized', message: 'Sign in first.', providers } },
    'GET /auth/github/login': { status: 503, body: { error: 'not_configured', message: 'x' } },
  });
  await loadSession(true);
  const router = testRouter();
  await router.push('/sign-in?return_to=/publish');
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  mounted.push(w);
  await settle();
  return { w, calls };
}

afterEach(() => {
  document.removeEventListener('click', blockNavigation, true);
  while (mounted.length) mounted.pop()!.unmount();
  document.body.replaceChildren();
  session.me = null;
});

describe('sign-in', () => {
  it('never requests /auth/*/login itself, not on load and not on a click', async () => {
    document.addEventListener('click', blockNavigation, true);
    const { w, calls } = await open({ github: true });
    await w.find('[data-testid="sign-in-github"]').trigger('click');
    await settle();
    expect(calls.filter((c) => c.path.startsWith('/auth/')).map((c) => c.path)).toEqual([]);
    expect(w.find('[data-testid="sign-in-github"]').attributes('href')).toBe('/auth/github/login?return_to=%2Fpublish');
  });

  it('has one door, GitHub, for publishers, reviewers and administrators alike', async () => {
    const { w } = await open({ github: true });
    const doors = w.findAll('a[href^="/auth/"]');
    expect(doors.map((a) => a.attributes('href'))).toEqual(['/auth/github/login?return_to=%2Fpublish']);
    expect(w.find('[data-testid="sign-in-oidc"]').exists()).toBe(false);
    expect(w.text()).not.toMatch(/keycloak|oidc/i);
    expect(w.text()).toContain('administration repository');
  });

  it('says on load, from /api/v1/me, which sign-in this server has not set up', async () => {
    const { w, calls } = await open({ github: false });
    const gh = w.find('[data-testid="sign-in-github"]');
    expect(gh.attributes('href')).toBeUndefined();
    expect(gh.attributes('aria-disabled')).toBe('true');
    expect(w.find('[data-testid="sign-in-github-note"]').text()).toContain('not configured');
    expect(calls.filter((c) => c.path.startsWith('/auth/'))).toEqual([]);
  });
});
