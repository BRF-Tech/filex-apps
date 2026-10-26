// Every page, light and dark, at 1280 and 375 px, in three engines: no
// sideways scroll of the page, filex's palette actually applied, and (in
// Chromium) axe's WCAG 2.1 A/AA rules. Chromium also writes the screenshots
// the owner reviews (out/shots, not committed).
import { expect, test, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

type Persona = 'anon' | 'publisher' | 'reviewer' | 'admin';
const OUT = fileURLToPath(new URL('./out/', import.meta.url));
mkdirSync(OUT + 'shots', { recursive: true });

// `open`: a disclosure to open before measuring, `opened` what shows then.
const PAGES: { name: string; path: string; who: Persona; ready: string; open?: string; opened?: string }[] = [
  { name: 'catalog', path: '/', who: 'anon', ready: '[data-testid="app-link-filex-sign"]' },
  { name: 'catalog-filtered', path: '/?kind=app&filex=0.50.0&verified=1', who: 'anon', ready: '[data-testid="app-link-filex-convert"]' },
  { name: 'catalog-category', path: '/?category=security', who: 'anon', ready: '[data-testid="app-link-pdf-redact"]' },
  { name: 'catalog-list', path: '/?view=list', who: 'anon', ready: '[data-table-id="store.catalog"] [data-testid="app-link-filex-sign"]' },
  { name: 'catalog-empty', path: '/?q=no-such-app-anywhere', who: 'anon', ready: '[data-testid="empty-state"]' },
  // The file-type search: filex's own support, the apps that name the kind,
  // the ones offered on every file, and the honest empty answer.
  { name: 'catalog-type-docx', path: '/?q=.docx', who: 'anon', ready: '[data-testid="ft-builtin-docx"]' },
  { name: 'catalog-type-drawio', path: '/?q=.drawio', who: 'anon', ready: '[data-testid="app-use-filex-drawio-drawio"]' },
  { name: 'catalog-type-pdf-docx', path: '/?q=pdf+docx', who: 'anon', ready: '[data-testid="app-use-filex-sign-pdf"]' },
  { name: 'catalog-type-list', path: '/?q=.docx&view=list', who: 'anon', ready: '[data-table-id="store.catalog"] [data-testid="app-link-pdf-redact"]' },
  { name: 'catalog-type-unknown', path: '/?q=.xyz', who: 'anon', ready: '[data-testid="ft-empty"]' },
  { name: 'app-works-with', path: '/apps/filex-drawio', who: 'anon', ready: '[data-testid="works-with-drawio"]' },
  // A kind only filex-convert's listing names, and its conversion table open.
  { name: 'catalog-type-converted', path: '/?q=.mp4', who: 'anon', ready: '[data-testid="app-use-filex-convert-mp4"]' },
  {
    name: 'app-conversions',
    path: '/apps/filex-convert',
    who: 'anon',
    ready: '[data-testid="conversions"]',
    open: '[data-testid="conversions"] > summary',
    opened: '[data-table-id="store.app.conversions"] [role="row"] >> nth=1',
  },
  { name: 'app', path: '/apps/filex-sign', who: 'anon', ready: '[data-testid="install"]' },
  { name: 'app-yanked-version', path: '/apps/filex-sign/versions/0.2.0', who: 'anon', ready: '[data-testid="version-yanked"]' },
  { name: 'app-revoked', path: '/apps/quick-share', who: 'anon', ready: '[data-testid="app-revoked"]' },
  { name: 'app-language-pack', path: '/apps/filex-lang-es', who: 'anon', ready: '[data-testid="install"]' },
  { name: 'app-storage-plugin', path: '/apps/webdavfs', who: 'anon', ready: '[data-testid="storage-conformance"]' },
  { name: 'catalog-storage', path: '/?kind=storage', who: 'anon', ready: '[data-testid="app-card-webdavfs"]' },
  { name: 'app-as-admin', path: '/apps/filex-sign', who: 'admin', ready: '[data-testid="app-admin"]' },
  { name: 'app-paid', path: '/apps/markdown-preview', who: 'anon', ready: '[data-testid="app-paid"]' },
  { name: 'manage-app', path: '/publish/apps/markdown-preview', who: 'publisher', ready: '[data-table-id="store.manage.licences"] [role="row"] >> nth=1' },
  { name: 'my-instances', path: '/me/instances', who: 'publisher', ready: '[data-table-id="store.me.instances"] [role="row"] >> nth=1' },
  { name: 'my-notifications', path: '/me/notifications', who: 'publisher', ready: '[data-testid="webhook-add"]' },
  { name: 'my-notifications-admin', path: '/me/notifications', who: 'admin', ready: '[data-table-id="store.me.webhooks"] [role="row"] >> nth=1' },
  { name: 'publishers', path: '/publishers', who: 'anon', ready: '[data-testid="publisher-card-northwind"]' },
  { name: 'publisher', path: '/publishers/brf-tech', who: 'anon', ready: '[data-testid="publisher-apps"] [data-testid="app-card-filex-sign"]' },
  { name: 'publisher-community', path: '/publishers/northwind', who: 'anon', ready: '[data-testid="publisher-apps"] [data-testid="app-card-photo-exif"]' },
  { name: 'sign-in', path: '/sign-in', who: 'anon', ready: '[data-testid="sign-in-github"]' },
  { name: 'publish', path: '/publish', who: 'publisher', ready: '[data-testid="my-submissions"] a' },
  { name: 'publish-signed-out', path: '/publish', who: 'anon', ready: '[data-testid="access-gate"]' },
  { name: 'new-publisher', path: '/publish/new-publisher', who: 'publisher', ready: '[data-testid="publisher-form"]' },
  { name: 'submit', path: '/submit', who: 'publisher', ready: '[data-testid="consent-helper"]' },
  { name: 'submission-for-review', path: '/submissions/101', who: 'reviewer', ready: '[data-testid="checks-table"] [role="row"] >> nth=1' },
  { name: 'submission-failed', path: '/submissions/102', who: 'publisher', ready: '[data-testid="submission-failure"]' },
  { name: 'review-queue', path: '/review', who: 'reviewer', ready: '[data-testid="queue-101"]' },
  { name: 'admin-publishers', path: '/admin/publishers', who: 'admin', ready: '[data-testid="pub-actions-acme-tools"]' },
  { name: 'admin-apps', path: '/admin/apps', who: 'admin', ready: '[data-testid="app-actions-filex-sign"]' },
  { name: 'admin-index', path: '/admin/index', who: 'admin', ready: '[data-testid="index-serial"]' },
  { name: 'admin-validators', path: '/admin/validators', who: 'admin', ready: '[data-testid="admin-plugin-validators"] [role="row"] >> nth=1' },
  { name: 'admin-audit', path: '/admin/audit', who: 'admin', ready: '[data-testid="chain-ok"]' },
  { name: 'not-found', path: '/no/such/page', who: 'anon', ready: '[data-testid="not-found"]' },
];

const BG = { light: 'rgb(255, 255, 255)', dark: 'rgb(21, 23, 28)' };
const axeFindings: Record<string, unknown>[] = [];
const roNotices: string[] = [];

async function signIn(page: Page, who: Persona, then: string) {
  if (who === 'anon') return page.goto(then);
  return page.goto(`/auth/github/login?as=${who}&return_to=${encodeURIComponent(then)}`);
}

test.beforeAll(async ({ request }) => {
  await request.post('http://127.0.0.1:8080/__mock/reset');
});

for (const theme of ['light', 'dark'] as const) {
  for (const width of [1280, 375]) {
    test.describe(`${theme} ${width}px`, () => {
      test.use({ viewport: { width, height: width > 600 ? 900 : 812 }, colorScheme: theme });

      for (const pg of PAGES) {
        test(pg.name, async ({ page, browserName }) => {
          await page.addInitScript((t) => {
            try {
              localStorage.setItem('fapps.theme', t);
            } catch {
              /* private mode */
            }
          }, theme);
          const errors: string[] = [];
          page.on('pageerror', (e) => {
            // WebKit reports filex DataTable's ResizeObserver settling as an
            // error event; it is the observer's own notice, not a script
            // failure. Recorded for the report (filex side), not failed on.
            if (/ResizeObserver loop/.test(e.message)) roNotices.push(`${browserName} ${pg.name} ${theme} ${width}`);
            else errors.push(e.message);
          });
          await signIn(page, pg.who, pg.path);
          await expect(page.locator('h1')).toHaveCount(1);
          await page.locator(pg.ready).first().waitFor({ state: 'visible' });
          if (pg.open) {
            await page.locator(pg.open).click();
            await page.locator(pg.opened ?? pg.ready).first().waitFor({ state: 'visible' });
          }
          await page.waitForLoadState('networkidle');

          const m = await page.evaluate(() => {
            const w = document.documentElement.clientWidth;
            // What sticks out: elements past the right edge whose ancestors
            // do not clip them (a scrolling table clips its own cells).
            const name = (el: Element | null) => (el ? `${el.tagName.toLowerCase()}.${[...el.classList].join('.')}` : '-');
            // An absolutely positioned box is clipped only by ancestors at or
            // above its containing block (the nearest positioned ancestor):
            // a visually hidden span in a scrolled table cell escapes it.
            const clipper = (el: Element) => {
              let abs = ['absolute', 'fixed'].includes(getComputedStyle(el).position);
              for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
                const cs = getComputedStyle(p);
                if (abs && cs.position === 'static') continue;
                if (cs.overflowX !== 'visible') return p;
                if (abs) abs = ['absolute', 'fixed'].includes(cs.position);
              }
              return null;
            };
            const culprits = [...document.querySelectorAll('body *')]
              .filter((el) => el.getBoundingClientRect().right > w + 1 && !clipper(el))
              .slice(0, 6)
              .map((el) => `${name(el)} right=${Math.round(el.getBoundingClientRect().right)} parent=${name(el.parentElement)}`);
            // Content that spills out of a box that does not clip it (a long
            // unbreakable word) widens the page without any box doing so.
            for (const el of document.querySelectorAll('body *')) {
              if (culprits.length >= 10) break;
              const r = el.getBoundingClientRect();
              if (!clipper(el) && getComputedStyle(el).overflowX === 'visible' && r.left + el.scrollWidth > w + 1 && el.scrollWidth > el.clientWidth + 1) {
                culprits.push(`spill ${name(el)} scroll=${el.scrollWidth} client=${el.clientWidth}`);
              }
            }
            // Badges in a table cell stay on the row's one line: a group
            // that wraps stacks its badges and makes the row taller than
            // its neighbours (the catalog's Official / Verified, 2026-10-03).
            const spilledBadges = [...document.querySelectorAll('.fe-list__row .fa-badges')].flatMap((g) => {
              const tops = [...g.querySelectorAll('.fa-badge')]
                .map((b) => b.getBoundingClientRect())
                .filter((r) => r.height > 0)
                .map((r) => Math.round(r.top));
              return new Set(tops).size > 1 ? [g.textContent?.trim().replace(/\s+/g, ' ') ?? ''] : [];
            });
            return {
              spilledBadges,
              over: document.documentElement.scrollWidth - w,
              culprits,
              dark: document.documentElement.classList.contains('dark'),
              bg: getComputedStyle(document.body).backgroundColor,
              title: document.title,
            };
          });
          expect(m.over, `the page scrolls sideways by ${m.over}px: ${m.culprits.join(' | ')}`).toBeLessThanOrEqual(0);
          expect(m.spilledBadges, 'badge groups wrapped onto a second line in a table row').toEqual([]);
          expect(m.dark).toBe(theme === 'dark');
          expect(m.bg).toBe(BG[theme]);
          expect(m.title).toContain('Example Apps');
          expect(errors).toEqual([]);

          if (browserName === 'chromium') {
            const r = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
            // Findings inside filex's DataTable belong to filex (the fix goes
            // there, not into a copy here): recorded for the report, not
            // failed on. Everything the store draws itself must be clean.
            const ours: string[] = [];
            for (const v of r.violations) {
              for (const n of v.nodes) {
                const where = n.target.join(' ');
                const inTable = await page.locator(String(n.target[0])).evaluate((el) => !!el.closest('.fe-table, .fe-colmenu__backdrop, .fe-ctx-backdrop')).catch(() => false);
                const row = { page: pg.name, theme, width, rule: v.id, impact: v.impact, where, summary: n.failureSummary?.split('\n')[1]?.trim() };
                if (inTable) axeFindings.push({ ...row, owner: 'filex DataTable' });
                else ours.push(`${v.id} (${v.impact}) at ${where}: ${row.summary}`);
              }
            }
            expect(ours, 'axe violations in the store\'s own markup').toEqual([]);
            await page.screenshot({ path: `${OUT}shots/${pg.name}-${theme}-${width}.png`, fullPage: true });
          }
        });
      }
    });
  }
}

// For the reviewer of the screenshots: one sheet per page with its four
// variants side by side (light and dark, 1280 and 375 px).
test.afterAll(async ({ browser, browserName }) => {
  if (roNotices.length) writeFileSync(`${OUT}resize-observer-${browserName}.txt`, roNotices.join('\n') + '\n');
  if (browserName !== 'chromium') return;
  writeFileSync(OUT + 'axe-datatable.json', JSON.stringify(axeFindings, null, 2));
  mkdirSync(OUT + 'sheets', { recursive: true });
  const page = await browser.newPage({ viewport: { width: 1800, height: 1000 } });
  for (const pg of PAGES) {
    const cells: string[] = [];
    for (const [theme, width] of [['light', 1280], ['dark', 1280], ['light', 375], ['dark', 375]] as const) {
      const file = `${OUT}shots/${pg.name}-${theme}-${width}.png`;
      if (!existsSync(file)) continue;
      const src = 'data:image/png;base64,' + readFileSync(file).toString('base64');
      cells.push(`<figure><figcaption>${pg.name} · ${theme} · ${width}px</figcaption><img src="${src}" style="width:${width > 600 ? 560 : 220}px"></figure>`);
    }
    await page.setContent(`<body style="margin:12px;font:13px sans-serif;display:flex;gap:12px;align-items:flex-start;background:#fff;color:#000">${cells.join('')}</body>`);
    await page.screenshot({ path: `${OUT}sheets/${pg.name}.png`, fullPage: true });
  }
  await page.close();
});
