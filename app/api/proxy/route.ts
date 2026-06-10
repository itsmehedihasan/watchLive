import type { NextRequest } from 'next/server';

export const runtime = 'edge';

// Spoof a real browser so stream servers don't block us
const BROWSER_HEADERS = {
  'User-Agent':
    'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36',
  Accept: '*/*',
  'Accept-Language': 'en-US,en;q=0.9',
  Connection: 'keep-alive',
};

export async function GET(request: NextRequest) {
  const { searchParams, origin } = new URL(request.url);
  const targetUrl = searchParams.get('url');

  if (!targetUrl || !/^https?:\/\//i.test(targetUrl)) {
    return new Response('Missing or invalid url parameter', { status: 400 });
  }

  try {
    const upstream = await fetch(targetUrl, {
      headers: {
        ...BROWSER_HEADERS,
        Referer: new URL(targetUrl).origin + '/',
        Origin: new URL(targetUrl).origin,
      },
      // Don't cache on Vercel edge — streams are always live
      cache: 'no-store',
    });

    if (!upstream.ok) {
      return new Response(`Upstream returned ${upstream.status}`, {
        status: upstream.status,
      });
    }

    const contentType = upstream.headers.get('content-type') ?? '';
    const isPlaylist =
      contentType.includes('mpegurl') ||
      contentType.includes('m3u8') ||
      /\.m3u8(\?|$)/i.test(targetUrl);

    if (isPlaylist) {
      // ── Playlist: rewrite all URLs inside to go through this proxy ──
      const text = await upstream.text();
      const base = targetUrl.slice(0, targetUrl.lastIndexOf('/') + 1);

      function resolve(href: string): string {
        if (/^https?:\/\//i.test(href)) return href;
        if (href.startsWith('//')) return 'https:' + href;
        if (href.startsWith('/')) return new URL(targetUrl!).origin + href;
        return base + href;
      }

      const rewritten = text
        .split('\n')
        .map(line => {
          const trimmed = line.trim();
          // Skip comments and empty lines — but URI= attributes inside tags need rewriting too
          if (trimmed.startsWith('#')) {
            // Rewrite URI="..." inside EXT-X-KEY, EXT-X-MAP, etc.
            return line.replace(/URI="([^"]+)"/g, (_match, uri) => {
              const abs = resolve(uri);
              return `URI="${origin}/api/proxy?url=${encodeURIComponent(abs)}"`;
            });
          }
          if (!trimmed) return line;
          // Plain URL line (variant playlist or segment)
          const abs = resolve(trimmed);
          return `${origin}/api/proxy?url=${encodeURIComponent(abs)}`;
        })
        .join('\n');

      return new Response(rewritten, {
        headers: {
          'Content-Type': 'application/vnd.apple.mpegurl',
          'Access-Control-Allow-Origin': '*',
          'Cache-Control': 'no-cache, no-store',
        },
      });
    }

    // ── Segment / binary content: stream straight through ──
    return new Response(upstream.body, {
      status: upstream.status,
      headers: {
        'Content-Type': contentType || 'video/MP2T',
        'Access-Control-Allow-Origin': '*',
        'Cache-Control': 'no-cache, no-store',
      },
    });
  } catch (err) {
    console.error('[proxy]', err);
    return new Response('Proxy fetch failed', { status: 502 });
  }
}
