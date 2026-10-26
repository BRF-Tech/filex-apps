// Development mock of the store's API (api/openapi.yaml) with realistic,
// stateful data, so every page can be built, measured and photographed before
// the backend lane's routes land. Run: `pnpm mock` (port 8080 or MOCK_PORT),
// then `pnpm dev` or `pnpm preview` (they proxy /api, /v1, /auth to it).
//
// Sign-in is simulated: /auth/github/login sets a cookie naming a persona and
// redirects back. `?as=` picks one, all GitHub accounts like on the server:
// publisher (jdoe), newcomer (no publisher), reviewer (triage on the
// administration repository) and admin (write on it).
// MOCK_NO_SIGNIN=1 answers 503 not_configured, like a server without OAuth.
//
// Mock only: nothing here reaches the bundle (src/ never imports it).
import { readFileSync } from 'node:fs';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import type { components } from '../src/api/schema';
import { buildState, type AppRecord, type State } from './fixtures.ts';

type Sch = components['schemas'];
type Persona = 'publisher' | 'newcomer' | 'reviewer' | 'admin';

const PORT = Number(process.env.MOCK_PORT ?? 8080);
const NO_SIGNIN = process.env.MOCK_NO_SIGNIN === '1';
const PIPELINE_STEP_MS = Number(process.env.MOCK_STEP_MS ?? 2500);

let state: State = buildState();
// Test-only: GET paths that answer an error (POST /__mock/fail?path=..&status=429; reset clears).
const failing = new Map<string, number>();
const members = new Map<string, Set<string>>();
const live = new Map<number, number>();
// Install links, saved instances and licenses of this mock run.
const URL_MSG = 'Enter your filex address with https and without a path, e.g. https://files.example.com.';
const saved = new Map<string, Sch['SavedInstance'][]>();
const intents = new Map<string, { id: string; app: string; version: string; origin: string; used: boolean }>();
const licences = new Map<string, { lic: Sch['License']; key: string; activations: Sch['Activation'][] }>();
const mockHex = (n: number) => Array.from({ length: n }, () => '0123456789abcdef'[Math.floor(Math.random() * 16)]).join('');
const mockUuid = () => `${mockHex(8)}-${mockHex(4)}-4${mockHex(3)}-a${mockHex(3)}-${mockHex(12)}`;
// Notifications of this mock run: one webhook list and one browser-event
// choice per persona (the administrator starts with one webhook).
const USER_KINDS: Sch['NotificationKind'][] = ['submission.approved', 'submission.rejected', 'filex.released'];
const ADMIN_KINDS: Sch['NotificationKind'][] = [...USER_KINDS, 'submission.app_new', 'submission.version_new', 'publisher.requested', 'pipeline.error'];
const hooks = new Map<string, Sch['Webhook'][]>();
const pushEvents = new Map<string, Sch['NotificationKind'][]>();
function hooksOf(p: string): Sch['Webhook'][] {
  if (!hooks.has(p)) {
    hooks.set(
      p,
      p === 'admin'
        ? [
            {
              id: '6f9619ff-8b86-4d01-b42d-00cf4fc964ff',
              name: 'Team alerts',
              format: 'generic',
              url_hint: 'https://notify.example/…',
              header_name: 'Authorization',
              signed: true,
              events: ADMIN_KINDS,
              created_at: '2026-10-20T08:00:00Z',
              last: { status: 'delivered', http_status: 201, at: '2026-10-20T09:00:00Z' },
            },
          ]
        : [],
    );
  }
  return hooks.get(p)!;
}
function instancesOf(p: string): Sch['SavedInstance'][] {
  if (!saved.has(p)) saved.set(p, []);
  return saved.get(p)!;
}
function mockOrigin(raw: string): string {
  const m = /^(https:\/\/[a-z0-9.-]+(?::\d+)?|http:\/\/(?:localhost|127\.0\.0\.1)(?::\d+)?)\/?$/i.exec(raw.trim());
  return m ? m[1].toLowerCase() : '';
}
function seedLicences() {
  licences.clear();
  saved.clear();
  hooks.clear();
  pushEvents.clear();
  intents.clear();
  const lic: Sch['License'] = {
    id: '0b9e6a3c-1f2d-4e5a-9b8c-7d6e5f4a3b2c',
    app: 'markdown-preview',
    key_prefix: 'FXL-7K2Q',
    licensee_name: 'Northwind Labs',
    licensee_contact: 'ops@northwind.example',
    seats: 3,
    seats_used: 1,
    valid_until: '2027-10-04',
    status: 'active',
    created_by: 'jdoe',
    created_at: '2026-09-20T10:00:00Z',
  };
  licences.set(lic.id, {
    lic,
    key: 'FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-4C',
    activations: [{ instance_id: 'nw-files-01', first_seen: '2026-09-21T08:00:00Z', last_seen: '2026-10-04T08:00:00Z', active: true, filex_version: 'v0.51.0' }],
  });
  saved.set('publisher', [{ id: '5c1f2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b', label: 'Office filex', url: 'https://files.jdoe.example', created_at: '2026-09-30T12:00:00Z', connected: false }]);
}
seedLicences();

function resetMembers() {
  members.clear();
  members.set('jdoe', new Set(['jdoe']));
  members.set('acme-tools', new Set(['jdoe']));
  members.set('brf-tech', new Set(['filex-maintainer']));
}
resetMembers();

const ADMIN_REPO = 'acme/store-admins';
const PERSONAS: Record<Persona, { login: string; name: string; id: number; roles: ('reviewer' | 'admin')[]; permission: string }> = {
  publisher: { login: 'jdoe', name: 'Jane Doe', id: 11, roles: [], permission: 'none' },
  newcomer: { login: 'newdev', name: 'New Developer', id: 12, roles: [], permission: 'none' },
  reviewer: { login: 'rita', name: 'Rita Reviewer', id: 21, roles: ['reviewer'], permission: 'triage' },
  admin: { login: 'alice', name: 'Alice', id: 1, roles: ['admin'], permission: 'write' },
};

function persona(req: IncomingMessage): Persona | null {
  const m = /(?:^|;\s*)fapps_session=([a-z]+)/.exec(req.headers.cookie ?? '');
  const p = m?.[1] as Persona | undefined;
  return p && p in PERSONAS ? p : null;
}

function me(p: Persona): Sch['Me'] {
  const who = PERSONAS[p];
  const pubs = state.publishers
    .filter((x) => members.get(x.id)?.has(who.login))
    .map((x) => ({ id: x.id, name: x.name, verified: x.verified, official: x.official, role: 'owner' as const, status: x.status }));
  return {
    account: { id: who.id, display_name: who.name },
    identity: { provider: 'github', login: who.login },
    roles: who.roles,
    role_source: `github repo permission ${ADMIN_REPO}: ${who.permission}`,
    publishers: pubs,
    csrf_token: 'csrf-' + p,
    providers: PROVIDERS,
  };
}

const PROVIDERS: Sch['SignInProviders'] = { github: !NO_SIGNIN };

// ---------------------------------------------------------------- http helpers
function send(res: ServerResponse, status: number, body?: unknown, headers: Record<string, string> = {}) {
  const data = body === undefined ? '' : JSON.stringify(body);
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store', ...headers });
  res.end(data);
}
const err = (res: ServerResponse, status: number, error: string, message: string, extra: Record<string, unknown> = {}) =>
  send(res, status, { error, message, ...extra });

async function readBody(req: IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = [];
  for await (const c of req) chunks.push(c as Buffer);
  if (!chunks.length) return {};
  try {
    return JSON.parse(Buffer.concat(chunks).toString('utf8')) as Record<string, unknown>;
  } catch {
    return {};
  }
}

function page<T>(items: T[], q: URLSearchParams) {
  const per = Math.min(100, Math.max(1, Number(q.get('per_page') ?? 50) || 50));
  const p = Math.max(1, Number(q.get('page') ?? 1) || 1);
  return { items: items.slice((p - 1) * per, p * per), total: items.length, page: p, per_page: per };
}

// ---------------------------------------------------------------- semver ranges (the mock's simple version)
function cmp(a: string, b: string): number {
  const pa = a.split('.').map(Number);
  const pb = b.split('.').map(Number);
  for (let i = 0; i < 3; i++) if ((pa[i] ?? 0) !== (pb[i] ?? 0)) return (pa[i] ?? 0) - (pb[i] ?? 0);
  return 0;
}
function admits(range: string, v: string): boolean {
  if (!range.trim()) return true;
  return range.split('||').some((alt) =>
    alt
      .trim()
      .split(/\s+/)
      .every((c) => {
        const m = /^(>=|<=|>|<|=)?(\d+\.\d+\.\d+)$/.exec(c);
        if (!m) return false;
        const d = cmp(v, m[2]);
        switch (m[1]) {
          case '>=':
            return d >= 0;
          case '<=':
            return d <= 0;
          case '>':
            return d > 0;
          case '<':
            return d < 0;
          default:
            return d === 0;
        }
      }),
  );
}

function summary(a: AppRecord, filex?: string): Sch['AppSummary'] {
  const d = a.detail;
  const s: Sch['AppSummary'] = {
    name: d.name,
    kind: d.kind,
    label: d.label,
    summary: d.summary,
    publisher: d.publisher,
    categories: d.categories,
    icon_url: d.icon_url,
    latest: d.latest,
    updated_at: d.updated_at,
    pricing: d.pricing ?? { model: 'free' },
  };
  if (filex) {
    const v = d.versions.find((x) => !x.yanked && admits(x.filex, filex));
    if (v) s.compatible = v.version;
  }
  return s;
}

// ---- file-type searches. The server reads any search made of extensions
// and matches the manifests (backend/internal/filetypes); the mock answers
// these few, with the apps' file types (fixtures) and filex's own support
// from the very data the server embeds.
interface BuiltinData {
  filex: string;
  engines: Record<string, { label: string; kind: 'service' | 'tool'; setting?: string; in_image?: boolean }>;
  types: Record<string, { can: Sch['BuiltinFileType']['can'] }>;
  mimes: Record<string, string>;
}
const BUILTIN = JSON.parse(readFileSync(new URL('../../backend/internal/filetypes/filex-builtin.json', import.meta.url), 'utf8')) as BuiltinData;
const TYPE_SEARCHES: Record<string, string[]> = {
  docx: ['docx'],
  '.docx': ['docx'],
  '*.docx': ['docx'],
  pdf: ['pdf'],
  '.pdf': ['pdf'],
  drawio: ['drawio'],
  '.drawio': ['drawio'],
  '.xyz': ['xyz'],
  'pdf docx': ['pdf', 'docx'],
  mp4: ['mp4'],
  '.mp4': ['mp4'],
  md: ['md'],
  '.md': ['md'],
};

function fileTypeSearch(exts: string[]): Sch['AppPage'] {
  const named: Sch['AppSummary'][] = [];
  const every: Sch['AppSummary'][] = [];
  for (const a of state.apps.filter((x) => x.detail.status === 'listed')) {
    const v = a.versions[a.detail.latest];
    const types = v?.file_types ?? [];
    // The manifest's roles for each kind, then what the listing says it
    // converts the kind into (the server's filetypes.Match).
    const uses = exts.flatMap((e) => {
      const own = types.filter((u) => u.ext === e).flatMap((u) => u.roles);
      const conv = (v?.conversions ?? []).find((r) => r.from.includes(e))?.roles ?? [];
      return own.length || conv.length ? [{ ext: e, roles: [...own, ...conv] }] : [];
    });
    const any = types.filter((u) => u.any_file);
    if (!uses.length && !any.length) continue;
    (uses.length ? named : every).push({ ...summary(a), matched_extensions: [...uses, ...any] });
  }
  const builtin = exts.flatMap((e) => (BUILTIN.types[e] ? [{ ext: e, mime: BUILTIN.mimes[e], can: BUILTIN.types[e].can }] : []));
  const needs = [...new Set(builtin.flatMap((b) => b.can.flatMap((c) => (c.needs ? [c.needs] : []))))].sort();
  const items = [...named, ...every];
  return {
    items,
    total: items.length,
    page: 1,
    per_page: 50,
    file_types: { extensions: exts, named: named.length, filex: BUILTIN.filex, builtin, engines: needs.map((id) => ({ id, ...BUILTIN.engines[id] })), near: [] },
  };
}

function listApps(q: URLSearchParams): Sch['AppPage'] {
  const typed = TYPE_SEARCHES[(q.get('q') ?? '').trim().toLowerCase()];
  if (typed) return fileTypeSearch(typed);
  const words = (q.get('q') ?? '').toLowerCase().split(/\s+/).filter(Boolean);
  const filex = q.get('filex') ?? '';
  let apps = state.apps.filter((a) => a.detail.status === 'listed');
  if (words.length)
    apps = apps.filter((a) => {
      const hay = [a.detail.name, a.detail.label.en, a.detail.summary?.en ?? '', a.detail.publisher.name].join(' ').toLowerCase();
      return words.every((w) => hay.includes(w));
    });
  if (q.get('kind')) apps = apps.filter((a) => a.detail.kind === q.get('kind'));
  if (q.get('category')) apps = apps.filter((a) => a.detail.categories.includes(q.get('category')!));
  if (q.get('publisher')) apps = apps.filter((a) => a.detail.publisher.id === q.get('publisher'));
  if (q.get('verified') === 'true') apps = apps.filter((a) => a.detail.publisher.verified);
  if (filex) apps = apps.filter((a) => a.detail.versions.some((v) => !v.yanked && admits(v.filex, filex)));
  const sort = q.get('sort') ?? 'name';
  apps = [...apps].sort((x, y) =>
    sort === 'updated' ? y.detail.updated_at.localeCompare(x.detail.updated_at) : (x.detail.label.en ?? '').localeCompare(y.detail.label.en ?? ''),
  );
  return page(apps.map((a) => summary(a, filex || undefined)), q);
}

// GET /api/v1/publishers: the active publishers, with how many listed apps
// each has (contract: PublisherPage).
function listPublishers(q: URLSearchParams): Sch['PublisherPage'] {
  const words = (q.get('q') ?? '').toLowerCase().split(/\s+/).filter(Boolean);
  const count = (id: string) => state.apps.filter((a) => a.detail.publisher.id === id && a.detail.status === 'listed').length;
  let pubs: Sch['PublisherListItem'][] = state.publishers
    .filter((x) => x.status === 'active')
    .map((x) => ({
      id: x.id,
      name: x.name,
      verified: x.verified,
      official: x.official,
      github_owner: x.github_owner,
      owner_type: x.owner_type,
      ...(x.homepage ? { homepage: x.homepage } : {}),
      ...(x.description ? { description: x.description } : {}),
      ...(x.avatar_url ? { avatar_url: x.avatar_url } : {}),
      apps: count(x.id),
    }));
  if (words.length) pubs = pubs.filter((x) => words.every((w) => [x.id, x.name, x.github_owner, x.description ?? ''].join(' ').toLowerCase().includes(w)));
  pubs.sort((a, b) => (q.get('sort') === 'apps' ? b.apps - a.apps || a.name.localeCompare(b.name) : a.name.localeCompare(b.name)));
  return page(pubs, q);
}

// ---------------------------------------------------------------- the pipeline, simulated
function submissionView(s: Sch['SubmissionDetail']): Sch['SubmissionDetail'] {
  const t0 = live.get(s.id);
  if (t0 === undefined || !['queued', 'resolving', 'validating'].includes(s.status)) return s;
  const step = Math.floor((Date.now() - t0) / PIPELINE_STEP_MS);
  const template = state.submissions.find((x) => x.id === 101)!;
  if (step <= 0) return { ...s, status: 'queued', checks: [] };
  if (step === 1) return { ...s, status: 'resolving', checks: template.checks.slice(0, 3).map((c) => ({ ...c, kind: c.kind })) };
  if (step === 2)
    return {
      ...s,
      status: 'validating',
      checks: [...template.checks.slice(0, 7), { kind: 'dry_run', validator: 'latest', status: 'running', started_at: new Date().toISOString() }],
    };
  s.status = 'needs_review';
  s.updated_at = new Date().toISOString();
  s.checks = template.checks.map((c) => ({ ...c, status: c.status === 'warned' ? 'passed' : c.status, code: undefined, message: c.kind === 'policy' ? 'Version is new. License: MIT.' : c.message }));
  s.dry_runs = template.dry_runs;
  s.resolution = { ...template.resolution, ref: s.ref, version: s.version };
  live.delete(s.id);
  return s;
}

function audit(p: Persona | null, action: string, type: string, id: string, data: Record<string, unknown> = {}) {
  const role = p === 'admin' ? 'admin' : p === 'reviewer' ? 'reviewer' : p ? 'publisher' : 'system';
  state.audit.unshift({ id: (state.audit[0]?.id ?? 0) + 1, at: new Date().toISOString(), actor: p ? PERSONAS[p].login : undefined, actor_role: role, action, target_type: type, target_id: id, data });
}
function publishIndex(reason: string): Sch['IndexRelease'] {
  state.serial += 1;
  const now = new Date();
  return {
    serial: state.serial,
    generated_at: now.toISOString(),
    expires_at: new Date(now.getTime() + 60 * 86400000).toISOString(),
    sha256: 'f'.repeat(8) + String(state.serial).padStart(56, '0'),
    key_id: 'idx-2026-demo',
    reason,
  };
}

const isReviewer = (p: Persona | null) => p === 'reviewer' || p === 'admin';

// ---------------------------------------------------------------- router
async function handle(req: IncomingMessage, res: ServerResponse) {
  const url = new URL(req.url ?? '/', 'http://mock');
  const path = url.pathname;
  const q = url.searchParams;
  const method = req.method ?? 'GET';
  const p = persona(req);
  let m: RegExpExecArray | null;

  if (method !== 'GET' && method !== 'HEAD' && (path.startsWith('/api/') || path === '/auth/logout')) {
    if (!p || req.headers['x-csrf-token'] !== 'csrf-' + p) return err(res, 403, 'csrf_failed', 'The CSRF token is missing or wrong.');
  }

  if (path === '/__mock/reset' && method === 'POST') {
    state = buildState();
    resetMembers();
    seedLicences();
    live.clear();
    failing.clear();
    return send(res, 204);
  }
  if (path === '/__mock/fail' && method === 'POST') {
    failing.set(q.get('path') ?? '', Number(q.get('status') ?? 429));
    return send(res, 204);
  }
  if (method === 'GET' && failing.has(path)) {
    const status = failing.get(path)!;
    return err(res, status, status === 429 ? 'rate_limited' : 'failed', status === 429 ? 'Too many requests.' : 'The request failed.');
  }
  if (path === '/healthz') return send(res, 200, { status: 'ok', version: 'mock', started_at: new Date().toISOString() });

  // ---- index (filex contract)
  if (path === '/v1/index.json') {
    const body = JSON.stringify({ schema: 1, serial: state.serial, generated_at: new Date().toISOString(), apps: state.apps.length }, null, 2) + '\n';
    res.writeHead(200, { 'Content-Type': 'application/json', ETag: `"${'a'.repeat(56)}${String(state.serial).padStart(8, '0')}"`, 'X-Index-Serial': String(state.serial) });
    return res.end(method === 'HEAD' ? undefined : body);
  }
  if (path === '/v1/index.json.sig') {
    res.writeHead(200, { 'Content-Type': 'text/plain', 'X-Index-Serial': String(state.serial) });
    return res.end('ab'.repeat(64) + '\n');
  }
  if (path === '/v1/keys.json') return send(res, 200, { keys: state.keys.map((k) => ({ id: k.id, use: k.use, ed25519: k.public_hex, status: k.status })) });
  if ((m = /^\/v1\/media\/([0-9a-f]{64}\.png)$/.exec(path))) {
    const media = state.media.get(path);
    if (!media) return err(res, 404, 'not_found', 'No such image.');
    res.writeHead(200, { 'Content-Type': 'image/png', 'Cache-Control': 'public, max-age=31536000, immutable' });
    return res.end(media.bytes);
  }

  // ---- auth
  if (path === '/auth/github/login') {
    if (NO_SIGNIN) return err(res, 503, 'not_configured', 'This sign-in is not configured on this server.');
    const asked = q.get('as') as Persona | null;
    const who: Persona = asked && asked in PERSONAS ? asked : 'publisher';
    const back = /^\/(?![/\\])/.test(q.get('return_to') ?? '') ? q.get('return_to')! : '/';
    res.writeHead(302, { Location: back, 'Set-Cookie': `fapps_session=${who}; Path=/; HttpOnly; SameSite=Lax` });
    return res.end();
  }
  if (path === '/auth/logout' && method === 'POST') {
    res.writeHead(204, { 'Set-Cookie': 'fapps_session=; Path=/; Max-Age=0' });
    return res.end();
  }

  // ---- catalog
  if (path === '/api/v1/apps' && method === 'GET') return send(res, 200, listApps(q));
  if ((m = /^\/api\/v1\/apps\/([^/]+)$/.exec(path)) && method === 'GET') {
    const a = state.apps.find((x) => x.detail.name === decodeURIComponent(m![1]));
    return a ? send(res, 200, a.detail) : err(res, 404, 'not_found', 'No such app.');
  }
  if ((m = /^\/api\/v1\/apps\/([^/]+)\/versions\/([^/]+)$/.exec(path)) && method === 'GET') {
    const a = state.apps.find((x) => x.detail.name === decodeURIComponent(m![1]));
    const v = a?.versions[decodeURIComponent(m[2])];
    return v ? send(res, 200, v) : err(res, 404, 'not_found', 'No such version.');
  }
  if (path === '/api/v1/publishers' && method === 'GET') return send(res, 200, listPublishers(q));
  if ((m = /^\/api\/v1\/publishers\/([^/]+)$/.exec(path)) && method === 'GET') {
    const pub = state.publishers.find((x) => x.id === decodeURIComponent(m![1]) && x.status !== 'pending');
    if (!pub) return err(res, 404, 'not_found', 'No such publisher.');
    return send(res, 200, { publisher: pub, apps: state.apps.filter((a) => a.detail.publisher.id === pub.id && a.detail.status === 'listed').map((a) => summary(a)) });
  }
  if (path === '/api/v1/categories') return send(res, 200, { items: state.categories });
  // How the store presents itself (FAPPS_STORE_NAME, FAPPS_OPERATOR_*, ...):
  // a made-up operator, so the pages are measured with the footer's line.
  if (path === '/api/v1/store' && method === 'GET') {
    return send(res, 200, {
      name: 'Example Apps',
      operator_name: 'Example Org',
      operator_url: 'https://www.example.com/',
      contact: 'mailto:store@example.com',
      terms_url: 'https://www.example.com/terms',
      privacy_url: 'https://www.example.com/privacy',
    });
  }

  // ---- me
  if (path === '/api/v1/me') return p ? send(res, 200, me(p)) : err(res, 401, 'unauthorized', 'Sign in first.', { providers: PROVIDERS });

  // ---- what a filex server calls (the mock signs nothing: the signature is a stand-in)
  if (path === '/v1/licenses/verify' && method === 'POST') {
    const b = await readBody(req);
    const now = new Date();
    const known = [...licences.values()].find((l) => l.key === String(b.key ?? '').trim().toUpperCase());
    const result = !known ? 'invalid' : known.lic.app !== b.app ? 'wrong_app' : known.lic.status === 'revoked' ? 'revoked' : 'valid';
    const stamp = (ms: number) => new Date(now.getTime() + ms).toISOString().slice(0, 19) + 'Z';
    const payload = { result, app: String(b.app ?? ''), instance_id: String(b.instance_id ?? ''), checked_at: stamp(0), next_check_by: stamp(86400000), grace_until: stamp(14 * 86400000) };
    return send(res, 200, { payload, key_id: 'lic-2026-demo', signature: '00'.repeat(64) });
  }
  if ((m = /^\/v1\/install\/([A-Za-z0-9_-]{43})(\/complete)?$/.exec(path))) {
    const it = intents.get(m[1]);
    if (!it) return err(res, 404, 'not_found', 'No such install link.');
    if (it.used) return err(res, 410, 'intent_used', 'This install link was used already.');
    if (m[2] && method === 'POST') {
      it.used = true;
      return send(res, 200, { token_id: it.id, result: 'installed', completed_at: new Date().toISOString() });
    }
    return send(res, 200, { payload: { store: 'http://mock', filex_origin: it.origin, token_id: it.id, app: it.app, version: it.version }, key_id: 'idx-2026-demo', signature: '00'.repeat(64) });
  }

  if (path.startsWith('/api/') && !p) return err(res, 401, 'unauthorized', 'Not signed in.');
  const who = p ? PERSONAS[p] : null;

  // ---- notifications (no push service here: browser notifications are off)
  if (path === '/api/v1/me/notifications' && method === 'GET') {
    const kinds = who!.roles.includes('admin') ? ADMIN_KINDS : USER_KINDS;
    return send(res, 200, {
      events: kinds.map((k) => ({ kind: k, admin: !USER_KINDS.includes(k) })),
      formats: ['generic', 'slack', 'discord', 'teams'],
      push: { available: false, events: pushEvents.get(p!) ?? kinds, browsers: [] },
      webhooks: hooksOf(p!),
    });
  }
  if (path === '/api/v1/me/notifications/push' && method === 'PUT') {
    const b = await readBody(req);
    pushEvents.set(p!, (b.events as Sch['NotificationKind'][]) ?? []);
    return send(res, 200, { events: pushEvents.get(p!) });
  }
  if (path === '/api/v1/me/webhooks' && method === 'POST') {
    const b = await readBody(req);
    const url = String(b.url ?? '');
    if (!/^https:\/\/[^/\s]+/.test(url)) return err(res, 400, 'invalid_field', 'url must be an https address.');
    const u = new URL(url);
    const hook: Sch['Webhook'] = {
      id: mockUuid(),
      name: String(b.name ?? ''),
      format: b.format as Sch['WebhookFormat'],
      url_hint: u.origin + (u.pathname.length > 1 || u.search ? '/…' : ''),
      header_name: b.header_name ? String(b.header_name) : undefined,
      signed: b.format === 'generic',
      events: (b.events as Sch['NotificationKind'][]) ?? [],
      created_at: new Date().toISOString(),
    };
    hooksOf(p!).push(hook);
    return send(res, 201, b.format === 'generic' ? { webhook: hook, secret: 'whsec_' + mockHex(43) } : { webhook: hook });
  }
  if ((m = /^\/api\/v1\/me\/webhooks\/([0-9a-f-]{36})(\/test)?$/.exec(path))) {
    const list = hooksOf(p!);
    const i = list.findIndex((x) => x.id === m![1]);
    if (i < 0) return err(res, 404, 'not_found', 'No such webhook.');
    if (m[2] && method === 'POST') {
      list[i] = { ...list[i], last: { status: 'delivered', http_status: 204, at: new Date().toISOString() } };
      return send(res, 200, { status: 'delivered', http_status: 204 });
    }
    if (!m[2] && method === 'PATCH') {
      const b = await readBody(req);
      list[i] = { ...list[i], name: b.name ? String(b.name) : list[i].name, events: (b.events as Sch['NotificationKind'][]) ?? list[i].events };
      return send(res, 200, list[i]);
    }
    if (!m[2] && method === 'DELETE') {
      list.splice(i, 1);
      return send(res, 204);
    }
  }

  // ---- install links and saved instances (one list per persona)
  if (path === '/api/v1/me/instances' && method === 'GET') return send(res, 200, { items: instancesOf(p!) });
  if (path === '/api/v1/me/instances' && method === 'POST') {
    const b = await readBody(req);
    const origin = mockOrigin(String(b.url ?? ''));
    if (!origin) return err(res, 400, 'filex_url_invalid', URL_MSG);
    const list = instancesOf(p!);
    const had = list.find((i) => i.url === origin);
    if (had) return send(res, 200, had);
    const inst = { id: mockUuid(), label: String(b.label ?? '').trim() || new URL(origin).host, url: origin, created_at: new Date().toISOString(), connected: false };
    list.push(inst);
    return send(res, 201, inst);
  }
  if ((m = /^\/api\/v1\/me\/instances\/([0-9a-f-]{36})$/.exec(path)) && method === 'DELETE') {
    const list = instancesOf(p!);
    const i = list.findIndex((x) => x.id === m![1]);
    if (i < 0) return err(res, 404, 'not_found', 'No such saved instance.');
    list.splice(i, 1);
    return send(res, 204);
  }
  if (path === '/api/v1/install-intents' && method === 'POST') {
    const b = await readBody(req);
    const a = state.apps.find((x) => x.detail.name === b.app);
    if (!a) return err(res, 404, 'not_found', 'No such app.');
    if (a.detail.status === 'revoked') return err(res, 409, 'app_revoked', 'The app is revoked; it is not installed any more.');
    let origin = '';
    let instance: Sch['SavedInstance'] | undefined;
    if (b.instance_id) {
      instance = instancesOf(p!).find((x) => x.id === b.instance_id);
      if (!instance) return err(res, 404, 'instance_not_found', 'No such saved instance.');
      origin = instance.url;
    } else {
      origin = mockOrigin(String(b.filex_url ?? ''));
      if (!origin) return err(res, 400, 'filex_url_invalid', URL_MSG);
      if (b.save_instance) {
        const list = instancesOf(p!);
        instance = list.find((x) => x.url === origin);
        if (!instance) {
          instance = { id: mockUuid(), label: String(b.instance_label ?? '').trim() || new URL(origin).host, url: origin, created_at: new Date().toISOString(), connected: false };
          list.push(instance);
        }
      }
    }
    const token = Buffer.from(Array.from({ length: 32 }, () => Math.floor(Math.random() * 256))).toString('base64url');
    const version = String(b.version ?? '') || a.detail.latest;
    intents.set(token, { id: mockHex(16), app: a.detail.name, version, origin, used: false });
    return send(res, 201, {
      token,
      magic_link: `${origin}/admin/store-install#store=${encodeURIComponent('http://' + (req.headers.host ?? 'mock'))}&intent=${token}`,
      expires_at: new Date(Date.now() + 30 * 60000).toISOString(),
      app: a.detail.name,
      version,
      ...(instance ? { instance } : {}),
    });
  }

  // ---- price and licenses: the app's publisher's members and administrators
  if (path === '/api/v1/me/apps' && method === 'GET') {
    const mine = state.apps.filter((a) => members.get(a.detail.publisher.id)?.has(who!.login));
    return send(res, 200, { items: mine.map((a) => ({ ...summary(a), status: a.detail.status })) });
  }
  if ((m = /^\/api\/v1\/apps\/([^/]+)\/(pricing|licenses)$/.exec(path))) {
    const a = state.apps.find((x) => x.detail.name === decodeURIComponent(m![1]));
    if (!a) return err(res, 404, 'not_found', 'No such app.');
    if (p !== 'admin' && !members.get(a.detail.publisher.id)?.has(who!.login))
      return err(res, 403, 'not_a_member', 'Only a member of the publisher or an administrator manages the licenses of this app.');
    if (m[2] === 'pricing' && method === 'PUT') {
      const b = await readBody(req);
      a.detail.pricing =
        b.model === 'paid'
          ? { model: 'paid', ...(b.price_text ? { price_text: String(b.price_text) } : {}), ...(b.purchase_url ? { purchase_url: String(b.purchase_url) } : {}) }
          : { model: 'free' };
      return send(res, 200, a.detail.pricing);
    }
    if (m[2] === 'licenses' && method === 'GET') {
      const items = [...licences.values()].filter((l) => l.lic.app === a.detail.name).map((l) => l.lic).reverse();
      return send(res, 200, page(items, q));
    }
    if (m[2] === 'licenses' && method === 'POST') {
      const b = await readBody(req);
      if (!String(b.licensee_name ?? '').trim()) return err(res, 400, 'invalid_field', 'licensee_name is required.');
      const raw = Array.from({ length: 26 }, () => '0123456789ABCDEFGHJKMNPQRSTVWXYZ'[Math.floor(Math.random() * 32)]).join('');
      const key = 'FXL-' + raw.replace(/(.{4})(?=.)/g, '$1-');
      const lic: Sch['License'] = {
        id: mockUuid(),
        app: a.detail.name,
        key_prefix: key.slice(0, 8),
        licensee_name: String(b.licensee_name).trim(),
        ...(b.licensee_contact ? { licensee_contact: String(b.licensee_contact) } : {}),
        seats: Number(b.seats ?? 1) || 1,
        seats_used: 0,
        status: 'active',
        created_by: who!.login,
        created_at: new Date().toISOString(),
        ...(b.valid_until ? { valid_until: String(b.valid_until) } : {}),
        ...(b.updates_until ? { updates_until: String(b.updates_until) } : {}),
      };
      licences.set(lic.id, { lic, key, activations: [] });
      return send(res, 201, { license: lic, key });
    }
  }
  if ((m = /^\/api\/v1\/licenses\/([0-9a-f-]{36})\/(revoke|activations)(?:\/([^/]+)\/release)?$/.exec(path))) {
    const l = licences.get(m[1]);
    if (!l) return err(res, 404, 'not_found', 'No such license.');
    if (m[2] === 'revoke' && method === 'POST') {
      if (l.lic.status === 'revoked') return err(res, 409, 'already_revoked', 'The license is already revoked.');
      l.lic = { ...l.lic, status: 'revoked', revoked_at: new Date().toISOString() };
      return send(res, 200, l.lic);
    }
    if (m[2] === 'activations' && !m[3] && method === 'GET') return send(res, 200, { items: l.activations });
    if (m[2] === 'activations' && m[3] && method === 'POST') {
      const act = l.activations.find((x) => x.instance_id === decodeURIComponent(m![3]));
      if (!act) return err(res, 404, 'not_found', 'No such activation.');
      act.active = false;
      act.released_at = new Date().toISOString();
      l.lic = { ...l.lic, seats_used: l.activations.filter((x) => x.active).length };
      return send(res, 200, act);
    }
  }

  // ---- publishing
  if (path === '/api/v1/publishers' && method === 'POST') {
    if (!who) return err(res, 401, 'unauthorized', 'Not signed in.');
    const b = await readBody(req);
    const id = String(b.id ?? '');
    if (!/^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/.test(id)) return err(res, 400, 'invalid_field', 'The publisher id is not valid.');
    if (state.publishers.some((x) => x.id === id)) return err(res, 409, 'publisher_exists', `The publisher id "${id}" is taken.`);
    const pub: Sch['Publisher'] = { id, name: String(b.name ?? id), verified: false, official: false, github_owner: String(b.github_owner ?? who.login), owner_type: b.github_owner && b.github_owner !== who.login ? 'Organization' : 'User', status: 'pending', created_at: new Date().toISOString(), ...(b.homepage ? { homepage: String(b.homepage) } : {}) };
    state.publishers.push(pub);
    members.set(id, new Set([who.login]));
    audit(p, 'publisher.request', 'publisher', id);
    return send(res, 201, pub);
  }
  if (path === '/api/v1/submissions' && method === 'POST') {
    if (!who) return err(res, 401, 'unauthorized', 'Not signed in.');
    const b = await readBody(req);
    const pub = state.publishers.find((x) => x.id === b.publisher);
    if (!pub || !members.get(pub.id)?.has(who.login)) return err(res, 403, 'not_a_member', 'You are not a member of this publisher.');
    if (pub.status !== 'active') return err(res, 403, 'forbidden', 'This publisher is not active.');
    const repo = String(b.repo ?? '').replace(/^https:\/\/github\.com\//, '');
    const ref = String(b.ref ?? '');
    if (ref === 'main' || ref === 'master') return err(res, 400, 'not_a_tag', `"${ref}" is a branch; submit a tag.`);
    const open = state.submissions.find((s) => s.repo === repo && s.ref === ref && ['queued', 'resolving', 'validating', 'needs_review'].includes(s.status));
    if (open) return err(res, 409, 'submission_open', 'This repository at this tag is already in the pipeline.', { submission: open });
    const now = new Date().toISOString();
    const s: Sch['SubmissionDetail'] = {
      id: state.nextSubmission++,
      publisher: { id: pub.id, name: pub.name, verified: pub.verified, official: pub.official },
      repo,
      ref,
      app: repo.split('/')[1]?.replace(/^filex-/, ''),
      version: ref.replace(/^v/, ''),
      status: 'queued',
      attempt: 1,
      submitted_by: who.login,
      created_at: now,
      updated_at: now,
      ...(b.note ? { note: String(b.note) } : {}),
      checks: [],
      dry_runs: [],
      reviews: [],
    };
    state.submissions.unshift(s);
    live.set(s.id, Date.now());
    audit(p, 'submission.create', 'submission', String(s.id));
    return send(res, 202, s);
  }
  if (path === '/api/v1/submissions' && method === 'GET') {
    let subs = state.submissions.map(submissionView);
    if (!isReviewer(p)) subs = subs.filter((s) => members.get(s.publisher.id)?.has(who!.login));
    if (q.get('status')) subs = subs.filter((s) => s.status === q.get('status'));
    if (q.get('publisher')) subs = subs.filter((s) => s.publisher.id === q.get('publisher'));
    const pg = page(subs, q);
    return send(res, 200, { items: pg.items, total: pg.total });
  }
  if ((m = /^\/api\/v1\/submissions\/(\d+)(?:\/(withdraw|retry))?$/.exec(path))) {
    const s = state.submissions.find((x) => x.id === Number(m![1]));
    if (!s || (!isReviewer(p) && !members.get(s.publisher.id)?.has(who!.login))) return err(res, 404, 'not_found', 'No such submission.');
    if (!m[2]) return send(res, 200, submissionView(s));
    if (m[2] === 'withdraw') {
      if (['approved', 'rejected', 'withdrawn'].includes(s.status)) return err(res, 409, 'already_decided', 'This submission is decided.');
      s.status = 'withdrawn';
      live.delete(s.id);
    } else {
      if (s.status !== 'failed') return err(res, 409, 'not_failed', 'Only a failed submission runs again.');
      s.status = 'queued';
      s.attempt += 1;
      s.failure = undefined;
      live.set(s.id, Date.now());
      s.updated_at = new Date().toISOString();
      return send(res, 202, submissionView(s));
    }
    s.updated_at = new Date().toISOString();
    audit(p, 'submission.' + m[2], 'submission', String(s.id));
    return send(res, 200, s);
  }

  // ---- review
  if (path.startsWith('/api/v1/review/') && !isReviewer(p)) return err(res, 403, 'forbidden', 'This needs the reviewer role.');
  if (path === '/api/v1/review/queue') {
    const subs = state.submissions.map(submissionView).filter((s) => s.status === 'needs_review').sort((a, b) => a.created_at.localeCompare(b.created_at));
    const pg = page(subs, q);
    return send(res, 200, { items: pg.items, total: pg.total });
  }
  if ((m = /^\/api\/v1\/review\/submissions\/(\d+)\/(approve|reject|comment|recheck)$/.exec(path)) && method === 'POST') {
    const s = state.submissions.find((x) => x.id === Number(m![1]));
    if (!s) return err(res, 404, 'not_found', 'No such submission.');
    const b = await readBody(req);
    const reviewId = 600 + Math.floor(Math.random() * 1000);
    if (m[2] === 'comment') {
      const r: Sch['Review'] = { id: reviewId, reviewer: who!.login, decision: 'comment', note: String(b.reason ?? ''), created_at: new Date().toISOString() };
      s.reviews.push(r);
      return send(res, 201, r);
    }
    if (m[2] === 'recheck') {
      s.status = 'validating';
      live.set(s.id, Date.now() - 2 * PIPELINE_STEP_MS);
      return send(res, 202, s);
    }
    if (s.status !== 'needs_review') return err(res, 409, 'not_pending', 'This submission is not waiting for a decision.');
    s.decided_at = s.updated_at = new Date().toISOString();
    if (m[2] === 'reject') {
      s.status = 'rejected';
      s.reviews.push({ id: reviewId, reviewer: who!.login, decision: 'reject', note: String(b.reason ?? ''), created_at: s.decided_at });
      audit(p, 'submission.reject', 'submission', String(s.id));
      return send(res, 200, s);
    }
    s.status = 'approved';
    s.reviews.push({ id: reviewId, reviewer: who!.login, decision: 'approve', note: b.note ? String(b.note) : undefined, security: !!b.security, created_at: s.decided_at });
    const rel = publishIndex('approve');
    audit(p, 'submission.approve', 'submission', String(s.id), { serial: rel.serial });
    const app = state.apps.find((a) => a.detail.name === s.app);
    const version = app?.versions[app.detail.latest] ?? state.apps[0].versions[state.apps[0].detail.latest];
    return send(res, 200, { submission: s, version: { ...version, version: s.version ?? version.version }, index_serial: rel.serial });
  }

  // ---- admin
  if (path.startsWith('/api/v1/admin/') && p !== 'admin') return err(res, 403, 'forbidden', 'This needs the administrator role.');
  if ((m = /^\/api\/v1\/admin\/apps\/([^/]+)(\/revoke|\/versions\/([^/]+)\/yank)?$/.exec(path))) {
    const a = state.apps.find((x) => x.detail.name === decodeURIComponent(m![1]));
    if (!a) return err(res, 404, 'not_found', 'No such app.');
    if (!m[2] && method === 'PATCH') {
      const b = await readBody(req);
      if (b.status === 'listed' || b.status === 'unlisted') a.detail.status = b.status;
      if (Array.isArray(b.categories)) a.detail.categories = (b.categories as string[]).slice(0, 3);
      audit(p, 'app.update', 'app', a.detail.name, b);
      publishIndex('app.update');
      return send(res, 200, a.detail);
    }
    if (m[2] === '/revoke') {
      const b = method === 'POST' ? await readBody(req) : {};
      a.detail.revoked = method === 'POST' ? { reason: String(b.reason ?? ''), at: new Date().toISOString() } : null;
      a.detail.status = method === 'POST' ? 'revoked' : 'listed';
      audit(p, method === 'POST' ? 'app.revoke' : 'app.unrevoke', 'app', a.detail.name, b);
      publishIndex('revoke');
      return send(res, 200, a.detail);
    }
    if (m[3]) {
      const v = a.versions[decodeURIComponent(m[3])];
      if (!v) return err(res, 404, 'not_found', 'No such version.');
      const b = method === 'POST' ? await readBody(req) : {};
      v.yanked = method === 'POST' ? { reason: String(b.reason ?? ''), at: new Date().toISOString() } : null;
      const sv = a.detail.versions.find((x) => x.version === v.version);
      if (sv) sv.yanked = v.yanked;
      a.detail.latest = a.detail.versions.find((x) => !x.yanked)?.version ?? '';
      audit(p, method === 'POST' ? 'version.yank' : 'version.unyank', 'app_version', `${a.detail.name}@${v.version}`, b);
      publishIndex('yank');
      return send(res, 200, v);
    }
  }
  if (path === '/api/v1/admin/apps' && method === 'GET') {
    const want = q.get('status') ?? 'all';
    const words = (q.get('q') ?? '').toLowerCase().split(/\s+/).filter(Boolean);
    let apps = state.apps.filter((a) => want === 'all' || a.detail.status === want);
    if (words.length)
      apps = apps.filter((a) => {
        const hay = [a.detail.name, a.detail.label.en, a.detail.summary?.en ?? '', a.detail.publisher.name].join(' ').toLowerCase();
        return words.every((w) => hay.includes(w));
      });
    apps = [...apps].sort((x, y) => x.detail.name.localeCompare(y.detail.name));
    const pg = page(
      apps.map((a) => ({ ...summary(a), status: a.detail.status, ...(a.detail.revoked ? { revoked_at: a.detail.revoked.at } : {}) })),
      q,
    );
    return send(res, 200, pg);
  }
  if (path === '/api/v1/admin/publishers' && method === 'GET') {
    const order = { pending: 0, active: 1, suspended: 2 } as const;
    let pubs = [...state.publishers].sort((a, b) => order[a.status] - order[b.status] || a.id.localeCompare(b.id));
    if (q.get('status')) pubs = pubs.filter((x) => x.status === q.get('status'));
    const pg = page(pubs, q);
    return send(res, 200, { items: pg.items, total: pg.total });
  }
  if ((m = /^\/api\/v1\/admin\/publishers\/([^/]+)$/.exec(path)) && method === 'PATCH') {
    const pub = state.publishers.find((x) => x.id === decodeURIComponent(m![1]));
    if (!pub) return err(res, 404, 'not_found', 'No such publisher.');
    const b = await readBody(req);
    if (b.status === 'active' || b.status === 'suspended') pub.status = b.status;
    if (b.status === 'active') pub.status_reason = undefined;
    if (typeof b.status_reason === 'string') pub.status_reason = b.status_reason;
    if (typeof b.verified === 'boolean') pub.verified = b.verified;
    if (typeof b.official === 'boolean') pub.official = b.official;
    audit(p, 'publisher.update', 'publisher', pub.id, b);
    return send(res, 200, pub);
  }
  if (path === '/api/v1/admin/index/publish' && method === 'POST') {
    const rel = publishIndex('manual');
    audit(p, 'index.publish', 'index', String(rel.serial), { reason: 'manual' });
    return send(res, 200, rel);
  }
  if (path === '/api/v1/admin/keys') return send(res, 200, { items: state.keys });
  if (path === '/api/v1/admin/validators') return send(res, 200, { items: state.validators, plugin_validators: state.pluginValidators });
  if (path === '/api/v1/admin/audit') {
    let rows = state.audit;
    for (const k of ['action', 'target_type', 'target_id'] as const) {
      const v = q.get(k);
      if (v) rows = rows.filter((r) => r[k] === v);
    }
    const pg = page(rows, q);
    return send(res, 200, { items: pg.items, total: pg.total, chain_ok: true });
  }

  if (path.startsWith('/api/') || path.startsWith('/v1/')) return err(res, 404, 'not_found', 'No such endpoint.');
  return err(res, 404, 'not_found', 'The mock serves the API only; open the SPA through vite.');
}

createServer((req, res) => {
  handle(req, res).catch((e: unknown) => err(res, 500, 'mock_error', e instanceof Error ? e.message : String(e)));
}).listen(PORT, '127.0.0.1', () => {
  console.log(`filex app store mock API on http://127.0.0.1:${PORT}`);
});
