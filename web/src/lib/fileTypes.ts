// The words for a file-type search's answer. Only words: which app works
// with which kind, and what filex does by itself, is the server's
// (backend/internal/filetypes reads the manifests, matches, and carries the
// data generated from a filex release); nothing here decides it.
import type { BuiltinFileType, ConversionRow, FileTypeRole, FileTypeSearch, FileTypeUse } from '@/api/client';
import { S, fmt } from '@/strings';
import { text } from '@/lib/format';

/** ".docx" */
export const dotted = (ext: string) => `.${ext}`;

/** ".pdf", ".pdf or .docx", ".pdf, .docx or .odt" */
export function extList(exts: readonly string[]): string {
  const d = exts.map(dotted);
  if (d.length <= 1) return d[0] ?? '';
  return `${d.slice(0, -1).join(', ')} ${S.fileTypes.or} ${d[d.length - 1]}`;
}

/** An engine's name: the server's label, else the store's word for an app's engine id ("office": ONLYOFFICE). */
export function engineName(id: string | undefined, search?: FileTypeSearch | null): string {
  if (!id) return '';
  return search?.engines.find((e) => e.id === id)?.label ?? S.fileTypes.engines[id] ?? id;
}

/** ".pdf, .epub, .zip +7 more": at most `max` extensions, then how many more. */
export function someExts(exts: readonly string[], max = Infinity): string {
  const shown = exts.slice(0, max).map(dotted).join(', ');
  return exts.length > max ? `${shown} ${fmt(S.fileTypes.more, { count: exts.length - max })}` : shown;
}

/** An app engine's name (a manifest's `engines:<id>`): the store's word for it, else the id. */
export const appEngineName = (id: string) => S.fileTypes.engines[id] ?? id;

/**
 * What a conversion needs, from the server's `needs_any` (any one of these
 * sets, every engine of a set): "ONLYOFFICE", "ImageMagick, Ghostscript or
 * Poppler", "ONLYOFFICE and Poppler, or ONLYOFFICE and Ghostscript". Empty
 * when it needs nothing.
 */
export function needsPhrase(needsAny: readonly (readonly string[])[] | undefined): string {
  if (!needsAny?.length) return '';
  const sets = needsAny.map((set) => set.map(appEngineName).join(` ${S.fileTypes.and} `));
  if (sets.length === 1) return sets[0];
  // Single engines: "A, B or C"; sets of several: "A and B, or A and C".
  if (needsAny.every((set) => set.length === 1)) return `${sets.slice(0, -1).join(', ')} ${S.fileTypes.or} ${sets[sets.length - 1]}`;
  return sets.join(`, ${S.fileTypes.or} `);
}

/** A `convert` role in full: "Converts .docx → .odt, .rtf (with ONLYOFFICE)". */
export function convertPhrase(r: FileTypeRole, ext: string | undefined, max = Infinity): string {
  const what = fmt(ext ? S.fileTypes.converts : S.fileTypes.convertsTo, { ext: ext ? dotted(ext) : '', to: someExts(r.to ?? [], max) });
  const needs = needsPhrase(r.needs_any);
  return needs ? fmt(S.fileTypes.withEngine, { what, engine: needs }) : what;
}

/**
 * The card's one line for an app's conversions of a kind: the conversions
 * that need nothing (or, when every one needs something, the first
 * condition's) with at most `max` targets, then how many more need an
 * engine: "Converts .docx → .pdf, .epub, .zip +7 more, and 19 more with an
 * engine". Empty when the use has no `convert` role.
 */
export function convertSummary(u: FileTypeUse, max = 3): string {
  const roles = u.roles.filter((r) => r.role === 'convert');
  if (!roles.length) return '';
  const [first, ...rest] = roles;
  const head = convertPhrase(first, u.ext, max);
  const more = rest.reduce((n, r) => n + (r.to?.length ?? 0), 0);
  return more ? fmt(S.fileTypes.andMoreWithEngine, { what: head, count: more }) : head;
}

/** A conversion as a listing declares it, for a reviewer: ".docx, .odt → .pdf (with ONLYOFFICE)". */
export function declarationLine(c: { from: readonly string[]; to: readonly string[]; needs?: readonly string[] }): string {
  const what = `${c.from.map(dotted).join(', ')} → ${c.to.map(dotted).join(', ')}`;
  return c.needs?.length ? fmt(S.fileTypes.withEngine, { what, engine: needsPhrase([c.needs]) }) : what;
}

/** One thing an app does with a kind: "Opens", "Sign", "To PDF → .pdf", "Signatures panel", "Converts .docx → .pdf". */
export function rolePhrase(r: FileTypeRole, ext?: string): string {
  if (r.role === 'convert') return convertPhrase(r, ext);
  let what: string;
  if (r.role === 'action') {
    const label = (text(r.label) || r.id || '').replace(/…$/, '');
    what = r.output_ext ? fmt(S.fileTypes.actionTo, { label, ext: dotted(r.output_ext) }) : label;
  } else {
    what = fmt(S.fileTypes.role[r.role] ?? r.role, { label: text(r.label) || r.id || '' });
  }
  return r.engine ? fmt(S.fileTypes.withEngine, { what, engine: engineName(r.engine) }) : what;
}

/** Everything an app does with one kind, each phrase once. */
export function usePhrases(u: FileTypeUse): string[] {
  return [...new Set(u.roles.map((r) => rolePhrase(r, u.ext)))];
}

/** What a card says an app does with one kind: the manifest's roles in full, its conversions summed up (convertSummary). */
export function cardPhrases(u: FileTypeUse): string[] {
  const own = [...new Set(u.roles.filter((r) => r.role !== 'convert').map((r) => rolePhrase(r, u.ext)))];
  const conv = convertSummary(u);
  return conv ? [...own, conv] : own;
}

/** A conversion table row's cells: the kinds, what needs nothing, what needs an engine (each condition said once). */
export function conversionCells(row: ConversionRow): { from: string; to: string; withEngine: string } {
  const free = row.roles.filter((r) => r.role === 'convert' && !r.needs_any?.length);
  const engine = row.roles.filter((r) => r.role === 'convert' && r.needs_any?.length);
  return {
    from: row.from.map(dotted).join(', '),
    to: free.map((r) => someExts(r.to ?? [])).join(', '),
    withEngine: engine.map((r) => fmt(S.fileTypes.toWith, { to: someExts(r.to ?? []), engine: needsPhrase(r.needs_any) })).join('; '),
  };
}

/** The kind a use is about: ".docx", "image/*" or "Any file". */
export function useKind(u: FileTypeUse): string {
  if (u.ext) return dotted(u.ext);
  if (u.mime) return u.mime;
  return S.fileTypes.anyFile;
}

/** A stable key for a use in a list. */
export const useKey = (u: FileTypeUse) => (u.ext ? `ext:${u.ext}` : u.mime ? `mime:${u.mime}` : 'any');

/** Whether the server matched the app for a kind it names itself (not only for every file). */
export function namesAKind(app: { matched_extensions?: FileTypeUse[] }): boolean {
  return !!app.matched_extensions?.some((u) => !u.any_file);
}

const OPENS = ['preview', 'play', 'browse'];

/**
 * The panel's title, as true as the data: "out of the box" only when filex
 * shows the file with nothing connected; "with ONLYOFFICE" when it needs
 * that; otherwise only that it does something with it.
 */
export function builtinTitle(t: BuiltinFileType, search: FileTypeSearch): string {
  const ext = dotted(t.ext);
  const opens = t.can.filter((c) => OPENS.includes(c.do));
  if (opens.some((c) => !c.needs)) return fmt(S.fileTypes.builtinTitle, { ext });
  if (opens.length) return fmt(S.fileTypes.builtinTitleWith, { ext, engine: engineName(opens[0].needs, search) });
  return fmt(S.fileTypes.builtinTitleSome, { ext });
}

/** What filex does, grouped by what it needs: "preview, new file; edit with ONLYOFFICE". */
export function builtinPhrase(t: BuiltinFileType, search: FileTypeSearch): string {
  const groups = new Map<string, string[]>();
  for (const c of t.can) {
    const key = c.needs ?? '';
    groups.set(key, [...(groups.get(key) ?? []), S.fileTypes.can[c.do] ?? c.do]);
  }
  return [...groups.entries()]
    .sort(([a], [b]) => (a === '' ? -1 : b === '' ? 1 : 0))
    .map(([needs, what]) => (needs ? fmt(S.fileTypes.canWith, { what: what.join(', '), engine: engineName(needs, search) }) : what.join(', ')))
    .join('; ');
}

/** For each engine the panel names: what it is and where a filex administrator turns it on. */
export function engineNotes(search: FileTypeSearch): string[] {
  return search.engines.map((e) => {
    const tmpl =
      e.kind === 'service'
        ? S.fileTypes.engineService
        : e.in_image
          ? e.setting
            ? S.fileTypes.engineToolImageSetting
            : S.fileTypes.engineToolImage
          : e.setting
            ? S.fileTypes.engineToolSetting
            : S.fileTypes.engineTool;
    return fmt(tmpl, { label: e.label, setting: e.setting ?? '' });
  });
}
