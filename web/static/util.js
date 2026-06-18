export function proxyUrl(url) { return '/api/proxy?url=' + encodeURIComponent(url); }

export function streamKind(url) {
  const u = String(url).split('#')[0];
  if (/\.mpd(\?|$)/i.test(u)) return 'dash';
  if (/\.m3u8(\?|$)/i.test(u)) return 'hls';
  if (/\.ts(\?|$)/i.test(u)) return 'ts';
  return 'hls';
}

export function formatViewers(n) { return n >= 1000 ? (n / 1000).toFixed(1) + 'K' : String(n); }

export function logoOrFallback(ch, imgClass, fbClass) {
  const fallback = document.createElement('div');
  fallback.className = fbClass;
  fallback.textContent = ch.name.slice(0, 2).toUpperCase();
  if (!ch.logo) return fallback;
  const img = document.createElement('img');
  img.className = imgClass;
  img.src = ch.logo;
  img.alt = ch.name;
  img.loading = 'lazy';
  img.onerror = function () { if (img.parentNode) img.parentNode.replaceChild(fallback, img); };
  return img;
}
