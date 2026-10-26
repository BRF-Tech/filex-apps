// The publisher's `.filex-apps.json` (schema/filex-apps.schema.json), written
// from a form. The store reads the listing ONLY from this file in the
// repository, at the reviewed commit (docs/ARCHITECTURE.md Q10): this helper
// writes the file for the publisher to commit and never sends it anywhere.
// The checks below mirror the schema's rules so a mistake shows while typing;
// the server's own validation is the authority.
import { S, fmt } from '@/strings';

export interface ConsentScreenshot {
  path: string;
  caption: string;
}

export interface ConsentForm {
  publisher: string;
  categories: string[];
  summary: string;
  description: string;
  icon: string;
  screenshots: ConsentScreenshot[];
}

export const PUBLISHER_ID = /^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/;
export const CATEGORY_ID = /^[a-z0-9][a-z0-9-]{0,31}$/;
// The schema's `$defs.path`: relative, no `..` segment, a small alphabet.
export const REPO_PATH = /^(?!\/)(?!.*(^|\/)\.\.(\/|$))[A-Za-z0-9._/-]+$/;
export const MAX_CATEGORIES = 3;
export const MAX_SCREENSHOTS = 8;
export const MAX_TEXT = 300;

export function emptyConsent(publisher = ''): ConsentForm {
  return { publisher, categories: [], summary: '', description: '', icon: '', screenshots: [] };
}

/** The document, with empty optional fields left out (key order as in the schema). */
export function buildConsent(f: ConsentForm): Record<string, unknown> {
  const out: Record<string, unknown> = { publisher: f.publisher.trim() };
  if (f.categories.length) out.categories = [...f.categories];
  const summary = f.summary.trim();
  if (summary) out.summary = { en: summary };
  const description = f.description.trim();
  if (description) out.description = description;
  const icon = f.icon.trim();
  if (icon) out.icon = icon;
  const shots = f.screenshots
    .map((s) => ({ path: s.path.trim(), caption: s.caption.trim() }))
    .filter((s) => s.path !== '')
    .map((s) => (s.caption ? { path: s.path, caption: { en: s.caption } } : { path: s.path }));
  if (shots.length) out.screenshots = shots;
  return out;
}

export function consentJSON(f: ConsentForm): string {
  return JSON.stringify(buildConsent(f), null, 2) + '\n';
}

function pathOk(p: string): boolean {
  return p.length >= 1 && p.length <= MAX_TEXT && REPO_PATH.test(p);
}

/** Every rule the form breaks, as sentences; [] when the file is valid. */
export function validateConsent(f: ConsentForm): string[] {
  const errs: string[] = [];
  if (!PUBLISHER_ID.test(f.publisher.trim())) errs.push(S.consent.errPublisher);
  if (f.categories.length > MAX_CATEGORIES) errs.push(S.consent.errCategories);
  if (f.summary.trim().length > MAX_TEXT) errs.push(S.consent.errSummary);
  const d = f.description.trim();
  if (d && !pathOk(d)) errs.push(fmt(S.consent.errPath, { field: S.consent.description }));
  const i = f.icon.trim();
  if (i && !pathOk(i)) errs.push(fmt(S.consent.errPath, { field: S.consent.icon }));
  const shots = f.screenshots.filter((s) => s.path.trim() !== '');
  if (shots.length > MAX_SCREENSHOTS) errs.push(S.consent.errScreenshots);
  f.screenshots.forEach((s, n) => {
    const p = s.path.trim();
    if (p && !pathOk(p)) errs.push(fmt(S.consent.errPath, { field: fmt(S.consent.screenshotPath, { n: n + 1 }) }));
    if (s.caption.trim().length > MAX_TEXT) errs.push(fmt(S.consent.errCaption, { n: n + 1 }));
  });
  return errs;
}
