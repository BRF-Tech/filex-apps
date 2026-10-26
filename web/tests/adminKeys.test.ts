// Administration > Index and keys: every signing key is named by its own use.
// The license key (it signs the license verdicts, migration 0006) was shown
// as "Artifact" while the table knew only two uses.
import { afterEach, describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import { defineComponent, h, ref } from 'vue';
import { RouterView } from 'vue-router';
import { provideTableEnv } from '@brftech/filex-core';
import { session } from '@/lib/session';
import type { Me } from '@/api/client';
import { ME_ADMIN, mockFetch, settle, testRouter } from './helpers';

const key = (id: string, use: string) => ({
  id,
  use,
  public_hex: 'ab'.repeat(32),
  status: 'active',
  demo: true,
  created_at: '2026-10-01T10:00:00Z',
  activated_at: '2026-10-01T10:00:00Z',
});

const Host = defineComponent({
  setup() {
    provideTableEnv({ locale: ref('en'), theme: ref('light') });
    return () => h(RouterView);
  },
});

afterEach(() => {
  document.body.replaceChildren();
  session.me = null;
});

describe('Administration > Index and keys', () => {
  it('names each key by its use: index, artifact, license', async () => {
    session.loaded = true;
    session.me = ME_ADMIN as unknown as Me;
    mockFetch({
      'HEAD /v1/index.json': { headers: { 'X-Index-Serial': '14', ETag: '"abc"' } },
      'GET /api/v1/admin/keys': { body: { items: [key('demo-index-1', 'index'), key('demo-artifact-1', 'artifact'), key('demo-license-1', 'license')] } },
    });
    const router = testRouter();
    await router.push('/admin/index');
    await router.isReady();
    const w = mount(Host, { global: { plugins: [router] }, attachTo: document.body });
    await settle();

    const grid = w.find('[data-table-id="store.admin.keys"]');
    expect(grid.exists()).toBe(true);
    const rowOf = (id: string) => grid.findAll('[role="row"]').find((r) => r.text().includes(id));
    expect(rowOf('demo-index-1')?.text()).toContain('Index');
    expect(rowOf('demo-artifact-1')?.text()).toContain('Artifact');
    const lic = rowOf('demo-license-1');
    expect(lic?.text()).toContain('License');
    expect(lic?.text()).not.toContain('Artifact');
  });
});
