// An app that is set up after it is installed (filex 0.60.0): its page says
// so before the permissions - its own setup step, its own addresses (the
// setup asks where they live), the short-link role, the warning before its
// data is erased - and lists what an administrator grants inside it, with
// who holds each until then. An app that declares none of it has no such
// section.
import { afterEach, describe, expect, it } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { mockFetch, settle, testRouter } from './helpers';

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

const mounted: VueWrapper[] = [];
afterEach(() => {
  while (mounted.length) mounted.pop()!.unmount();
  document.body.replaceChildren();
});

const PUB = { id: 'northwind', name: 'Northwind Labs', verified: true, official: false };

const app = (name: string) => ({
  name,
  kind: 'app',
  label: { en: 'Short links' },
  summary: { en: 'Short addresses for filex links.' },
  publisher: PUB,
  categories: [],
  latest: '0.1.0',
  updated_at: '2026-11-12T10:00:00Z',
  repo: `northwind-labs/filex-${name}`,
  license: 'MIT',
  status: 'listed',
  pricing: { model: 'free' },
  revoked: null,
  screenshots: [],
  versions: [{ version: '0.1.0', ref: 'v0.1.0', filex: '>=0.60.0', published_at: '2026-11-12', security: false, yanked: null }],
});

const version = (name: string, setup?: unknown) => ({
  version: '0.1.0',
  ref: 'v0.1.0',
  filex: '>=0.60.0',
  published_at: '2026-11-12',
  security: false,
  yanked: null,
  commit: 'c'.repeat(40),
  manifest: { url: 'https://raw.githubusercontent.com/m.json', sha256: 'f'.repeat(64), sig: '0'.repeat(128) },
  wasm: { url: 'https://github.com/x/plugin.wasm', sha256: 'a'.repeat(64), size: 900_000, sig: '1'.repeat(128) },
  permissions: [
    { id: 'data', label: 'Keeps data of its own' },
    { id: 'public_routes', label: 'Answers addresses of its own' },
    { id: 'link_shortener', label: 'Makes short links' },
  ],
  languages: [{ code: 'en', percent: 100 }],
  engines: [],
  install: { github_repo: `northwind-labs/filex-${name}`, ref: 'v0.1.0' },
  file_types: [],
  conversions: [],
  ...(setup ? { setup } : {}),
});

const SETUP = {
  needs_setup: true,
  wizard: true,
  routes: [{ id: 'go', label: { en: 'Short links', tr: 'Kısa bağlantılar' }, default_prefix: 'l' }],
  link_shortener: { route: 'go', requires: 'use' },
  reset_warning: { en: 'Every short link this app made stops working.' },
  user_permissions: [
    { id: 'use', label: { en: 'Get short links' }, description: { en: '"Get short link" on the links the person may make.' }, default: 'user' },
    { id: 'manage', label: { en: 'Manage every short link' }, default: 'admin' },
  ],
};

async function pageOf(name: string, setup?: unknown) {
  mockFetch({
    'GET /api/v1/categories': { body: { items: [] } },
    [`GET /api/v1/apps/${name}`]: { body: app(name) },
    [`GET /api/v1/apps/${name}/versions/0.1.0`]: { body: version(name, setup) },
  });
  const router = testRouter();
  await router.push(`/apps/${name}`);
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  mounted.push(w);
  await settle(6);
  return w;
}

describe('an app that is set up after it is installed', () => {
  it('says the setup comes first, and what it asks', async () => {
    const w = await pageOf('shortlink', SETUP);
    const sec = w.find('[data-testid="app-setup"]');
    expect(sec.exists()).toBe(true);
    expect(sec.find('h2').text()).toBe('Setting it up');
    expect(w.find('[data-testid="setup-intro"]').text()).toContain('an administrator sets this app up once');
    expect(w.find('[data-testid="setup-wizard"]').text()).toContain('Its own setup step');
    const route = w.find('[data-testid="setup-route-go"]').text();
    expect(route).toContain('Its own addresses: Short links');
    expect(route).toContain('the setup asks where they live');
    expect(route).toContain('your filex\'s address followed by /l');
    const short = w.find('[data-testid="setup-shortener"]').text();
    expect(short).toContain('a short address on "Short links"');
    expect(short).toContain('People need "Get short links" for it.');
    expect(w.find('[data-testid="setup-reset"]').text()).toContain('Every short link this app made stops working.');
    // The by-hand install says the setup follows it.
    expect(w.find('[data-testid="install-step-setup"]').text()).toContain('Then filex opens the app');
  });

  it('lists what an administrator grants inside it, with who holds each until then', async () => {
    const w = await pageOf('shortlink', SETUP);
    const use = w.find('[data-testid="setup-people-use"]').text();
    expect(use).toContain('Get short links');
    expect(use).toContain('Until granted: the accounts that can change files');
    expect(use).toContain('use');
    expect(w.find('[data-testid="setup-people-manage"]').text()).toContain('Until granted: administrators only');
  });

  it('comes before the permissions, which keep filex\'s words', async () => {
    const w = await pageOf('shortlink', SETUP);
    const html = w.html();
    expect(html.indexOf('data-testid="app-setup"')).toBeLessThan(html.indexOf('id="perm-h"'));
    expect(w.find('[data-testid="permissions"]').text()).toContain('Answers addresses of its own');
  });

  it('an app without the blocks has no such section', async () => {
    const w = await pageOf('plain');
    expect(w.find('#perm-h').exists()).toBe(true);
    expect(w.find('[data-testid="app-setup"]').exists()).toBe(false);
    expect(w.find('[data-testid="install-step-setup"]').exists()).toBe(false);
  });

  it('user permissions alone: the list, without the setup\'s rows', async () => {
    const w = await pageOf('perms', { needs_setup: false, wizard: false, routes: [], user_permissions: SETUP.user_permissions });
    expect(w.find('[data-testid="app-setup"]').exists()).toBe(true);
    expect(w.find('[data-testid="setup-intro"]').exists()).toBe(false);
    expect(w.find('[data-testid="setup-rows"]').exists()).toBe(false);
    expect(w.find('[data-testid="setup-people"]').exists()).toBe(true);
  });
});
