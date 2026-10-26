// How this store presents itself: its name, its logo and who runs it
// (GET /api/v1/store, from the server's FAPPS_STORE_* and FAPPS_OPERATOR_*
// settings). One reactive answer for the whole interface, read once at start.
//
// The name is known before any request: the server writes it into the page's
// <meta name="application-name"> (and <title>), so the brand never shows
// another name first. Without that tag (the dev server) the page's host
// stands in, which is also the server's own default.
import { reactive } from 'vue';
import { getStore, isStorePath, type StoreInfo } from '@/api/client';

export interface BrandState {
  name: string;
  /** The store's own logo (a path on the store); '' shows filex's mark. */
  logoUrl: string;
  operatorName: string;
  operatorUrl: string;
  contact: string;
  termsUrl: string;
  privacyUrl: string;
  loaded: boolean;
}

function initialName(): string {
  try {
    const v = document.querySelector('meta[name="application-name"]')?.getAttribute('content')?.trim();
    if (v) return v;
  } catch {
    /* no document (tests) */
  }
  try {
    return location.host || 'App store';
  } catch {
    return 'App store';
  }
}

export const brand = reactive<BrandState>({
  name: initialName(),
  logoUrl: '',
  operatorName: '',
  operatorUrl: '',
  contact: '',
  termsUrl: '',
  privacyUrl: '',
  loaded: false,
});

/** An address the footer may link to: https, or mailto for the contact. The
 *  server checks the same; this keeps a link from ever being javascript:. */
export function safeLink(v: unknown, mailto = false): string {
  if (typeof v !== 'string') return '';
  const s = v.trim();
  if (/^https:\/\/[^\s/?#]+/i.test(s)) return s;
  if (mailto && /^mailto:[^\s?]+@[^\s?]+$/i.test(s)) return s;
  return '';
}

/** Fill the brand from the server's answer (missing or odd fields ignored). */
export function applyStore(info: Partial<StoreInfo> | null | undefined): void {
  if (!info) return;
  if (typeof info.name === 'string' && info.name.trim()) brand.name = info.name.trim();
  brand.logoUrl = typeof info.logo_url === 'string' && isStorePath(info.logo_url) ? info.logo_url : '';
  brand.operatorName = typeof info.operator_name === 'string' ? info.operator_name.trim() : '';
  brand.operatorUrl = safeLink(info.operator_url);
  brand.contact = safeLink(info.contact, true);
  brand.termsUrl = safeLink(info.terms_url);
  brand.privacyUrl = safeLink(info.privacy_url);
}

let pending: Promise<void> | null = null;

/** Read GET /api/v1/store once; a failure keeps the name the page carries
 *  and shows no footer links. */
export function loadBrand(force = false): Promise<void> {
  if (pending && !force) return pending;
  pending = (async () => {
    try {
      applyStore(await getStore());
    } catch {
      /* the page's own name stays */
    } finally {
      brand.loaded = true;
    }
  })();
  return pending;
}

/** The name in the brand's two weights: the last word is the tag ("Acme"
 *  + "Apps"); a name of one word (a host) is all head. */
export function brandParts(name: string): { head: string; tag: string } {
  const n = name.trim();
  const i = n.lastIndexOf(' ');
  return i > 0 ? { head: n.slice(0, i), tag: n.slice(i + 1) } : { head: n, tag: '' };
}

/** The operator line's link text: their name, else their page's host. */
export function operatorLabel(b: Pick<BrandState, 'operatorName' | 'operatorUrl'>): string {
  if (b.operatorName) return b.operatorName;
  try {
    return b.operatorUrl ? new URL(b.operatorUrl).host : '';
  } catch {
    return '';
  }
}

/** Whether the footer has anything about who runs the store. */
export function hasOperatorInfo(b: BrandState): boolean {
  return Boolean(b.operatorName || b.operatorUrl || b.contact || b.termsUrl || b.privacyUrl);
}
