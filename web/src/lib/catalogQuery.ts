// The catalog's filters live in the address (shareable, Back works). This is
// the one translation between the route's query and `GET /api/v1/apps`.
import type { LocationQuery, LocationQueryRaw } from 'vue-router';
import type { AppKind, AppQuery } from '@/api/client';

export interface CatalogFilters {
  q: string;
  kind: AppKind | '';
  category: string;
  publisher: string;
  filex: string;
  verified: boolean;
  sort: 'name' | 'updated';
  page: number;
}

export const PER_PAGE = 50;
export const FILEX_VERSION = /^[0-9]+[.][0-9]+[.][0-9]+$/;
const KINDS: AppKind[] = ['app', 'language_pack', 'storage'];

function one(v: LocationQuery[string] | undefined): string {
  const s = Array.isArray(v) ? v[0] : v;
  return typeof s === 'string' ? s : '';
}

/** Read filters from a route query; anything malformed falls back to its default. */
export function parseCatalogQuery(q: LocationQuery): CatalogFilters {
  const kind = one(q.kind) as AppKind;
  const page = Number.parseInt(one(q.page), 10);
  const filex = one(q.filex).trim();
  return {
    q: one(q.q).slice(0, 100),
    kind: KINDS.includes(kind) ? kind : '',
    category: one(q.category),
    publisher: one(q.publisher),
    filex: FILEX_VERSION.test(filex) ? filex : '',
    verified: one(q.verified) === '1' || one(q.verified) === 'true',
    sort: one(q.sort) === 'updated' ? 'updated' : 'name',
    page: Number.isFinite(page) && page > 1 ? page : 1,
  };
}

/** The route query for these filters: defaults are left out, so `/` is the plain catalog. */
export function catalogRouteQuery(f: CatalogFilters): LocationQueryRaw {
  const out: LocationQueryRaw = {};
  if (f.q.trim()) out.q = f.q.trim();
  if (f.kind) out.kind = f.kind;
  if (f.category) out.category = f.category;
  if (f.publisher) out.publisher = f.publisher;
  if (f.filex) out.filex = f.filex;
  if (f.verified) out.verified = '1';
  if (f.sort !== 'name') out.sort = f.sort;
  if (f.page > 1) out.page = String(f.page);
  return out;
}

/** The API query for these filters. */
export function catalogApiQuery(f: CatalogFilters): AppQuery {
  return {
    q: f.q.trim() || undefined,
    kind: f.kind || undefined,
    category: f.category || undefined,
    publisher: f.publisher || undefined,
    filex: f.filex || undefined,
    verified: f.verified || undefined,
    sort: f.sort,
    page: f.page,
    per_page: PER_PAGE,
  };
}

export function filtersActive(f: CatalogFilters): boolean {
  return !!(f.q.trim() || f.kind || f.category || f.publisher || f.filex || f.verified);
}

/** How the results are drawn: cards (the shop window) or filex's table. */
export type CatalogView = 'grid' | 'list';

/** The view is display state, not a filter: it rides in the address beside them. */
export function parseCatalogView(q: LocationQuery): CatalogView {
  return one(q.view) === 'list' ? 'list' : 'grid';
}

export function withView(q: LocationQueryRaw, view: CatalogView): LocationQueryRaw {
  return view === 'list' ? { ...q, view: 'list' } : q;
}
