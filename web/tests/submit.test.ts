import { afterEach, describe, expect, it, vi } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { session } from '@/lib/session';
import type { Me } from '@/api/client';
import { ME_PUBLISHER, mockFetch, settle, testRouter } from './helpers';

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

// Unmount, then clear: removing the DOM under a live app makes its next
// patch fail on a missing node (an unhandled error that fails the run).
const mounted: VueWrapper[] = [];

async function submitWith(
  answer: { status: number; body: unknown; headers?: Record<string, string> },
  before?: (w: VueWrapper) => Promise<void>,
) {
  session.loaded = true;
  session.me = ME_PUBLISHER as unknown as Me;
  const { calls } = mockFetch({ 'GET /api/v1/categories': { body: { items: [] } }, 'POST /api/v1/submissions': answer });
  const router = testRouter();
  await router.push('/submit');
  await router.isReady();
  const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
  mounted.push(w);
  await settle();
  await w.find('[data-testid="sb-repo"]').setValue('https://github.com/jdoe/filex-md');
  await w.find('[data-testid="sb-ref"]').setValue('v1.0.0');
  if (before) await before(w);
  await w.find('[data-testid="submit-form"]').trigger('submit');
  await settle();
  return { w, router, calls };
}

afterEach(() => {
  while (mounted.length) mounted.pop()!.unmount();
  document.body.replaceChildren();
  session.me = null;
});

describe('submitting', () => {
  it('sends owner/name and the tag, then follows the new submission', async () => {
    const { router, calls } = await submitWith({ status: 202, body: { id: 120 } });
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ publisher: 'jdoe', repo: 'jdoe/filex-md', ref: 'v1.0.0' });
    await vi.waitFor(() => expect(router.currentRoute.value.fullPath).toBe('/submissions/120'));
  });

  it('a storage plugin is submitted as one: its kind is sent, the page says what the store reads', async () => {
    let hint = '';
    let checks = '';
    const { calls } = await submitWith({ status: 202, body: { id: 121 } }, async (w) => {
      expect(w.find('[data-testid="sb-kind-hint"]').text()).toContain('filex-app.json');
      await w.find('[data-testid="sb-kind-storage"]').trigger('click');
      await settle();
      hint = w.find('[data-testid="sb-kind-hint"]').text();
      checks = w.find('.fa-steps').text();
    });
    expect(hint).toContain('filex-storage.json');
    expect(hint).toContain('linux/amd64 and linux/arm64');
    expect(checks).toContain('Feed (filex-storage.json)');
    expect(checks).toContain('Conformance run');
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ publisher: 'jdoe', repo: 'jdoe/filex-md', ref: 'v1.0.0', kind: 'storage' });
  });

  it('says a known refusal in words, with its code', async () => {
    const { w } = await submitWith({ status: 403, body: { error: 'repo_archived', message: 'archived' } });
    expect(w.find('[data-testid="sb-error"]').text()).toContain('archived on GitHub. Unarchive it');
    expect(w.find('[data-testid="sb-error"]').text()).toContain('(repo_archived)');
  });

  it('links the submission that is already open for this tag', async () => {
    const { w } = await submitWith({ status: 409, body: { error: 'submission_open', message: 'open', submission: { id: 101 } } });
    expect(w.find('[data-testid="sb-error"] a').attributes('href')).toBe('/submissions/101');
  });

  it('keeps the server\'s sentence for a code it does not know', async () => {
    const { w } = await submitWith({ status: 400, body: { error: 'something_new', message: 'The server explains.' } });
    expect(w.find('[data-testid="sb-error"]').text()).toContain('The server explains. (something_new)');
  });
});
