// The store's own name, logo and operator (lib/brand.ts, GET /api/v1/store):
// the brand, the tab title and the footer follow the server, never a name
// built into the interface.
import { afterEach, describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import App from '@/App.vue';
import { session } from '@/lib/session';
import { applyStore, brand, brandParts, hasOperatorInfo, loadBrand, operatorLabel, safeLink } from '@/lib/brand';
import { setTitle } from '@/router';
import { mockFetch, settle, testRouter } from './helpers';

const blank = { name: 'apps.example.com', logoUrl: '', operatorName: '', operatorUrl: '', contact: '', termsUrl: '', privacyUrl: '', loaded: false };

afterEach(() => {
  document.body.replaceChildren();
  Object.assign(brand, blank);
  session.me = null;
  session.loaded = false;
});

async function mountApp() {
  session.loaded = true;
  mockFetch({ 'GET /api/v1/apps': { body: { items: [], total: 0, page: 1, per_page: 50 } }, 'GET /api/v1/categories': { body: { items: [] } } });
  const router = testRouter();
  await router.push('/');
  const w = mount(App, { global: { plugins: [router] }, attachTo: document.body });
  await settle();
  return w;
}

describe('the brand', () => {
  it('splits a name into the brand\'s two weights; a host is one word', () => {
    expect(brandParts('filex Apps')).toEqual({ head: 'filex', tag: 'Apps' });
    expect(brandParts('Acme Corp. App Store')).toEqual({ head: 'Acme Corp. App', tag: 'Store' });
    expect(brandParts('apps.example.com')).toEqual({ head: 'apps.example.com', tag: '' });
    expect(brandParts('  Acme  ')).toEqual({ head: 'Acme', tag: '' });
  });

  it('links only to https (and mailto for the contact)', () => {
    expect(safeLink('https://www.example.com/terms')).toBe('https://www.example.com/terms');
    expect(safeLink('mailto:store@example.com')).toBe('');
    expect(safeLink('mailto:store@example.com', true)).toBe('mailto:store@example.com');
    for (const bad of ['javascript:alert(1)', 'http://www.example.com', '//www.example.com', 'https://', 'mailto:x', 42, null]) {
      expect(safeLink(bad, true)).toBe('');
    }
  });

  it('takes the server\'s answer, keeping only store paths for the logo', () => {
    applyStore({ name: 'Acme Apps', logo_url: '/api/v1/store/logo?v=abc', operator_url: 'https://www.example.com/', contact: 'javascript:alert(1)' });
    expect(brand.name).toBe('Acme Apps');
    expect(brand.logoUrl).toBe('/api/v1/store/logo?v=abc');
    expect(brand.contact).toBe('');
    expect(operatorLabel(brand)).toBe('www.example.com');
    expect(hasOperatorInfo(brand)).toBe(true);
    applyStore({ name: '', logo_url: 'https://elsewhere.example/logo.svg' });
    expect(brand.name).toBe('Acme Apps');
    expect(brand.logoUrl).toBe('');
    expect(hasOperatorInfo(brand)).toBe(false);
  });

  it('reads GET /api/v1/store once and keeps the page\'s name when it fails', async () => {
    const { calls } = mockFetch({ 'GET /api/v1/store': { status: 503, body: { error: 'not_ready', message: 'x' } } });
    await loadBrand(true);
    expect(calls.map((c) => c.path)).toEqual(['/api/v1/store']);
    expect(brand.name).toBe('apps.example.com');
    expect(brand.loaded).toBe(true);
    mockFetch({ 'GET /api/v1/store': { body: { name: 'Acme Apps', terms_url: 'https://www.example.com/terms' } } });
    await loadBrand(true);
    expect(brand.name).toBe('Acme Apps');
    expect(brand.termsUrl).toBe('https://www.example.com/terms');
  });

  it('the tab title ends with the store\'s name, and follows it', async () => {
    brand.name = 'Acme Apps';
    setTitle('Catalog');
    expect(document.title).toBe('Catalog - Acme Apps');
    brand.name = 'Globex Store';
    await settle();
    expect(document.title).toBe('Catalog - Globex Store');
    setTitle('');
    expect(document.title).toBe('Globex Store');
  });
});

describe('the shell with a brand', () => {
  it('shows the store\'s name and filex\'s mark when it has no logo, and no operator line', async () => {
    brand.name = 'Acme Apps';
    const w = await mountApp();
    const head = w.find('[data-testid="brand"]');
    expect(head.text()).toBe('Acme Apps');
    expect(head.find('.fa-brand__tag').text()).toBe('Apps');
    expect(head.find('svg').exists()).toBe(true);
    expect(w.find('[data-testid="brand-logo"]').exists()).toBe(false);
    expect(w.find('[data-testid="footer-about"]').exists()).toBe(false);
    expect(w.text()).not.toContain('filex Apps');
    w.unmount();
  });

  it('a host is the whole brand, no tag', async () => {
    const w = await mountApp();
    expect(w.find('[data-testid="brand"]').text()).toBe('apps.example.com');
    expect(w.find('[data-testid="brand"] .fa-brand__tag').exists()).toBe(false);
    w.unmount();
  });

  it('shows the store\'s own logo and only the operator links that are set', async () => {
    applyStore({ name: 'Acme Apps', logo_url: '/api/v1/store/logo?v=1', operator_name: 'Acme Corp.', operator_url: 'https://www.example.com/',
      contact: 'mailto:store@example.com', privacy_url: 'https://www.example.com/privacy' });
    const w = await mountApp();
    const logo = w.find('[data-testid="brand-logo"]');
    expect(logo.attributes('src')).toBe('/api/v1/store/logo?v=1');
    expect(logo.attributes('alt')).toBe('');
    expect(w.find('[data-testid="brand"] svg').exists()).toBe(false);
    const about = w.find('[data-testid="footer-about"]');
    expect(about.attributes('aria-label')).toBe('About this store');
    expect(w.find('[data-testid="footer-operator"]').text()).toBe('Operated by Acme Corp.');
    expect(w.find('[data-testid="footer-operator"] a').attributes('href')).toBe('https://www.example.com/');
    expect(w.find('[data-testid="footer-contact"]').attributes('href')).toBe('mailto:store@example.com');
    expect(w.find('[data-testid="footer-privacy"]').attributes('href')).toBe('https://www.example.com/privacy');
    expect(w.find('[data-testid="footer-terms"]').exists()).toBe(false);
    w.unmount();
  });

  it('an operator without a page is plain text', async () => {
    applyStore({ name: 'Acme Apps', operator_name: 'Acme Corp.' });
    const w = await mountApp();
    expect(w.find('[data-testid="footer-operator"]').text()).toBe('Operated by Acme Corp.');
    expect(w.find('[data-testid="footer-operator"] a').exists()).toBe(false);
    w.unmount();
  });
});
