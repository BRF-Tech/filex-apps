import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { mockFetch, settle, testRouter } from './helpers';

const APP = {
  name: 'filex-sign',
  kind: 'app',
  label: { en: 'filex Sign' },
  summary: { en: 'Sign PDFs.' },
  publisher: { id: 'brf-tech', name: 'BRF Tech', verified: true, official: true },
  categories: ['documents'],
  latest: '0.3.0',
  compatible: '0.3.0',
  updated_at: '2026-10-01T10:00:00Z',
};
const COMMUNITY = {
  ...APP,
  name: 'markdown-preview',
  label: { en: 'Markdown Preview' },
  publisher: { id: 'jdoe', name: 'Jane Doe', verified: false, official: false },
  categories: ['developer'],
};
const PACK = { ...APP, name: 'filex-lang-es', kind: 'language_pack', label: { en: 'Español' }, categories: [] };

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

async function mountAt(path: string, items: unknown[] = [APP]) {
  const { calls } = mockFetch({
    'GET /api/v1/apps': { body: { items, total: items.length, page: 1, per_page: 50 } },
    'GET /api/v1/categories': {
      body: {
        items: [
          { id: 'documents', label: { en: 'Documents' } },
          { id: 'developer', label: { en: 'Developer tools' } },
        ],
      },
    },
  });
  const router = testRouter();
  await router.push(path);
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  await settle();
  return { w, router, calls };
}
const apiCalls = (calls: { path: string }[]) => calls.filter((c) => c.path.startsWith('/api/v1/apps'));

describe('the catalog', () => {
  beforeEach(() => vi.useRealTimers());
  afterEach(() => document.body.replaceChildren());

  it('is a shop window: one card per app with its link, publisher and badges, and no table', async () => {
    const { w } = await mountAt('/');
    expect(w.find('[role="grid"]').exists()).toBe(false);
    expect(w.find('table').exists()).toBe(false);
    const card = w.find('[data-testid="app-card-filex-sign"]');
    expect(card.element.tagName).toBe('ARTICLE');
    expect(card.find('[data-testid="app-link-filex-sign"]').attributes('href')).toBe('/apps/filex-sign');
    expect(card.find('a[href="/publishers/brf-tech"]').text()).toBe('by BRF Tech');
    expect(card.text()).toContain('Official');
    expect(card.text()).toContain('Verified');
    expect(card.text()).toContain('Documents');
    expect(card.text()).toContain('v0.3.0');
    // The results are a region named "Apps", like the list view's table.
    expect(w.find('[data-testid="catalog-results"]').attributes('aria-label')).toBe('Apps');
  });

  it('puts the plain first page on shelves: the official publisher, the community, language packs', async () => {
    const { w } = await mountAt('/', [APP, COMMUNITY, PACK]);
    expect(w.find('[data-testid="shelf-official"] [data-testid="app-card-filex-sign"]').exists()).toBe(true);
    expect(w.find('[data-testid="shelf-community"] [data-testid="app-card-markdown-preview"]').exists()).toBe(true);
    expect(w.find('[data-testid="shelf-languages"] [data-testid="app-card-filex-lang-es"]').exists()).toBe(true);
    expect(w.findAll('h2').map((x) => x.text())).toEqual(['Official apps', 'Community apps', 'Language packs']);
  });

  it('the first band shows the icons of the listed apps once there are three, as decoration', async () => {
    const few = await mountAt('/', [APP, COMMUNITY]);
    expect(few.w.find('[data-testid="catalog-hero-art"]').exists()).toBe(false);
    few.w.unmount();
    const { w } = await mountAt('/', [APP, COMMUNITY, PACK]);
    const art = w.find('[data-testid="catalog-hero-art"]');
    expect(art.attributes('aria-hidden')).toBe('true');
    expect(art.findAll('.fa-appicon')).toHaveLength(3);
  });

  it('shows one grid of results, not shelves, once something is filtered', async () => {
    const { w } = await mountAt('/?q=mark', [COMMUNITY]);
    expect(w.find('[data-testid="shelf-community"]').exists()).toBe(false);
    expect(w.find('[data-testid="catalog-grid"] [data-testid="app-card-markdown-preview"]').exists()).toBe(true);
  });

  it('the list view is filex\'s DataTable, remembered as store.catalog, with a link per app', async () => {
    const { w } = await mountAt('/?view=list');
    const grid = w.find('[role="grid"]');
    expect(grid.exists()).toBe(true);
    expect(grid.attributes('data-table-id')).toBe('store.catalog');
    expect(w.find('table').exists()).toBe(false);
    expect(w.find('[data-testid="app-link-filex-sign"]').attributes('href')).toBe('/apps/filex-sign');
    expect(w.text()).toContain('BRF Tech');
    expect(w.text()).toContain('Documents');
  });

  it('switching the view keeps the filters in the address and asks the server nothing new', async () => {
    const { w, router, calls } = await mountAt('/?kind=app');
    const before = apiCalls(calls).length;
    await w.find('[data-testid="catalog-view-list"]').trigger('click');
    await settle();
    expect(router.currentRoute.value.query).toEqual({ kind: 'app', view: 'list' });
    expect(w.find('[data-testid="catalog-view-list"]').attributes('aria-checked')).toBe('true');
    expect(w.find('[role="grid"]').exists()).toBe(true);
    expect(apiCalls(calls).length).toBe(before);
  });

  it('sends the address\'s filters to the API with the contract\'s names', async () => {
    const { calls } = await mountAt('/?q=sign&kind=app&verified=1&filex=0.50.0&page=2');
    const call = apiCalls(calls)[0];
    const q = new URL(call.path, 'http://x').searchParams;
    expect(Object.fromEntries(q)).toEqual({ q: 'sign', kind: 'app', filex: '0.50.0', verified: 'true', sort: 'name', page: '2', per_page: '50' });
  });

  it('a filter change goes into the address and starts from page 1', async () => {
    const { w, router } = await mountAt('/?page=3');
    await w.find('[data-testid="catalog-kind"]').trigger('click');
    await settle();
    document.querySelector<HTMLElement>('[data-testid="catalog-kind-option-language_pack"]')!.click();
    await settle();
    expect(router.currentRoute.value.query).toEqual({ kind: 'language_pack' });
  });

  it('a category chip is a toggle: pressed, in the address, and pressed again to clear', async () => {
    const { w, router } = await mountAt('/');
    const chip = () => w.find('[data-testid="catalog-cat-developer"]');
    expect(chip().attributes('aria-pressed')).toBe('false');
    expect(w.find('[data-testid="catalog-cat-all"]').attributes('aria-pressed')).toBe('true');
    await chip().trigger('click');
    await settle();
    expect(router.currentRoute.value.query).toEqual({ category: 'developer' });
    expect(chip().attributes('aria-pressed')).toBe('true');
    await chip().trigger('click');
    await settle();
    expect(router.currentRoute.value.query).toEqual({});
  });

  it('the sort choice goes into the address', async () => {
    const { w, router } = await mountAt('/');
    await w.find('[data-testid="catalog-sort"]').trigger('keydown', { key: 'ArrowDown' });
    await w.find('[data-testid="catalog-sort"]').trigger('keydown', { key: 'ArrowDown' });
    await w.find('[data-testid="catalog-sort"]').trigger('keydown', { key: 'Enter' });
    await settle();
    expect(router.currentRoute.value.query).toEqual({ sort: 'updated' });
  });

  it('names the version for "Works with filex" on a card and in the list\'s column', async () => {
    const grid = await mountAt('/?filex=0.50.0');
    expect(grid.w.find('[data-testid="app-card-filex-sign"]').text()).toContain('v0.3.0 for filex 0.50.0');
    grid.w.unmount();
    const list = await mountAt('/?filex=0.50.0&view=list');
    expect(list.w.text()).toContain('For filex 0.50.0');
  });

  it('refuses a malformed filex version without changing the address', async () => {
    const { w, router } = await mountAt('/');
    const input = w.find('[data-testid="catalog-filex"]');
    await input.setValue('0.50');
    await input.trigger('change');
    await settle();
    expect(w.text()).toContain('Write a filex version like 0.50.0.');
    expect(input.attributes('aria-invalid')).toBe('true');
    expect(router.currentRoute.value.query).toEqual({});
  });

  it('says why it is empty, and offers the way on', async () => {
    const plain = await mountAt('/', []);
    expect(plain.w.find('[data-testid="empty-state"]').text()).toContain('No apps are listed yet.');
    expect(plain.w.find('[data-testid="empty-state"] a[href="/publish"]').exists()).toBe(true);
    plain.w.unmount();
    const filtered = await mountAt('/?q=zzz', []);
    expect(filtered.w.text()).toContain('No apps match these filters.');
    await filtered.w.find('[data-testid="catalog-empty-clear"]').trigger('click');
    await settle();
    expect(filtered.router.currentRoute.value.query).toEqual({});
  });

  it('draws grey cards while the first answer is on its way, and no empty message', async () => {
    mockFetch({ 'GET /api/v1/apps': () => new Promise(() => {}), 'GET /api/v1/categories': { body: { items: [] } } });
    const router = testRouter();
    await router.push('/');
    await router.isReady();
    const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
    await settle();
    expect(w.find('[data-testid="catalog-skeleton"]').exists()).toBe(true);
    expect(w.find('[data-testid="catalog-skeleton"]').attributes('aria-hidden')).toBe('true');
    expect(w.find('[data-testid="catalog-count"]').text()).toBe('Loading apps…');
    expect(w.text()).not.toContain('No apps are listed yet.');
  });
});
