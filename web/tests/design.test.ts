// The shop window's own parts: the icon placeholder, the screenshot viewer,
// the permissions in filex's words, the step indicator, and the app page that
// puts them together.
import { afterEach, describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import AppIcon from '@/components/AppIcon.vue';
import PermissionList from '@/components/PermissionList.vue';
import ScreenshotGallery from '@/components/ScreenshotGallery.vue';
import Stepper from '@/components/Stepper.vue';
import { TONES, initialOf, languageName, permissionKind, toneOf } from '@/lib/format';
import { parseCatalogView, withView } from '@/lib/catalogQuery';
import { session } from '@/lib/session';
import { mockFetch, settle, testRouter } from './helpers';

afterEach(() => {
  document.body.replaceChildren();
  session.me = null;
});

describe('helpers behind the look', () => {
  it('a name always gets the same tone, one of the palette\'s', () => {
    expect(toneOf('filex-sign')).toBe(toneOf('filex-sign'));
    const tones = new Set(['a', 'b', 'filex-sign', 'photo-exif', 'kanban-notes', 'Español', 'x'.repeat(80)].map(toneOf));
    for (const t of tones) expect(t >= 0 && t < TONES).toBe(true);
    expect(tones.size).toBeGreaterThan(2);
  });

  it('the initial is the first letter or digit, upper-cased', () => {
    expect(initialOf('draw.io for filex')).toBe('D');
    expect(initialOf('  ëlan')).toBe('Ë');
    expect(initialOf('.42')).toBe('4');
    expect(initialOf('')).toBe('?');
  });

  it('a permission\'s picture comes from its id, in either spelling', () => {
    expect(permissionKind('files:read')).toBe('files');
    expect(permissionKind('files.write')).toBe('files');
    expect(permissionKind('ui')).toBe('ui');
    expect(permissionKind('ui-viewer:.drawio')).toBe('ui');
    expect(permissionKind('ui-net:api.example')).toBe('net');
    expect(permissionKind('net.github')).toBe('net');
    expect(permissionKind('thumbnail:.jpg')).toBe('thumbnail');
    expect(permissionKind('convert.office')).toBe('convert');
    expect(permissionKind('office-engine')).toBe('convert');
    expect(permissionKind('something-new')).toBe('other');
  });

  it('a language is named in its own language, never as a code with a percentage', () => {
    expect(languageName('en')).toBe('English');
    expect(languageName('tr')).toBe('Türkçe');
    expect(languageName('es')).toBe('Español');
    expect(languageName('de')).toBe('Deutsch');
    expect(languageName('not a code')).toBe('not a code');
  });

  it('the view rides in the address only when it is not the default', () => {
    expect(parseCatalogView({})).toBe('grid');
    expect(parseCatalogView({ view: 'list' })).toBe('list');
    expect(parseCatalogView({ view: 'table' })).toBe('grid');
    expect(withView({ q: 'x' }, 'grid')).toEqual({ q: 'x' });
    expect(withView({ q: 'x' }, 'list')).toEqual({ q: 'x', view: 'list' });
  });
});

describe('AppIcon', () => {
  it('draws the mirrored image when there is one, decorative', () => {
    const sha = 'a'.repeat(64);
    const w = mount(AppIcon, { props: { url: `/v1/media/${sha}.png`, name: 'filex Sign', size: 48 } });
    expect(w.find('img').attributes('src')).toBe(`/v1/media/${sha}.png`);
    expect(w.find('img').attributes('alt')).toBe('');
  });

  it('without one: the initial on the name\'s tone, full colour when large, soft when small', () => {
    const big = mount(AppIcon, { props: { name: 'Kanban Notes', size: 48 } });
    const el = big.find('[data-testid="app-icon-placeholder"]');
    expect(el.text()).toBe('K');
    expect(el.attributes('aria-hidden')).toBe('true');
    expect(el.classes()).toContain(`fa-tone-${toneOf('Kanban Notes')}`);
    expect(el.classes()).toContain('fa-appicon--solid');
    const small = mount(AppIcon, { props: { name: 'Kanban Notes', size: 24 } });
    expect(small.find('[data-testid="app-icon-placeholder"]').classes()).not.toContain('fa-appicon--solid');
  });

  it('never draws an image from another host', () => {
    const w = mount(AppIcon, { props: { url: 'https://evil.example/x.png', name: 'X' } });
    expect(w.find('img').exists()).toBe(false);
  });
});

describe('PermissionList', () => {
  it('shows filex\'s words, the publisher\'s reason and the id, with a picture per kind', () => {
    const w = mount(PermissionList, {
      props: {
        rows: [
          { id: 'files:read', label: 'Reads the files you pick', reason: { en: 'To open the PDF.' } },
          { id: 'ui-net:api.example', label: 'Talks to api.example' },
        ],
      },
    });
    const items = w.findAll('li');
    expect(items).toHaveLength(2);
    expect(items[0].text()).toContain('Reads the files you pick');
    expect(items[0].text()).toContain('Why: To open the PDF.');
    expect(items[0].find('code').text()).toBe('files:read');
    expect(items[0].find('.fa-perm__icon').attributes('aria-hidden')).toBe('true');
    expect(items[1].text()).not.toContain('Why:');
    expect(items[0].classes()).not.toEqual(items[1].classes());
  });
});

describe('ScreenshotGallery', () => {
  const shots = [1, 2, 3].map((n) => ({ src: `/v1/media/${String(n).repeat(64)}.png`, caption: `Shot ${n}`, alt: `Shot ${n}` }));
  const caption = (w: ReturnType<typeof mount>) => w.find('[data-testid="lightbox-caption"]').text();

  it('each picture is a button that opens the gallery on it, with the close button focused', async () => {
    const w = mount(ScreenshotGallery, { props: { shots, label: 'Screenshots of X' }, attachTo: document.body });
    expect(w.find('[data-testid="gallery"]').attributes('aria-label')).toBe('Screenshots of X');
    const second = w.find('[data-testid="shot-2"]');
    expect(second.element.tagName).toBe('BUTTON');
    expect(second.attributes('aria-label')).toBe('Open screenshot 2 of 3: Shot 2');
    await second.trigger('click');
    await settle();
    const dlg = w.find('[data-testid="lightbox"]');
    expect((dlg.element as HTMLDialogElement).open).toBe(true);
    expect(caption(w)).toBe('Screenshot 2 of 3: Shot 2');
    expect(document.activeElement).toBe(w.find('[data-testid="lightbox-close"]').element);
    expect(document.documentElement.classList.contains('fa-noscroll')).toBe(true);
  });

  it('moves by the buttons, the arrow keys, Home and End, and the thumbnails, round the ends', async () => {
    const w = mount(ScreenshotGallery, { props: { shots, label: 'X' }, attachTo: document.body });
    await w.find('[data-testid="shot-2"]').trigger('click');
    await settle();
    const dlg = w.find('[data-testid="lightbox"]');
    await w.find('[data-testid="lightbox-next"]').trigger('click');
    expect(caption(w)).toContain('Shot 3');
    await w.find('[data-testid="lightbox-next"]').trigger('click');
    expect(caption(w)).toContain('Shot 1');
    await dlg.trigger('keydown', { key: 'ArrowLeft' });
    expect(caption(w)).toContain('Shot 3');
    await dlg.trigger('keydown', { key: 'Home' });
    expect(caption(w)).toContain('Shot 1');
    await dlg.trigger('keydown', { key: 'End' });
    expect(caption(w)).toContain('Shot 3');
    expect(dlg.find('.fa-lightbox__img').attributes('src')).toBe(shots[2].src);
    await w.find('[data-testid="lightbox-thumb-1"]').trigger('click');
    await settle();
    expect(caption(w)).toContain('Shot 1');
    expect(w.find('[data-testid="lightbox-thumb-1"]').attributes('aria-current')).toBe('true');
    expect(w.find('[data-testid="lightbox-thumb-2"]').attributes('aria-current')).toBeUndefined();
  });

  it('a sideways swipe on a touch screen moves; a mouse drag and a short move do not', async () => {
    const w = mount(ScreenshotGallery, { props: { shots, label: 'X' }, attachTo: document.body });
    await w.find('[data-testid="shot-1"]').trigger('click');
    await settle();
    const stage = w.find('[data-testid="lightbox-stage"]');
    const swipe = async (type: string, x0: number, x1: number, y1 = 300) => {
      await stage.trigger('pointerdown', { pointerType: type, clientX: x0, clientY: 300 });
      await stage.trigger('pointerup', { pointerType: type, clientX: x1, clientY: y1 });
    };
    await swipe('touch', 300, 180);
    expect(caption(w)).toContain('Shot 2');
    await swipe('touch', 180, 300);
    expect(caption(w)).toContain('Shot 1');
    await swipe('mouse', 300, 100);
    expect(caption(w)).toContain('Shot 1');
    await swipe('touch', 300, 280);
    expect(caption(w)).toContain('Shot 1');
    await swipe('touch', 300, 200, 500);
    expect(caption(w)).toContain('Shot 1');
  });

  it('Close closes, the page scrolls again and focus goes back to the picture that opened it', async () => {
    const w = mount(ScreenshotGallery, { props: { shots, label: 'X' }, attachTo: document.body });
    const second = w.find('[data-testid="shot-2"]');
    await second.trigger('click');
    await settle();
    const dlg = w.find('[data-testid="lightbox"]');
    await w.find('[data-testid="lightbox-close"]').trigger('click');
    // happy-dom does not fire the dialog's close event itself.
    await dlg.trigger('close');
    expect((dlg.element as HTMLDialogElement).open).toBe(false);
    expect(document.documentElement.classList.contains('fa-noscroll')).toBe(false);
    expect(document.activeElement).toBe(second.element);
  });

  it('one picture has no previous, next or thumbnails', async () => {
    const w = mount(ScreenshotGallery, { props: { shots: shots.slice(0, 1), label: 'X' }, attachTo: document.body });
    await w.find('[data-testid="shot-1"]').trigger('click');
    await settle();
    expect(w.find('[data-testid="lightbox-next"]').exists()).toBe(false);
    expect(w.find('[data-testid="lightbox-thumb-1"]').exists()).toBe(false);
  });
});

describe('Stepper', () => {
  it('marks the current step for a screen reader and says which are done', () => {
    const w = mount(Stepper, { props: { steps: [{ title: 'One' }, { title: 'Two' }, { title: 'Three' }], current: 1, done: [0], label: 'How' } });
    const items = w.findAll('li');
    expect(w.find('ol').attributes('aria-label')).toBe('How');
    expect(items[1].attributes('aria-current')).toBe('step');
    expect(items[0].attributes('aria-current')).toBeUndefined();
    expect(items[0].text()).toContain('(Done)');
    expect(items[2].text()).not.toContain('(Done)');
  });
});

describe('the app page', () => {
  const Host = defineComponent({
    setup() {
      provideTableEnv({ locale: ref('en'), theme: ref('light') });
      return () => h(RouterView);
    },
  });
  const sha = (c: string) => c.repeat(64);
  const art = (c: string) => ({ url: `https://github.com/o/r/releases/download/v1/${c}`, sha256: sha(c), sig: c.repeat(128), size: 1000 });
  const APP = {
    name: 'filex-sign',
    kind: 'app',
    label: { en: 'filex Sign' },
    summary: { en: 'Sign PDFs in place.' },
    publisher: { id: 'brf-tech', name: 'BRF Tech', verified: true, official: true },
    categories: ['documents'],
    latest: '0.3.0',
    updated_at: '2026-10-01T10:00:00Z',
    repo: 'BRF-Tech/filex-sign',
    license: 'MIT',
    status: 'listed',
    revoked: null,
    screenshots: [{ url: `/v1/media/${sha('b')}.png`, sha256: sha('b'), caption: { en: 'Signing' } }],
    versions: [
      { version: '0.3.0', ref: 'v0.3.0', filex: '>=0.49.0', published_at: '2026-10-01', security: false, yanked: null },
      { version: '0.2.0', ref: 'v0.2.0', filex: '>=0.48.0', published_at: '2026-09-01', security: false, yanked: { reason: 'Broken', at: '2026-09-20T00:00:00Z' } },
    ],
  };
  const version = (v: string, yanked: unknown = null) => ({
    version: v,
    ref: 'v' + v,
    filex: '>=0.49.0',
    published_at: '2026-10-01',
    security: false,
    yanked,
    commit: 'c'.repeat(40),
    manifest: art('a'),
    wasm: null,
    ui: art('d'),
    permissions: [{ id: 'files:read', label: 'Reads the files you pick', reason: { en: 'To open the PDF.' } }],
    languages: [{ code: 'en', percent: 100 }],
    engines: [],
    install: { github_repo: 'BRF-Tech/filex-sign', ref: 'v' + v },
  });

  async function open(path: string, app: Record<string, unknown> = APP, oldYanked = true) {
    session.loaded = true;
    mockFetch({
      'GET /api/v1/apps/filex-sign': { body: app },
      'GET /api/v1/apps/filex-sign/versions/0.3.0': { body: version('0.3.0') },
      'GET /api/v1/apps/filex-sign/versions/0.2.0': { body: version('0.2.0', oldYanked ? { reason: 'Broken', at: '2026-09-20T00:00:00Z' } : null) },
      'GET /api/v1/categories': { body: { items: [{ id: 'documents', label: { en: 'Documents' } }] } },
    });
    const router = testRouter();
    await router.push(path);
    await router.isReady();
    const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
    await settle(5);
    return w;
  }

  it('leads with the install steps: the repository and the tag to paste, each with Copy', async () => {
    const w = await open('/apps/filex-sign');
    expect(w.find('[data-testid="app-title"]').text()).toBe('filex Sign');
    const install = w.find('[data-testid="install"]');
    expect(install.find('[data-testid="install-repo"]').text()).toBe('BRF-Tech/filex-sign');
    expect(install.find('[data-testid="install-ref"]').text()).toBe('v0.3.0');
    expect(install.findAll('[data-testid="copy"]')).toHaveLength(2);
    expect(w.find('[data-testid="install-jump"]').attributes('href')).toBe('#install');
  });

  it('shows the screenshots, the permissions in filex\'s words, and keeps the hashes folded away', async () => {
    const w = await open('/apps/filex-sign');
    const langs = w.find('[data-testid="version-section"]').text();
    expect(langs).toContain('English');
    expect(langs).not.toContain('%');
    expect(w.find('[data-testid="shot-1"]').exists()).toBe(true);
    expect(w.find('[data-testid="permissions"]').text()).toContain('Reads the files you pick');
    expect(w.text()).toContain('1 permission');
    const adv = w.find('[data-testid="integrity"]');
    expect(adv.element.tagName).toBe('DETAILS');
    expect(adv.attributes('open')).toBeUndefined();
    expect(adv.find('[data-table-id="store.app.artifacts"]').exists()).toBe(true);
    expect(w.find('[data-table-id="store.app.versions"]').exists()).toBe(true);
  });

  it('a yanked version says so up front and is not offered for installation', async () => {
    const w = await open('/apps/filex-sign/versions/0.2.0');
    expect(w.find('[data-testid="version-yanked"]').attributes('role')).toBe('alert');
    expect(w.find('[data-testid="install"]').exists()).toBe(false);
    expect(w.find('[data-testid="install-jump"]').exists()).toBe(false);
    expect(w.find('[data-testid="not-installable"]').text()).toContain('yanked');
  });

  it('an older version that is fine can be installed, and points to the latest', async () => {
    const app = { ...APP, versions: [APP.versions[0], { ...APP.versions[1], yanked: null }] };
    const w = await open('/apps/filex-sign/versions/0.2.0', app, false);
    expect(w.find('[data-testid="app-not-latest"]').text()).toContain('The latest is 0.3.0');
    expect(w.find('[data-testid="install-ref"]').text()).toBe('v0.2.0');
  });

  it('a revoked app carries the band and no install', async () => {
    const w = await open('/apps/filex-sign', { ...APP, status: 'revoked', revoked: { reason: 'Harmful', at: '2026-10-02T00:00:00Z' } });
    expect(w.find('[data-testid="app-revoked"]').text()).toContain('Harmful');
    expect(w.find('[data-testid="install"]').exists()).toBe(false);
    expect(w.find('[data-testid="not-installable"]').text()).toContain('revoked');
  });

  // The owner's report (2026-10-05): the publisher's logo was missing on the
  // app page although its own page showed it.
  it('the publisher card shows the publisher\'s mirrored logo, else its initial', async () => {
    const logo = `/v1/media/${sha('c')}.png`;
    const withLogo = await open('/apps/filex-sign', { ...APP, publisher: { ...APP.publisher, avatar_url: logo } });
    const card = withLogo.find('[data-testid="app-publisher"]');
    expect(card.find('img').attributes('src')).toBe(logo);
    expect(card.find('[data-testid="app-icon-placeholder"]').exists()).toBe(false);
    withLogo.unmount();
    const without = await open('/apps/filex-sign');
    const plain = without.find('[data-testid="app-publisher"]');
    expect(plain.find('img').exists()).toBe(false);
    expect(plain.find('[data-testid="app-icon-placeholder"]').text()).toBe('B');
  });
});
