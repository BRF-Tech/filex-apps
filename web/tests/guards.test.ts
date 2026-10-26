// Source guards for what the web interface must never do:
// no table but filex's DataTable, every DataTable remembered under its own
// id, no colour that is not a filex token, no request to another host, no
// secret in the bundle. They read the sources, so they hold for code that no
// unit test happens to render.
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

// vitest runs from web/ (vitest.config.ts); happy-dom's import.meta.url is not a file URL.
const SRC = resolve(process.cwd(), 'src');

function files(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) files(p, out);
    else if (/\.(vue|ts|css)$/.test(name) && !name.endsWith('.d.ts')) out.push(p);
  }
  return out;
}
if (!statSync(SRC).isDirectory()) throw new Error('no src at ' + SRC);
const SOURCES = files(SRC).map((p) => ({ path: relative(SRC, p).replace(/\\/g, '/'), text: readFileSync(p, 'utf8') }));
/** Comments say why a rule exists and may name what it forbids; only code counts. */
const code = (t: string) => t.replace(/\/\*[\s\S]*?\*\//g, '').replace(/<!--[\s\S]*?-->/g, '').replace(/(^|[^:'"`])\/\/.*$/gm, '$1');

describe('one table: filex\'s DataTable', () => {
  it('no <table>, table rows or a table look-alike anywhere in src', () => {
    const hits = SOURCES.filter((f) => /<\s*(table|thead|tbody|tr|td|th)[\s>]|role=["'](table|grid|row|cell|gridcell|columnheader)["']|display\s*:\s*table/i.test(code(f.text)));
    expect(hits.map((f) => f.path)).toEqual([]);
  });

  it('every DataTable has a literal table-id, and no two share one', () => {
    const ids: string[] = [];
    const missing: string[] = [];
    for (const f of SOURCES.filter((s) => s.path.endsWith('.vue'))) {
      for (const m of code(f.text).matchAll(/<DataTable\b([^>]*)>/g)) {
        const id = /\btable-id="([^"]+)"/.exec(m[1])?.[1];
        if (id) ids.push(id);
        else missing.push(f.path);
      }
    }
    expect(missing).toEqual([]);
    expect(ids.length).toBeGreaterThan(10);
    expect(ids.filter((id, i) => ids.indexOf(id) !== i)).toEqual([]);
    expect(ids.every((id) => id.startsWith('store.'))).toBe(true);
  });
});

describe('colours are filex tokens only', () => {
  const NAMED =
    /(?<![-\w])(white|black|red|green|blue|gray|grey|yellow|orange|purple|pink|navy|teal|silver|maroon|olive|lime|aqua|fuchsia|crimson|gold|indigo|violet|tomato|salmon|coral|khaki|beige|ivory|lavender|tan)(?![-\w])/i;
  it('no hex, rgb(), hsl() or named colour in a stylesheet or a style block', () => {
    const hits: string[] = [];
    for (const f of SOURCES) {
      const css = f.path.endsWith('.css') ? f.text : [...f.text.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((m) => m[1]).join('\n');
      const body = code(css);
      for (const decl of body.matchAll(/:\s*([^;{}]+);/g)) {
        const v = decl[1];
        if (/#[0-9a-f]{3,8}\b|\b(rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\(/i.test(v) || NAMED.test(v)) hits.push(`${f.path}: ${v.trim()}`);
      }
    }
    expect(hits).toEqual([]);
  });

  it('no colour in an inline style either', () => {
    const hits = SOURCES.filter((f) => f.path.endsWith('.vue')).filter((f) =>
      /(?:^|\s):?style="[^"]*(#[0-9a-f]{3,8}\b|rgba?\(|hsla?\(|(?:^|[;{\s'])(color|background)\s*:)/im.test(code(f.text)),
    );
    expect(hits.map((f) => f.path)).toEqual([]);
  });
});

describe('the interface talks to the store and nothing else', () => {
  it('only src/api/client.ts makes requests', () => {
    const hits = SOURCES.filter((f) => f.path !== 'api/client.ts').filter((f) =>
      /\bfetch\s*\(|XMLHttpRequest|new\s+WebSocket|new\s+EventSource|sendBeacon|import\s*\(\s*['"]https?:/.test(code(f.text)),
    );
    expect(hits.map((f) => f.path)).toEqual([]);
  });

  it('the client never puts a scheme or a host in front of a path', () => {
    const client = code(SOURCES.find((f) => f.path === 'api/client.ts')!.text);
    expect(client).not.toMatch(/['"`]https?:\/\//);
    expect(client).not.toMatch(/fetch\(\s*['"`]\/\//);
  });

  it('the development mock never reaches the bundle', () => {
    expect(SOURCES.filter((f) => /from\s+['"][./]*mock\//.test(f.text)).map((f) => f.path)).toEqual([]);
  });

  it('no token, key or secret is written into the sources', () => {
    const SECRET = /\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|client_secret|BEGIN [A-Z ]*PRIVATE KEY|FAPPS_[A-Z_]*(KEY|SECRET|TOKEN))/;
    expect(SOURCES.filter((f) => SECRET.test(f.text)).map((f) => f.path)).toEqual([]);
  });
});

describe('no native dropdown', () => {
  // The owner, 2026-10-04: no plain dropdown anywhere.
  // A choice is FaSelect (a listbox) or FaSegmented (a few side by side).
  it('no <select> in any component or view', () => {
    const hits = SOURCES.filter((f) => f.path.endsWith('.vue') && /<select[\s>]/i.test(code(f.text))).map((f) => f.path);
    expect(hits).toEqual([]);
  });
});

describe('pages are navigable by heading', () => {
  it('every page view draws exactly one <h1> (admin sections draw h2 under the layout\'s)', () => {
    const bad: string[] = [];
    for (const f of SOURCES.filter((s) => /^views\/[^/]+\.vue$/.test(s.path) || s.path === 'views/admin/AdminLayout.vue')) {
      const n = (code(f.text).match(/<h1[\s>]/g) ?? []).length;
      if (n !== 1) bad.push(`${f.path}: ${n}`);
    }
    for (const f of SOURCES.filter((s) => /^views\/admin\/Admin(?!Layout).*\.vue$/.test(s.path))) {
      if (/<h1[\s>]/.test(code(f.text))) bad.push(`${f.path}: has an h1`);
    }
    expect(bad).toEqual([]);
  });
});
