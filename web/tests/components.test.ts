import { afterEach, describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import ActionDialog from '@/components/ActionDialog.vue';
import AccessGate from '@/components/AccessGate.vue';
import ConsentHelper from '@/components/ConsentHelper.vue';
import App from '@/App.vue';
import { session } from '@/lib/session';
import type { Me } from '@/api/client';
import { ME_ADMIN, ME_PUBLISHER, ME_REVIEWER, mockFetch, settle, testRouter } from './helpers';

afterEach(() => {
  document.body.replaceChildren();
  session.me = null;
  session.loaded = false;
});

describe('ActionDialog', () => {
  it('asks for a reason and does not confirm without one', async () => {
    const w = mount(ActionDialog, {
      props: { open: false, title: 'Revoke x', confirmLabel: 'Revoke', reason: true },
      attachTo: document.body,
    });
    await w.setProps({ open: true });
    await settle();
    await w.find('form').trigger('submit');
    expect(w.emitted('confirm')).toBeUndefined();
    expect(w.text()).toContain('Required.');
    expect(w.find('textarea').attributes('aria-invalid')).toBe('true');
    await w.find('textarea').setValue('  harmful  ');
    await w.find('form').trigger('submit');
    expect(w.emitted('confirm')).toEqual([['harmful']]);
  });

  it('validates itself instead of the browser (novalidate) and is labelled by its title', async () => {
    const w = mount(ActionDialog, { props: { open: true, title: 'Yank 1.0', confirmLabel: 'Yank', reason: true }, attachTo: document.body });
    await settle();
    expect(w.find('form').attributes('novalidate')).toBeDefined();
    const id = w.find('dialog').attributes('aria-labelledby')!;
    expect(w.find(`#${id}`).text()).toBe('Yank 1.0');
  });
});

describe('AccessGate', () => {
  const at = async (need: 'github' | 'reviewer' | 'admin', me: unknown) => {
    session.loaded = true;
    session.me = me as Me | null;
    const router = testRouter();
    await router.push('/review');
    return mount(AccessGate, { props: { need }, slots: { default: '<p data-testid="inside">secret page</p>' }, global: { plugins: [router] } });
  };

  it('asks a visitor to sign in, keeping the way back', async () => {
    const w = await at('reviewer', null);
    expect(w.find('[data-testid="inside"]').exists()).toBe(false);
    expect(w.find('[data-testid="access-gate"]').attributes('data-state')).toBe('sign-in');
    expect(w.find('a').attributes('href')).toBe('/sign-in?return_to=/review');
  });

  it('says which role is missing, and opens for the right one', async () => {
    expect((await at('admin', ME_REVIEWER)).find('[data-testid="access-gate"]').attributes('data-state')).toBe('role');
    expect((await at('reviewer', ME_ADMIN)).find('[data-testid="inside"]').exists()).toBe(true);
    // Every session is a GitHub one: a reviewer may publish too.
    expect((await at('github', ME_REVIEWER)).find('[data-testid="inside"]').exists()).toBe(true);
    expect((await at('github', ME_PUBLISHER)).find('[data-testid="inside"]').exists()).toBe(true);
  });
});

describe('ConsentHelper', () => {
  it('writes the file live from what is typed, and never sends it', async () => {
    const { fn } = mockFetch({});
    const w = mount(ConsentHelper, { props: { publisher: 'jdoe', categories: [{ id: 'developer', label: { en: 'Developer tools' } }] } });
    await w.find('[data-testid="cs-summary"]').setValue('Preview Markdown.');
    await w.find('input[type="checkbox"]').setValue(true);
    const json = JSON.parse(w.find('[data-testid="cs-json"]').text());
    expect(json).toEqual({ publisher: 'jdoe', categories: ['developer'], summary: { en: 'Preview Markdown.' } });
    expect(w.find('[data-testid="cs-valid"]').exists()).toBe(true);
    await w.find('[data-testid="cs-icon"]').setValue('../icon.png');
    expect(w.find('[data-testid="cs-errors"]').text()).toContain('relative path');
    expect(fn).not.toHaveBeenCalled();
  });

  it('states the image limits the pipeline enforces', () => {
    const w = mount(ConsentHelper, { props: { publisher: 'jdoe', categories: [] } });
    expect(w.find('#cs-icon-h').text()).toContain('square PNG or WebP, 128 to 1024 px');
    expect(w.find('[data-testid="cs-icon"]').attributes('aria-describedby')).toBe('cs-icon-h');
    expect(w.find('[data-testid="cs-shots-hint"]').text()).toContain('320 to 3840 px, at most 3840 x 2160');
  });
});

describe('the shell', () => {
  const mountApp = async (me: unknown) => {
    session.loaded = true;
    session.me = me as Me | null;
    mockFetch({ 'GET /api/v1/apps': { body: { items: [], total: 0, page: 1, per_page: 50 } }, 'GET /api/v1/categories': { body: { items: [] } } });
    const router = testRouter();
    await router.push('/');
    const w = mount(App, { global: { plugins: [router] }, attachTo: document.body });
    await settle();
    return w;
  };

  it('shows Review and Admin only to the roles that may use them', async () => {
    const pub = await mountApp(ME_PUBLISHER);
    expect(pub.find('[data-testid="nav-review"]').exists()).toBe(false);
    expect(pub.find('[data-testid="nav-admin"]').exists()).toBe(false);
    expect(pub.find('[data-testid="who"]').text()).toContain('jdoe');
    pub.unmount();
    const rev = await mountApp(ME_REVIEWER);
    expect(rev.find('[data-testid="nav-review"]').exists()).toBe(true);
    expect(rev.find('[data-testid="nav-admin"]').exists()).toBe(false);
    rev.unmount();
    const adm = await mountApp(ME_ADMIN);
    expect(adm.find('[data-testid="nav-admin"]').exists()).toBe(true);
  });

  it('marks Publish active on its own pages, not on a publisher\'s page (/publishers/... starts with /publish too)', async () => {
    session.loaded = true;
    session.me = ME_PUBLISHER as unknown as Me;
    mockFetch({
      'GET /api/v1/publishers/brf-tech': { status: 404, body: { error: 'not_found', message: 'x' } },
      'GET /api/v1/submissions': { body: { items: [], total: 0, page: 1, per_page: 25 } },
      'GET /api/v1/categories': { body: { items: [] } },
    });
    const router = testRouter();
    await router.push('/publishers/brf-tech');
    const w = mount(App, { global: { plugins: [router] }, attachTo: document.body });
    await settle();
    expect(w.find('[data-testid="nav-publish"]').classes()).not.toContain('is-active');
    await router.push('/publish/new-publisher');
    await settle();
    expect(w.find('[data-testid="nav-publish"]').classes()).toContain('is-active');
    w.unmount();
  });

  it('has a skip link to the main landmark and a labelled navigation', async () => {
    const w = await mountApp(null);
    expect(w.find('a.fa-skip').attributes('href')).toBe('#main');
    expect(w.find('main#main').exists()).toBe(true);
    expect(w.find('nav').attributes('aria-label')).toBe('Main');
    expect(w.find('[data-testid="sign-in"]').exists()).toBe(true);
  });

  it('the theme switch puts filex\'s dark palette on <html>', async () => {
    const w = await mountApp(null);
    await w.find('[data-testid="theme-dark"]').trigger('click');
    await settle();
    expect(document.documentElement.classList.contains('dark')).toBe(true);
    await w.find('[data-testid="theme-light"]').trigger('click');
    await settle();
    expect(document.documentElement.classList.contains('dark')).toBe(false);
    w.unmount();
  });

  it('the theme switch is a radio group of three labelled buttons, moved by the arrow keys', async () => {
    const w = await mountApp(null);
    const group = w.find('[data-testid="theme"]');
    expect(group.attributes('role')).toBe('radiogroup');
    expect(group.find('select').exists()).toBe(false);
    const radios = group.findAll('[role="radio"]');
    expect(radios.map((r) => r.attributes('aria-label'))).toEqual(['Light', 'System', 'Dark']);
    expect(radios.map((r) => r.attributes('title'))).toEqual(['Light', 'System', 'Dark']);
    await w.find('[data-testid="theme-light"]').trigger('click');
    await settle();
    const checked = () => group.findAll('[role="radio"]').map((r) => r.attributes('aria-checked'));
    expect(checked()).toEqual(['true', 'false', 'false']);
    expect(group.findAll('[role="radio"]').map((r) => r.attributes('tabindex'))).toEqual(['0', '-1', '-1']);
    await w.find('[data-testid="theme-light"]').trigger('keydown', { key: 'ArrowRight' });
    await settle();
    expect(checked()).toEqual(['false', 'true', 'false']);
    await w.find('[data-testid="theme-system"]').trigger('keydown', { key: 'ArrowRight' });
    await settle();
    expect(checked()).toEqual(['false', 'false', 'true']);
    expect(document.documentElement.classList.contains('dark')).toBe(true);
    await w.find('[data-testid="theme-dark"]').trigger('keydown', { key: 'ArrowRight' });
    await settle();
    expect(checked()).toEqual(['true', 'false', 'false']);
    await w.find('[data-testid="theme-light"]').trigger('keydown', { key: 'End' });
    await settle();
    expect(checked()).toEqual(['false', 'false', 'true']);
    w.unmount();
  });
});

