// What an app converts, as its listing declares it: the words the store
// says it in (a search result, a card, an app page's table, the review
// page). Which kinds, which targets and which engines are the server's
// answer (backend/internal/filetypes); the page only draws them.
import { afterEach, describe, expect, it } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { session } from '@/lib/session';
import type { FileTypeRole, FileTypeUse, Me } from '@/api/client';
import { cardPhrases, conversionCells, convertPhrase, convertSummary, declarationLine, needsPhrase, usePhrases } from '@/lib/fileTypes';
import { ME_REVIEWER, mockFetch, settle, testRouter } from './helpers';

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

// filex-convert 0.2.0 on .docx, as the server answers it from its seed listing.
const DOCX_CONVERT: FileTypeRole[] = [
  { role: 'convert', to: ['pdf', 'epub', 'zip', 'tar', 'tar.gz', 'tar.zst', 'tar.xz', 'txt', 'md', 'html'] },
  { role: 'convert', to: ['png', 'jpg', 'gif', 'bmp', 'tiff', 'webp', 'ico', 'svg', 'qoi', 'ppm', 'tga'], needs_any: [['ghostscript'], ['imagemagick'], ['poppler']] },
  { role: 'convert', to: ['avif', 'psd', 'dds', 'exr'], needs_any: [['imagemagick']] },
  { role: 'convert', to: ['odt', 'rtf'], needs_any: [['office']] },
  { role: 'convert', to: ['ps', 'eps'], needs_any: [['ghostscript'], ['poppler']] },
];
const DOCX_USE: FileTypeUse = { ext: 'docx', roles: DOCX_CONVERT };
const EVERY: FileTypeUse = { any_file: true, roles: [{ role: 'action', id: 'convert', label: { en: 'Convert…' }, output: 'sibling' }] };

const PUB = { id: 'brf-tech', name: 'BRF Tech', verified: true, official: true };
const CONVERT = {
  name: 'filex-convert',
  kind: 'app',
  label: { en: 'Convert' },
  summary: { en: 'Convert anything to anything.' },
  publisher: PUB,
  categories: [],
  latest: '0.2.0',
  updated_at: '2026-10-01T10:00:00Z',
  matched_extensions: [DOCX_USE, EVERY],
};

afterEach(() => {
  document.body.replaceChildren();
  session.me = null;
});

describe('the words', () => {
  it('a conversion says what it needs, and only when it needs something', () => {
    expect(needsPhrase(undefined)).toBe('');
    expect(needsPhrase([['office']])).toBe('ONLYOFFICE');
    expect(needsPhrase([['ghostscript'], ['imagemagick'], ['poppler']])).toBe('Ghostscript, ImageMagick or Poppler');
    expect(needsPhrase([['office', 'poppler'], ['ghostscript', 'office']])).toBe('ONLYOFFICE and Poppler, or Ghostscript and ONLYOFFICE');
    expect(needsPhrase([['rsvg']])).toBe('librsvg');
    expect(needsPhrase([['an-engine-the-store-has-no-word-for']])).toBe('an-engine-the-store-has-no-word-for');
    expect(convertPhrase(DOCX_CONVERT[0], 'docx')).toBe('Converts .docx → .pdf, .epub, .zip, .tar, .tar.gz, .tar.zst, .tar.xz, .txt, .md, .html');
    expect(convertPhrase(DOCX_CONVERT[3], 'docx')).toBe('Converts .docx → .odt, .rtf (with ONLYOFFICE)');
    expect(convertPhrase(DOCX_CONVERT[1], 'docx', 2)).toBe('Converts .docx → .png, .jpg +9 more (with Ghostscript, ImageMagick or Poppler)');
  });

  it('a card sums the conversions up: what needs nothing first, then how many more need an engine', () => {
    expect(convertSummary(DOCX_USE)).toBe('Converts .docx → .pdf, .epub, .zip +7 more, and 19 more with an engine');
    // Every conversion needs something: the first condition leads, said with it.
    expect(convertSummary({ ext: 'doc', roles: [DOCX_CONVERT[3]] })).toBe('Converts .doc → .odt, .rtf (with ONLYOFFICE)');
    expect(convertSummary({ ext: 'x', roles: [{ role: 'open', id: 'v' }] })).toBe('');
    // A manifest's roles stay whole beside it; the full text keeps every condition.
    const both: FileTypeUse = { ext: 'docx', roles: [{ role: 'open', id: 'viewer', label: { en: 'E2E Hello' } }, DOCX_CONVERT[0], DOCX_CONVERT[3]] };
    expect(cardPhrases(both)).toEqual(['Opens', 'Converts .docx → .pdf, .epub, .zip +7 more, and 2 more with an engine']);
    expect(usePhrases(both)[2]).toBe('Converts .docx → .odt, .rtf (with ONLYOFFICE)');
  });

  it('a table row and a declared entry', () => {
    expect(conversionCells({ from: ['jpg', 'jpeg'], roles: [DOCX_CONVERT[0], DOCX_CONVERT[3], DOCX_CONVERT[4]] })).toEqual({
      from: '.jpg, .jpeg',
      to: '.pdf, .epub, .zip, .tar, .tar.gz, .tar.zst, .tar.xz, .txt, .md, .html',
      withEngine: '.odt, .rtf with ONLYOFFICE; .ps, .eps with Ghostscript or Poppler',
    });
    expect(declarationLine({ from: ['docx', 'odt'], to: ['pdf'] })).toBe('.docx, .odt → .pdf');
    expect(declarationLine({ from: ['doc'], to: ['pdf'], needs: ['office', 'poppler'] })).toBe('.doc → .pdf (with ONLYOFFICE and Poppler)');
  });
});

/** A use's row as "<kind> | <what>" and its hover text. */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function useRow(w: VueWrapper<any>, testid: string) {
  const text = w.find(`[data-testid="${testid}"] .fa-card__usetext`);
  return { line: `${w.find(`[data-testid="${testid}"] .fa-chip--kind`).text()} | ${text.text()}`, title: text.attributes('title') };
}

async function catalogAt(path: string, items: unknown[], named: number) {
  mockFetch({
    'GET /api/v1/apps': {
      body: { items, total: items.length, page: 1, per_page: 50, file_types: { extensions: ['docx'], named, filex: '0.51.0', builtin: [], engines: [], near: [] } },
    },
    'GET /api/v1/categories': { body: { items: [] } },
  });
  const router = testRouter();
  await router.push(path);
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  await settle(4);
  return w;
}

describe('a file-type search finds an app by what it converts', () => {
  it('.docx: the converter is among the apps that work with it, with a line that says what it converts .docx into', async () => {
    const w = await catalogAt('/?q=.docx', [CONVERT], 1);
    const apps = w.find('[data-testid="ft-apps"]');
    expect(apps.find('[data-testid="app-card-filex-convert"]').exists()).toBe(true);
    // The section does not say "from the manifest" alone: a conversion comes from the listing.
    expect(apps.find('.fa-section__sub').text()).toBe('What each app does with it, from its manifest and its listing, both reviewed with the code.');
    expect(w.find('[data-testid="ft-any"]').exists()).toBe(false);
    const row = useRow(w, 'app-use-filex-convert-docx');
    expect(row.line).toBe('.docx | Converts .docx → .pdf, .epub, .zip +7 more, and 19 more with an engine');
    expect(row.title).toContain('Converts .docx → .odt, .rtf (with ONLYOFFICE)');
    expect(row.title).toContain('Converts .docx → .ps, .eps (with Ghostscript or Poppler)');
    // Its action on every file is still said, on the same card.
    expect(useRow(w, 'app-use-filex-convert-any').line).toBe('Any file | Convert');
    // The count agrees with the groups: one app names the kind, nothing is apart.
    expect(w.find('[data-testid="catalog-count"]').text()).toBe('1 app works with .docx');
  });

  it('the list view says the same in its cell, all of it on hover', async () => {
    const w = await catalogAt('/?q=.docx&view=list', [CONVERT], 1);
    const table = w.find('[data-testid="catalog-table"]');
    expect(table.text()).toContain('.docx: Converts .docx → .pdf, .epub, .zip +7 more, and 19 more with an engine; Any file: Convert');
    expect(table.html()).toContain('.docx: Converts .docx → .pdf, .epub, .zip, .tar, .tar.gz, .tar.zst, .tar.xz, .txt, .md, .html; Converts .docx → .png');
  });
});

describe('an app page lists what it converts', () => {
  const app = {
    name: 'filex-convert',
    kind: 'app',
    label: { en: 'Convert' },
    publisher: PUB,
    categories: [],
    latest: '0.2.0',
    updated_at: '2026-10-01T10:00:00Z',
    repo: 'BRF-Tech/filex-convert',
    status: 'listed',
    revoked: null,
    screenshots: [],
    versions: [{ version: '0.2.0', ref: 'v0.2.0', filex: '>=0.50.0', published_at: '2026-10-01', security: false, yanked: null }],
  };
  const version = (conversions: unknown, fileTypes: unknown[] = [EVERY]) => ({
    version: '0.2.0',
    ref: 'v0.2.0',
    filex: '>=0.50.0',
    published_at: '2026-10-01',
    security: false,
    yanked: null,
    commit: 'c'.repeat(40),
    manifest: { url: 'https://raw.githubusercontent.com/m.json', sha256: 'a'.repeat(64), sig: 'a'.repeat(128) },
    permissions: [],
    languages: [],
    engines: [],
    install: { github_repo: 'BRF-Tech/filex-convert', ref: 'v0.2.0' },
    file_types: fileTypes,
    ...(conversions === undefined ? {} : { conversions }),
  });
  async function appAt(conversions: unknown, fileTypes?: unknown[]) {
    mockFetch({
      'GET /api/v1/apps/filex-convert': { body: app },
      'GET /api/v1/apps/filex-convert/versions/0.2.0': { body: version(conversions, fileTypes) },
      'GET /api/v1/categories': { body: { items: [] } },
    });
    const router = testRouter();
    await router.push('/apps/filex-convert');
    await router.isReady();
    const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
    await settle(6);
    return w;
  }

  it('a table under Works with: each kind a link to the search, what needs nothing, what needs an engine', async () => {
    const w = await appAt([
      { from: ['docx'], roles: DOCX_CONVERT },
      { from: ['jpg', 'jpeg'], roles: [{ role: 'convert', to: ['png'] }] },
    ]);
    const works = w.find('[data-testid="works-with"]');
    expect(works.find('[data-testid="works-with-any"]').text()).toContain('Convert');
    expect(works.find('[data-testid="works-with-none"]').exists()).toBe(false);
    expect(works.find('h3').text()).toBe('What it converts');
    const box = works.find('[data-testid="conversions"]');
    expect(box.element.tagName).toBe('DETAILS');
    expect(box.find('summary').text()).toContain('2 kinds of file it converts');
    const table = box.find('[data-testid="conversions-table"]');
    expect(w.find('[data-table-id="store.app.conversions"]').exists()).toBe(true);
    expect(table.text()).toContain('.pdf, .epub, .zip, .tar, .tar.gz, .tar.zst, .tar.xz, .txt, .md, .html');
    expect(table.text()).toContain('.odt, .rtf with ONLYOFFICE');
    const link = table.find('a[href="/?q=.jpeg"]');
    expect(link.text()).toBe('.jpeg');
    expect(link.attributes('aria-label')).toBe('Find every app that works with .jpeg');
  });

  it('a manifest that names no kind, a listing that converts: the table, and no "names no kind of file"', async () => {
    const w = await appAt([{ from: ['jpg', 'jpeg'], roles: [{ role: 'convert', to: ['png'] }] }], []);
    expect(w.find('[data-testid="works-with-none"]').exists()).toBe(false);
    expect(w.find('[data-testid="conversions"] summary').text()).toContain('1 kind of file it converts');
  });

  it('none declared, or an older server that does not say: no table', async () => {
    for (const conversions of [[], undefined]) {
      const w = await appAt(conversions);
      expect(w.find('[data-testid="conversions"]').exists()).toBe(false);
      w.unmount();
      document.body.replaceChildren();
    }
  });
});

describe('the review page shows what the listing declares', () => {
  it('how many entries and pairs, then every entry as declared', async () => {
    session.loaded = true;
    session.me = ME_REVIEWER as unknown as Me;
    const passed = (kind: string) => ({ kind, status: 'passed', started_at: '2026-10-03T10:00:00Z', ended_at: '2026-10-03T10:00:02Z' });
    const sub = {
      id: 101,
      publisher: PUB,
      app: 'filex-convert',
      version: '0.3.0',
      repo: 'BRF-Tech/filex-convert',
      ref: 'v0.3.0',
      status: 'needs_review',
      attempt: 1,
      created_at: '2026-10-03T10:00:00Z',
      updated_at: '2026-10-03T10:01:00Z',
      checks: ['repo_access', 'tag', 'manifest', 'consent'].map(passed),
      dry_runs: [],
      reviews: [],
      resolution: {
        name: 'convert',
        consent: {
          publisher: 'brf-tech',
          conversions: [
            { from: ['docx', 'odt'], to: ['pdf', 'txt'] },
            { from: ['doc'], to: ['pdf'], needs: ['office'] },
          ],
        },
      },
    };
    mockFetch({ 'GET /api/v1/submissions/101': { body: sub }, 'GET /api/v1/apps/filex-convert': { body: { versions: [] } } });
    const router = testRouter();
    await router.push('/submissions/101');
    await router.isReady();
    const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
    await settle();
    const dd = w.find('[data-testid="listing-conversions"]');
    expect(dd.text()).toContain('2 entries, 5 from-to pairs.');
    expect(dd.findAll('li').map((li) => li.text())).toEqual(['.docx, .odt → .pdf, .txt', '.doc → .pdf (with ONLYOFFICE)']);
  });
});
