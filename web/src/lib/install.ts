// Install links, on the page side. The server is the authority on every
// rule (api/openapi.yaml InstallIntentRequest); these only give a person
// feedback before the request and keep what the page draws safe.

/** A filex address the store will take: http(s), a host, no path, query or fragment. */
export function looksLikeFilexUrl(input: string): boolean {
  return /^https?:\/\/[^\s/?#\\@]+\/?$/i.test(input.trim());
}

/** The magic link as an anchor's href: only an http(s) link the store made, else ''. */
export function openableLink(link: string | null | undefined): string {
  if (!link) return '';
  try {
    const u = new URL(link);
    if ((u.protocol !== 'https:' && u.protocol !== 'http:') || u.username || u.password) return '';
    if (u.pathname !== '/admin/store-install' || !u.hash.startsWith('#store=')) return '';
    return u.href;
  } catch {
    return '';
  }
}

/** A license key as the store writes it (FXL- and 26 Crockford base32 characters). */
export const LICENSE_KEY = /^FXL-[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){5}-[0-9A-HJKMNP-TV-Z]{2}$/;
