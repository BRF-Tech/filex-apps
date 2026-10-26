// Small pure helpers the views share. Dates and sizes go through filex's own
// formatters (one date format across the product: "Oct 3, 2026, 2:50 PM").
import { formatByteSize, formatWhen, formatWhenFull } from '@brftech/filex-core';
import type { Check, SubmissionStatus, Text } from '@/api/client';

export const LOCALE = 'en';

/** A localised string: English first (the contract promises it), else any. */
export function text(t: Text | null | undefined): string {
  if (!t) return '';
  return t.en ?? Object.values(t)[0] ?? '';
}

export function when(iso: string | null | undefined, time = true): string {
  return iso ? formatWhen(iso, LOCALE, { time }) : '';
}

export function whenFull(iso: string | null | undefined): string {
  return iso ? formatWhenFull(iso, LOCALE) : '';
}

export function bytes(n: number | null | undefined): string {
  return formatByteSize(n ?? null);
}

/** The first and last characters of a long hex string, for a narrow cell. */
export function shortHash(h: string | null | undefined, keep = 12): string {
  if (!h) return '';
  return h.length <= keep * 2 + 1 ? h : `${h.slice(0, keep)}…${h.slice(-6)}`;
}

/** Seconds between two instants, "12 s" / "2 min 5 s"; '' when unknown. */
export function took(start?: string, end?: string): string {
  if (!start || !end) return '';
  const ms = Date.parse(end) - Date.parse(start);
  if (!Number.isFinite(ms) || ms < 0) return '';
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  const r = s % 60;
  return r ? `${m} min ${r} s` : `${m} min`;
}

/** A badge's tone: the colour comes from a filex token, the meaning from here. */
export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'muted';

export function submissionTone(s: SubmissionStatus): Tone {
  switch (s) {
    case 'approved':
      return 'ok';
    case 'needs_review':
      return 'warn';
    case 'failed':
    case 'rejected':
      return 'danger';
    case 'withdrawn':
      return 'muted';
    default:
      return 'info';
  }
}

export function checkTone(s: Check['status']): Tone {
  switch (s) {
    case 'passed':
      return 'ok';
    case 'warned':
      return 'warn';
    case 'failed':
      return 'danger';
    case 'running':
    case 'pending':
      return 'info';
    default:
      return 'muted';
  }
}

export function publisherTone(s: 'pending' | 'active' | 'suspended' | undefined): Tone {
  return s === 'active' ? 'ok' : s === 'suspended' ? 'danger' : 'warn';
}

export function appStatusTone(s: 'listed' | 'unlisted' | 'revoked'): Tone {
  return s === 'listed' ? 'ok' : s === 'revoked' ? 'danger' : 'warn';
}

/** Statuses the pipeline is still working on: the page keeps following them. */
export function inFlight(s: SubmissionStatus): boolean {
  return s === 'queued' || s === 'resolving' || s === 'validating';
}

/** Statuses a publisher may still withdraw from (no decision yet). */
export function undecided(s: SubmissionStatus): boolean {
  return s === 'queued' || s === 'resolving' || s === 'validating' || s === 'needs_review' || s === 'failed';
}

const MEDIA = /^\/v1\/media\/[0-9a-f]{64}\.(png|jpg|webp)$/;

/**
 * The store-relative path of a mirrored image, or '' when the address is not
 * one. Images are always the store's own `/v1/media/<sha256>.<ext>` (never
 * GitHub); anything else is not drawn, so a viewer's browser never fetches
 * from another host.
 */
export function mediaPath(url: string | null | undefined): string {
  if (!url) return '';
  let path = url;
  if (!url.startsWith('/')) {
    try {
      path = new URL(url).pathname;
    } catch {
      return '';
    }
  }
  return MEDIA.test(path) ? path : '';
}

/** `owner/name` from an address or the name itself; '' when it is neither. */
export function normalizeRepo(input: string): string {
  const s = input.trim().replace(/\/+$/, '').replace(/\.git$/, '');
  const m = /^(?:https:\/\/github\.com\/)?([A-Za-z0-9-]{1,39})\/([A-Za-z0-9._-]{1,100})$/.exec(s);
  return m ? `${m[1]}/${m[2]}` : '';
}

const GH = 'https://github.com/';
const enc = (s: string) => s.split('/').map(encodeURIComponent).join('/');

/** Plain links a person may follow; the interface itself never requests them. */
export const github = {
  repo: (repo: string) => GH + enc(repo),
  owner: (owner: string) => GH + encodeURIComponent(owner),
  tag: (repo: string, ref: string) => `${GH}${enc(repo)}/releases/tag/${encodeURIComponent(ref)}`,
  commit: (repo: string, sha: string) => `${GH}${enc(repo)}/commit/${encodeURIComponent(sha)}`,
  compare: (repo: string, from: string, to: string) =>
    `${GH}${enc(repo)}/compare/${encodeURIComponent(from)}...${encodeURIComponent(to)}`,
};

/** An address as a person reads it: no scheme, no trailing slash. */
export function bareUrl(url: string): string {
  return url.replace(/^https?:\/\//, '').replace(/\/$/, '');
}

/** Only https links are drawn as links; anything else is shown as text. */
export function safeHref(url: string | null | undefined): string {
  if (!url) return '';
  try {
    const u = new URL(url);
    return u.protocol === 'https:' ? u.toString() : '';
  } catch {
    return '';
  }
}

/** The empty-table message, unless the load failed: a failed load shows the
 *  error callout alone, never "nothing matches" next to it (the table has no
 *  rows because nothing arrived, not because nothing exists). `loading` needs
 *  no help here: the table itself says "Loading" until the call settles. */
export function emptyUnlessError(message: string, error: string): string {
  return error ? '' : message;
}

/** How many tones an app placeholder can take (styles.css `.fa-tone-N`). */
export const TONES = 7;

/** A stable tone for a name: the same app always gets the same colour. */
export function toneOf(name: string): number {
  let h = 0;
  for (const ch of name) h = (h * 31 + (ch.codePointAt(0) ?? 0)) >>> 0;
  return h % TONES;
}

/** The letter a placeholder shows: the first letter or digit of the name. */
export function initialOf(name: string): string {
  const m = /[\p{L}\p{N}]/u.exec(name);
  return (m?.[0] ?? '?').toLocaleUpperCase('en');
}

/** What a permission touches, read from its id (filex writes `files:read`,
 *  `ui-viewer:.drawio`, `net:...`; older manifests `files.read`). Only picks a
 *  picture: the words shown are always filex's own label. */
export type PermissionKind = 'files' | 'ui' | 'net' | 'convert' | 'thumbnail' | 'other';

export function permissionKind(id: string): PermissionKind {
  const head = id.toLowerCase().split(/[:.]/)[0];
  if (head === 'files' || head === 'file' || head === 'storage') return 'files';
  if (head === 'thumbnail' || head === 'thumbnails') return 'thumbnail';
  if (head === 'net' || head === 'ui-net' || head === 'network' || head === 'http') return 'net';
  if (head === 'convert' || head === 'engine' || head === 'office' || head === 'office-engine') return 'convert';
  if (head === 'ui' || head.startsWith('ui-')) return 'ui';
  return 'other';
}

/** A language by its own name ("English", "Türkçe", "Español"), the way a
 *  language picker lists them; the code itself when the engine does not know
 *  it. The public pages name languages only: how much of the interface a
 *  version covers is the reviewer's detail (the dry runs), not a shop
 *  window's. */
export function languageName(code: string | null | undefined): string {
  if (!code) return '';
  try {
    const name = new Intl.DisplayNames([code], { type: 'language' }).of(code);
    if (name && name !== code) return name.charAt(0).toLocaleUpperCase(code) + name.slice(1);
  } catch {
    /* an unknown or malformed code: shown as it is */
  }
  return code;
}
