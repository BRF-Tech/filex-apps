// Instance connections: the owner's two doors - a one-time code for a
// saved instance, and taking a connected filex's key away - go to the
// instance's own paths with the session's CSRF token; the strings say what
// the code is for without the British spelling or a long dash.
import { beforeEach, describe, expect, it } from 'vitest';
import { createConnectCode, disconnectInstance, setCsrfToken } from '@/api/client';
import { S } from '@/strings';
import { mockFetch } from './helpers';

const ID = '0f1e2d3c-4b5a-4968-8776-655443322110';

describe('instance connections', () => {
  beforeEach(() => setCsrfToken('tok-inst'));

  it('asks for a code and disconnects at the instance\'s own paths, with CSRF', async () => {
    const { calls } = mockFetch({
      [`POST /api/v1/me/instances/${ID}/connect-code`]: { status: 201, body: { code: 'fxc_' + 'A'.repeat(43), expires_at: '2026-10-06T12:30:00Z' } },
      [`DELETE /api/v1/me/instances/${ID}/connection`]: { status: 204 },
    });
    const c = await createConnectCode(ID);
    expect(c.code).toMatch(/^fxc_[A-Za-z0-9_-]{43}$/);
    await disconnectInstance(ID);
    expect(calls.map((x) => `${x.method} ${x.path}`)).toEqual([
      `POST /api/v1/me/instances/${ID}/connect-code`,
      `DELETE /api/v1/me/instances/${ID}/connection`,
    ]);
    for (const call of calls) expect(call.headers['X-CSRF-Token']).toBe('tok-inst');
  });

  it('the dialog says where the code goes and that it works once', () => {
    expect(S.instances.connectIntro).toMatch(/once/);
    expect(S.instances.connectIntro).toMatch(/30 minutes/);
    for (const v of Object.values(S.instances)) {
      expect(v).not.toMatch(/[\u2013\u2014]/);
    }
  });
});
