import { afterEach, describe, expect, it, vi } from 'vitest';
import { mount } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { session } from '@/lib/session';
import type { Me } from '@/api/client';
import { ME_ADMIN, ME_PUBLISHER, ME_REVIEWER, mockFetch, settle, testRouter, type Handler } from './helpers';

const passed = (kind: string) => ({ kind, status: 'passed', started_at: '2026-10-03T10:00:00Z', ended_at: '2026-10-03T10:00:02Z' });
const SUB = {
  id: 101,
  publisher: { id: 'jdoe', name: 'Jane Doe', verified: false, official: false },
  app: 'markdown-preview',
  version: '1.1.0',
  repo: 'jdoe/filex-markdown-preview',
  ref: 'v1.1.0',
  status: 'needs_review',
  attempt: 1,
  created_at: '2026-10-03T10:00:00Z',
  updated_at: '2026-10-03T10:01:00Z',
  checks: ['repo_access', 'tag', 'manifest', 'consent', 'policy', 'artifacts', 'media', 'dry_run'].map(passed),
  dry_runs: [],
  reviews: [],
  resolution: { name: 'markdown-preview', description_md: '# Title\n\n<b>not bold</b> <script>x()</script>' },
};

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

async function open(me: unknown, sub: Record<string, unknown> = SUB, extra: Record<string, Handler | object> = {}) {
  session.loaded = true;
  session.me = me as Me | null;
  const f = mockFetch({ 'GET /api/v1/submissions/101': { body: sub }, 'GET /api/v1/apps/markdown-preview': { body: { versions: [] } }, ...extra });
  const router = testRouter();
  await router.push('/submissions/101');
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  await settle();
  return { w, ...f };
}

afterEach(() => {
  document.body.replaceChildren();
  session.me = null;
  vi.useRealTimers();
});

describe('a submission, for a reviewer', () => {
  it('can be approved once every check passed', async () => {
    const { w } = await open(ME_REVIEWER);
    expect(w.find('[data-testid="approve"]').attributes('disabled')).toBeUndefined();
    expect(w.find('[data-testid="withdraw"]').exists()).toBe(false);
  });

  it('cannot be decided by a reviewer who is a member of its publisher (four eyes); an admin may (Q9)', async () => {
    const member = { ...ME_REVIEWER, publishers: [{ id: 'jdoe', name: 'Jane Doe', verified: false, official: false, role: 'owner', status: 'active' }] };
    const { w } = await open(member);
    expect(w.find('[data-testid="approve"]').attributes('disabled')).toBeDefined();
    expect(w.find('[data-testid="reject"]').attributes('disabled')).toBeDefined();
    expect(w.text()).toContain('another reviewer has to decide');
    w.unmount();
    const admin = await open({ ...ME_ADMIN, publishers: member.publishers });
    expect(admin.w.find('[data-testid="approve"]').attributes('disabled')).toBeUndefined();
  });

  it('cannot be approved while a check is still failed or running', async () => {
    const { w } = await open(ME_REVIEWER, { ...SUB, checks: [...SUB.checks.slice(0, 7), { ...passed('dry_run'), status: 'failed' }] });
    expect(w.find('[data-testid="approve"]').attributes('disabled')).toBeDefined();
  });

  it('approves with the security flag and says which index serial was published', async () => {
    const { w, calls } = await open(ME_REVIEWER, SUB, {
      'POST /api/v1/review/submissions/101/approve': { body: { submission: { ...SUB, status: 'approved' }, version: {}, index_serial: 13 } },
    });
    await w.find('[data-testid="approve"]').trigger('click');
    await settle();
    await w.find('[data-testid="approve-security"]').setValue(true);
    await w.find('dialog form').trigger('submit');
    await settle(6);
    const post = calls.find((c) => c.method === 'POST')!;
    expect(post.path).toBe('/api/v1/review/submissions/101/approve');
    expect(post.body).toEqual({ security: true });
    expect(w.find('[data-testid="action-done"]').text()).toContain('serial 13');
  });

  it('explains each check\'s code in words, and shows a code it does not know raw', async () => {
    const checks = [
      { ...passed('artifacts'), status: 'failed', code: 'sha256_mismatch', message: 'ui.zip: expected 6f1e, got 0c9d.' },
      { ...passed('dry_run'), status: 'failed', code: 'brand_new_code', message: 'Something new.' },
    ];
    const { w } = await open(ME_REVIEWER, { ...SUB, status: 'failed', checks });
    const text = w.find('[data-testid="checks-table"]').text();
    expect(text).toContain('SHA-256 differs from the manifest');
    expect(text).toContain('ui.zip: expected 6f1e, got 0c9d.');
    expect(text).toContain('"brand_new_code"');
  });

  it('shows the description\'s Markdown as text, never as markup', async () => {
    const { w } = await open(ME_REVIEWER);
    const pre = w.find('[data-testid="description-md"]');
    expect(pre.text()).toContain('<b>not bold</b>');
    expect(pre.find('b').exists()).toBe(false);
    expect(w.find('script').exists()).toBe(false);
  });
});

describe('a submission, for its publisher', () => {
  it('can be withdrawn, not decided', async () => {
    const { w } = await open(ME_PUBLISHER);
    expect(w.find('[data-testid="withdraw"]').exists()).toBe(true);
    expect(w.find('[data-testid="approve"]').exists()).toBe(false);
  });

  it('a failed one runs again', async () => {
    const { w, calls } = await open(ME_PUBLISHER, { ...SUB, status: 'failed', failure: { code: 'sha256_mismatch', message: 'Bundle changed.' } }, {
      'POST /api/v1/submissions/101/retry': { status: 202, body: { ...SUB, status: 'queued' } },
    });
    expect(w.find('[data-testid="submission-failure"]').text()).toContain('sha256_mismatch');
    expect(w.find('[data-testid="failure-explained"]').text()).toContain('SHA-256 differs from the manifest');
    await w.find('[data-testid="retry"]').trigger('click');
    await settle();
    expect(calls.some((c) => c.method === 'POST' && c.path === '/api/v1/submissions/101/retry')).toBe(true);
  });

  it('follows the pipeline while it runs and stops when it settles', async () => {
    // Only the page's own clock: flushPromises keeps the real setImmediate.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    let n = 0;
    const states = ['queued', 'resolving', 'needs_review'];
    const { calls } = await open(ME_PUBLISHER, SUB, {
      'GET /api/v1/submissions/101': () => ({ body: { ...SUB, status: states[Math.min(n++, 2)] } }),
    });
    const reads = () => calls.filter((c) => c.path === '/api/v1/submissions/101').length;
    expect(reads()).toBe(1);
    await vi.advanceTimersByTimeAsync(3000);
    expect(reads()).toBe(2);
    await vi.advanceTimersByTimeAsync(3000);
    expect(reads()).toBe(3);
    await vi.advanceTimersByTimeAsync(9000);
    expect(reads()).toBe(3);
  });
});
