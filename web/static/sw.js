/* LiveTV service worker — app-shell cache with network-first for API routes. */

var CACHE = 'livetv-shell-v7';

// Static assets that form the app shell.  sw.js itself is not listed — the
// browser always fetches it fresh from the network for update detection.
var SHELL = [
  '/',
  '/static/style.css',
  '/static/app.js',
  '/static/hls.min.js',
  '/static/shaka-player.compiled.js',
  '/static/mpegts.js',
  '/static/icon-192.svg',
  '/static/icon-512.svg',
  '/static/manifest.json'
];

self.addEventListener('install', function (e) {
  e.waitUntil(
    caches.open(CACHE).then(function (c) { return c.addAll(SHELL); })
  );
  // Skip the waiting phase so the new SW activates immediately on first install.
  self.skipWaiting();
});

self.addEventListener('activate', function (e) {
  // Delete any old shell caches from previous SW versions.
  e.waitUntil(
    caches.keys().then(function (keys) {
      return Promise.all(
        keys.filter(function (k) { return k !== CACHE; })
            .map(function (k) { return caches.delete(k); })
      );
    }).then(function () { return self.clients.claim(); })
  );
});

self.addEventListener('fetch', function (e) {
  var url = new URL(e.request.url);

  // Only handle same-origin requests.
  if (url.origin !== self.location.origin) return;

  var path = url.pathname;

  // API routes and the proxy: always go to the network; never cache.
  if (path.startsWith('/api/') || path.startsWith('/api/proxy')) return;

  // App shell: cache-first, fall back to network (keeps app usable offline).
  e.respondWith(
    caches.match(e.request).then(function (cached) {
      var networkFetch = fetch(e.request).then(function (resp) {
        if (resp && resp.status === 200 && e.request.method === 'GET') {
          var clone = resp.clone();
          caches.open(CACHE).then(function (c) { c.put(e.request, clone); });
        }
        return resp;
      });
      return cached || networkFetch;
    })
  );
});
