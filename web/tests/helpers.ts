// Test helpers: a fetch double answering by method and path, a router on
// memory history over the real route table, and a way to let promises settle.
import { vi } from 'vitest';
import { createMemoryHistory, createRouter, type Router } from 'vue-router';
import { flushPromises } from '@vue/test-utils';
import { routes } from '@/router';

export interface Answer {
  status?: number;
  body?: unknown;
  headers?: Record<string, string>;
}
export type Handler = (url: URL, init: RequestInit) => Answer | Promise<Answer>;

export interface FetchCall {
  method: string;
  path: string;
  headers: Record<string, string>;
  body: unknown;
}

/** Stub `fetch`: `routes` maps "METHOD /path" (no query) to an answer. Unknown paths answer 404. */
export function mockFetch(table: Record<string, Answer | Handler>) {
  const calls: FetchCall[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), 'http://store.test');
    const method = (init.method ?? 'GET').toUpperCase();
    const headers = (init.headers ?? {}) as Record<string, string>;
    calls.push({ method, path: url.pathname + url.search, headers, body: init.body ? JSON.parse(String(init.body)) : undefined });
    const h = table[`${method} ${url.pathname}`];
    const a: Answer = typeof h === 'function' ? await h(url, init) : h ?? { status: 404, body: { error: 'not_found', message: 'No such endpoint.' } };
    const status = a.status ?? 200;
    return new Response(a.body === undefined || status === 204 ? null : JSON.stringify(a.body), {
      status,
      headers: { 'Content-Type': 'application/json', ...(a.headers ?? {}) },
    });
  });
  vi.stubGlobal('fetch', fn);
  return { fn, calls };
}

export function testRouter(): Router {
  return createRouter({ history: createMemoryHistory(), routes });
}

export async function settle(times = 3) {
  for (let i = 0; i < times; i++) await flushPromises();
}

export const ME_PUBLISHER = {
  account: { id: 11, display_name: 'Jane Doe' },
  identity: { provider: 'github', login: 'jdoe' },
  roles: [],
  publishers: [
    { id: 'jdoe', name: 'Jane Doe', verified: false, official: false, role: 'owner', status: 'active' },
    { id: 'acme-tools', name: 'Acme Tools', verified: false, official: false, role: 'owner', status: 'pending' },
  ],
  role_source: 'github repo permission acme/store-admins: none',
  csrf_token: 'tok-pub',
  providers: { github: true },
} as const;

export const ME_REVIEWER = {
  account: { id: 21, display_name: 'Rita' },
  identity: { provider: 'github', login: 'rita' },
  roles: ['reviewer'],
  role_source: 'github repo permission acme/store-admins: triage',
  publishers: [],
  csrf_token: 'tok-rev',
  providers: { github: true },
} as const;

export const ME_ADMIN = {
  account: { id: 1, display_name: 'Alice' },
  identity: { provider: 'github', login: 'alice' },
  roles: ['admin'],
  role_source: 'github repo permission acme/store-admins: write',
  publishers: [],
  csrf_token: 'tok-adm',
  providers: { github: true },
} as const;
