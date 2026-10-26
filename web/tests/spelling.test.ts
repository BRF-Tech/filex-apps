// One spelling, the American one, as in filex itself (filex's dictionary gate
// wants "license"): no "licence" in what the store shows or says about itself.
// Read from the sources, so it holds for text no unit test renders: web/src
// (the strings, the components, and schema.d.ts, which carries the OpenAPI
// file's descriptions), the documents (docs/, README.md) and api/openapi.yaml.
//
// Kept on purpose, and only in web/src code: names that are not words on the
// screen. The data-testids the e2e and browser suites click (licence-new,
// licence-actions-<prefix>, licences), the DataTable id the tests and the
// browser round find the table by (store.manage.licences), and a few variable
// and key names (loadLicences, S.licensing.licences, newLicence, the
// 'licences' row action). Each is matched whole; the documents have none.
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

// vitest runs from web/ (vitest.config.ts); the repository is its parent.
const WEB = process.cwd();
const REPO = resolve(WEB, '..');

const BRITISH = /licenc/i;

const KEPT_IN_CODE: RegExp[] = [
  /(?<=["'`])licence(?:-[a-z]+)+-?(?=["'`$])/g, // data-testids: "licence-new", `licence-actions-${...}`
  /(?<=data-testid=")licences(?=")/g, // the licenses table's data-testid
  /\bstore\.manage\.licences\b/g, // the DataTable id
  /\b(?:load|new)Licences?\b/g, // loadLicences(), S.licensing.newLicence
  /\bS\.licensing\.licences\b/g, // the strings key, where it is read
  /^ {4}licences(?=: ')/gm, // the strings key, where it is defined
  /(?<=const )licences(?= = ref)/g, // the page's ref ...
  /\blicences(?=\??\.(?:value|items|total)\b)/g, // ... and where it is read
  /(?<=key(?: ===|:) ')licences(?=')/g, // the Administration > Apps row action
];

type Source = { path: string; text: string; code: boolean };

function walk(dir: string, keep: (name: string) => boolean, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, keep, out);
    else if (keep(name)) out.push(p);
  }
  return out;
}

function read(p: string, code: boolean): Source {
  return { path: relative(REPO, p).replace(/\\/g, '/'), text: readFileSync(p, 'utf8'), code };
}

function sources(): Source[] {
  for (const need of ['web/src', 'docs', 'README.md', 'api/openapi.yaml']) {
    if (!existsSync(join(REPO, need))) throw new Error(`the spelling guard reads ${need}, which is not at ${join(REPO, need)}`);
  }
  return [
    ...walk(join(REPO, 'web/src'), (n) => /\.(vue|ts|css|html)$/.test(n)).map((p) => read(p, /\.(vue|ts)$/.test(p))),
    ...walk(join(REPO, 'docs'), (n) => n.endsWith('.md')).map((p) => read(p, false)),
    read(join(REPO, 'README.md'), false),
    read(join(REPO, 'api/openapi.yaml'), false),
  ];
}

/** Every line that says "licence" in any form, once the kept names are blanked out. */
function britishLines(s: Source): string[] {
  let t = s.text;
  if (s.code) for (const k of KEPT_IN_CODE) t = t.replace(k, (m) => ' '.repeat(m.length));
  const out: string[] = [];
  t.split('\n').forEach((line, i) => {
    if (BRITISH.test(line)) out.push(`${s.path}:${i + 1}: ${s.text.split('\n')[i].trim()}`);
  });
  return out;
}

describe('one spelling: license', () => {
  const SOURCES = sources();

  it('reads the interface, the documents and the contract', () => {
    const paths = SOURCES.map((s) => s.path);
    expect(paths).toContain('web/src/strings.ts');
    expect(paths).toContain('web/src/api/schema.d.ts');
    expect(paths).toContain('docs/ARCHITECTURE.md');
    expect(paths).toContain('README.md');
    expect(paths).toContain('api/openapi.yaml');
  });

  it('no "licence" in web/src, the documents or the OpenAPI file', () => {
    expect(SOURCES.flatMap(britishLines)).toEqual([]);
  });

  it('the exception is narrow: words are caught, in code and in documents alike', () => {
    const code = (text: string) => britishLines({ path: 'x.vue', text, code: true });
    const doc = (text: string) => britishLines({ path: 'x.md', text, code: false });
    expect(code("    title: 'Price and licences',")).toHaveLength(1);
    expect(code("    newLicence: 'New licence…',")).toHaveLength(1);
    expect(code('// the licence key is shown once')).toHaveLength(1);
    expect(code('<p>Your licence</p>')).toHaveLength(1);
    expect(code('<button data-testid="licence-new">')).toHaveLength(0);
    expect(code('table-id="store.manage.licences"')).toHaveLength(0);
    expect(doc('table-id="store.manage.licences" and data-testid="licence-new"')).toHaveLength(1);
    expect(doc('the licence-key HMAC')).toHaveLength(1);
  });

  it('every kept name is still in use (a stale exception is removed, not kept)', () => {
    const code = SOURCES.filter((s) => s.code)
      .map((s) => s.text)
      .join('\n');
    const unused = KEPT_IN_CODE.filter((k) => !new RegExp(k.source, k.flags).test(code)).map(String);
    expect(unused).toEqual([]);
  });
});
