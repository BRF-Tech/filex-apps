// A table whose load failed shows the error and nothing else: never the
// "nothing matches" message beside it. While loading it says "Loading" (not
// the empty message); only a load that really returned no rows says empty.
import { afterEach, describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { session } from '@/lib/session';
import type { Me } from '@/api/client';
import { S } from '@/strings';
import { emptyUnlessError } from '@/lib/format';
import { ME_ADMIN, ME_PUBLISHER, ME_REVIEWER, mockFetch, settle, testRouter, type Answer } from './helpers';

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

const PAGE = { items: [], total: 0, page: 1, per_page: 50 };
const CASES = [
  { name: 'Administration > Publishers', me: ME_ADMIN, path: '/admin/publishers', api: 'GET /api/v1/admin/publishers', empty: S.admin.publishersEmpty, body: { items: [], total: 0 } },
  { name: 'Administration > Apps', me: ME_ADMIN, path: '/admin/apps', api: 'GET /api/v1/admin/apps', empty: S.admin.appsEmpty, body: PAGE },
  { name: 'Administration > Index', me: ME_ADMIN, path: '/admin/index', api: 'GET /api/v1/admin/keys', empty: S.admin.keysEmpty, body: { items: [] } },
  { name: 'Administration > Validators', me: ME_ADMIN, path: '/admin/validators', api: 'GET /api/v1/admin/validators', empty: S.admin.validatorsEmpty, body: { items: [] } },
  { name: 'Administration > Audit', me: ME_ADMIN, path: '/admin/audit', api: 'GET /api/v1/admin/audit', empty: S.admin.auditEmpty, body: { items: [], total: 0, chain_ok: true } },
  { name: 'Review queue', me: ME_REVIEWER, path: '/review', api: 'GET /api/v1/review/queue', empty: S.review.empty, body: PAGE },
  { name: 'My submissions', me: ME_PUBLISHER, path: '/publish', api: 'GET /api/v1/submissions', empty: S.publish.submissionsEmpty, body: PAGE },
  { name: 'Catalog', me: ME_PUBLISHER, path: '/', api: 'GET /api/v1/apps', empty: S.catalog.emptyNone, body: PAGE },
];

async function open(c: (typeof CASES)[number], answer: Answer | Promise<Answer>) {
  session.loaded = true;
  session.me = c.me as unknown as Me;
  mockFetch({
    [c.api]: () => answer,
    'GET /api/v1/categories': { body: { items: [] } },
    // the publish page also lists the person's apps (price and licenses)
    'GET /api/v1/me/apps': { body: { items: [] } },
  });
  const router = testRouter();
  await router.push(c.path);
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  await settle();
  return w;
}

afterEach(() => {
  document.body.replaceChildren();
  session.me = null;
});

describe('empty-table message vs failed load', () => {
  it('emptyUnlessError keeps the message only without an error', () => {
    expect(emptyUnlessError('Nothing.', '')).toBe('Nothing.');
    expect(emptyUnlessError('Nothing.', 'Could not load.')).toBe('');
  });

  for (const c of CASES) {
    describe(c.name, () => {
      it('a 429 shows the error and not the empty message', async () => {
        const w = await open(c, { status: 429, body: { error: 'rate_limited', message: 'Too many requests.' } });
        expect(w.find('[role="alert"]').exists()).toBe(true);
        expect(w.text()).toContain('Too many requests');
        expect(w.text()).not.toContain(c.empty);
      });

      it('while loading it does not show the empty message', async () => {
        const w = await open(c, new Promise<Answer>(() => {}));
        expect(w.text()).not.toContain(c.empty);
      });

      it('a real empty result shows the empty message and no error', async () => {
        const w = await open(c, { body: c.body });
        expect(w.text()).toContain(c.empty);
        expect(w.find('[role="alert"]').exists()).toBe(false);
      });
    });
  }
});
