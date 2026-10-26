// Realistic data for the development mock (mock/server.ts), typed against the
// contract (src/api/schema.d.ts), so `vue-tsc` refuses a fixture the API could
// never answer. Mock data only: nothing here reaches the bundle.
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import type { components } from '../src/api/schema';
import { appIcon, uiShot, type Glyph, type Layout, type Rgb } from './png.ts';

type Sch = components['schemas'];

export interface Media {
  path: string;
  sha256: string;
  bytes: Buffer;
}

export interface AppRecord {
  detail: Sch['AppDetail'];
  versions: Record<string, Sch['VersionDetail']>;
}

export interface State {
  categories: Sch['Category'][];
  publishers: Sch['Publisher'][];
  apps: AppRecord[];
  submissions: Sch['SubmissionDetail'][];
  keys: Sch['SigningKey'][];
  validators: Sch['ValidatorStatus'][];
  pluginValidators: Sch['PluginValidatorStatus'][];
  audit: Sch['AuditEntry'][];
  media: Map<string, Media>;
  serial: number;
  nextSubmission: number;
}

const sha = (b: Buffer | string) => createHash('sha256').update(b).digest('hex');
const hex = (seed: string, n = 64) => {
  let out = '';
  let i = 0;
  while (out.length < n) out += sha(seed + ':' + i++);
  return out.slice(0, n);
};
const T0 = Date.parse('2026-10-03T12:00:00Z');
const at = (daysAgo: number, h = 0) => new Date(T0 - daysAgo * 86400000 + h * 3600000).toISOString();

function addMedia(state: State, bytes: Buffer): Sch['IndexMedia'] {
  const s = sha(bytes);
  const path = `/v1/media/${s}.png`;
  state.media.set(path, { path, sha256: s, bytes });
  return { url: path, sha256: s };
}

function artifact(seed: string, url: string, size: number): Sch['IndexArtifact'] {
  return { url, sha256: hex(seed + 'sha'), size, sig: hex(seed + 'sig', 128) };
}

const PERMS: Record<string, string> = {
  'files.read': 'Read the files you open with it',
  'files.write': 'Save files next to the ones you open with it',
  'ui.page': 'Add a page of its own to filex',
  'ui.context_menu': 'Add entries to the right-click menu of files',
  'net.github': 'Reach github.com from its interface',
  'convert.office': 'Ask filex to convert documents with its office engine',
  'thumbnail.jpeg': 'Draw thumbnails for JPEG photos in the explorer',
};

function perms(ids: string[], reasons: Record<string, string> = {}): Sch['PermissionRow'][] {
  return ids.map((id) => ({ id, label: PERMS[id] ?? id, ...(reasons[id] ? { reason: { en: reasons[id] } } : {}) }));
}

// What the apps work with, as the server answers it: the official apps' from
// their real manifests (filex-sign 0.3.0, filex-convert 0.2.0, filextext,
// filex-drawio 31.5.3), the community ones made up in the same shape.
const L = (en: string) => ({ en });
// What filex-convert 0.2.0 converts, as the server answers it from its seed
// listing (seed/official/filex-convert/listing.json, made by
// scripts/gen-convert-conversions.sh): filetypes.ConversionTable's rows,
// captured from the Go code; filetypes' TestTheMockConversionsAreTheServers
// fails when they drift.
export const CONVERT_CONVERSIONS = JSON.parse(readFileSync(new URL('./convert-conversions.json', import.meta.url), 'utf8')) as Sch['ConversionRow'][];

export const FILE_TYPES: Record<string, Sch['FileTypeUse'][]> = {
  sign: [
    {
      ext: 'pdf',
      roles: [
        { role: 'inspect', id: 'status', label: L('Signatures') },
        { role: 'action', id: 'sign', label: L('Sign…'), output: 'sibling' },
        { role: 'action', id: 'request', label: L('Request signatures…'), output: 'none' },
        { role: 'action', id: 'fill', label: L('Sign / Fill'), output: 'version' },
        { role: 'action', id: 'verify', label: L('Verify'), output: 'none' },
      ],
    },
  ],
  convert: [{ any_file: true, roles: [{ role: 'action', id: 'convert', label: L('Convert…'), output: 'sibling' }] }],
  filextext: [{ ext: 'fxtxt', roles: [{ role: 'open', id: 'editor', label: L('filextext') }, { role: 'create', id: 'editor', label: L('Encrypted workspace (.fxtxt)') }] }],
  drawio: [
    { ext: 'drawio', roles: [{ role: 'open', id: 'editor', label: L('draw.io') }, { role: 'create', id: 'editor', label: L('draw.io diagram') }] },
    { ext: 'dio', roles: [{ role: 'open', id: 'editor', label: L('draw.io') }] },
  ],
  markdown: [
    { ext: 'md', roles: [{ role: 'open', id: 'preview', label: L('Markdown Preview') }] },
    { ext: 'markdown', roles: [{ role: 'open', id: 'preview', label: L('Markdown Preview') }] },
  ],
  redact: [
    { ext: 'pdf', roles: [{ role: 'action', id: 'redact', label: L('Redact…'), output: 'sibling' }] },
    { ext: 'docx', roles: [{ role: 'action', id: 'redact', label: L('Redact…'), engine: 'office', output: 'sibling', output_ext: 'pdf' }] },
  ],
};

interface VSpec {
  version: string;
  daysAgo: number;
  filex: string;
  perms: string[];
  wasm?: boolean;
  ui?: boolean;
  security?: boolean;
  yanked?: { reason: string; daysAgo: number };
  languages?: { code: string; percent: number }[];
  /** A storage plugin's builds (platforms) and what the store's plugin validator measured. */
  builds?: string[];
  conformance?: Sch['Conformance'];
}

function makeApp(
  state: State,
  a: {
    name: string;
    label: string;
    summary: string;
    kind: Sch['AppKind'];
    publisher: string;
    repo: string;
    categories: string[];
    license: string;
    color?: Rgb;
    /** Draws an icon (a tile in `color` with this glyph); without it the store shows the initial. */
    glyph?: Glyph;
    shots?: { layout: Layout; caption?: string }[];
    description?: string;
    status?: 'listed' | 'unlisted' | 'revoked';
    revoked?: { reason: string; daysAgo: number };
    reasons?: Record<string, string>;
    versions: VSpec[];
    pricing?: Sch['Pricing'];
    /** What the versions work with (the server reads it from the manifest; here, the real apps' answers). */
    fileTypes?: Sch['FileTypeUse'][];
    /** What the versions convert (the server reads it from the listing's declaration). */
    conversions?: Sch['ConversionRow'][];
  },
): AppRecord {
  const pub = state.publishers.find((p) => p.id === a.publisher)!;
  const ref = { id: pub.id, name: pub.name, verified: pub.verified, official: pub.official, ...(pub.avatar_url ? { avatar_url: pub.avatar_url } : {}) };
  const iconMedia = a.color && a.glyph ? addMedia(state, appIcon(a.color, a.glyph)) : undefined;
  const screenshots = (a.shots ?? []).map((sh) => ({
    ...addMedia(state, uiShot(a.color ?? [47, 108, 235], sh.layout)),
    ...(sh.caption ? { caption: { en: sh.caption } } : {}),
  }));
  const latest = a.versions.find((v) => !v.yanked)?.version ?? '';
  const versions: Record<string, Sch['VersionDetail']> = {};
  for (const v of a.versions) {
    const tag = 'v' + v.version;
    const base = `https://github.com/${a.repo}/releases/download/${tag}/`;
    versions[v.version] = {
      version: v.version,
      ref: tag,
      filex: v.filex,
      published_at: at(v.daysAgo),
      security: !!v.security,
      yanked: v.yanked ? { reason: v.yanked.reason, at: at(v.yanked.daysAgo) } : null,
      commit: hex(a.name + v.version + 'commit', 40),
      manifest: v.builds
        ? artifact(a.name + v.version + 'm', base + 'filex-storage.json', 1400)
        : artifact(a.name + v.version + 'm', `https://raw.githubusercontent.com/${a.repo}/${hex(a.name + v.version + 'commit', 40)}/filex-app.json`, 1800 + v.perms.length * 90),
      ...(v.wasm ? { wasm: artifact(a.name + v.version + 'w', base + `${a.name}.wasm`, 2_400_000 + v.daysAgo * 1000) } : {}),
      ...(v.ui ? { ui: artifact(a.name + v.version + 'u', base + `${a.name}-ui.zip`, 880_000) } : {}),
      permissions: perms(v.perms, a.reasons),
      languages: v.languages ?? [
        { code: 'en', percent: 100 },
        { code: 'tr', percent: 100 },
      ],
      engines: [],
      notes_url: `https://github.com/${a.repo}/releases/tag/${tag}`,
      install: { github_repo: a.repo, ref: tag },
      file_types: a.fileTypes ?? [],
      conversions: a.conversions ?? [],
      ...(v.builds
        ? {
            builds: v.builds.map((plat) => ({ platform: plat, ...artifact(a.name + v.version + plat, base + `${a.name}-${plat.replace('/', '-')}${plat.startsWith('windows/') ? '.exe' : ''}`, 9_400_000) })),
            ...(v.conformance ? { conformance: v.conformance } : {}),
          }
        : {}),
    };
  }
  const detail: Sch['AppDetail'] = {
    name: a.name,
    kind: a.kind,
    label: { en: a.label },
    summary: { en: a.summary },
    publisher: ref,
    categories: a.categories,
    ...(iconMedia ? { icon_url: iconMedia.url } : {}),
    latest,
    updated_at: at(a.versions[0].daysAgo, 3),
    repo: a.repo,
    homepage: `https://github.com/${a.repo}`,
    license: a.license,
    status: a.status ?? 'listed',
    pricing: a.pricing ?? { model: 'free' },
    revoked: a.revoked ? { reason: a.revoked.reason, at: at(a.revoked.daysAgo) } : null,
    ...(a.description ? { description_html: a.description } : {}),
    screenshots,
    versions: a.versions.map((v) => {
      const d = versions[v.version];
      return { version: d.version, ref: d.ref, filex: d.filex, published_at: d.published_at, security: d.security, yanked: d.yanked };
    }),
  };
  return { detail, versions };
}

export function buildState(): State {
  const state: State = {
    // The server's own list (backend/migrations/0001_init.sql).
    categories: [
      { id: 'documents', label: { en: 'Documents' } },
      { id: 'conversion', label: { en: 'Conversion' } },
      { id: 'media', label: { en: 'Media' } },
      { id: 'productivity', label: { en: 'Productivity' } },
      { id: 'security', label: { en: 'Security' } },
      { id: 'developer', label: { en: 'Developer tools' } },
      { id: 'integration', label: { en: 'Integrations' } },
      { id: 'fun', label: { en: 'Fun' } },
      { id: 'storage', label: { en: 'Storage' } },
    ],
    publishers: [
      { id: 'brf-tech', name: 'BRF Tech', verified: true, official: true, github_owner: 'BRF-Tech', owner_type: 'Organization', homepage: 'https://filex.sh', status: 'active', created_at: at(60), description: 'The people who make filex. Official apps and the language packs.' },
      { id: 'jdoe', name: 'Jane Doe', verified: false, official: false, github_owner: 'jdoe', owner_type: 'User', status: 'active', created_at: at(20), description: 'Small tools for writing in Markdown.' },
      { id: 'acme-tools', name: 'Acme Tools', verified: false, official: false, github_owner: 'acme-tools', owner_type: 'Organization', homepage: 'https://acme.example', status: 'pending', created_at: at(1) },
      { id: 'northwind', name: 'Northwind Labs', verified: true, official: false, github_owner: 'northwind-labs', owner_type: 'Organization', homepage: 'https://northwind.example', status: 'active', created_at: at(45), description: 'Privacy tools for documents and photos.' },
      { id: 'kmoreno', name: 'Kai Moreno', verified: false, official: false, github_owner: 'kmoreno', owner_type: 'User', status: 'active', created_at: at(14), description: 'Boards, lists and other ways to sort notes.' },
      { id: 'spamco', name: 'Spam Co', verified: false, official: false, github_owner: 'spamco', owner_type: 'User', status: 'suspended', status_reason: 'Submitted a copy of another publisher\'s app under its name.', created_at: at(9) },
    ],
    apps: [],
    submissions: [],
    keys: [
      { id: 'idx-2026-demo', use: 'index', public_hex: hex('index-key'), status: 'active', demo: true, created_at: at(30), activated_at: at(30) },
      { id: 'art-2026-demo', use: 'artifact', public_hex: hex('artifact-key'), status: 'active', demo: true, created_at: at(30), activated_at: at(30) },
      { id: 'lic-2026-demo', use: 'license', public_hex: hex('license-key'), status: 'active', demo: true, created_at: at(30), activated_at: at(30) },
    ],
    validators: [
      { label: 'latest', base_url: 'http://filex-latest:8080', reachable: true, filex_version: '0.50.0' },
      { label: 'floor', base_url: 'http://filex-floor:8080', reachable: true, filex_version: '0.49.0' },
    ],
    pluginValidators: [{ label: 'plugins', kind: 'storage', reachable: true, filex_version: '0.55.0', platform: 'linux/amd64' }],
    audit: [],
    media: new Map(),
    serial: 12,
    nextSubmission: 110,
  };

  // Mirrored avatars, as the server keeps them (/v1/media/...): two of the
  // publishers have one, the others show their initial.
  for (const [id, color, g] of [
    ['brf-tech', [47, 108, 235], 'board'],
    ['northwind', [15, 118, 110], 'redact'],
  ] as const) {
    const pub = state.publishers.find((x) => x.id === id)!;
    pub.avatar_url = addMedia(state, appIcon(color as unknown as Rgb, g as Glyph)).url;
  }

  const sign =
    '<p>Sign a PDF where it lies: draw, type or upload a signature, place it on the page and save a signed copy next to the original.</p><h3>What it does</h3><ul><li><strong>Requests:</strong> send a link, collect signatures in order.</li><li>Every signature is stamped with a hash of the page it was placed on.</li><li>Fill form fields before you sign.</li></ul><p>Needs filex 0.49 or newer.</p>';
  state.apps.push(
    makeApp(state, {
      name: 'filex-sign',
      label: 'filex Sign',
      summary: 'Sign PDF documents in place and ask others to sign them.',
      kind: 'app',
      publisher: 'brf-tech',
      repo: 'BRF-Tech/filex-sign',
      categories: ['documents', 'security'],
      license: 'AGPL-3.0-only',
      color: [47, 108, 235],
      glyph: 'sign',
      shots: [
        { layout: 'document', caption: 'Place a signature on the page' },
        { layout: 'explorer', caption: 'Sign from the file explorer' },
        { layout: 'dialog', caption: 'Ask others to sign, in order' },
      ],
      description: sign,
      reasons: {
        'files.read': 'To open the PDF you sign.',
        'files.write': 'To save the signed copy next to the original.',
        'ui.page': 'The signing page.',
      },
      fileTypes: FILE_TYPES.sign,
      versions: [
        { version: '0.3.0', daysAgo: 3, filex: '>=0.49.0', perms: ['files.read', 'files.write', 'ui.page'], wasm: true, ui: true },
        { version: '0.2.1', daysAgo: 12, filex: '>=0.48.0', perms: ['files.read', 'files.write', 'ui.page'], wasm: true, ui: true, security: true },
        { version: '0.2.0', daysAgo: 25, filex: '>=0.48.0', perms: ['files.read', 'files.write'], wasm: true, ui: true, yanked: { reason: 'Signatures on rotated pages were placed off the page; use 0.2.1', daysAgo: 12 } },
      ],
    }),
    makeApp(state, {
      name: 'filex-convert',
      label: 'filex Convert',
      summary: 'Convert documents, spreadsheets and images between formats from the right-click menu.',
      kind: 'app',
      publisher: 'brf-tech',
      repo: 'BRF-Tech/filex-convert',
      categories: ['conversion', 'documents'],
      license: 'AGPL-3.0-only',
      color: [13, 148, 136],
      glyph: 'convert',
      shots: [
        { layout: 'dialog', caption: 'Pick the format to convert to' },
        { layout: 'explorer', caption: 'The converted copy lands next to the original' },
      ],
      description:
        '<p>Right-click a document, a spreadsheet or an image and choose <em>Convert to</em>. The copy is written next to the original; the original is never changed.</p><p>Office formats go through the office engine your filex already runs.</p>',
      fileTypes: FILE_TYPES.convert,
      conversions: CONVERT_CONVERSIONS,
      versions: [{ version: '0.2.0', daysAgo: 5, filex: '>=0.50.0', perms: ['files.read', 'files.write', 'ui.context_menu', 'convert.office'], wasm: true }],
    }),
    makeApp(state, {
      name: 'filextext',
      label: 'filex Text',
      summary: 'A fast plain-text and Markdown editor with a live preview.',
      kind: 'app',
      publisher: 'brf-tech',
      repo: 'BRF-Tech/filextext-app',
      categories: ['productivity', 'developer'],
      license: 'MIT',
      color: [124, 58, 237],
      glyph: 'text',
      shots: [{ layout: 'editor', caption: 'Source and preview side by side' }],
      description: '<p>Open any text file and edit it in place, with syntax colours for forty languages and a live Markdown preview.</p>',
      fileTypes: FILE_TYPES.filextext,
      versions: [{ version: '1.4.0', daysAgo: 8, filex: '>=0.49.0', perms: ['files.read', 'files.write', 'ui.page'], ui: true }],
    }),
    makeApp(state, {
      name: 'filex-drawio',
      label: 'draw.io for filex',
      summary: 'Open and edit .drawio diagrams without leaving filex.',
      kind: 'app',
      publisher: 'brf-tech',
      repo: 'BRF-Tech/filex-drawio',
      categories: ['productivity'],
      license: 'Apache-2.0',
      color: [234, 88, 12],
      glyph: 'diagram',
      shots: [
        { layout: 'diagram', caption: 'Edit a diagram where it is stored' },
        { layout: 'explorer', caption: 'New diagram from the explorer' },
      ],
      fileTypes: FILE_TYPES.drawio,
      versions: [{ version: '31.5.3', daysAgo: 15, filex: '>=0.49.0', perms: ['files.read', 'files.write', 'ui.page'], ui: true }],
    }),
    makeApp(state, {
      name: 'filex-lang-es',
      label: 'Español',
      summary: 'Spanish for the whole filex interface.',
      kind: 'language_pack',
      publisher: 'brf-tech',
      repo: 'BRF-Tech/filex-lang-es',
      categories: [],
      license: 'CC-BY-4.0',
      versions: [{ version: '0.1.8', daysAgo: 1, filex: '>=0.50.0', perms: [], languages: [{ code: 'es', percent: 100 }] }],
    }),
    makeApp(state, {
      name: 'filex-lang-de',
      label: 'Deutsch',
      summary: 'German for the whole filex interface.',
      kind: 'language_pack',
      publisher: 'brf-tech',
      repo: 'BRF-Tech/filex-lang-de',
      categories: [],
      license: 'CC-BY-4.0',
      versions: [{ version: '0.1.8', daysAgo: 1, filex: '>=0.50.0', perms: [], languages: [{ code: 'de', percent: 91 }] }],
    }),
    makeApp(state, {
      name: 'filex-lang-fr',
      label: 'Français',
      summary: 'French for the whole filex interface.',
      kind: 'language_pack',
      publisher: 'brf-tech',
      repo: 'BRF-Tech/filex-lang-fr',
      categories: [],
      license: 'CC-BY-4.0',
      versions: [{ version: '0.1.8', daysAgo: 1, filex: '>=0.50.0', perms: [], languages: [{ code: 'fr', percent: 96 }] }],
    }),
    makeApp(state, {
      name: 'markdown-preview',
      label: 'Markdown Preview',
      summary: 'Preview Markdown files with diagrams and maths, side by side with the source.',
      kind: 'app',
      publisher: 'jdoe',
      repo: 'jdoe/filex-markdown-preview',
      categories: ['developer'],
      license: 'MIT',
      color: [22, 163, 74],
      glyph: 'markdown',
      shots: [{ layout: 'editor', caption: 'Mermaid diagrams and KaTeX maths' }],
      reasons: { 'net.github': 'To show images the document links from github.com.' },
      fileTypes: FILE_TYPES.markdown,
      versions: [{ version: '1.0.0', daysAgo: 6, filex: '>=0.49.0 <0.51.0', perms: ['files.read', 'net.github'], ui: true }],
      pricing: { model: 'paid', price_text: '$12 per instance', purchase_url: 'https://jdoe.example/buy' },
    }),
    makeApp(state, {
      name: 'photo-exif',
      label: 'Photo Details',
      summary: 'See where and how a photo was taken, and strip its location before you share it.',
      kind: 'app',
      publisher: 'northwind',
      repo: 'northwind-labs/filex-photo-exif',
      categories: ['media', 'security'],
      license: 'MIT',
      shots: [{ layout: 'photos', caption: 'Details beside every photo' }],
      reasons: { 'files.write': 'To save a copy without the location.' },
      versions: [{ version: '2.1.0', daysAgo: 9, filex: '>=0.49.0', perms: ['files.read', 'files.write', 'thumbnail.jpeg', 'ui.page'], wasm: true, ui: true }],
    }),
    makeApp(state, {
      name: 'pdf-redact',
      label: 'PDF Redact',
      summary: 'Black out names, numbers and whole passages in a PDF, for good.',
      kind: 'app',
      publisher: 'northwind',
      repo: 'northwind-labs/filex-pdf-redact',
      categories: ['documents', 'security'],
      license: 'GPL-3.0-only',
      color: [55, 65, 81],
      glyph: 'redact',
      shots: [{ layout: 'document', caption: 'Mark what to remove, then apply' }],
      fileTypes: FILE_TYPES.redact,
      versions: [{ version: '0.9.2', daysAgo: 20, filex: '>=0.50.0', perms: ['files.read', 'files.write', 'ui.page'], wasm: true, ui: true }],
    }),
    makeApp(state, {
      name: 'kanban-notes',
      label: 'Kanban Notes',
      summary: 'Turn a folder of Markdown notes into a board you can drag cards across.',
      kind: 'app',
      publisher: 'kmoreno',
      repo: 'kmoreno/kanban-notes',
      categories: ['productivity', 'fun'],
      license: 'MIT',
      shots: [{ layout: 'board', caption: 'Each column is a folder' }],
      versions: [{ version: '0.4.1', daysAgo: 4, filex: '>=0.49.0', perms: ['files.read', 'files.write', 'ui.page'], ui: true }],
    }),
    makeApp(state, {
      name: 'quick-share',
      label: 'Quick Share',
      summary: 'Share a file to chat services in one click.',
      kind: 'app',
      publisher: 'jdoe',
      repo: 'jdoe/quick-share',
      categories: ['integration'],
      license: 'MIT',
      color: [2, 132, 199],
      glyph: 'share',
      status: 'revoked',
      revoked: { reason: 'Sent file names to a third-party server without saying so', daysAgo: 4 },
      versions: [{ version: '0.9.0', daysAgo: 10, filex: '>=0.49.0', perms: ['files.read', 'net.github'], ui: true }],
    }),
    makeApp(state, {
      name: 'old-notes',
      label: 'Old Notes',
      summary: 'Keep short notes beside a file.',
      kind: 'app',
      publisher: 'jdoe',
      repo: 'jdoe/old-notes',
      categories: ['productivity'],
      license: 'MIT',
      color: [202, 138, 4],
      glyph: 'notes',
      status: 'unlisted',
      versions: [{ version: '0.3.0', daysAgo: 40, filex: '>=0.49.0', perms: ['files.read'], ui: true }],
    }),
    // A storage plugin: its release's feed and a build per platform,
    // the linux/amd64 one run by the store's plugin validator.
    makeApp(state, {
      name: 'webdavfs',
      label: 'WebDAV storage',
      summary: 'Keep a filex storage on any WebDAV server: Nextcloud, ownCloud, a NAS.',
      kind: 'storage',
      publisher: 'northwind',
      repo: 'northwind-labs/filex-webdavfs',
      categories: ['storage'],
      license: 'Apache-2.0',
      versions: [
        {
          version: '1.2.0',
          daysAgo: 3,
          filex: '>=0.55.0',
          perms: [],
          languages: [],
          builds: ['linux/amd64', 'linux/arm64', 'windows/amd64', 'darwin/arm64'],
          conformance: {
            platform: 'linux/amd64',
            filex: '0.55.0',
            verified: true,
            passed: 31,
            failed: 0,
            skipped: 3,
            driver: 'webdavfs',
            capabilities: ['copy', 'delete', 'mkdir', 'move', 'range', 'set_mtime', 'write'],
          },
        },
      ],
    }),
  );

  // ---------------------------------------------------------------- submissions
  const brf = state.publishers[0];
  const jdoe = state.publishers[1];
  const pref = (p: Sch['Publisher']) => ({ id: p.id, name: p.name, verified: p.verified, official: p.official });
  const checksAll = (start: string, warnPolicy = false): Sch['Check'][] => {
    const kinds: Sch['Check']['kind'][] = ['repo_access', 'tag', 'manifest', 'consent', 'policy', 'artifacts', 'media'];
    const msgs: Record<string, string> = {
      repo_access: 'Public repository; jdoe has admin; owner matches the publisher.',
      tag: 'Tag v1.1.0 is commit 4be1c0d; not signed.',
      manifest: 'filex-app.json read at the commit (2.1 KB).',
      consent: '.filex-apps.json names publisher "jdoe".',
      policy: warnPolicy ? 'The name is one edit away from "markdown-previewer" (unlisted). License: MIT.' : 'Version is new and higher than 1.0.0. License: MIT.',
      artifacts: 'Interface bundle downloaded (880 KB), SHA-256 matches the manifest.',
      media: 'Icon and 2 screenshots mirrored.',
    };
    const out: Sch['Check'][] = kinds.map((kind, i) => ({
      kind,
      status: kind === 'policy' && warnPolicy ? 'warned' : 'passed',
      ...(kind === 'policy' && warnPolicy ? { code: 'name_similar' } : {}),
      message: msgs[kind],
      started_at: new Date(Date.parse(start) + i * 1500).toISOString(),
      ended_at: new Date(Date.parse(start) + i * 1500 + 1200).toISOString(),
    }));
    for (const [j, v] of ['latest', 'floor'].entries()) {
      out.push({
        kind: 'dry_run',
        validator: v,
        status: 'passed',
        message: `filex ${v === 'latest' ? '0.50.0' : '0.49.0'} accepted the install review.`,
        started_at: new Date(Date.parse(start) + 12000 + j * 30000).toISOString(),
        ended_at: new Date(Date.parse(start) + 38000 + j * 30000).toISOString(),
      });
    }
    return out;
  };
  const mp = state.apps.find((a) => a.detail.name === 'markdown-preview')!;
  const resolution = (version: string, sha256: string): Sch['Resolution'] => ({
    repo_owner: 'jdoe',
    repo_name: 'filex-markdown-preview',
    repo_id: 812345678,
    owner_id: 4512345,
    owner_type: 'User',
    ref: 'v' + version,
    commit_sha: hex('mp' + version, 40),
    tag_verified: false,
    manifest_url: `https://raw.githubusercontent.com/jdoe/filex-markdown-preview/${hex('mp' + version, 40)}/filex-app.json`,
    manifest_sha256: sha256,
    name: 'markdown-preview',
    version,
    kind: 'app',
    filex_range: '>=0.49.0 <0.52.0',
    permissions: ['files.read', 'files.write', 'net.github'],
    languages: ['en', 'tr'],
    homepage: 'https://github.com/jdoe/filex-markdown-preview',
    license: 'MIT',
    ui: { url: `https://github.com/jdoe/filex-markdown-preview/releases/download/v${version}/ui.zip`, sha256: hex('mpui' + version), size: 880_412 },
    consent: {
      publisher: 'jdoe',
      categories: ['developer', 'productivity'],
      summary: { en: 'Preview Markdown files with diagrams and maths, side by side with the source.' },
      description: 'docs/STORE.md',
      icon: 'assets/icon.png',
      screenshots: [{ path: 'assets/shot-1.png', caption: { en: 'Side by side' } }, { path: 'assets/shot-2.png' }],
    },
  });
  const manifestSha = hex('mp-manifest-1.1.0');
  state.submissions.push(
    {
      id: 101,
      publisher: pref(jdoe),
      app: 'markdown-preview',
      version: '1.1.0',
      repo: 'jdoe/filex-markdown-preview',
      ref: 'v1.1.0',
      status: 'needs_review',
      attempt: 1,
      submitted_by: 'jdoe',
      created_at: at(0, -5),
      updated_at: at(0, -4),
      note: 'Adds saving the rendered HTML next to the source, hence files.write.',
      resolution: resolution('1.1.0', manifestSha),
      checks: checksAll(at(0, -5), true),
      dry_runs: ['latest', 'floor'].map((v) => ({
        validator: v,
        filex_version: v === 'latest' ? '0.50.0' : '0.49.0',
        ok: true,
        kind: 'app' as const,
        manifest_sha256: manifestSha,
        ui_sha256: hex('mpui1.1.0'),
        compat_ok: true,
        languages: [
          { code: 'en', percent: 100, keys: 412, total: 412 },
          { code: 'tr', percent: v === 'latest' ? 96 : 100, keys: v === 'latest' ? 396 : 371, total: v === 'latest' ? 412 : 371 },
        ],
        permissions: perms(['files.read', 'files.write', 'net.github'], {
          'files.write': 'To save the rendered HTML next to the source.',
          'net.github': 'To show images the document links from github.com.',
        }),
        engines_missing: [],
      })),
      reviews: [],
      diff: { from: '1.0.0', permissions_added: ['files.write'], permissions_removed: [], filex_from: '>=0.49.0 <0.51.0', filex_to: '>=0.49.0 <0.52.0', module_changed: false, ui_changed: true },
    },
    {
      id: 102,
      publisher: pref(jdoe),
      app: 'markdown-preview',
      version: '1.0.1',
      repo: 'jdoe/filex-markdown-preview',
      ref: 'v1.0.1',
      status: 'failed',
      attempt: 2,
      failure: { code: 'sha256_mismatch', message: 'The interface bundle\'s SHA-256 does not match the manifest: the release asset was replaced after tagging.' },
      submitted_by: 'jdoe',
      created_at: at(1, -2),
      updated_at: at(1, -1),
      checks: [
        ...checksAll(at(1, -2)).slice(0, 5),
        { kind: 'artifacts', status: 'failed', code: 'sha256_mismatch', message: 'ui.zip: expected 6f1e…a2, got 0c9d…41.', started_at: at(1, -1.9), ended_at: at(1, -1.89) },
        { kind: 'media', status: 'skipped', message: 'Not run: an earlier check failed.', started_at: at(1, -1.89), ended_at: at(1, -1.89) },
      ],
      dry_runs: [],
      reviews: [],
    },
    {
      id: 103,
      publisher: pref(brf),
      app: 'filex-sign',
      version: '0.3.0',
      repo: 'BRF-Tech/filex-sign',
      ref: 'v0.3.0',
      status: 'approved',
      attempt: 1,
      submitted_by: 'filex-maintainer',
      created_at: at(3, -3),
      updated_at: at(3),
      decided_at: at(3),
      checks: [],
      dry_runs: [],
      reviews: [{ id: 501, reviewer: 'alice', decision: 'approve', note: 'Checked the signing page and the permission reasons.', created_at: at(3) }],
    },
    {
      id: 104,
      publisher: pref(jdoe),
      app: 'quick-share',
      version: '1.0.0',
      repo: 'jdoe/quick-share',
      ref: 'v1.0.0',
      status: 'rejected',
      attempt: 1,
      submitted_by: 'jdoe',
      created_at: at(4, -6),
      updated_at: at(4),
      decided_at: at(4),
      checks: [],
      dry_runs: [],
      reviews: [{ id: 502, reviewer: 'alice', decision: 'reject', note: 'The interface posts file names to an undeclared server.', created_at: at(4) }],
    },
  );
  void mp;

  const actions: [string, Sch['AuditEntry']['actor_role'], string, string, string][] = [
    ['alice', 'admin', 'publisher.activate', 'publisher', 'jdoe'],
    ['jdoe', 'publisher', 'submission.create', 'submission', '101'],
    ['system', 'system', 'index.publish', 'index', '12'],
    ['alice', 'admin', 'app.revoke', 'app', 'quick-share'],
    ['alice', 'reviewer', 'submission.approve', 'submission', '103'],
    ['alice', 'admin', 'version.yank', 'app_version', 'filex-sign@0.2.0'],
    ['acme-bot', 'publisher', 'publisher.request', 'publisher', 'acme-tools'],
  ];
  for (let i = 0; i < 64; i++) {
    const [actor, role, action, type, id] = actions[i % actions.length];
    state.audit.push({
      id: 900 - i,
      at: at(i * 0.4),
      actor: role === 'system' ? undefined : actor,
      actor_role: role,
      action,
      target_type: type,
      target_id: id,
      data: action === 'app.revoke' ? { reason: 'Sent file names to a third-party server without saying so' } : action === 'index.publish' ? { serial: 12 - (i % 5), reason: 'approve' } : {},
    });
  }
  return state;
}
