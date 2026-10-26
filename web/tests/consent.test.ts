import { describe, expect, it } from 'vitest';
import { buildConsent, consentJSON, emptyConsent, validateConsent, type ConsentForm } from '@/lib/consent';

const form = (patch: Partial<ConsentForm> = {}): ConsentForm => ({ ...emptyConsent('jdoe'), ...patch });

describe('.filex-apps.json from the form', () => {
  it('writes only what was filled in, in the schema\'s shape', () => {
    expect(buildConsent(form())).toEqual({ publisher: 'jdoe' });
    expect(
      buildConsent(
        form({
          categories: ['developer'],
          summary: '  Preview Markdown.  ',
          description: 'docs/STORE.md',
          icon: 'assets/icon.png',
          screenshots: [
            { path: 'a.png', caption: 'One' },
            { path: '  ', caption: 'ignored: no path' },
            { path: 'b.png', caption: '' },
          ],
        }),
      ),
    ).toEqual({
      publisher: 'jdoe',
      categories: ['developer'],
      summary: { en: 'Preview Markdown.' },
      description: 'docs/STORE.md',
      icon: 'assets/icon.png',
      screenshots: [{ path: 'a.png', caption: { en: 'One' } }, { path: 'b.png' }],
    });
  });

  it('prints two-space JSON with a trailing newline', () => {
    expect(consentJSON(form())).toBe('{\n  "publisher": "jdoe"\n}\n');
  });
});

describe('the schema\'s rules, while typing', () => {
  it('passes a valid file', () => {
    expect(validateConsent(form({ categories: ['a', 'b', 'c'], icon: 'assets/icon.png', summary: 'x'.repeat(300) }))).toEqual([]);
  });

  it('refuses a publisher id the schema refuses', () => {
    for (const p of ['', 'ab', '-abc', 'abc-', 'Abc', 'a'.repeat(41)]) {
      expect(validateConsent(form({ publisher: p })).length, p).toBe(1);
    }
  });

  it('refuses more than three categories and a summary over 300 characters', () => {
    expect(validateConsent(form({ categories: ['a', 'b', 'c', 'd'] }))).toHaveLength(1);
    expect(validateConsent(form({ summary: 'x'.repeat(301) }))).toHaveLength(1);
  });

  it('refuses paths that leave the repository or use other characters', () => {
    for (const bad of ['/etc/passwd', '../x.png', 'a/../../x.png', 'assets/..', 'a b.png', 'x\\y.png']) {
      expect(validateConsent(form({ icon: bad })).length, bad).toBe(1);
    }
    for (const ok of ['assets/icon.png', 'a..b.png', '.github/shot.webp', 'docs/STORE.md']) {
      expect(validateConsent(form({ icon: ok })), ok).toEqual([]);
    }
  });

  it('refuses a ninth screenshot, a bad screenshot path and a long caption', () => {
    const nine = Array.from({ length: 9 }, (_, i) => ({ path: `s${i}.png`, caption: '' }));
    expect(validateConsent(form({ screenshots: nine }))).toHaveLength(1);
    expect(validateConsent(form({ screenshots: [{ path: '../s.png', caption: '' }] }))).toHaveLength(1);
    expect(validateConsent(form({ screenshots: [{ path: 's.png', caption: 'x'.repeat(301) }] }))).toHaveLength(1);
  });
});
