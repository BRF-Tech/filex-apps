// filex Apps: the store's service worker. It does one thing: show the
// notifications the store's server sends (Web Push, encrypted by the server;
// the browser decrypts them) and open the page a click asks for. It caches
// nothing and answers no request.

self.addEventListener('install', () => {
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener('push', (event) => {
  let p = {};
  try {
    p = event.data ? event.data.json() : {};
  } catch {
    p = { body: event.data ? event.data.text() : '' };
  }
  const title = typeof p.title === 'string' && p.title ? p.title : 'filex Apps';
  const options = {
    body: typeof p.body === 'string' ? p.body : '',
    icon: '/icons/icon-192.png',
    badge: '/icons/icon-192.png',
    data: { url: typeof p.url === 'string' ? p.url : '/' },
  };
  if (typeof p.tag === 'string' && p.tag) options.tag = p.tag;
  event.waitUntil(self.registration.showNotification(title, options));
});

// A click opens the store's own page, or filex's release notes on GitHub;
// any other address opens the store's front page instead.
function target(raw) {
  let u;
  try {
    u = new URL(raw || '/', self.location.origin);
  } catch {
    return new URL('/', self.location.origin).href;
  }
  if (u.origin === self.location.origin) return u.href;
  if (u.origin === 'https://github.com' && u.pathname.startsWith('/BRF-Tech/filex/releases/')) return u.href;
  return new URL('/', self.location.origin).href;
}

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const href = target(event.notification.data && event.notification.data.url);
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
      for (const w of windows) {
        if (w.url === href && 'focus' in w) return w.focus();
      }
      return self.clients.openWindow(href);
    })(),
  );
});
