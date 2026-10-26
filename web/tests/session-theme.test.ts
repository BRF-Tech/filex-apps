import { afterEach, describe, expect, it, vi } from 'vitest';
import { activePublishers, hasRole, loadSession, session } from '@/lib/session';
import { THEME_KEY, applyTheme, resolveDark, setThemePref, tableTheme, themePref } from '@/lib/theme';
import { api } from '@/api/client';
import { ME_ADMIN, ME_PUBLISHER, ME_REVIEWER, mockFetch } from './helpers';
import type { Me } from '@/api/client';

describe('roles come from the session', () => {
  it('an administrator may do what a reviewer may; a publisher neither', () => {
    expect(hasRole(ME_ADMIN as unknown as Me, 'reviewer')).toBe(true);
    expect(hasRole(ME_ADMIN as unknown as Me, 'admin')).toBe(true);
    expect(hasRole(ME_REVIEWER as unknown as Me, 'admin')).toBe(false);
    expect(hasRole(ME_PUBLISHER as unknown as Me, 'reviewer')).toBe(false);
    expect(hasRole(null, 'reviewer')).toBe(false);
  });

  it('submits only for active publishers', () => {
    expect(activePublishers(ME_PUBLISHER as unknown as Me).map((p) => p.id)).toEqual(['jdoe']);
  });

  it('reads 401 as nobody, and keeps the CSRF token of a session for later writes', async () => {
    mockFetch({ 'GET /api/v1/me': { status: 401, body: { error: 'unauthorized', message: 'x' } } });
    await loadSession(true);
    expect([session.me, session.loaded, session.error]).toEqual([null, true, '']);

    const { calls } = mockFetch({ 'GET /api/v1/me': { body: ME_PUBLISHER }, 'POST /auth/logout': { status: 204 } });
    await loadSession(true);
    expect(session.me?.identity.login).toBe('jdoe');
    await api('POST', '/auth/logout');
    expect(calls[1].headers['X-CSRF-Token']).toBe('tok-pub');
  });
});

describe('light and dark', () => {
  afterEach(() => {
    setThemePref('system');
    document.documentElement.classList.remove('dark');
  });

  it('follows the system unless the person chose', () => {
    expect(resolveDark('system', true)).toBe(true);
    expect(resolveDark('system', false)).toBe(false);
    expect(resolveDark('light', true)).toBe(false);
    expect(resolveDark('dark', false)).toBe(true);
  });

  it('puts filex\'s dark class on <html> and tells the tables', () => {
    setThemePref('dark');
    applyTheme();
    expect(document.documentElement.classList.contains('dark')).toBe(true);
    expect(tableTheme.value).toBe('dark');
    expect(localStorage.getItem(THEME_KEY)).toBe('dark');
    setThemePref('light');
    applyTheme();
    expect(document.documentElement.classList.contains('dark')).toBe(false);
    expect(tableTheme.value).toBe('light');
  });

  it('still works when storage is refused', () => {
    const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('denied');
    });
    expect(() => setThemePref('dark')).not.toThrow();
    expect(themePref.value).toBe('dark');
    spy.mockRestore();
  });
});
