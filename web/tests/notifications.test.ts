// Notifications: the page chooses where (this browser, webhooks) and
// which events; the server writes and sends every notification. An
// administrator is offered the administrators' events, a publisher only
// their own; a webhook's address is shown by its host only; the signing
// secret appears once.
import { afterEach, describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { session } from '@/lib/session';
import { createWebhook, setCsrfToken, setPushEvents, testWebhook, type Me } from '@/api/client';
import { keyBytes } from '@/lib/push';
import { S } from '@/strings';
import { ME_ADMIN, ME_PUBLISHER, mockFetch, settle, testRouter, type Answer, type Handler } from './helpers';

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

const HOOK = '6f9619ff-8b86-4d01-b42d-00cf4fc964ff';
const USER_EVENTS = [
  { kind: 'submission.approved', admin: false },
  { kind: 'submission.rejected', admin: false },
  { kind: 'filex.released', admin: false },
];
const ADMIN_EVENTS = [
  ...USER_EVENTS,
  { kind: 'submission.app_new', admin: true },
  { kind: 'submission.version_new', admin: true },
  { kind: 'publisher.requested', admin: true },
  { kind: 'pipeline.error', admin: true },
];
const hook = {
  id: HOOK,
  name: 'Team alerts',
  format: 'generic',
  url_hint: 'https://notify.example/…',
  header_name: 'Authorization',
  signed: true,
  events: ADMIN_EVENTS.map((e) => e.kind),
  created_at: '2026-10-20T08:00:00Z',
  last: { status: 'delivered', http_status: 201, at: '2026-10-20T09:00:00Z' },
};
const settings = (admin: boolean, webhooks: unknown[] = []) => ({
  events: admin ? ADMIN_EVENTS : USER_EVENTS,
  formats: ['generic', 'slack', 'discord', 'teams'],
  push: { available: false, events: [], browsers: [] },
  webhooks,
});

async function open(me: unknown, extra: Record<string, Answer | Handler> = {}, webhooks: unknown[] = []) {
  session.loaded = true;
  session.me = me as Me | null;
  setCsrfToken(me ? 'tok-test' : '');
  const f = mockFetch({
    'GET /api/v1/me/notifications': { body: settings(me === ME_ADMIN, webhooks) },
    ...extra,
  });
  const router = testRouter();
  await router.push('/me/notifications');
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

describe('notifications page', () => {
  it('lists a webhook by its host, format and last delivery', async () => {
    const { w } = await open(ME_ADMIN, {}, [hook]);
    const table = w.find('[data-table-id="store.me.webhooks"]');
    expect(table.exists()).toBe(true);
    const text = table.text();
    expect(text).toContain('Team alerts');
    expect(text).toContain('https://notify.example/…');
    expect(text).toContain('Generic JSON (signed)');
    expect(text).toContain('7 of 7 events');
    expect(text).toContain('HTTP 201');
    expect(w.find('[data-table-id="store.me.browsers"]').exists()).toBe(true);
    expect(w.find('[data-testid="push-unavailable"]').exists()).toBe(true);
  });

  it('a publisher is offered their own three events; an administrator all seven', async () => {
    const pub = await open(ME_PUBLISHER);
    await pub.w.find('[data-testid="webhook-add"]').trigger('click');
    await settle();
    expect(pub.w.findAll('[data-testid^="webhook-event-"]').length).toBe(3);
    expect(pub.w.text()).not.toContain(S.notifications.events['submission.app_new']);
    document.body.replaceChildren();
    const adm = await open(ME_ADMIN);
    await adm.w.find('[data-testid="webhook-add"]').trigger('click');
    await settle();
    expect(adm.w.findAll('[data-testid^="webhook-event-"]').length).toBe(7);
    expect(adm.w.text()).toContain(S.notifications.forAdmins);
  });

  it('adds a generic webhook with a header and shows its secret once', async () => {
    const { w, calls } = await open(ME_ADMIN, {
      'POST /api/v1/me/webhooks': { status: 201, body: { webhook: hook, secret: 'whsec_' + 'a'.repeat(43) } },
    });
    await w.find('[data-testid="webhook-add"]').trigger('click');
    await settle();
    // Nothing goes without a name, an address and an event.
    await w.find('[data-testid="webhook-event-pipeline.error"]').setValue(false);
    await w.findAll('[data-testid="dialog-confirm"]')[0].trigger('click');
    await settle();
    expect(calls.some((c) => c.method === 'POST')).toBe(false);
    await w.find('[data-testid="webhook-name"]').setValue('Team alerts');
    await w.find('[data-testid="webhook-url"]').setValue('https://notify.example/api/notify/v1/send?group=filex-apps');
    await w.find('[data-testid="webhook-header-name"]').setValue('Authorization');
    await w.find('[data-testid="webhook-header-value"]').setValue('Bearer bn_test');
    await w.findAll('[data-testid="dialog-confirm"]')[0].trigger('click');
    await settle(6);
    const post = calls.find((c) => c.method === 'POST' && c.path === '/api/v1/me/webhooks')!;
    expect(post.headers['X-CSRF-Token']).toBe('tok-test');
    expect(post.body).toEqual({
      name: 'Team alerts',
      format: 'generic',
      url: 'https://notify.example/api/notify/v1/send?group=filex-apps',
      header_name: 'Authorization',
      header_value: 'Bearer bn_test',
      events: ADMIN_EVENTS.map((e) => e.kind).filter((k) => k !== 'pipeline.error'),
    });
    expect(w.find('[data-testid="webhook-secret-value"]').text()).toBe('whsec_' + 'a'.repeat(43));
  });

  it('a Slack webhook sends no header (the field is not even offered)', async () => {
    const { w, calls } = await open(ME_PUBLISHER, {
      'POST /api/v1/me/webhooks': { status: 201, body: { webhook: { ...hook, format: 'slack', signed: false } } },
    });
    await w.find('[data-testid="webhook-add"]').trigger('click');
    await settle();
    await w.find('[data-testid="webhook-format-slack"]').trigger('click');
    await settle();
    expect(w.find('[data-testid="webhook-header-name"]').exists()).toBe(false);
    expect(w.find('[data-testid="webhook-format-hint"]').text()).toContain('hooks.slack.com');
    await w.find('[data-testid="webhook-name"]').setValue('#releases');
    await w.find('[data-testid="webhook-url"]').setValue('https://hooks.slack.com/services/T0/B0/x');
    await w.findAll('[data-testid="dialog-confirm"]')[0].trigger('click');
    await settle(6);
    const post = calls.find((c) => c.method === 'POST')!;
    expect(post.body).toEqual({ name: '#releases', format: 'slack', url: 'https://hooks.slack.com/services/T0/B0/x', events: USER_EVENTS.map((e) => e.kind) });
    expect(w.find('[data-testid="webhook-secret"]').exists()).toBe(false);
  });

  it('a test says what the receiver answered', async () => {
    const { w, calls } = await open(
      ME_ADMIN,
      { [`POST /api/v1/me/webhooks/${HOOK}/test`]: { body: { status: 'failed', http_status: 500, error: 'the receiver answered HTTP 500' } } },
      [hook],
    );
    await w.find(`[data-testid="webhook-actions-${HOOK}"]`).trigger('click');
    await settle();
    Array.from(document.querySelectorAll<HTMLElement>('[role="menuitem"]')).find((el) => el.textContent?.includes(S.notifications.test))?.click();
    await settle(6);
    expect(calls.some((c) => c.method === 'POST' && c.path === `/api/v1/me/webhooks/${HOOK}/test`)).toBe(true);
    expect(w.find('[data-testid="notify-status"]').text()).toContain('The test to Team alerts failed: the receiver answered HTTP 500');
  });
});

describe('notifications client and words', () => {
  it('every call goes to the caller\'s own paths with the CSRF token', async () => {
    setCsrfToken('tok-n');
    const { calls } = mockFetch({
      'PUT /api/v1/me/notifications/push': { body: { events: ['filex.released'] } },
      'POST /api/v1/me/webhooks': { status: 201, body: { webhook: hook } },
      [`POST /api/v1/me/webhooks/${HOOK}/test`]: { body: { status: 'delivered', http_status: 204 } },
    });
    await setPushEvents(['filex.released']);
    await createWebhook({ name: 'x', format: 'discord', url: 'https://discord.com/api/webhooks/1/x', events: ['filex.released'] });
    expect((await testWebhook(HOOK)).status).toBe('delivered');
    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      'PUT /api/v1/me/notifications/push',
      'POST /api/v1/me/webhooks',
      `POST /api/v1/me/webhooks/${HOOK}/test`,
    ]);
    for (const c of calls) expect(c.headers['X-CSRF-Token']).toBe('tok-n');
  });

  it('the VAPID key decodes from base64url to its 65 bytes', () => {
    // RFC 8291's example browser point (public), base64url.
    const point = ['BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcx', 'aOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4'].join('');
    const b = keyBytes(point);
    expect(b.length).toBe(65);
    expect(b[0]).toBe(4);
  });

  it('every event has words, and none uses a long dash', () => {
    for (const e of ADMIN_EVENTS) expect(S.notifications.events[e.kind as keyof typeof S.notifications.events]).toBeTruthy();
    const walk = (v: unknown): string[] => (typeof v === 'string' ? [v] : v && typeof v === 'object' ? Object.values(v).flatMap(walk) : []);
    for (const s of walk(S.notifications)) expect(s).not.toMatch(/[\u2013\u2014]/);
  });
});
