// The publishers' page: one card per publisher (not a table), searchable and
// sortable, with the choice in the address; each card leads to the
// publisher's own page.
import { afterEach, describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import { defineComponent, h } from 'vue';
import { RouterView } from 'vue-router';
import { mockFetch, settle, testRouter, type Answer } from './helpers';

const BRF = {
  id: 'brf-tech',
  name: 'BRF Tech',
  github_owner: 'BRF-Tech',
  owner_type: 'Organization',
  verified: true,
  official: true,
  apps: 7,
  description: 'The people who make filex.',
  avatar_url: `/v1/media/${'a'.repeat(64)}.png`,
};
const JDOE = { id: 'jdoe', name: 'Jane Doe', github_owner: 'jdoe', owner_type: 'User', verified: false, official: false, apps: 1, description: 'Small tools.' };

const Host = defineComponent({ setup: () => () => h(RouterView) });

async function open(path: string, answer: Answer | (() => Answer | Promise<Answer>) = { body: { items: [BRF, JDOE], total: 2, page: 1, per_page: 48 } }) {
  const f = mockFetch({ 'GET /api/v1/publishers': typeof answer === 'function' ? answer : () => answer });
  const router = testRouter();
  await router.push(path);
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  await settle();
  return { w, router, ...f };
}
const listCalls = (calls: { path: string }[]) => calls.filter((c) => c.path.startsWith('/api/v1/publishers'));

afterEach(() => document.body.replaceChildren());

describe('the publishers page', () => {
  it('is a grid of cards, not a table: name links to the publisher, badges, GitHub account, app count, about', async () => {
    const { w } = await open('/publishers');
    expect(w.find('h1').text()).toBe('Who makes filex apps');
    expect(w.find('[role="grid"]').exists()).toBe(false);
    const card = w.find('[data-testid="publisher-card-brf-tech"]');
    expect(card.element.tagName).toBe('ARTICLE');
    expect(card.find('[data-testid="publisher-link-brf-tech"]').attributes('href')).toBe('/publishers/brf-tech');
    expect(card.text()).toContain('Official');
    expect(card.text()).toContain('Verified');
    expect(card.text()).toContain('BRF-Tech');
    expect(card.text()).toContain('7 apps');
    expect(card.text()).toContain('The people who make filex.');
    expect(card.find('img.fa-appicon').attributes('src')).toBe(BRF.avatar_url);
    const other = w.find('[data-testid="publisher-card-jdoe"]');
    expect(other.text()).toContain('1 app');
    expect(other.text()).toContain('Small tools.');
    expect(other.text()).not.toContain('Official');
    expect(other.find('[data-testid="app-icon-placeholder"]').text()).toBe('J');
    expect(w.find('[data-testid="publishers-count"]').text()).toBe('2 publishers');
  });

  it('asks the API with the address\'s search, order and page', async () => {
    const { calls } = await open('/publishers?q=brf&sort=apps&page=2');
    const q = new URL(listCalls(calls)[0].path, 'http://x').searchParams;
    expect(Object.fromEntries(q)).toEqual({ q: 'brf', sort: 'apps', page: '2', per_page: '48' });
  });

  it('the order goes into the address and starts from page 1', async () => {
    const { w, router } = await open('/publishers?page=3');
    await w.find('[data-testid="publishers-sort-apps"]').trigger('click');
    await settle();
    expect(router.currentRoute.value.query).toEqual({ sort: 'apps' });
  });

  it('says why it is empty, and clears the search', async () => {
    const { w, router } = await open('/publishers?q=zzz', { body: { items: [], total: 0, page: 1, per_page: 48 } });
    expect(w.find('[data-testid="empty-state"]').text()).toContain('No publisher matches.');
    await w.find('[data-testid="publishers-clear"]').trigger('click');
    await settle();
    expect(router.currentRoute.value.query).toEqual({});
  });

  it('a failed load shows the error alone, with a way to try again', async () => {
    const { w } = await open('/publishers', { status: 429, body: { error: 'rate_limited', message: 'Too many requests.' } });
    expect(w.find('[role="alert"]').text()).toContain('Too many requests.');
    expect(w.find('[data-testid="empty-state"]').exists()).toBe(false);
  });

  it('draws grey cards while the first answer is on its way', async () => {
    const { w } = await open('/publishers', () => new Promise<Answer>(() => {}));
    expect(w.find('[data-testid="publishers-skeleton"]').attributes('aria-hidden')).toBe('true');
    expect(w.find('[data-testid="publishers-count"]').text()).toBe('Loading publishers…');
  });
});
