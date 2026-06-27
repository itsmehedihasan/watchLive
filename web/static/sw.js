/* LiveTV service worker.
 *
 * Caching strategy is split by asset class so a rebuild is picked up on the
 * NEXT normal load (no hard-reload needed):
 *   - Our own app shell (HTML document + first-party .js/.css): NETWORK-FIRST
 *     with a cache fallback, so a fresh build always wins when online but the
 *     app still opens offline.
 *   - Large immutable vendored libs (hls/shaka/mpegts): CACHE-FIRST — they
 *     almost never change and are big, so serving them from cache is a win.
 *   - /api/* (and the proxy): network-only, never cached.
 */

var CACHE = 'livetv-shell-v8';

// Big third-party players: versioned/immutable in practice → cache-first.
var VENDORED = [
  '/static/hls.min.js',
  '/static/shaka-player.compiled.js',
  '/static/mpegts.js'
];

// Precache the shell entry + vendored libs + PWA assets so the app works
// offline. First-party ES modules are intentionally NOT listed: they are
// served network-first, and precaching them would just go stale.
var PRECACHE = [
  '/',
  '/static/app.js',
  '/static/icon-192.svg',
  '/static/icon-512.svg',
  '/static/manifest.json'
].concat(VENDORED);

self.addEventListener('install', function (e) {
  e.waitUntil(
    caches.open(CACHE).then(function (c) { return c.addAll(PRECACHE); })
  );
  // Activate immediately rather than waiting for all tabs to close.
  self.skipWaiting();
});

self.addEventListener('activate', function (e) {
  // Drop caches from previous SW versions.
  e.waitUntil(
    caches.keys().then(function (keys) {
      return Promise.all(
        keys.filter(function (k) { return k !== CACHE; })
            .map(function (k) { return caches.delete(k); })
      );
    }).then(function () { return self.clients.claim(); })
  );
});

function isVendored(path) {
  return VENDORED.indexOf(path) !== -1;
}

// Cache-first: serve from cache, fall back to network and populate the cache.
function cacheFirst(req) {
  return caches.match(req).then(function (cached) {
    if (cached) return cached;
    return fetch(req).then(function (resp) {
      if (resp && resp.status === 200 && req.method === 'GET') {
        var clone = resp.clone();
        caches.open(CACHE).then(function (c) { c.put(req, clone); });
      }
      return resp;
    });
  });
}

// Network-first: try the network (and refresh the cache), fall back to cache
// when offline. This is what keeps first-party JS/CSS fresh after a rebuild.
function networkFirst(req) {
  return fetch(req).then(function (resp) {
    if (resp && resp.status === 200 && req.method === 'GET') {
      var clone = resp.clone();
      caches.open(CACHE).then(function (c) { c.put(req, clone); });
    }
    return resp;
  }).catch(function () {
    return caches.match(req).then(function (cached) {
      return cached || Promise.reject(new Error('offline and uncached'));
    });
  });
}

self.addEventListener('fetch', function (e) {
  var req = e.request;
  if (req.method !== 'GET') return;

  var url = new URL(req.url);
  if (url.origin !== self.location.origin) return; // only same-origin

  var path = url.pathname;

  // API + proxy: always live, never cached.
  if (path.startsWith('/api/')) return;

  if (isVendored(path)) {
    e.respondWith(cacheFirst(req));
  } else {
    // HTML document + first-party .js/.css and everything else same-origin.
    e.respondWith(networkFirst(req));
  }
});
