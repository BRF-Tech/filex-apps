#!/usr/bin/env node
// Writes backend/internal/filetypes/filex-builtin.json: what filex does by
// itself, with no app installed, with each kind of file - read from filex's
// own source at a tag, never written by hand. The store's file-type search
// shows it ("filex opens .docx with ONLYOFFICE ...").
//
//   node scripts/gen-filex-builtin.mjs <filex-checkout> <tag>          write the file
//   node scripts/gen-filex-builtin.mjs <filex-checkout> <tag> --check  compare, exit 1 on a difference
//
// The checkout is only read (`git show <tag>:<path>`); nothing is checked
// out. Every list below is taken from one named place in filex; when filex
// moves or renames one, this script stops with the place it could not find,
// and whoever bumps the tag reads the new code before changing the anchor.
// Over-claiming is the failure that matters: a capability goes in only when
// the code shows filex doing it, and the exceptions say why (`// not
// claimed:`).
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const [repo, tag, flag] = process.argv.slice(2);
if (!repo || !tag || (flag && flag !== '--check')) {
  console.error('usage: node scripts/gen-filex-builtin.mjs <filex-checkout> <tag> [--check]');
  process.exit(2);
}
const here = dirname(fileURLToPath(import.meta.url));
const OUT = join(here, '..', 'backend', 'internal', 'filetypes', 'filex-builtin.json');

const git = (...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8', maxBuffer: 64 << 20 });
const commit = git('rev-parse', `${tag}^{commit}`).trim();
const version = tag.replace(/^v/, '');
const cache = new Map();
function show(path) {
  if (!cache.has(path)) cache.set(path, git('show', `${tag}:${path}`));
  return cache.get(path);
}

const sources = [];
/** The 1-based line of the first match of `re` in path, recorded as a source. */
function anchor(path, re, what) {
  const src = show(path);
  const m = re.exec(src);
  if (!m) throw new Error(`${path}: ${what}: not found (${re}); read the code at ${tag} and update this script`);
  const start = src.slice(0, m.index).split('\n').length;
  const end = start + m[0].split('\n').length - 1;
  sources.push({ what, path, lines: start === end ? String(start) : `${start}-${end}` });
  return m;
}
const noComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/[^\n]*/g, '');
const quoted = (s) => [...noComments(s).matchAll(/['"]([^'"]+)['"]/g)].map((m) => m[1]);

/** A TS array (or `new Set([...])`) of strings named `name`. */
function tsList(path, name, what) {
  const m = anchor(path, new RegExp(`(?:const|export const)\\s+${name}\\b[^=]*=\\s*(?:new Set\\()?\\[([\\s\\S]*?)\\]`), what);
  return quoted(m[1]);
}
/** The keys of a TS object literal named `name` (`key: value` or `'key': value`). */
function tsKeys(path, name, what) {
  const m = anchor(path, new RegExp(`const\\s+${name}\\b[^=]*=\\s*\\{([\\s\\S]*?)\\n\\};`), what);
  return [...noComments(m[1]).matchAll(/(?:^|[\s,{])'?([a-z0-9]+)'?\s*:/g)].map((x) => x[1]);
}
/** A Go slice literal `var name = []string{...}`: its strings. */
function goList(path, name, what) {
  // One line (`{"csv"}`) or several lines up to the closing brace.
  const m = anchor(path, new RegExp(`var\\s+${name}\\s*=\\s*\\[\\]string\\s*\\{(?:([^\\n]*)\\}(?=[ \\t]*\\n)|\\n([\\s\\S]*?)\\n\\})`), what);
  return quoted(m[1] ?? m[2]);
}
const sameSet = (a, b) => a.length === new Set(a).size && b.length === new Set(b).size && a.length === b.length && a.every((x) => b.includes(x));
/** A Go map literal `var name = map[string]T{...}`: its pairs. */
function goMap(path, name, what) {
  // One line (`{"txt": true, ...}`) or one pair per line up to the closing brace.
  const m = anchor(path, new RegExp(`var\\s+${name}\\s*=\\s*map\\[string\\](?:string|bool)\\s*\\{(?:([^\\n]*)\\}(?=[ \\t]*\\n)|\\n([\\s\\S]*?)\\n\\})`), what);
  m[1] = m[1] ?? m[2];
  return [...noComments(m[1]).matchAll(/"([^"]+)"\s*:\s*("([^"]*)"|true)/g)].map((x) => [x[1], x[3] ?? true]);
}

// ---------------------------------------------------------------- read filex

const PREVIEW = 'packages/core/src/modals/PreviewModal.vue';
const IMAGE = tsList(PREVIEW, 'IMAGE', 'images the preview shows in an <img>');
const VIDEO = tsList(PREVIEW, 'VIDEO', 'videos the preview plays');
const AUDIO = tsList(PREVIEW, 'AUDIO', 'audio the preview plays');
anchor(PREVIEW, /if \(e === 'pdf'\) return 'pdf';/, 'PDF in the browser\'s own viewer');
anchor(PREVIEW, /if \(e === 'md' \|\| e === 'markdown'\) return 'markdown';/, 'Markdown, rendered');
const CODE = tsKeys(PREVIEW, 'CODE_LANGS', 'code and text, highlighted');
const TEXT_PLAIN = tsList(PREVIEW, 'TEXT_PLAIN', 'plain text');
const viewerMap = anchor(PREVIEW, /const VIEWER_MAP\b[^\n]*=\s*\{([\s\S]*?)\n\};/,'the other viewers, by extension')[1];
const VIEWERS = [...noComments(viewerMap).matchAll(/'?([a-z0-9]+)'?\s*:\s*\(\)\s*=>\s*import\('\.\.\/viewers\/(\w+)\.vue'\)/g)].map((m) => [m[1], m[2]]);
// Since filex 0.54 the preview keeps no list of its own of
// what is an office document or editable text: it asks the server's rule
// (lib/serverRules isOfficeExt and isTextEditable, fed by
// /api/files/capabilities `edit_kinds`), and backend/internal/editkind
// decides it. These lines are the preview's use of that rule; the lists are
// read from editkind below.
anchor(PREVIEW, /if \(isOfficeExt\(e\)\) return 'office';/, 'office documents preview in ONLYOFFICE (the server\'s rule)');
anchor(PREVIEW, /if \(isTextEditable\(\{ basename: props\.file\?\.basename, extension: e \}\)\) return wantEdit \? 'code' : 'text';/, 'other text the server saves: the plain text view and editor');
const editButton = anchor(PREVIEW, /const canEditKind = computed<boolean>\(\(\) => \{[\s\S]*?if \(props\.inOffice === true\) return true;[\s\S]*?if \(isOfficeExt\(e\)\) return true;\s*if \(!isTextEditable\([^\n]*\)\) return false;\s*return kind\.value !== 'viewer' \|\| EDITING_VIEWERS\.has\(e\)( \|\| SOURCE_VIEWERS\.has\(e\))?;\s*\}\);/,
  'the Edit button: an office document, or text the server saves that no show-only viewer takes');
const EDITING_VIEWERS = tsList(PREVIEW, 'EDITING_VIEWERS', 'the viewers offered Edit on what they show');
// Since filex 0.56 a viewer that draws a diagram from its text
// (Mermaid) is offered Edit too, and in edit mode the preview opens that text
// in the built-in text editor (`kind` 'code'), which save-text saves. Before
// 0.56 Edit reopened the viewer, which has no save (not claimed then).
const SOURCE_VIEWERS = editButton[1]
  ? (anchor(PREVIEW, /if \(e in VIEWER_MAP\) \{\s*if \(wantEdit && SOURCE_VIEWERS\.has\(e\) && isTextEditable\(\{ basename: props\.file\?\.basename, extension: e \}\)\) return 'code';\s*return 'viewer';/,
      'in edit mode a source viewer\'s text opens in the text editor'),
    tsList(PREVIEW, 'SOURCE_VIEWERS', 'the viewers whose Edit opens their source in the text editor'))
  : [];
const saving = anchor(PREVIEW, /if \(((?:e === '[a-z0-9]+'(?: \|\| )?)+)\) \{[^}]*?base\.saveUrl = props\.saveTextEndpoint/, 'the viewers handed a save address (save-text)')[1];
const SAVING_VIEWERS = [...saving.matchAll(/'([a-z0-9]+)'/g)].map((m) => m[1]);
const MODEL3D = tsList('packages/core/src/viewers/Viewer3D.vue', 'SUPPORTED_EXTS', 'the 3D formats the 3D viewer draws');

// The server's rule of how a file is edited (editkind.Published, what
// `edit_kinds` publishes): an office document is a type the document server
// opens (DocumentType) that is neither fixed-layout (viewOnly: .pdf, .xps,
// .epub, filex's own viewers show those) nor text (textExts: .txt, .csv,
// .html, .xml open as text since 0.54); text is what the built-in editor
// opens and save-text saves.
const EDITKIND = 'backend/internal/editkind/editkind.go';
const docTypes = anchor(EDITKIND, /func DocumentType\(ext string\) string \{([\s\S]*?)\n\}/, 'the types the document server opens (ONLYOFFICE\'s own mapping)')[1];
const DOC_TYPES = [...docTypes.matchAll(/case ([^:\n]+):/g)].flatMap((m) => quoted(m[1]));
const DOC_SERVER = goList(EDITKIND, 'documentServerExts', 'the same types, as the list the published rule is made from');
if (!sameSet(DOC_TYPES, DOC_SERVER)) throw new Error(`${EDITKIND}: DocumentType and documentServerExts disagree; read the code at ${tag} and update this script`);
const VIEW_ONLY_FORMATS = goMap(EDITKIND, 'viewOnly', 'fixed-layout types: not office documents').map(([k]) => k);
const TEXT = quoted(anchor(EDITKIND, /func init\(\) \{\s*for _, e := range \[\]string\{([\s\S]*?)\n\t\} \{\s*textExts\[e\] = true/, 'text the built-in editor opens and save-text saves')[1]);
if (!/var textExts = map\[string\]bool\{\}\n/.test(show(EDITKIND)) || (show(EDITKIND).match(/textExts\[[^\]\n]+\]\s*=[^=]/g) ?? []).length !== 1) throw new Error(`${EDITKIND}: textExts is written outside its init; read the code at ${tag} and update this script`);
anchor(EDITKIND, /if e == "" \|\| DocumentType\(e\) == "" \|\| viewOnly\[e\] \|\| textExts\[e\] \{\s*return false/, 'an office document: the document server\'s, not fixed-layout, not text');
anchor(EDITKIND, /for _, e := range documentServerExts \{\s*if OfficeExt\(e\) \{\s*k\.Office = append\(k\.Office, e\)[\s\S]*?for e := range textExts \{\s*k\.Text = append\(k\.Text, e\)/, 'what the server publishes as edit_kinds');
const OFFICE = DOC_SERVER.filter((e) => !VIEW_ONLY_FORMATS.includes(e) && !TEXT.includes(e));
// Read again from the copy the server's tests hold equal to editkind.Published
// (and the clients' tests to their lookups): a difference is this script
// misreading editkind, so it stops.
const MIRROR = 'backend/internal/api/handlers/testdata/rule-mirrors.json';
anchor(MIRROR, /"edit_kinds": \{[^\n]*\}/, 'checked against: the published rule as the server\'s tests hold it');
const mirror = JSON.parse(show(MIRROR)).edit_kinds;
if (!sameSet(mirror.office, OFFICE) || !sameSet(mirror.text, TEXT)) {
  throw new Error(`${MIRROR}: edit_kinds is not what ${EDITKIND} was read as (office ${OFFICE.join(',')}; text ${TEXT.join(',')}); read the code at ${tag} and update this script`);
}
// not claimed: editkind's textNames (`Makefile`, a bare `Dockerfile`) are
// whole names with no extension, and this file is by extension.

// The kinds the document server opens as a choice beside filex's own viewer
// (a .csv): the server's list; packages/core lib/appViewer OFFICE_OPEN_KINDS
// is its twin, held to it by a test.
const OFFICE_OPEN = goList('backend/internal/assoc/assoc.go', 'onlyOfficeOpenKinds', 'kinds ONLYOFFICE also opens (and edits)');
anchor(PREVIEW, /if \(props\.inOffice\) return 'office';/, 'a kind ONLYOFFICE also opens, open there');
// The table filex turns an app's media types into kinds with (`image/*` is
// every image kind here): the store matches an app's media-type rule the same way.
const MIMES = Object.fromEntries(goMap('backend/internal/assoc/kinds.go', 'knownKinds', 'filex\'s media type of each extension'));

const THUMB = 'backend/internal/thumb/';
const THUMB_MIMES = goMap(THUMB + 'pipeline.go', 'extMimes', 'thumbnails: the type of each extension');
const THUMB_TEXT = goMap(THUMB + 'text.go', 'textThumbExts', 'thumbnails of text: the first lines').map(([k]) => k);
const THUMB_OFFICE = goMap(THUMB + 'office.go', 'officeKinds', 'thumbnails of office documents, by ONLYOFFICE').map(([k]) => k);
anchor(THUMB + 'office.go', /if ext == "key" \{/, 'a .key is drawn only when it is a Keynote package');
const archiveFn = anchor(THUMB + 'archive.go', /func archiveKind\(name string\) string \{([\s\S]*?)\n\}/, 'thumbnails of archives: their file list')[1];
const THUMB_ARCHIVE = [...archiveFn.matchAll(/HasSuffix\(n, "\.([a-z0-9.]+)"\)/g)].map((m) => m[1]);
const goImages = anchor(THUMB + 'image.go', /import \(([\s\S]*?)\n\)/, 'image formats Go decodes')[1];
const GO_DECODES = new Set([...goImages.matchAll(/"(?:image|golang\.org\/x\/image)\/(\w+)"/g)].map((m) => m[1]));
anchor(THUMB + 'jpegout.go', /"image\/jpeg"/, 'JPEG decoder');
GO_DECODES.add('jpeg');
anchor(THUMB + 'pipeline.go', /const svgMime = "image\/svg\+xml"/, 'SVG drawn by the SVG engine');
const heif = anchor(THUMB + 'notool.go', /func isHEIF\(mime string\) bool \{([\s\S]*?)\n\}/, 'HEIF and AVIF need ImageMagick')[1];
const HEIF = new Set(quoted(heif));
anchor(THUMB + 'notool.go', /case strings\.HasPrefix\(mime, "video\/"\):\s*return ToolVideo/, 'video needs ffmpeg');
anchor(THUMB + 'notool.go', /case strings\.HasPrefix\(mime, "audio\/"\):\s*return ToolAudio/, 'audio needs ffmpeg');
anchor(THUMB + 'notool.go', /case mime == "application\/pdf":\s*return ToolPDF/, 'PDF needs Ghostscript or poppler');

const providers = anchor('backend/internal/archivecli/archivecli.go', /Name: "builtin-zip"[\s\S]*?ExtractFormats: \[\]string\{([^}]*)\}/, 'archives filex extracts by itself')[1];
const ARCHIVE_BUILTIN = quoted(providers);
const sevenZip = anchor('backend/internal/archivecli/archivecli.go', /Name: "sevenzip"[\s\S]*?ExtractFormats: \[\]string\{([^}]*)\}/, 'archives 7-Zip extracts')[1];
const ARCHIVE_7Z = quoted(sevenZip);
anchor('backend/internal/archivecli/archivecli.go', /if probe\.rar \{/, 'RAR, when the 7-Zip found has it');
const ARCHIVE_UI = tsList('packages/core/src/FileExplorer.vue', 'ARCHIVE_EXTENSIONS', 'archives the explorer offers to open and extract');

const registry = anchor('backend/internal/newdoc/newdoc.go', /var registry = \[\]tmpl\{([\s\S]*?)\n\}/, 'the New menu')[1];
const NEW = [
  ...[...registry.matchAll(/container\("([a-z0-9]+)", \w+, [^,]+, (\w+)\)/g)].map((m) => [m[1], m[2]]),
  ...[...registry.matchAll(/textType\("([a-z0-9]+)"/g)].map((m) => [m[1], '']),
];

const CONFIG = 'backend/internal/config/config.go';
anchor(CONFIG, /os\.Getenv\("FILEX_ONLYOFFICE_URL"\)/, 'setting: the ONLYOFFICE Document Server');
anchor(CONFIG, /os\.Getenv\("FILEX_ONLYOFFICE_JWT"\)/, 'setting: the ONLYOFFICE secret');
anchor(CONFIG, /os\.Getenv\("FILEX_DRAWIO_URL"\)/, 'setting: the draw.io server');
anchor(CONFIG, /os\.Getenv\("FILEX_ARCHIVE_7Z_BIN"\)/, 'setting: 7-Zip');

// ---------------------------------------------------------------- what filex does

// What the default image carries (a tool there works out of the box in
// filex's own image; the slim image and other installs may lack it).
const DOCKER = 'docker/Dockerfile';
const apk = anchor(DOCKER, /RUN apk --no-cache add \\([\s\S]*?)&& addgroup/, 'the tools in filex\'s default image')[1];
const inImage = (pkg) => new RegExp(`(^|\\s|')${pkg.replace(/[+]/g, '\\+')}(\\s|>|'|\\\\)`).test(apk);

// Services an administrator connects, and programs on the server.
const ENGINES = {
  office: { label: 'ONLYOFFICE', kind: 'service', setting: 'FILEX_ONLYOFFICE_URL with FILEX_ONLYOFFICE_JWT, or Admin > External services' },
  drawio: { label: 'draw.io', kind: 'service', setting: 'FILEX_DRAWIO_URL' },
  ffmpeg: { label: 'ffmpeg', kind: 'tool', in_image: inImage('ffmpeg') },
  ghostscript: { label: 'Ghostscript or poppler', kind: 'tool', in_image: inImage('ghostscript') || inImage('poppler-utils') },
  imagemagick: { label: 'ImageMagick with libheif', kind: 'tool', in_image: inImage('imagemagick-heic') },
  sevenzip: { label: '7-Zip', kind: 'tool', setting: 'FILEX_ARCHIVE_7Z_BIN', in_image: inImage('7zip') },
  // Whether a 7-Zip reads RAR is probed at run time (archivecli probe.rar):
  // the image is not promised to.
  'sevenzip-rar': { label: '7-Zip with RAR support', kind: 'tool', setting: 'FILEX_ARCHIVE_7Z_BIN', in_image: false },
};
const NEEDS = { RequiresOnlyOffice: 'office', RequiresDrawio: 'drawio' };
const ORDER = ['preview', 'play', 'browse', 'edit', 'thumbnail', 'create'];

const types = {};
function can(ext, what, needs = '', via = '') {
  ext = ext.toLowerCase();
  const t = (types[ext] ??= { can: [] });
  if (t.can.some((c) => c.do === what)) return;
  t.can.push({ do: what, ...(needs ? { needs } : {}), ...(via ? { via } : {}) });
}

// Preview, in the order the preview picks a kind (PreviewModal `kind`).
const taken = new Set();
const pick = (ext) => !taken.has(ext) && (taken.add(ext), true);
for (const e of IMAGE) {
  // not claimed: HEIC goes to an <img>, which only Safari decodes; filex
  // itself does not convert it for the browser.
  if (pick(e) && e !== 'heic') can(e, 'preview', '', 'image viewer');
}
for (const e of VIDEO) if (pick(e)) can(e, 'play', '', 'video player');
for (const e of AUDIO) if (pick(e)) can(e, 'play', '', 'audio player');
if (pick('pdf')) can('pdf', 'preview', '', 'the browser\'s PDF viewer');
for (const e of ['md', 'markdown']) if (pick(e)) can(e, 'preview', '', 'Markdown');
for (const e of CODE) if (pick(e)) can(e, 'preview', '', 'code view');
const VIEWER_NAMES = { EpubViewer: 'e-book viewer', MermaidViewer: 'Mermaid', TiffViewer: 'TIFF viewer', PsdViewer: 'Photoshop viewer',
  IpynbViewer: 'notebook viewer', CsvViewer: 'table view', DrawioViewer: 'draw.io', Viewer3D: '3D viewer', ArchiveViewer: 'archive list' };
const builtinArchives = new Set(ARCHIVE_BUILTIN.flatMap((f) => [f, ...(f === 'tar.gz' ? ['tgz'] : f === 'tar.bz2' ? ['tbz2'] : [])]));
const sevenArchives = new Set(ARCHIVE_7Z.flatMap((f) => [f, ...(f === 'tar.xz' ? ['txz'] : [])]));
function browse(e) {
  if (builtinArchives.has(e)) can(e, 'browse', '', 'archive list');
  else if (sevenArchives.has(e)) can(e, 'browse', 'sevenzip', 'archive list');
  else if (e === 'rar') can(e, 'browse', 'sevenzip-rar', 'archive list');
}
for (const [e, comp] of VIEWERS) {
  if (!pick(e)) continue;
  if (!(comp in VIEWER_NAMES)) throw new Error(`${PREVIEW}: a viewer this script does not know: ${comp} (${e}); read it and add it`);
  if (comp === 'Viewer3D' && !MODEL3D.includes(e)) continue; // not claimed: routed to the 3D viewer, which refuses it
  if (comp === 'ArchiveViewer') browse(e);
  else if (comp === 'DrawioViewer') can(e, 'preview', 'drawio', VIEWER_NAMES[comp]);
  else can(e, 'preview', '', VIEWER_NAMES[comp]);
}
for (const e of OFFICE) if (pick(e)) can(e, 'preview', 'office', 'ONLYOFFICE');
for (const e of TEXT_PLAIN) if (pick(e)) can(e, 'preview', '', 'text view');
for (const e of TEXT) if (pick(e)) can(e, 'preview', '', 'text view');
for (const e of [...ARCHIVE_UI, ...ARCHIVE_BUILTIN, ...ARCHIVE_7Z]) browse(e);

// Edit: the Edit button (PreviewModal canEditKind), and what saves. The
// surface a kind opens in, in edit mode, in the order the preview picks it.
function editSurface(e) {
  if (IMAGE.includes(e)) return 'image';
  if (VIDEO.includes(e) || AUDIO.includes(e) || e === 'pdf') return 'media';
  if (e === 'md' || e === 'markdown') return 'markdown';
  if (CODE.includes(e)) return 'code';
  if (VIEWERS.some(([x]) => x === e)) return 'viewer';
  if (OFFICE.includes(e)) return 'office';
  if (TEXT_PLAIN.includes(e) || TEXT.includes(e)) return 'code';
  return 'other';
}
for (const e of TEXT) {
  const s = editSurface(e);
  if (s === 'markdown') can(e, 'edit', '', 'Markdown editor');
  else if (s === 'code') can(e, 'edit', '', 'code editor');
  else if (s === 'image') continue; // not claimed: an image (svg) shows as a picture, in edit mode too
  else if (s === 'viewer') {
    // A source viewer's Edit (Mermaid, filex 0.56): its text in the text
    // editor, which save-text saves.
    if (SOURCE_VIEWERS.includes(e)) { can(e, 'edit', '', 'code editor'); continue; }
    // not claimed: a viewer that only shows its file (the table a .csv is
    // drawn as) gets no Edit.
    if (!EDITING_VIEWERS.includes(e)) continue;
    // not claimed: only a viewer handed a save address saves what it shows
    // (draw.io); before filex 0.56 Edit was also offered on a Mermaid
    // diagram, whose viewer has no save.
    if (!SAVING_VIEWERS.includes(e)) continue;
    const comp = VIEWERS.find(([x]) => x === e)[1];
    if (comp !== 'DrawioViewer') throw new Error(`${PREVIEW}: a viewer that saves which this script does not know: ${comp} (${e}); read it and add it`);
    can(e, 'edit', 'drawio', 'draw.io');
  } else throw new Error(`${EDITKIND}: text the preview opens as ${s}: ${e}; read the preview at ${tag} and update this script`);
}
for (const e of OFFICE) can(e, 'edit', 'office', 'ONLYOFFICE');
for (const e of OFFICE_OPEN) can(e, 'edit', 'office', 'ONLYOFFICE');

// Thumbnails.
for (const [e, mime] of THUMB_MIMES) {
  const sub = mime.split('/')[1];
  if (mime === 'image/svg+xml') can(e, 'thumbnail', '', 'SVG engine');
  else if (HEIF.has(mime)) can(e, 'thumbnail', 'imagemagick');
  else if (mime.startsWith('image/') && GO_DECODES.has(sub)) can(e, 'thumbnail');
  else if (mime.startsWith('video/') || mime.startsWith('audio/')) can(e, 'thumbnail', 'ffmpeg');
  else if (mime === 'application/pdf') can(e, 'thumbnail', 'ghostscript');
  // office types: by name below (OnlyOfficeDraws)
}
for (const e of THUMB_OFFICE) {
  if (e === 'key') continue; // not claimed: only a Keynote package; the same extension names TLS and license keys
  can(e, 'thumbnail', 'office', 'ONLYOFFICE');
}
for (const e of THUMB_TEXT) can(e, 'thumbnail', '', 'first lines');
for (const e of THUMB_ARCHIVE) can(e, 'thumbnail', '', 'file list');

// The New menu.
for (const [e, req] of NEW) {
  if (req && !(req in NEEDS)) throw new Error(`newdoc.go: a requirement this script does not know: ${req}`);
  can(e, 'create', req ? NEEDS[req] : '');
}

for (const t of Object.values(types)) t.can.sort((a, b) => ORDER.indexOf(a.do) - ORDER.indexOf(b.do));
const sorted = (o) => Object.fromEntries(Object.keys(o).sort().map((k) => [k, o[k]]));
const doc = {
  filex: version,
  tag,
  commit,
  generated_by: 'scripts/gen-filex-builtin.mjs',
  engines: ENGINES,
  types: sorted(types),
  mimes: sorted(MIMES),
  sources,
};
const text = JSON.stringify(doc, null, 2) + '\n';

if (flag === '--check') {
  let current = '';
  try {
    current = readFileSync(OUT, 'utf8');
  } catch {
    // absent: a difference
  }
  if (current === text) {
    console.log(`filex-builtin.json is what ${tag} (${commit.slice(0, 10)}) says: ${Object.keys(types).length} kinds`);
    process.exit(0);
  }
  const was = current ? JSON.parse(current) : { types: {} };
  for (const e of new Set([...Object.keys(was.types ?? {}), ...Object.keys(types)])) {
    const a = JSON.stringify(was.types?.[e] ?? null);
    const b = JSON.stringify(types[e] ?? null);
    if (a !== b) console.log(`${e}: ${a} -> ${b}`);
  }
  console.error(`filex-builtin.json differs from what ${tag} says; run without --check to write it`);
  process.exit(1);
}
writeFileSync(OUT, text);
console.log(`wrote ${OUT}: filex ${version} (${commit.slice(0, 10)}), ${Object.keys(types).length} kinds, ${Object.keys(MIMES).length} media types`);
