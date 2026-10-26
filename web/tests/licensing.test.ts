import { afterEach, describe, expect, it, vi } from 'vitest';
import { mount } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { session } from '@/lib/session';
import { setCsrfToken, type Me } from '@/api/client';
import { LICENSE_KEY, looksLikeFilexUrl, openableLink } from '@/lib/install';
import AppCard from '@/components/AppCard.vue';
import { ME_ADMIN, ME_PUBLISHER, mockFetch, settle, testRouter, type Answer, type Handler } from './helpers';

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

const sha = (c: string) => c.repeat(64);
const PAID = { model: 'paid', price_text: '$29 per instance', purchase_url: 'https://jdoe.example/buy' };
const app = (pricing: Record<string, unknown> = { model: 'free' }) => ({
  name: 'hello',
  kind: 'app',
  label: { en: 'Hello' },
  summary: { en: 'Says hello.' },
  publisher: { id: 'jdoe', name: 'Jane Doe', verified: false, official: false },
  categories: [],
  latest: '1.2.0',
  updated_at: '2026-10-01T10:00:00Z',
  repo: 'jdoe/filex-hello',
  status: 'listed',
  revoked: null,
  screenshots: [],
  versions: [{ version: '1.2.0', ref: 'v1.2.0', filex: '>=0.50.0', published_at: '2026-10-01', security: false, yanked: null }],
  pricing,
});
const version = {
  version: '1.2.0',
  ref: 'v1.2.0',
  filex: '>=0.50.0',
  published_at: '2026-10-01',
  security: false,
  yanked: null,
  commit: 'c'.repeat(40),
  manifest: { url: 'https://raw.githubusercontent.com/m.json', sha256: sha('a'), sig: 'a'.repeat(128) },
  permissions: [],
  languages: [],
  engines: [],
  install: { github_repo: 'jdoe/filex-hello', ref: 'v1.2.0' },
};
const TOKEN = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJ-_01234';
const LINK = `https://files.example.com/admin/store-install#store=https%3A%2F%2Fapps.example.com&intent=${TOKEN}`;
const KEY = 'FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-4C';

async function open(path: string, me: unknown, extra: Record<string, Answer | Handler> = {}, pricing?: Record<string, unknown>) {
  session.loaded = true;
  session.me = me as Me | null;
  setCsrfToken(me ? 'tok-test' : '');
  const f = mockFetch({
    'GET /api/v1/apps/hello': { body: app(pricing) },
    'GET /api/v1/apps/hello/versions/1.2.0': { body: version },
    'GET /api/v1/categories': { body: { items: [] } },
    ...extra,
  });
  const router = testRouter();
  await router.push(path);
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  await settle(6);
  return { w, router, ...f };
}

afterEach(() => {
  document.body.replaceChildren();
  session.me = null;
  setCsrfToken('');
});

describe('Install on my filex', () => {
  it('signed out, Install goes to the sign-in and comes back to open the dialog', async () => {
    const { w, router } = await open('/apps/hello', null);
    await w.find('[data-testid="install-open"]').trigger('click');
    await vi.waitFor(() => expect(router.currentRoute.value.name).toBe('sign-in'));
    expect(router.currentRoute.value.query.return_to).toBe('/apps/hello?install=1');
  });

  it('makes a link to a new address, saving it, and shows it with Open and Copy', async () => {
    const { w, calls } = await open('/apps/hello', ME_PUBLISHER, {
      'GET /api/v1/me/instances': { body: { items: [] } },
      'POST /api/v1/install-intents': {
        status: 201,
        body: { token: TOKEN, magic_link: LINK, expires_at: '2026-10-04T12:30:00Z', app: 'hello', version: '1.2.0' },
      },
    });
    await w.find('[data-testid="install-open"]').trigger('click');
    await settle();
    expect(w.find('[data-testid="install-license"]').exists()).toBe(false);
    expect(w.find('[data-testid="install-requires"]').text()).toContain('filex 0.52.0 or later');
    await w.find('[data-testid="dialog-confirm"]').trigger('click');
    await settle();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    expect(w.text()).toContain('without a path');

    await w.find('[data-testid="install-url"]').setValue('https://files.example.com');
    await w.find('[data-testid="install-label"]').setValue('Office');
    await w.find('[data-testid="dialog-confirm"]').trigger('click');
    await settle();
    const post = calls.find((c) => c.method === 'POST' && c.path === '/api/v1/install-intents')!;
    expect(post.body).toEqual({ app: 'hello', version: '1.2.0', filex_url: 'https://files.example.com', save_instance: true, instance_label: 'Office' });
    expect(post.headers['X-CSRF-Token']).toBe('tok-test');
    expect(w.find('[data-testid="magic-link"]').text()).toBe(LINK);
    const a = w.find('[data-testid="install-link-open"]');
    expect(a.attributes('href')).toBe(LINK);
    expect(a.attributes('rel')).toContain('noopener');
    expect(w.text()).toContain('works once and for 30 minutes');
  });

  it('offers the saved instances, and a paid app asks for the license key', async () => {
    const { w, calls } = await open(
      '/apps/hello?install=1',
      ME_PUBLISHER,
      {
        'GET /api/v1/me/instances': {
          body: { items: [{ id: '11111111-1111-1111-1111-111111111111', label: 'Office', url: 'https://files.example.com', created_at: '2026-10-01T00:00:00Z' }] },
        },
        'POST /api/v1/install-intents': {
          status: 201,
          body: { token: TOKEN, magic_link: LINK, expires_at: '2026-10-04T12:30:00Z', app: 'hello', version: '1.2.0' },
        },
      },
      PAID,
    );
    // ?install=1 (the way back from the sign-in) opened the dialog by itself
    expect(w.find('[data-testid="install-instance"]').exists()).toBe(true);
    expect(w.find('[data-testid="app-paid"]').text()).toContain('$29 per instance');
    expect(w.find('[data-testid="app-buy"]').attributes('href')).toBe('https://jdoe.example/buy');
    await w.find('[data-testid="install-license"]').setValue(` ${KEY.toLowerCase()} `);
    await w.find('[data-testid="dialog-confirm"]').trigger('click');
    await settle();
    const post = calls.find((c) => c.method === 'POST')!;
    expect(post.body).toEqual({ app: 'hello', version: '1.2.0', instance_id: '11111111-1111-1111-1111-111111111111', save_instance: false, license_key: ` ${KEY.toLowerCase()} `.trim() });
    expect((w.find('[data-testid="install-license"]').exists())).toBe(false);
  });

  it('a server refusal is shown in the dialog', async () => {
    const { w } = await open('/apps/hello', ME_PUBLISHER, {
      'GET /api/v1/me/instances': { body: { items: [] } },
      'POST /api/v1/install-intents': { status: 400, body: { error: 'filex_url_invalid', message: 'Enter your filex address with https.' } },
    });
    await w.find('[data-testid="install-open"]').trigger('click');
    await settle();
    await w.find('[data-testid="install-url"]').setValue('http://files.example.com');
    await w.find('[data-testid="dialog-confirm"]').trigger('click');
    await settle();
    expect(w.find('[data-testid="dialog-error"]').text()).toContain('https');
    expect(w.find('[data-testid="magic-link"]').exists()).toBe(false);
  });

  it('members of the publisher and administrators get the way to the price and licenses', async () => {
    const pub = await open('/apps/hello', ME_PUBLISHER);
    expect(pub.w.find('[data-testid="app-manage"]').exists()).toBe(true);
    document.body.replaceChildren();
    const admin = await open('/apps/hello', ME_ADMIN);
    expect(admin.w.find('[data-testid="app-manage"]').exists()).toBe(true);
    document.body.replaceChildren();
    const other = await open('/apps/hello', { ...ME_PUBLISHER, publishers: [] });
    expect(other.w.find('[data-testid="app-manage"]').exists()).toBe(false);
  });
});

describe('a paid app in the catalog', () => {
  it('its card says Paid with the price text', () => {
    const w = mount(AppCard, {
      props: { app: app(PAID) as never },
      global: { stubs: { RouterLink: { template: '<a><slot /></a>' } } },
    });
    expect(w.find('[data-testid="app-paid-hello"]').text()).toBe('Paid: $29 per instance');
    const free = mount(AppCard, { props: { app: app() as never }, global: { stubs: { RouterLink: { template: '<a><slot /></a>' } } } });
    expect(free.find('[data-testid="app-paid-hello"]').exists()).toBe(false);
  });
});

const LIC = (over: Record<string, unknown> = {}) => ({
  id: '22222222-2222-2222-2222-222222222222',
  app: 'hello',
  key_prefix: 'FXL-7K2Q',
  licensee_name: 'Acme',
  seats: 2,
  seats_used: 1,
  status: 'active',
  created_at: '2026-10-01T00:00:00Z',
  ...over,
});

describe('Price and licenses', () => {
  function manage(extra: Record<string, Answer | Handler> = {}) {
    let licences = [LIC()];
    return open('/publish/apps/hello', ME_PUBLISHER, {
      'GET /api/v1/apps/hello/licenses': () => ({ body: { items: licences, total: licences.length, page: 1, per_page: 25 } }),
      'POST /api/v1/apps/hello/licenses': (_u, init) => {
        const body = JSON.parse(String(init.body));
        const lic = LIC({ id: '33333333-3333-3333-3333-333333333333', key_prefix: 'FXL-NEW1', licensee_name: body.licensee_name, seats_used: 0 });
        licences = [lic, ...licences];
        return { status: 201, body: { license: lic, key: KEY } };
      },
      'POST /api/v1/licenses/22222222-2222-2222-2222-222222222222/revoke': () => {
        licences = licences.map((l) => (l.id === '22222222-2222-2222-2222-222222222222' ? LIC({ status: 'revoked', revoked_at: '2026-10-04T00:00:00Z' }) : l));
        return { body: licences[licences.length - 1] };
      },
      'GET /api/v1/licenses/22222222-2222-2222-2222-222222222222/activations': {
        body: { items: [{ instance_id: 'inst/one', first_seen: '2026-10-01T00:00:00Z', last_seen: '2026-10-04T00:00:00Z', active: true, filex_version: 'v0.51.0' }] },
      },
      'POST /api/v1/licenses/22222222-2222-2222-2222-222222222222/activations/inst%2Fone/release': {
        body: { instance_id: 'inst/one', first_seen: '2026-10-01T00:00:00Z', last_seen: '2026-10-04T00:00:00Z', active: false },
      },
      'PUT /api/v1/apps/hello/pricing': (_u, init) => ({ body: JSON.parse(String(init.body)) }),
      ...extra,
    });
  }

  it('lists the licenses in one DataTable, without any key', async () => {
    const { w } = await manage();
    const grid = w.find('[data-table-id="store.manage.licences"]');
    expect(grid.exists()).toBe(true);
    expect(grid.text()).toContain('FXL-7K2Q');
    expect(grid.text()).toContain('1 of 2');
    expect(w.find('table').exists()).toBe(false);
  });

  it('shows a new key once, and forgets it when the dialog closes', async () => {
    const { w, calls } = await manage();
    await w.find('[data-testid="licence-new"]').trigger('click');
    await settle();
    await w.find('[data-testid="dialog-confirm"]').trigger('click');
    await settle();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    await w.find('[data-testid="licence-licensee"]').setValue('Buyer Ltd');
    await w.find('[data-testid="licence-seats"]').setValue('3');
    await w.find('[data-testid="dialog-confirm"]').trigger('click');
    await settle();
    const post = calls.find((c) => c.method === 'POST')!;
    expect(post.body).toEqual({ licensee_name: 'Buyer Ltd', seats: 3 });
    expect(w.find('[data-testid="licence-key-value"]').text()).toBe(KEY);
    expect(LICENSE_KEY.test(KEY)).toBe(true);
    expect(w.text()).toContain('not shown again');
    await w.find('[data-testid="dialog-confirm"]').trigger('click');
    await settle();
    expect(document.body.innerHTML).not.toContain(KEY);
    expect(w.find('[data-table-id="store.manage.licences"]').text()).toContain('Buyer Ltd');
  });

  it('frees a seat (the instance id travels encoded) and revokes a key', async () => {
    const { w, calls } = await manage();
    await w.find('[data-testid="licence-actions-FXL-7K2Q"]').trigger('click');
    await settle();
    const items = document.querySelectorAll<HTMLElement>('[role="menuitem"]');
    const instancesItem = Array.from(items).find((el) => el.textContent?.includes('Instances'));
    instancesItem?.click();
    await settle();
    expect(w.find('[data-table-id="store.manage.activations"]').text()).toContain('inst/one');
    const rowMenu = w.find('[data-table-id="store.manage.activations"]').find('.tbl-rowactions__btn');
    await rowMenu.trigger('click');
    await settle();
    Array.from(document.querySelectorAll<HTMLElement>('[role="menuitem"]')).find((el) => el.textContent?.includes('Free the seat'))?.click();
    await settle();
    expect(calls.some((c) => c.method === 'POST' && c.path.endsWith('/activations/inst%2Fone/release'))).toBe(true);

    await w.find('[data-testid="licence-actions-FXL-7K2Q"]').trigger('click');
    await settle();
    Array.from(document.querySelectorAll<HTMLElement>('[role="menuitem"]')).find((el) => el.textContent?.includes('Revoke'))?.click();
    await settle();
    await w.findAll('[data-testid="dialog-confirm"]').at(-1)!.trigger('click');
    await settle();
    expect(calls.some((c) => c.method === 'POST' && c.path.endsWith('/revoke'))).toBe(true);
    expect(w.find('[data-table-id="store.manage.licences"]').text()).toContain('Revoked');
  });

  it('sets a paid price; a free one sends no price text', async () => {
    const { w, calls } = await manage();
    await w.find('[data-testid="pricing-paid"]').setValue(true);
    await w.find('[data-testid="pricing-text"]').setValue('$9');
    await w.find('[data-testid="pricing-url"]').setValue('https://buy.example/hello');
    await w.find('[data-testid="pricing-form"]').trigger('submit');
    await settle();
    expect(calls.find((c) => c.method === 'PUT')!.body).toEqual({ model: 'paid', price_text: '$9', purchase_url: 'https://buy.example/hello' });
    expect(w.find('[data-testid="pricing-saved"]').exists()).toBe(true);
    await w.find('[data-testid="pricing-free"]').setValue(true);
    await w.find('[data-testid="pricing-form"]').trigger('submit');
    await settle();
    expect(calls.filter((c) => c.method === 'PUT').at(-1)!.body).toEqual({ model: 'free' });
  });

  it('says plainly when the server refuses (not a member)', async () => {
    const { w } = await manage({ 'GET /api/v1/apps/hello/licenses': { status: 403, body: { error: 'not_a_member', message: 'No.' } } });
    expect(w.find('[data-testid="manage-forbidden"]').exists()).toBe(true);
  });
});

describe('My filex instances', () => {
  it('lists, saves and forgets, the owner\'s only', async () => {
    let items = [{ id: '11111111-1111-1111-1111-111111111111', label: 'Office', url: 'https://files.example.com', created_at: '2026-10-01T00:00:00Z' }];
    const { w, calls } = await open('/me/instances', ME_PUBLISHER, {
      'GET /api/v1/me/instances': () => ({ body: { items } }),
      'POST /api/v1/me/instances': (_u, init) => {
        const b = JSON.parse(String(init.body));
        items = [...items, { id: '44444444-4444-4444-4444-444444444444', label: b.label ?? 'home.example', url: b.url, created_at: '2026-10-04T00:00:00Z' }];
        return { status: 201, body: items.at(-1) };
      },
      'DELETE /api/v1/me/instances/11111111-1111-1111-1111-111111111111': () => {
        items = items.slice(1);
        return { status: 204 };
      },
    });
    expect(w.find('[data-table-id="store.me.instances"]').text()).toContain('https://files.example.com');
    await w.find('[data-testid="instance-url"]').setValue('https://home.example/admin');
    await w.find('[data-testid="instance-form"]').trigger('submit');
    await settle();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    await w.find('[data-testid="instance-url"]').setValue('https://home.example');
    await w.find('[data-testid="instance-form"]').trigger('submit');
    await settle();
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ url: 'https://home.example' });
    expect(w.find('[data-table-id="store.me.instances"]').text()).toContain('https://home.example');
  });
});

describe('install helpers', () => {
  it('accepts an origin only, and opens only the store\'s own link shape', () => {
    for (const ok of ['https://files.example.com', 'https://files.example.com/', 'http://localhost:5212']) expect(looksLikeFilexUrl(ok)).toBe(true);
    for (const bad of ['files.example.com', 'https://files.example.com/admin', 'https://a@b.example', 'javascript:alert(1)', 'https://x.example?q=1'])
      expect(looksLikeFilexUrl(bad)).toBe(false);
    expect(openableLink(LINK)).toBe(LINK);
    expect(openableLink('javascript:alert(1)')).toBe('');
    expect(openableLink('https://files.example.com/elsewhere#store=x')).toBe('');
    expect(openableLink('https://u:p@files.example.com/admin/store-install#store=x')).toBe('');
  });
});
