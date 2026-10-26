import { describe, expect, it } from 'vitest';
import { catalogApiQuery, catalogRouteQuery, filtersActive, parseCatalogQuery } from '@/lib/catalogQuery';

describe('catalog filters in the address', () => {
  it('reads the plain catalog from an empty query', () => {
    const f = parseCatalogQuery({});
    expect(f).toEqual({ q: '', kind: '', category: '', publisher: '', filex: '', verified: false, sort: 'name', page: 1 });
    expect(filtersActive(f)).toBe(false);
    expect(catalogRouteQuery(f)).toEqual({});
  });

  it('drops malformed values instead of sending them to the server', () => {
    const f = parseCatalogQuery({ kind: 'plugin', filex: '0.50', page: '-3', sort: 'stars', verified: 'yes' });
    expect([f.kind, f.filex, f.page, f.sort, f.verified]).toEqual(['', '', 1, 'name', false]);
    expect(parseCatalogQuery({ page: 'abc' }).page).toBe(1);
  });

  it('round-trips every filter through the address', () => {
    const q = { q: 'markdown', kind: 'language_pack', category: 'developer', publisher: 'jdoe', filex: '0.50.0', verified: '1', sort: 'updated', page: '3' };
    const f = parseCatalogQuery(q);
    expect(f).toEqual({ q: 'markdown', kind: 'language_pack', category: 'developer', publisher: 'jdoe', filex: '0.50.0', verified: true, sort: 'updated', page: 3 });
    expect(catalogRouteQuery(f)).toEqual(q);
    expect(filtersActive(f)).toBe(true);
  });

  it('asks the API with the contract\'s parameter names', () => {
    expect(catalogApiQuery(parseCatalogQuery({ q: ' sign ', verified: 'true', page: '2' }))).toEqual({
      q: 'sign',
      kind: undefined,
      category: undefined,
      publisher: undefined,
      filex: undefined,
      verified: true,
      sort: 'name',
      page: 2,
      per_page: 50,
    });
  });
});
