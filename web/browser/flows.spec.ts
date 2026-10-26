// What a person does on the pages, in three engines, against the mock:
// the table is filex's real DataTable (resize, sort), the keyboard reaches
// everything, a submission is followed to review, a reviewer decides, an
// administrator acts on a publisher.
import { expect, test, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

async function as(page: Page, who: 'publisher' | 'reviewer' | 'admin', path: string) {
  await page.goto(`/auth/github/login?as=${who}&return_to=${encodeURIComponent(path)}`);
}

test.beforeEach(async ({ request }) => {
  await request.post('http://127.0.0.1:8080/__mock/reset');
});

test('the catalog\'s list view is filex\'s table: it resizes and sorts like the explorer', async ({ page }) => {
  await page.goto('/');
  await page.locator('[data-testid="app-link-filex-sign"]').waitFor();
  await page.locator('[data-testid="catalog-view-list"]').click();
  await expect(page).toHaveURL(/view=list/);
  await page.locator('[data-table-id="store.catalog"] [data-testid="app-link-filex-sign"]').waitFor();
  const head = page.locator('[role="columnheader"][data-col="summary"]');
  const before = (await head.boundingBox())!.width;
  const handle = head.locator('.fe-list__resize');
  const hb = (await handle.boundingBox())!;
  await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2);
  await page.mouse.down();
  await page.mouse.move(hb.x + hb.width / 2 + 90, hb.y + hb.height / 2, { steps: 6 });
  await page.mouse.up();
  const after = (await head.boundingBox())!.width;
  expect(after - before).toBeGreaterThan(60);

  await handle.focus();
  await page.keyboard.press('ArrowLeft');
  await page.keyboard.press('ArrowLeft');
  expect((await head.boundingBox())!.width).toBeLessThan(after);

  await page.locator('[role="columnheader"][data-col="updated"] button.fe-list__sort').click();
  await expect(page).toHaveURL(/sort=updated/);
  await expect(page.locator('[role="columnheader"][data-col="updated"]')).toHaveAttribute('aria-sort', 'descending');
  const first = await page.locator('[role="row"] [role="gridcell"] a').first().textContent();
  expect(first?.trim()).toMatch(/Deutsch|Español/);
});

test('search and filters live in the address', async ({ page }) => {
  await page.goto('/');
  await page.locator('[data-testid="catalog-search"]').fill('markdown');
  await expect(page).toHaveURL(/q=markdown/);
  await expect(page.locator('[data-testid="app-link-markdown-preview"]')).toBeVisible();
  await expect(page.locator('[data-testid="app-link-filex-sign"]')).toHaveCount(0);
  await page.locator('[data-testid="catalog-clear"]').click();
  await expect(page.locator('[data-testid="app-link-filex-sign"]')).toBeVisible();
  await page.locator('[data-testid="catalog-kind"]').click();
  await page.locator('[data-testid="catalog-kind-option-language_pack"]').click();
  await expect(page).toHaveURL(/kind=language_pack/);
  await expect(page.locator('[data-testid="app-link-filex-lang-de"]')).toBeVisible();
});

test('a file type typed in the search: what filex does with it, the apps for it, and back to a word', async ({ page }) => {
  await page.goto('/');
  await page.locator('[data-testid="app-link-filex-sign"]').waitFor();
  await page.locator('[data-testid="catalog-search"]').fill('.docx');
  await expect(page).toHaveURL(/q=\.docx/);
  await expect(page.locator('[data-testid="ft-builtin-docx"]')).toContainText('.docx: filex opens it with ONLYOFFICE');
  await expect(page.locator('[data-testid="ft-engine-note"]')).toContainText('FILEX_ONLYOFFICE_URL');
  await expect(page.locator('[data-testid="ft-apps"] [data-testid="app-use-pdf-redact-docx"]')).toContainText('Redact → .pdf (with ONLYOFFICE)');
  // filex-convert's listing says it converts .docx: it is among the apps for
  // it, its line says into what, and its action on every file stays on its card.
  await expect(page.locator('[data-testid="ft-apps"] [data-testid="app-use-filex-convert-docx"]')).toHaveText(
    '.docxConverts .docx → .pdf, .epub, .zip +7 more, and 19 more with an engine',
  );
  await expect(page.locator('[data-testid="ft-apps"] [data-testid="app-use-filex-convert-any"]')).toContainText('Convert');
  await expect(page.locator('[data-testid="ft-any"]')).toHaveCount(0);
  await expect(page.locator('[data-testid="catalog-count"]')).toHaveText('2 apps work with .docx');
  // the card is still one link to the app
  await page.locator('[data-testid="app-link-pdf-redact"]').click();
  await expect(page).toHaveURL(/\/apps\/pdf-redact$/);
  await page.goBack();
  await page.locator('[data-testid="catalog-search"]').fill('.xyz');
  await expect(page.locator('[data-testid="ft-empty"]')).toContainText('filex does not open .xyz by itself');
  await page.locator('[data-testid="catalog-search"]').fill('markdown');
  await expect(page.locator('[data-testid="ft-panel"]')).toHaveCount(0);
  await expect(page.locator('[data-testid="app-link-markdown-preview"]')).toBeVisible();
});

test('an app page says what it works with, and a kind leads to the search for it', async ({ page }) => {
  await page.goto('/apps/filex-drawio');
  const works = page.locator('[data-testid="works-with"]');
  await expect(works.locator('[data-testid="works-with-drawio"]')).toContainText('Opens, New file');
  await works.getByRole('link', { name: 'Find every app that works with .drawio' }).click();
  await expect(page).toHaveURL(/\?q=\.drawio$/);
  await expect(page.locator('[data-testid="ft-apps"] [data-testid="app-card-filex-drawio"]')).toBeVisible();
});

test('an app page lists what it converts: a table under Works with, each kind leading to the search for it', async ({ page }) => {
  await page.goto('/apps/filex-convert');
  const box = page.locator('[data-testid="conversions"]');
  await expect(box.locator('summary')).toContainText('71 kinds of file it converts');
  await box.locator('summary').click();
  const table = page.locator('[data-table-id="store.app.conversions"]');
  const docx = table.getByRole('row').filter({ has: page.getByRole('link', { name: 'Find every app that works with .docx', exact: true }) });
  await expect(docx).toContainText('.pdf, .epub, .zip, .tar');
  await expect(docx).toContainText('.odt, .rtf with ONLYOFFICE');
  await table.getByRole('link', { name: 'Find every app that works with .jpeg' }).click();
  await expect(page).toHaveURL(/\?q=\.jpeg$/);
});

test('an app page shows its publisher\'s logo, the one its publisher page shows, else the initial', async ({ page }) => {
  await page.goto('/apps/filex-sign');
  const logo = page.locator('[data-testid="app-publisher"] img');
  await expect(logo).toHaveAttribute('src', /^\/v1\/media\/[0-9a-f]{64}\.png$/);
  await expect.poll(() => logo.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
  const src = await logo.getAttribute('src');
  await page.goto('/publishers/brf-tech');
  await expect(page.locator(`img[src="${src}"]`).first()).toBeVisible();
  await page.goto('/apps/markdown-preview');
  await expect(page.locator('[data-testid="app-publisher"] [data-testid="app-icon-placeholder"]')).toHaveText('J');
});

test('a category chip narrows the shop window, and the whole card opens the app', async ({ page }) => {
  await page.goto('/');
  await page.locator('[data-testid="catalog-cat-security"]').click();
  await expect(page).toHaveURL(/category=security/);
  await expect(page.locator('[data-testid="catalog-cat-security"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('[data-testid="app-card-pdf-redact"]')).toBeVisible();
  await expect(page.locator('[data-testid="app-card-filextext"]')).toHaveCount(0);
  // A click on the card's summary, not on its title, still opens the app
  // (the title link covers the card). A real mouse click at that spot: the
  // locator's own click would refuse, seeing the link above the summary.
  const summary = page.locator('[data-testid="app-card-pdf-redact"] .fa-card__summary');
  await summary.scrollIntoViewIfNeeded();
  const box = (await summary.boundingBox())!;
  await page.mouse.click(box.x + 12, box.y + box.height / 2);
  await expect(page).toHaveURL(/\/apps\/pdf-redact$/);
  await expect(page.locator('h1')).toHaveText('PDF Redact');
});

for (const [width, theme] of [[1280, 'light'], [375, 'dark']] as const) {
  test(`the screenshot gallery at ${width}px, ${theme}: opens full screen, moves by buttons, keys, thumbnails and swipe, keeps focus, Escape gives it back`, async ({ page, browserName }) => {
    await page.addInitScript((t) => {
      try {
        localStorage.setItem('fapps.theme', t);
      } catch {
        /* private mode */
      }
    }, theme);
    await page.setViewportSize({ width, height: width > 600 ? 900 : 812 });
    await page.goto('/apps/filex-sign');
    const second = page.locator('[data-testid="shot-2"]');
    await second.scrollIntoViewIfNeeded();
    await second.click();
    const box = page.locator('dialog[open][data-testid="lightbox"]');
    const caption = box.locator('[data-testid="lightbox-caption"]');
    await expect(box).toBeVisible();
    // The whole window: the gallery is as large as the viewport.
    const r = (await box.boundingBox())!;
    expect(Math.round(r.width)).toBeGreaterThanOrEqual(width - 1);
    await expect(page.locator('[data-testid="lightbox-close"]')).toBeFocused();
    await expect(caption).toContainText('Sign from the file explorer');
    // The picture fits inside the window.
    const img = (await box.locator('.fa-lightbox__img').boundingBox())!;
    expect(img.x).toBeGreaterThanOrEqual(0);
    expect(img.x + img.width).toBeLessThanOrEqual(width + 1);
    await page.waitForTimeout(300);
    await page.screenshot({ path: `browser/out/gallery-${browserName}-${theme}-${width}.png` });

    await page.keyboard.press('ArrowRight');
    await expect(caption).toContainText('Ask others to sign, in order');
    await page.keyboard.press('ArrowRight');
    await expect(caption).toContainText('Place a signature on the page');
    await box.locator('[data-testid="lightbox-prev"]').click();
    await expect(caption).toContainText('Ask others to sign, in order');
    await box.locator('[data-testid="lightbox-thumb-1"]').click();
    await expect(caption).toContainText('Place a signature on the page');
    await expect(box.locator('[data-testid="lightbox-thumb-1"]')).toHaveAttribute('aria-current', 'true');

    // A swipe to the left on a touch screen goes to the next picture.
    const stage = box.locator('[data-testid="lightbox-stage"]');
    const s = (await stage.boundingBox())!;
    const y = s.y + s.height / 2;
    await stage.dispatchEvent('pointerdown', { pointerType: 'touch', clientX: s.x + s.width * 0.7, clientY: y, isPrimary: true });
    await stage.dispatchEvent('pointerup', { pointerType: 'touch', clientX: s.x + s.width * 0.3, clientY: y, isPrimary: true });
    await expect(caption).toContainText('Sign from the file explorer');

    // Tab never leaves the gallery: the page behind is inert.
    for (let i = 0; i < 8; i++) {
      await page.keyboard.press('Tab');
      const inside = await page.evaluate(() => {
        const a = document.activeElement;
        return !a || a === document.body || !!a.closest('dialog[open]');
      });
      expect(inside).toBe(true);
    }
    await page.keyboard.press('Escape');
    await expect(page.locator('dialog[open]')).toHaveCount(0);
    await expect(second).toBeFocused();
    // The dialog's close event is queued after Escape: wait for the page to scroll again.
    await expect.poll(() => page.evaluate(() => document.documentElement.classList.contains('fa-noscroll'))).toBe(false);
  });
}

test('the publishers page: search narrows the cards, a card opens the publisher', async ({ page }) => {
  await page.goto('/');
  await page.locator('[data-testid="nav-publishers"]').click();
  await expect(page).toHaveURL(/\/publishers$/);
  await expect(page.locator('h1')).toHaveText('Who makes filex apps');
  await expect(page.locator('[data-testid="publisher-card-brf-tech"]')).toContainText('7 apps');
  await page.locator('[data-testid="publishers-search"]').fill('northwind');
  await expect(page).toHaveURL(/q=northwind/);
  await expect(page.locator('[data-testid="publisher-card-brf-tech"]')).toHaveCount(0);
  await page.locator('[data-testid="publisher-link-northwind"]').click();
  await expect(page).toHaveURL(/\/publishers\/northwind$/);
  await expect(page.locator('h1')).toHaveText('Northwind Labs');
  await expect(page.locator('[data-testid="nav-publishers"]')).toHaveClass(/is-active/);
});

test('keyboard: skip link, then an app, and focus lands on its heading', async ({ page, browserName }) => {
  await page.goto('/');
  await page.locator('[data-testid="app-link-filex-sign"]').waitFor();
  // WebKit only tabs to links with "Tab to highlight each item" on (Option+Tab);
  // the skip link is still the first focusable element in the order.
  await page.keyboard.press(browserName === 'webkit' ? 'Alt+Tab' : 'Tab');
  await expect(page.locator(':focus')).toHaveText('Skip to content');
  await page.locator('[data-testid="app-link-filex-sign"]').focus();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/\/apps\/filex-sign$/);
  await expect(page.locator('h1')).toHaveText('filex Sign');
  await expect(page.locator('h1')).toBeFocused();
});

test('a publisher submits a tag and follows it to review', async ({ page }) => {
  await as(page, 'publisher', '/submit');
  await page.locator('[data-testid="sb-repo"]').fill('https://github.com/jdoe/filex-markdown-preview');
  await page.locator('[data-testid="sb-ref"]').fill('v1.2.0');
  await page.locator('[data-testid="cs-summary"]').fill('Preview Markdown.');
  await expect(page.locator('[data-testid="cs-json"]')).toContainText('"summary"');
  await page.locator('[data-testid="sb-send"]').click();
  await expect(page).toHaveURL(/\/submissions\/\d+$/);
  await expect(page.locator('[data-testid="submission-live"]')).toBeVisible();
  await expect(page.locator('[data-testid="submission-status"]')).toHaveText('Needs review', { timeout: 20_000 });
  await expect(page.locator('[data-testid="checks-table"] [role="row"]')).toHaveCount(10);
  await expect(page.locator('[data-testid="withdraw"]')).toBeVisible();
});

test('a submission form explains itself instead of the browser', async ({ page }) => {
  await as(page, 'publisher', '/submit');
  await page.locator('[data-testid="sb-send"]').click();
  await expect(page.locator('#sb-repo-e')).toHaveText(/owner\/name/);
  await expect(page.locator('[data-testid="sb-repo"]')).toHaveAttribute('aria-invalid', 'true');
  await expect(page).toHaveURL(/\/submit$/);
});

test('a reviewer approves from the queue; the dialog keeps focus and Escape closes it', async ({ page }) => {
  await as(page, 'reviewer', '/review');
  await page.locator('[data-testid="queue-101"]').click();
  await expect(page.locator('h1')).toHaveText('Submission #101');
  await page.locator('[data-testid="approve"]').click();
  const dialog = page.locator('dialog[open]');
  await expect(dialog).toBeVisible();
  expect(await page.evaluate(() => !!document.activeElement?.closest('dialog'))).toBe(true);
  await page.keyboard.press('Escape');
  await expect(page.locator('dialog[open]')).toHaveCount(0);
  await page.locator('[data-testid="approve"]').click();
  await page.locator('[data-testid="approve-security"]').check();
  await page.locator('[data-testid="dialog-confirm"]:visible').click();
  await expect(page.locator('[data-testid="action-done"]')).toContainText('serial 13');
  await expect(page.locator('[data-testid="submission-status"]')).toHaveText('Approved');
});

test('an administrator activates a pending publisher and suspends one with a reason', async ({ page }) => {
  await as(page, 'admin', '/admin/publishers');
  await page.locator('[data-testid="pub-actions-acme-tools"]').click();
  await page.getByRole('menuitem', { name: 'Activate' }).click();
  await expect(page.locator('[role="row"]', { hasText: 'Acme Tools' })).toContainText('Active');

  await page.locator('[data-testid="pub-actions-jdoe"]').click();
  await page.getByRole('menuitem', { name: /Suspend/ }).click();
  await page.locator('[data-testid="dialog-confirm"]:visible').click();
  await expect(page.locator('dialog[open]')).toContainText('Required.');
  await page.locator('dialog[open] [data-testid="dialog-reason"]').fill('Testing the suspension path.');
  await page.locator('[data-testid="dialog-confirm"]:visible').click();
  await expect(page.locator('[role="row"]', { hasText: 'Jane Doe' })).toContainText('Testing the suspension path.');
});

test('an administrator yanks a version; its page then warns and hides the install steps', async ({ page }) => {
  await as(page, 'admin', '/apps/filex-sign');
  await page.locator('[data-testid="versions-table"] [role="row"]', { hasText: '0.3.0' }).locator('.tbl-rowactions__btn').click();
  await page.getByRole('menuitem', { name: /Yank/ }).click();
  await page.locator('dialog[open] [data-testid="dialog-reason"]').fill('Breaks signing on rotated pages.');
  await page.locator('[data-testid="dialog-confirm"]:visible').click();
  await expect(page.locator('dialog[open]')).toHaveCount(0);
  await page.goto('/apps/filex-sign/versions/0.3.0');
  await expect(page.locator('[data-testid="version-yanked"]')).toContainText('Breaks signing');
  await expect(page.locator('[data-testid="install"]')).toHaveCount(0);
});

test('a failed list shows the error alone, never "no publisher matches" beside it', async ({ page, request }) => {
  await request.post('http://127.0.0.1:8080/__mock/fail?path=/api/v1/admin/publishers&status=429');
  await as(page, 'admin', '/admin/publishers');
  await expect(page.locator('[role="alert"]')).toContainText('Too many requests');
  await expect(page.getByText('No publisher matches.')).toHaveCount(0);
  await page.screenshot({ path: 'out/admin-publishers-429.png' });
});


test('Install on a paid app: a saved instance and a license key make a one-time link, and the dialog is accessible', async ({ page }) => {
  await as(page, 'publisher', '/apps/markdown-preview');
  await expect(page.locator('[data-testid="app-paid"]')).toContainText('$12 per instance');
  await page.locator('[data-testid="install-open"]').click();
  const dialog = page.locator('dialog[open]');
  await expect(dialog).toBeVisible();
  await expect(dialog.locator('[data-testid="install-instance"]')).toContainText('Office filex');
  await dialog.locator('[data-testid="install-license"]').fill('FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-4C');
  const axe = await new AxeBuilder({ page }).include('dialog[open]').withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
  expect(axe.violations.map((v) => v.id)).toEqual([]);
  await dialog.locator('[data-testid="dialog-confirm"]').click();
  const link = dialog.locator('[data-testid="magic-link"]');
  await expect(link).toContainText('https://files.jdoe.example/admin/store-install#store=');
  await expect(dialog.locator('[data-testid="install-link-open"]')).toHaveAttribute('href', /^https:\/\/files\.jdoe\.example\/admin\/store-install#store=.+&intent=[A-Za-z0-9_-]{43}$/);
  await dialog.locator('[data-testid="dialog-confirm"]').click();
  await expect(page.locator('dialog[open]')).toHaveCount(0);
});

test('a publisher makes a license key, sees it once, and frees a seat', async ({ page }) => {
  await as(page, 'publisher', '/publish/apps/markdown-preview');
  await page.locator('[data-testid="licence-new"]').click();
  const dialog = page.locator('dialog[open]');
  await dialog.locator('[data-testid="licence-licensee"]').fill('Browser buyer');
  await dialog.locator('[data-testid="dialog-confirm"]').click();
  const key = (await dialog.locator('[data-testid="licence-key-value"]').innerText()).trim();
  expect(key).toMatch(/^FXL-[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){5}-[0-9A-HJKMNP-TV-Z]{2}$/);
  await dialog.locator('[data-testid="dialog-confirm"]').click();
  await expect(page.locator('dialog[open]')).toHaveCount(0);
  expect(await page.content()).not.toContain(key);
  await expect(page.locator('[data-testid="licences"]')).toContainText(key.slice(0, 8));

  await page.locator('[data-testid="licence-actions-FXL-7K2Q"]').click();
  await page.getByRole('menuitem', { name: 'Instances' }).click();
  await expect(page.locator('[data-testid="activations-table"]')).toContainText('nw-files-01');
  await page.locator('[data-testid="activations-table"] .tbl-rowactions__btn').first().click();
  await page.getByRole('menuitem', { name: 'Free the seat' }).click();
  await expect(page.locator('[data-testid="activations-table"]')).toContainText('Freed');
});

for (const width of [1280, 375, 360]) {
  test(`the theme switch in the header at ${width}px: three icons in one pill, keys move it, both themes, nothing overflows`, async ({ page, browserName }) => {
    await page.setViewportSize({ width, height: width > 600 ? 800 : 740 });
    await as(page, 'publisher', '/');
    const sw = page.locator('[data-testid="theme"]');
    await expect(sw).toBeVisible();
    await expect(sw).toHaveAttribute('role', 'radiogroup');
    await expect(sw.locator('[role="radio"]')).toHaveCount(3);
    await expect(sw.locator('select')).toHaveCount(0);
    for (const theme of ['light', 'dark'] as const) {
      await page.locator(`[data-testid="theme-${theme}"]`).click();
      await expect(page.locator('html')).toHaveClass(theme === 'dark' ? /dark/ : /^(?!.*dark).*$/);
      await expect(page.locator(`[data-testid="theme-${theme}"]`)).toHaveAttribute('aria-checked', 'true');
      await page.mouse.move(0, 0);
      const box = (await sw.boundingBox())!;
      const bar = (await page.locator('.fa-topbar').boundingBox())!;
      expect(box.height, 'the pill fits the header row').toBeLessThanOrEqual(36);
      expect(box.y >= bar.y && box.y + box.height <= bar.y + bar.height, 'the pill sits inside the header').toBe(true);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      expect(overflow, 'no sideways scroll').toBeLessThanOrEqual(0);
      // animations: 'disabled' lets the switch's colour transition finish first
      await page.locator('.fa-topbar').screenshot({ path: `browser/out/header-${browserName}-${theme}-${width}.png`, animations: 'disabled' });
    }
    await page.locator('[data-testid="theme-dark"]').focus();
    await page.keyboard.press('ArrowLeft');
    await expect(page.locator('[data-testid="theme-system"]')).toHaveAttribute('aria-checked', 'true');
    await expect(page.locator('[data-testid="theme-system"]')).toBeFocused();
    await page.keyboard.press('ArrowLeft');
    await expect(page.locator('[data-testid="theme-light"]')).toHaveAttribute('aria-checked', 'true');
    await expect(page.locator('html')).not.toHaveClass(/dark/);
  });
}

for (const width of [1280, 375]) {
  for (const theme of ['light', 'dark'] as const) {
    test(`FaSelect at ${width}px, ${theme}: the catalog filters and the Install dialog open a listbox, keys choose, nothing leaves the screen`, async ({ page, browserName }) => {
      await page.addInitScript((t) => {
        try {
          localStorage.setItem('fapps.theme', t);
        } catch {
          /* private mode */
        }
      }, theme);
      await page.setViewportSize({ width, height: width > 600 ? 900 : 812 });
      await page.goto('/');
      await page.locator('[data-testid="app-link-filex-sign"]').waitFor();
      expect(await page.locator('select').count(), 'no native select on the page').toBe(0);
      const kind = page.locator('[data-testid="catalog-kind"]');
      await kind.scrollIntoViewIfNeeded();
      await kind.click();
      const list = page.locator('[data-testid="catalog-kind-list"]');
      await expect(list).toBeVisible();
      await expect(kind).toHaveAttribute('aria-expanded', 'true');
      const lb = (await list.boundingBox())!;
      expect(lb.x >= 0 && lb.x + lb.width <= width, 'the listbox stays inside the width').toBe(true);
      const axe = await new AxeBuilder({ page }).include('[data-testid="catalog-kind"]').include('[data-testid="catalog-kind-list"]').analyze();
      expect(axe.violations.map((v) => v.id)).toEqual([]);
      await page.screenshot({ path: `browser/out/select-catalog-${browserName}-${theme}-${width}.png`, animations: 'disabled' });
      await page.keyboard.press('ArrowDown');
      await page.keyboard.press('ArrowDown');
      await page.keyboard.press('Enter');
      await expect(page).toHaveURL(/kind=language_pack/);
      await expect(list).toHaveCount(0);
      await expect(kind).toBeFocused();
      await page.keyboard.press('Escape');

      // near the bottom of the window the listbox opens upward
      await page.evaluate(() => {
        const el = document.querySelector('[data-testid="catalog-sort"]')!;
        const r = el.getBoundingClientRect();
        window.scrollBy(0, r.bottom - window.innerHeight + 8);
      });
      const sort = page.locator('[data-testid="catalog-sort"]');
      await sort.click();
      const sl = page.locator('[data-testid="catalog-sort-list"]');
      await expect(sl).toBeVisible();
      const sb = (await sort.boundingBox())!;
      const pb = (await sl.boundingBox())!;
      if (sb.y + sb.height + 160 > (width > 600 ? 900 : 812)) expect(pb.y + pb.height, 'opened upward').toBeLessThanOrEqual(sb.y + 1);
      expect(pb.y >= 0 && pb.y + pb.height <= (width > 600 ? 900 : 812), 'the listbox stays inside the height').toBe(true);
      await page.mouse.click(2, 2);
      await expect(sl).toHaveCount(0);

      // the Install dialog: the listbox opens inside the modal dialog
      await as(page, 'publisher', '/apps/markdown-preview');
      await page.locator('[data-testid="install-open"]').click();
      const dialog = page.locator('dialog[open]');
      await expect(dialog).toBeVisible();
      const inst = dialog.locator('[data-testid="install-instance"]');
      await inst.click();
      const il = page.locator('[data-testid="install-instance-list"]');
      await expect(il).toBeVisible();
      expect(await il.evaluate((el) => !!el.closest('dialog[open]')), 'the listbox is in the dialog').toBe(true);
      await page.screenshot({ path: `browser/out/select-install-${browserName}-${theme}-${width}.png`, animations: 'disabled' });
      await page.keyboard.press('Enter');
      await expect(il).toHaveCount(0);
      await expect(inst).toContainText('Office filex');
      await inst.click();
      await expect(il).toBeVisible();
      await page.keyboard.press('Escape');
      await expect(il).toHaveCount(0);
      await expect(dialog, 'Escape in the listbox closes the listbox, not the dialog').toBeVisible();
      await page.keyboard.press('Escape');
      await expect(page.locator('dialog[open]')).toHaveCount(0);
    });
  }
}
