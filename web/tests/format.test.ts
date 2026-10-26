import { describe, expect, it } from 'vitest';
import { checkTone, github, inFlight, mediaPath, normalizeRepo, safeHref, shortHash, submissionTone, text, took, undecided } from '@/lib/format';

const SHA = 'a'.repeat(64);

describe('images come from the store only', () => {
  it('keeps the store\'s own media path, relative or absolute', () => {
    expect(mediaPath(`/v1/media/${SHA}.png`)).toBe(`/v1/media/${SHA}.png`);
    expect(mediaPath(`https://apps.example.com/v1/media/${SHA}.webp`)).toBe(`/v1/media/${SHA}.webp`);
  });

  it('draws nothing for any other address', () => {
    for (const bad of [
      `https://raw.githubusercontent.com/o/r/main/icon.png`,
      `/v1/media/${SHA}.svg`,
      `/v1/media/${SHA.slice(1)}.png`,
      `/v1/media/../secret.png`,
      'javascript:alert(1)',
      '',
      undefined,
    ]) {
      expect(mediaPath(bad), String(bad)).toBe('');
    }
  });
});

describe('links', () => {
  it('only https addresses become links', () => {
    expect(safeHref('https://example.com/x')).toBe('https://example.com/x');
    expect(safeHref('javascript:alert(1)')).toBe('');
    expect(safeHref('http://example.com')).toBe('');
    expect(safeHref('not a url')).toBe('');
  });

  it('builds GitHub addresses with every part encoded', () => {
    expect(github.tag('o/r', 'v1.0.0')).toBe('https://github.com/o/r/releases/tag/v1.0.0');
    expect(github.compare('o/r', 'v1', 'v2#x')).toBe('https://github.com/o/r/compare/v1...v2%23x');
    expect(github.owner('a b')).toBe('https://github.com/a%20b');
  });

  it('reads owner/name from what a publisher pastes', () => {
    expect(normalizeRepo('jdoe/filex-md')).toBe('jdoe/filex-md');
    expect(normalizeRepo(' https://github.com/jdoe/filex-md.git/ ')).toBe('jdoe/filex-md');
    for (const bad of ['jdoe', 'https://gitlab.com/a/b', 'a/b/c', 'http://github.com/a/b', 'a b/c']) {
      expect(normalizeRepo(bad), bad).toBe('');
    }
  });
});

describe('small formatters', () => {
  it('shortens long hashes and keeps short ones', () => {
    expect(shortHash('0123456789abcdef'.repeat(4))).toBe('0123456789ab…abcdef');
    expect(shortHash('abc')).toBe('abc');
  });

  it('says how long a step took', () => {
    expect(took('2026-10-03T10:00:00Z', '2026-10-03T10:00:12Z')).toBe('12 s');
    expect(took('2026-10-03T10:00:00Z', '2026-10-03T10:02:05Z')).toBe('2 min 5 s');
    expect(took('2026-10-03T10:00:00Z', undefined)).toBe('');
  });

  it('picks English from a localised string', () => {
    expect(text({ tr: 'Merhaba', en: 'Hello' })).toBe('Hello');
    expect(text({ de: 'Hallo' })).toBe('Hallo');
    expect(text(undefined)).toBe('');
  });

  it('maps states to tones and to what the page does next', () => {
    expect(submissionTone('approved')).toBe('ok');
    expect(submissionTone('failed')).toBe('danger');
    expect(submissionTone('needs_review')).toBe('warn');
    expect(checkTone('warned')).toBe('warn');
    expect(['queued', 'resolving', 'validating'].every((s) => inFlight(s as never))).toBe(true);
    expect(inFlight('needs_review')).toBe(false);
    expect(undecided('approved')).toBe(false);
    expect(undecided('failed')).toBe(true);
  });
});
