import { beforeEach, describe, expect, it } from 'vitest';
import { ApiError, api, isStorePath, listApps, queryString, setCsrfToken } from '@/api/client';
import { mockFetch } from './helpers';

describe('the API client talks to the store only', () => {
  it('accepts paths on this origin and refuses anything else', () => {
    expect(isStorePath('/api/v1/apps')).toBe(true);
    expect(isStorePath('/v1/index.json')).toBe(true);
    for (const bad of ['https://api.github.com/repos/x/y', '//evil.example/x', '/\\evil.example', 'api/v1/apps', 'javascript:alert(1)', '']) {
      expect(isStorePath(bad), bad).toBe(false);
    }
  });

  it('never calls fetch for an address outside the store', async () => {
    const { fn } = mockFetch({});
    await expect(api('GET', 'https://api.github.com/user')).rejects.toMatchObject({ code: 'not_a_store_path' });
    await expect(api('GET', '//raw.githubusercontent.com/x')).rejects.toBeInstanceOf(ApiError);
    expect(fn).not.toHaveBeenCalled();
  });
});

describe('CSRF', () => {
  beforeEach(() => setCsrfToken(''));

  it('sends the session token on every state change and never on a read', async () => {
    const { calls } = mockFetch({
      'GET /api/v1/apps': { body: { items: [], total: 0, page: 1, per_page: 50 } },
      'POST /api/v1/submissions': { status: 202, body: { id: 7 } },
      'DELETE /api/v1/admin/apps/x/revoke': { body: {} },
    });
    setCsrfToken('tok-123');
    await listApps();
    await api('POST', '/api/v1/submissions', { publisher: 'p', repo: 'a/b', ref: 'v1' });
    await api('DELETE', '/api/v1/admin/apps/x/revoke');
    expect(calls[0].headers['X-CSRF-Token']).toBeUndefined();
    expect(calls[1].headers['X-CSRF-Token']).toBe('tok-123');
    expect(calls[1].headers['Content-Type']).toBe('application/json');
    expect(calls[2].headers['X-CSRF-Token']).toBe('tok-123');
  });
});

describe('errors', () => {
  it('carries the contract error code, the sentence, the body and Retry-After', async () => {
    mockFetch({
      'POST /api/v1/submissions': {
        status: 429,
        body: { error: 'rate_limited', message: 'Too many submissions.', submission: { id: 3 } },
        headers: { 'Retry-After': '42' },
      },
    });
    const e = await api('POST', '/api/v1/submissions', {}).catch((x: unknown) => x);
    expect(e).toBeInstanceOf(ApiError);
    const a = e as ApiError;
    expect([a.status, a.code, a.message, a.retryAfter]).toEqual([429, 'rate_limited', 'Too many submissions.', 42]);
    expect(a.body.submission).toEqual({ id: 3 });
  });

  it('names an HTTP status when the body is not the contract error', async () => {
    mockFetch({ 'GET /api/v1/me': { status: 502, body: 'gateway' } });
    await expect(api('GET', '/api/v1/me')).rejects.toMatchObject({ status: 502, code: 'http_502' });
  });
});

describe('queryString', () => {
  it('leaves out empty values and false', () => {
    expect(queryString({ q: '', kind: 'app', verified: false, page: 2, x: undefined, y: null })).toBe('?kind=app&page=2');
    expect(queryString({})).toBe('');
    expect(queryString({ verified: true })).toBe('?verified=true');
  });
});
