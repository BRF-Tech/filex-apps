// The file-type search on the catalog and "Works with" on an app's page: the
// page draws what the server answered (which apps, what each does, what
// filex does by itself) and decides none of it.
import { afterEach, describe, expect, it } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { S } from '@/strings';
import { builtinPhrase, builtinTitle, engineNotes, extList, rolePhrase } from '@/lib/fileTypes';
import { mockFetch, settle, testRouter } from './helpers';

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

const PUB = { id: 'brf-tech', name: 'BRF Tech', verified: true, official: true };
const card = (name: string, label: string, matched: unknown[]) => ({
  name,
  kind: 'app',
  label: { en: label },
  summary: { en: `${label} does things.` },
  publisher: PUB,
  categories: [],
  latest: '1.0.0',
  updated_at: '2026-10-01T10:00:00Z',
  matched_extensions: matched,
});
const CONVERT = card('convert', 'Convert', [{ any_file: true, roles: [{ role: 'action', id: 'convert', label: { en: 'Convert…' }, output: 'sibling' }] }]);
const DRAWIO = card('drawio', 'draw.io', [
  { ext: 'drawio', roles: [{ role: 'open', id: 'editor', label: { en: 'draw.io' } }, { role: 'create', id: 'editor', label: { en: 'draw.io diagram' } }] },
]);
const SIGN = card('sign', 'Sign', [
  {
    ext: 'pdf',
    roles: [
      { role: 'inspect', id: 'status', label: { en: 'Signatures' } },
      { role: 'action', id: 'sign', label: { en: 'Sign…' }, output: 'sibling' },
      { role: 'action', id: 'verify', label: { en: 'Verify' }, output: 'none' },
    ],
  },
]);
const OFFICE = { id: 'office', label: 'ONLYOFFICE', kind: 'service', setting: 'FILEX_ONLYOFFICE_URL with FILEX_ONLYOFFICE_JWT, or Admin > External services' };
const GS = { id: 'ghostscript', label: 'Ghostscript or poppler', kind: 'tool', in_image: true };
const DOCX = { ext: 'docx', can: [{ do: 'preview', needs: 'office' }, { do: 'edit', needs: 'office' }, { do: 'thumbnail', needs: 'office' }, { do: 'create', needs: 'office' }] };
const PDF = { ext: 'pdf', can: [{ do: 'preview' }, { do: 'thumbnail', needs: 'ghostscript' }] };
const DRAWIO_BUILTIN = { ext: 'drawio', can: [{ do: 'preview', needs: 'drawio' }, { do: 'edit', needs: 'drawio' }, { do: 'create', needs: 'drawio' }] };
const DRAWIO_ENGINE = { id: 'drawio', label: 'draw.io', kind: 'service', setting: 'FILEX_DRAWIO_URL' };

const page = (exts: string[], items: unknown[], builtin: unknown[], engines: unknown[], near: unknown[] = []) => ({
  items,
  total: items.length,
  page: 1,
  per_page: 50,
  file_types: {
    extensions: exts,
    named: (items as { matched_extensions?: { any_file?: boolean }[] }[]).filter((a) => a.matched_extensions?.some((u) => !u.any_file)).length,
    filex: '0.51.0',
    builtin,
    engines,
    near,
  },
});

async function catalogAt(path: string, answer: unknown) {
  const { calls } = mockFetch({
    'GET /api/v1/apps': { body: answer },
    'GET /api/v1/categories': { body: { items: [] } },
  });
  const router = testRouter();
  await router.push(path);
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  await settle(4);
  return { w, router, calls };
}

afterEach(() => document.body.replaceChildren());

/** A use's row as "<kind> | <what>" (the chip and the words are two elements). */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function useRow(w: VueWrapper<any>, testid: string): string {
  const row = w.find(`[data-testid="${testid}"]`);
  if (!row.exists()) return '';
  return `${row.find('.fa-chip--kind').text()} | ${row.find('span:last-child').text()}`;
}

describe('a file-type search on the catalog', () => {
  it('asks the server with the words as typed and invites a file type in the search field', async () => {
    const { w, calls } = await catalogAt('/?q=*.DOCX', page(['docx'], [CONVERT], [DOCX], [OFFICE]));
    expect(calls.find((c) => c.path.startsWith('/api/v1/apps'))?.path).toContain('q=*.DOCX');
    expect(w.find('[data-testid="catalog-search"]').attributes('placeholder')).toBe('Search apps, or a file type like .docx');
  });

  it('.docx: filex opens it only with ONLYOFFICE, says so, and lists the app offered on every file apart', async () => {
    const { w } = await catalogAt('/?q=.docx', page(['docx'], [CONVERT], [DOCX], [OFFICE]));
    const panel = w.find('[data-testid="ft-builtin-docx"]');
    expect(panel.text()).toContain('.docx: filex opens it with ONLYOFFICE');
    expect(panel.text()).not.toContain('out of the box');
    expect(panel.text()).toContain('What filex 0.51.0 does with no app installed: preview, edit, thumbnails, new file with ONLYOFFICE');
    expect(w.find('[data-testid="ft-engine-note"]').text()).toBe(
      'ONLYOFFICE: only when a filex administrator connects it (FILEX_ONLYOFFICE_URL with FILEX_ONLYOFFICE_JWT, or Admin > External services). Without it filex offers the file as a download.',
    );
    expect(w.find('[data-testid="ft-noapps"]').text()).toContain('No app in the store works with .docx yet.');
    expect(w.find('[data-testid="ft-noapps"] a[href="/publish"]').text()).toBe('Publish an app');
    expect(w.find('[data-testid="ft-apps"]').exists()).toBe(false);
    const any = w.find('[data-testid="ft-any"]');
    expect(any.find('h2').text()).toBe('Offered on every file');
    expect(any.text()).toContain('What they can make of .docx is decided when they run.');
    expect(useRow(w, 'app-use-convert-any')).toBe('Any file | Convert');
    expect(w.find('[data-testid="ft-empty"]').exists()).toBe(false);
    expect(w.find('[data-testid="catalog-count"]').text()).toBe('No app works with .docx; 1 offered on every file');
  });

  it('.drawio: the app that opens it, with what it does on its card', async () => {
    const { w } = await catalogAt('/?q=.drawio', page(['drawio'], [DRAWIO, CONVERT], [DRAWIO_BUILTIN], [DRAWIO_ENGINE]));
    expect(w.find('[data-testid="ft-builtin-drawio"]').text()).toContain('.drawio: filex opens it with draw.io');
    const apps = w.find('[data-testid="ft-apps"]');
    expect(apps.find('h2').text()).toBe('Apps that work with .drawio');
    expect(useRow(w, 'app-use-drawio-drawio')).toBe('.drawio | Opens, New file');
    expect(apps.find('[data-testid="app-card-convert"]').exists()).toBe(false);
    expect(w.find('[data-testid="ft-any"] [data-testid="app-card-convert"]').exists()).toBe(true);
    expect(w.find('[data-testid="ft-noapps"]').exists()).toBe(false);
  });

  it('.xyz: says plainly that neither filex nor any app handles it, with the way to publish one', async () => {
    const { w } = await catalogAt('/?q=.xyz', page(['xyz'], [], [], [], [card('xyztool', 'XYZ notes', [])]));
    expect(w.find('[data-testid="ft-panel"]').exists()).toBe(false);
    const empty = w.find('[data-testid="ft-empty"]');
    expect(empty.text()).toContain('filex does not open .xyz by itself and no app in the store handles it yet');
    expect(empty.find('a[href="/publish"]').text()).toBe('Publish an app');
    expect(w.find('[data-testid="ft-near"] h2').text()).toBe('Other apps that mention xyz');
    expect(w.find('[data-testid="ft-near"] [data-testid="app-card-xyztool"]').exists()).toBe(true);
    expect(w.find('[data-testid="catalog-count"]').text()).toBe('No app works with .xyz');
  });

  it('several kinds: one panel each, and the kind filex does not open says so', async () => {
    const { w } = await catalogAt('/?q=pdf+xyz', page(['pdf', 'xyz'], [SIGN], [PDF], [GS]));
    expect(w.find('[data-testid="ft-builtin-pdf"]').text()).toContain('.pdf: filex opens it out of the box');
    expect(w.find('[data-testid="ft-builtin-pdf"]').text()).toContain('preview; thumbnails with Ghostscript or poppler');
    expect(w.find('[data-testid="ft-notbuiltin-xyz"]').text()).toBe('filex does not open .xyz by itself.');
    expect(w.find('[data-testid="ft-engine-note"]').text()).toBe("Ghostscript or poppler: a program on the filex server; filex's own Docker image has it.");
    expect(w.find('[data-testid="ft-apps"] h2').text()).toBe('Apps that work with .pdf or .xyz');
    expect(useRow(w, 'app-use-sign-pdf')).toBe('.pdf | Signatures panel, Sign, Verify');
  });

  it('works in the list view too: the table says what each app does with the kind', async () => {
    const { w } = await catalogAt('/?q=.pdf&view=list', page(['pdf'], [SIGN, CONVERT], [PDF], [GS]));
    expect(w.find('[data-testid="ft-builtin-pdf"]').exists()).toBe(true);
    const table = w.find('[data-testid="catalog-table"]');
    expect(table.text()).toContain('With .pdf');
    expect(table.text()).toContain('.pdf: Signatures panel, Sign, Verify');
    expect(table.text()).toContain('Any file: Convert');
  });

  it('an ordinary search draws no file-type block', async () => {
    const { w } = await catalogAt('/?q=sign', { items: [{ ...SIGN, matched_extensions: undefined }], total: 1, page: 1, per_page: 50 });
    expect(w.find('[data-testid="ft-panel"]').exists()).toBe(false);
    expect(w.find('[data-testid="app-uses-sign"]').exists()).toBe(false);
    expect(w.find('[data-testid="catalog-count"]').text()).toBe('1 app');
  });
});

describe('the words', () => {
  it('an action that writes another kind, offered only with an engine', () => {
    expect(rolePhrase({ role: 'action', id: 'redact', label: { en: 'Redact…' }, engine: 'office', output: 'sibling', output_ext: 'pdf' })).toBe(
      'Redact → .pdf (with ONLYOFFICE)',
    );
    expect(rolePhrase({ role: 'thumbnail' })).toBe('Thumbnails');
    expect(extList(['pdf', 'docx', 'odt'])).toBe('.pdf, .docx or .odt');
  });

  it('never says "out of the box" for what needs a service', () => {
    const search = page(['png', 'heic', 'mp4'], [], [], [{ id: 'ffmpeg', label: 'ffmpeg', kind: 'tool', in_image: true }]).file_types as never;
    expect(builtinTitle({ ext: 'png', can: [{ do: 'preview' }, { do: 'thumbnail' }] }, search)).toBe('.png: filex opens it out of the box');
    expect(builtinTitle({ ext: 'heic', can: [{ do: 'thumbnail', needs: 'imagemagick' }] }, search)).toBe('.heic: filex does some of it by itself');
    expect(builtinPhrase({ ext: 'mp4', can: [{ do: 'play' }, { do: 'thumbnail', needs: 'ffmpeg' }] }, search)).toBe('play; thumbnails with ffmpeg');
    expect(engineNotes({ ...(search as object), engines: [{ id: 'sevenzip-rar', label: '7-Zip with RAR support', kind: 'tool', setting: 'FILEX_ARCHIVE_7Z_BIN' }] } as never)).toEqual([
      '7-Zip with RAR support: a program on the filex server (FILEX_ARCHIVE_7Z_BIN).',
    ]);
    expect(S.fileTypes.builtinTitle).toContain('out of the box');
  });
});

describe('Works with, on an app page', () => {
  const app = {
    name: 'drawio',
    kind: 'app',
    label: { en: 'draw.io' },
    publisher: PUB,
    categories: [],
    latest: '31.5.3',
    updated_at: '2026-10-01T10:00:00Z',
    repo: 'BRF-Tech/filex-drawio',
    status: 'listed',
    revoked: null,
    screenshots: [],
    versions: [{ version: '31.5.3', ref: 'v31.5.3', filex: '>=0.48.0', published_at: '2026-10-01', security: false, yanked: null }],
  };
  const version = (fileTypes: unknown[]) => ({
    version: '31.5.3',
    ref: 'v31.5.3',
    filex: '>=0.48.0',
    published_at: '2026-10-01',
    security: false,
    yanked: null,
    commit: 'c'.repeat(40),
    manifest: { url: 'https://raw.githubusercontent.com/m.json', sha256: 'a'.repeat(64), sig: 'a'.repeat(128) },
    permissions: [],
    languages: [],
    engines: [],
    install: { github_repo: 'BRF-Tech/filex-drawio', ref: 'v31.5.3' },
    file_types: fileTypes,
  });

  async function appAt(fileTypes: unknown[]) {
    mockFetch({
      'GET /api/v1/apps/drawio': { body: app },
      'GET /api/v1/apps/drawio/versions/31.5.3': { body: version(fileTypes) },
      'GET /api/v1/categories': { body: { items: [] } },
    });
    const router = testRouter();
    await router.push('/apps/drawio');
    await router.isReady();
    const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
    await settle(6);
    return w;
  }

  it('lists each kind with what the version does, each a link to the search for it', async () => {
    const w = await appAt(DRAWIO.matched_extensions.concat([{ ext: 'dio', roles: [{ role: 'open', id: 'editor', label: { en: 'draw.io' } }] }]));
    const section = w.find('[data-testid="works-with"]');
    expect(section.find('h2').text()).toBe('Works with');
    expect(useRow(w, 'works-with-drawio')).toBe('.drawio | Opens, New file');
    const link = section.find('[data-testid="works-with-dio"] a');
    expect(link.attributes('href')).toBe('/?q=.dio');
    expect(link.attributes('aria-label')).toBe('Find every app that works with .dio');
    expect(w.text()).toContain('Runs on');
  });

  it('a version that names no kind says so', async () => {
    const w = await appAt([]);
    expect(w.find('[data-testid="works-with-none"]').text()).toBe('This version names no kind of file.');
  });
});
