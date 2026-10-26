// Browser notifications: the store's service worker (/sw.js, web/public) and
// the browser's push subscription. The server encrypts and sends every
// notification (Web Push with VAPID); this side only asks the person, makes
// the subscription with the store's public key and hands it to the server.
//
// Which subscription row is this browser's is kept per browser (the id the
// server answered), so the page can say "this browser" without sending the
// subscription again on every visit.

const ID_KEY = 'fapps.push.id';
const SW_PATH = '/sw.js';

export type PushState = 'unsupported' | 'denied' | 'default' | 'granted';

/** The browser can show the store's notifications at all. */
export function pushSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof navigator !== 'undefined' &&
    'serviceWorker' in navigator &&
    'PushManager' in window &&
    'Notification' in window
  );
}

/** What the person allowed this site ('unsupported' when the browser cannot). */
export function pushPermission(): PushState {
  if (!pushSupported()) return 'unsupported';
  return Notification.permission as PushState;
}

/** The base64url VAPID public key as the bytes PushManager.subscribe takes. */
export function keyBytes(base64url: string): Uint8Array {
  const pad = '='.repeat((4 - (base64url.length % 4)) % 4);
  const b64 = (base64url + pad).replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

function sameKey(a: ArrayBuffer | null | undefined, b: Uint8Array): boolean {
  if (!a) return false;
  const x = new Uint8Array(a);
  return x.length === b.length && x.every((v, i) => v === b[i]);
}

/** The id the server gave this browser's subscription ('' when none is known). */
export function rememberedId(): string {
  try {
    return localStorage.getItem(ID_KEY) ?? '';
  } catch {
    return '';
  }
}

export function rememberId(id: string): void {
  try {
    if (id) localStorage.setItem(ID_KEY, id);
    else localStorage.removeItem(ID_KEY);
  } catch {
    // Storage blocked: the page then does not know which row is this browser.
  }
}

export class PushRefused extends Error {
  constructor(readonly state: PushState) {
    super(state);
    this.name = 'PushRefused';
  }
}

async function registration(): Promise<ServiceWorkerRegistration> {
  await navigator.serviceWorker.register(SW_PATH, { scope: '/' });
  return navigator.serviceWorker.ready;
}

/** This browser's current subscription, if it has one with the store. */
export async function currentSubscription(): Promise<PushSubscription | null> {
  if (!pushSupported()) return null;
  const reg = await navigator.serviceWorker.getRegistration('/');
  return reg ? reg.pushManager.getSubscription() : null;
}

/**
 * Asks the person (the browser's own prompt), then subscribes with the
 * store's key; a subscription made with another key is replaced.
 */
export async function subscribeThisBrowser(publicKey: string): Promise<PushSubscriptionJSON> {
  if (!pushSupported()) throw new PushRefused('unsupported');
  const state = (await Notification.requestPermission()) as PushState;
  if (state !== 'granted') throw new PushRefused(state);
  const reg = await registration();
  const key = keyBytes(publicKey);
  let sub = await reg.pushManager.getSubscription();
  if (sub && !sameKey(sub.options.applicationServerKey, key)) {
    await sub.unsubscribe();
    sub = null;
  }
  if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key as BufferSource });
  return sub.toJSON();
}

/** Ends this browser's subscription (the server row is removed apart). */
export async function unsubscribeThisBrowser(): Promise<void> {
  const sub = await currentSubscription();
  if (sub) await sub.unsubscribe();
}
