// A storage plugin's listing: its page shows the release's feed and a
// build per platform in the integrity table, what the store's plugin
// validator measured and on which platform, and that it is a program filex
// runs outside any sandbox - where an app's page shows its permissions. The
// catalog files it under its own kind and shelf.
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
const REL = 'https://github.com/northwind-labs/filex-webdavfs/releases/download/v1.2.0/';
const art = (file: string, c: string) => ({ url: REL + file, sha256: c.repeat(64), size: 9_400_000, sig: c.repeat(128) });

const APP = {
  name: 'webdavfs',
  kind: 'storage',
  label: { en: 'WebDAV storage' },
  summary: { en: 'Keep a filex storage on any WebDAV server.' },
  publisher: PUB,
  categories: ['storage'],
  latest: '1.2.0',
  updated_at: '2026-10-05T10:00:00Z',
  repo: 'northwind-labs/filex-webdavfs',
  license: 'Apache-2.0',
  status: 'listed',
  pricing: { model: 'free' },
  revoked: null,
  screenshots: [],
  versions: [{ version: '1.2.0', ref: 'v1.2.0', filex: '>=0.55.0', published_at: '2026-10-05', security: false, yanked: null }],
};

const VERSION = {
  version: '1.2.0',
  ref: 'v1.2.0',
  filex: '>=0.55.0',
  published_at: '2026-10-05',
  security: false,
  yanked: null,
  commit: 'c'.repeat(40),
  manifest: { ...art('filex-storage.json', 'f'), size: 1400 },
  permissions: [],
  languages: [],
  engines: [],
  install: { github_repo: 'northwind-labs/filex-webdavfs', ref: 'v1.2.0' },
  file_types: [],
  conversions: [],
  builds: [
    { platform: 'linux/amd64', ...art('webdavfs-linux-amd64', 'a') },
    { platform: 'linux/arm64', ...art('webdavfs-linux-arm64', 'b') },
    { platform: 'windows/amd64', ...art('webdavfs-windows-amd64.exe', 'd') },
  ],
  conformance: {
    platform: 'linux/amd64',
    filex: '0.55.0',
    verified: true,
    passed: 31,
    failed: 0,
    skipped: 3,
    driver: 'webdavfs',
    capabilities: ['delete', 'move', 'write'],
  },
};

async function pageAt(path: string, routes: Record<string, { body: unknown }>) {
  const { calls } = mockFetch({ 'GET /api/v1/categories': { body: { items: [{ id: 'storage', label: { en: 'Storage' } }] } }, ...routes });
  const router = testRouter();
  await router.push(path);
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  mounted.push(w);
  await settle(6);
  return { w, calls };
}

describe('a storage plugin\'s page', () => {
  async function storagePage() {
    return (
      await pageAt('/apps/webdavfs', {
        'GET /api/v1/apps/webdavfs': { body: APP },
        'GET /api/v1/apps/webdavfs/versions/1.2.0': { body: VERSION },
      })
    ).w;
  }

  it('says it runs outside any sandbox, and what the store measured on which platform', async () => {
    const w = await storagePage();
    const sec = w.find('[data-testid="storage-section"]');
    expect(sec.exists()).toBe(true);
    expect(w.find('[data-testid="storage-warning"]').text()).toContain('outside any sandbox');
    expect(w.find('[data-testid="storage-conformance"]').text()).toBe(
      'filex 0.55.0 ran the linux/amd64 build: 31 checks passed, 3 capabilities not declared.',
    );
    expect(sec.text()).toContain('plugin:webdavfs');
    expect(w.find('[data-testid="storage-cap-move"]').text()).toBe('server-side move');
    // Builds for other platforms are pinned and signed, not run - it says so.
    expect(w.find('[data-testid="storage-others"]').text()).toContain('the store ran the linux/amd64 one');
  });

  it('shows no permission list: a storage plugin asks for none, it has filex\'s rights', async () => {
    const w = await storagePage();
    expect(w.find('#perm-h').exists()).toBe(false);
    expect(w.find('[data-testid="works-with"]').exists()).toBe(false);
  });

  it('the integrity table lists the feed and every build, each with its SHA-256 and signature', async () => {
    const w = await storagePage();
    const table = w.find('[data-table-id="store.app.artifacts"]');
    expect(table.exists()).toBe(true);
    const text = table.text();
    expect(text).toContain('Feed (filex-storage.json)');
    expect(text).toContain('linux/amd64 build');
    expect(text).toContain('linux/arm64 build');
    expect(text).toContain('windows/amd64 build');
    expect(text).not.toContain('Module');
    expect(w.find('[data-testid="app-platforms"]').text()).toContain('windows/amd64');
  });

  it('the by-hand install names the source filex\'s "From its source" takes', async () => {
    const w = await storagePage();
    expect(w.find('[data-testid="install-steps-storage"]').text()).toContain('From its source');
    expect(w.find('[data-testid="install-source"]').exists()).toBe(true);
    expect(w.find('[data-testid="install-ref"]').exists()).toBe(false);
  });

  it('an app\'s page is unchanged: no storage section', async () => {
    const app = { ...APP, name: 'md', kind: 'app' };
    const ver = { ...VERSION, builds: undefined, conformance: undefined, manifest: art('filex-app.json', 'e') };
    const { w } = await pageAt('/apps/md', {
      'GET /api/v1/apps/md': { body: app },
      'GET /api/v1/apps/md/versions/1.2.0': { body: ver },
    });
    expect(w.find('[data-testid="storage-section"]').exists()).toBe(false);
    expect(w.find('#perm-h').exists()).toBe(true);
  });
});

describe('the catalog', () => {
  it('a storage plugin has its own shelf on the shop front, and its own kind in the filter', async () => {
    const summary = {
      name: 'webdavfs',
      kind: 'storage',
      label: { en: 'WebDAV storage' },
      summary: { en: 'Keep a filex storage on any WebDAV server.' },
      publisher: PUB,
      categories: ['storage'],
      latest: '1.2.0',
      updated_at: '2026-10-05T10:00:00Z',
    };
    const { w } = await pageAt('/', { 'GET /api/v1/apps': { body: { items: [summary], total: 1, page: 1, per_page: 48 } } });
    const shelf = w.find('[data-testid="shelf-storage"]');
    expect(shelf.exists()).toBe(true);
    expect(shelf.text()).toContain('Storage plugins');
    expect(shelf.find('[data-testid="app-card-webdavfs"]').text()).toContain('Storage plugin');
  });

  it('?kind=storage asks the server for storage plugins only', async () => {
    const { calls } = await pageAt('/?kind=storage', { 'GET /api/v1/apps': { body: { items: [], total: 0, page: 1, per_page: 48 } } });
    const ask = calls.find((c) => c.method === 'GET' && c.path.startsWith('/api/v1/apps'));
    expect(ask?.path).toContain('kind=storage');
  });
});
