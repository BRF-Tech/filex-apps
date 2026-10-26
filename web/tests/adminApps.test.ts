import { afterEach, describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { session } from '@/lib/session';
import type { Me } from '@/api/client';
import { ME_ADMIN, mockFetch, settle, testRouter } from './helpers';

const row = (name: string, status: string) => ({
  name,
  kind: 'app',
  label: { en: name },
  publisher: { id: 'brf-tech', name: 'BRF Tech', verified: true, official: true },
  categories: [],
  latest: '1.0.0',
  updated_at: '2026-10-01T10:00:00Z',
  status,
});
const ALL = [row('alpha', 'listed'), row('bravo', 'unlisted'), row('charlie', 'revoked')];

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

async function open() {
  session.loaded = true;
  session.me = ME_ADMIN as unknown as Me;
  const f = mockFetch({
    'GET /api/v1/admin/apps': (url) => {
      const st = url.searchParams.get('status');
      const items = st ? ALL.filter((a) => a.status === st) : ALL;
      return { body: { items, total: items.length, page: 1, per_page: 50 } };
    },
  });
  const router = testRouter();
  await router.push('/admin/apps');
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  await settle();
  return { w, ...f };
}

afterEach(() => {
  document.body.replaceChildren();
  session.me = null;
});

describe('Administration > Apps', () => {
  it('is one DataTable (store.admin.apps) listing every status from the admin endpoint', async () => {
    const { w, calls } = await open();
    expect(w.find('[role="grid"]').attributes('data-table-id')).toBe('store.admin.apps');
    expect(w.find('table').exists()).toBe(false);
    const first = calls.find((c) => c.path.startsWith('/api/v1/admin/apps'))!;
    expect(new URL(first.path, 'http://x').searchParams.get('status')).toBeNull();
    expect(calls.some((c) => c.path.startsWith('/api/v1/apps'))).toBe(false);
    const text = w.text();
    for (const word of ['alpha', 'bravo', 'charlie', 'Listed', 'Unlisted', 'Revoked']) expect(text).toContain(word);
  });

  it('sends the chosen status to the API, from page 1', async () => {
    const { w, calls } = await open();
    expect(w.find('[data-testid="aa-status"]').attributes('role')).toBe('radiogroup');
    await w.find('[data-testid="aa-status-unlisted"]').trigger('click');
    await settle();
    const last = calls.filter((c) => c.path.startsWith('/api/v1/admin/apps')).at(-1)!;
    const q = new URL(last.path, 'http://x').searchParams;
    expect(q.get('status')).toBe('unlisted');
    expect(q.get('page')).toBe('1');
    expect(w.text()).toContain('bravo');
    expect(w.text()).not.toContain('alpha');
  });
});
