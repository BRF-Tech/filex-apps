// Who is signed in. One reactive answer for the whole interface, read once at
// start and again after a sign-out. The server is the authority: roles come
// from the session (the account's permission on the store's administration
// repository on GitHub, read at sign-in), never from anything the interface
// decides.
import { computed, reactive } from 'vue';
import { ApiError, getMe, logout, setCsrfToken, type Me, type MePublisher, type SignInProviders } from '@/api/client';

interface SessionState {
  me: Me | null;
  /** Which sign-ins this server can start (from /api/v1/me, signed in or
   *  not); null while unknown, and then the sign-in links stay usable. */
  providers: SignInProviders | null;
  loaded: boolean;
  error: string;
}

export const session = reactive<SessionState>({ me: null, providers: null, loaded: false, error: '' });

function readProviders(v: unknown): SignInProviders | null {
  const p = v as Partial<SignInProviders> | null | undefined;
  return p && typeof p.github === 'boolean' ? { github: p.github } : null;
}

let pending: Promise<void> | null = null;

/** Read `/api/v1/me`; 401 means "nobody", anything else is kept as an error. */
export function loadSession(force = false): Promise<void> {
  if (pending && !force) return pending;
  pending = (async () => {
    try {
      const me = await getMe();
      session.me = me;
      session.providers = readProviders(me.providers);
      session.error = '';
      setCsrfToken(me.csrf_token);
    } catch (e) {
      session.me = null;
      setCsrfToken('');
      // 401 SignedOut carries `providers` too: nobody has to probe /auth/*.
      session.providers = e instanceof ApiError ? readProviders(e.body.providers) : null;
      session.error = e instanceof ApiError && e.status === 401 ? '' : e instanceof Error ? e.message : String(e);
    } finally {
      session.loaded = true;
    }
  })();
  return pending;
}

export async function signOut(): Promise<void> {
  try {
    await logout();
  } finally {
    session.me = null;
    setCsrfToken('');
    pending = null;
  }
}

export type Role = 'reviewer' | 'admin';

export function hasRole(me: Me | null, role: Role): boolean {
  if (!me) return false;
  // An administrator may do everything a reviewer may (docs/ARCHITECTURE.md
  // "Who may do what").
  if (role === 'reviewer') return me.roles.includes('reviewer') || me.roles.includes('admin');
  return me.roles.includes(role);
}

export function isGithub(me: Me | null): boolean {
  return me?.identity.provider === 'github';
}

export function activePublishers(me: Me | null): MePublisher[] {
  return (me?.publishers ?? []).filter((p) => (p.status ?? 'active') === 'active');
}

export function isMemberOf(me: Me | null, publisherId: string): boolean {
  return (me?.publishers ?? []).some((p) => p.id === publisherId);
}

export const sessionView = {
  me: computed(() => session.me),
  isReviewer: computed(() => hasRole(session.me, 'reviewer')),
  isAdmin: computed(() => hasRole(session.me, 'admin')),
};

/** `/sign-in?return_to=<path>` for the current place, or a GitHub start. */
export function returnTo(path: string): string {
  return /^\/(?![/\\])/.test(path) ? path : '/';
}
