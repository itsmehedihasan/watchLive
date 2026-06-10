'use client';

import { useEffect, useRef, useState } from 'react';
import type { Channel } from '@/lib/parseM3U';

interface Props {
  channel: Channel | null;
  viewerCount?: number | null;
}

function proxyUrl(url: string) {
  return `/api/proxy?url=${encodeURIComponent(url)}`;
}

function formatViewers(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return `${n}`;
}

export default function VideoPlayer({ channel, viewerCount }: Props) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const hlsRef = useRef<InstanceType<typeof import('hls.js')['default']> | null>(null);
  const [state, setState] = useState<'idle' | 'loading' | 'playing' | 'error'>('idle');
  const [logoError, setLogoError] = useState(false);
  const [quality, setQuality] = useState('');
  const [retryCount, setRetryCount] = useState(0);

  const viewerLabel = viewerCount != null ? formatViewers(viewerCount) : null;

  useEffect(() => { setLogoError(false); }, [channel]);

  // F key → fullscreen
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.key === 'f' || e.key === 'F') && document.activeElement?.tagName !== 'INPUT') {
        const el = containerRef.current;
        if (!el) return;
        if (!document.fullscreenElement) el.requestFullscreen().catch(() => {});
        else document.exitFullscreen();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  useEffect(() => {
    if (!channel || !videoRef.current) return;

    const video = videoRef.current;
    setState('loading');
    setQuality('');

    if (hlsRef.current) { hlsRef.current.destroy(); hlsRef.current = null; }
    video.src = '';

    let cancelled = false;

    (async () => {
      try {
        const { default: Hls } = await import('hls.js');
        if (cancelled) return;

        if (Hls.isSupported()) {
          const hls = new Hls({
            enableWorker: false,
            lowLatencyMode: true,
            capLevelToPlayerSize: false,  // never downgrade quality to match player element size
            startLevel: -1,               // ABR start, then we override to highest
            maxMaxBufferLength: 30,
          });
          hlsRef.current = hls;
          hls.loadSource(proxyUrl(channel.url));
          hls.attachMedia(video);

          hls.once(Hls.Events.MANIFEST_PARSED, () => {
            if (cancelled) return;
            if (hls.levels.length > 0) {
              hls.currentLevel = hls.levels.length - 1;
              const top = hls.levels[hls.levels.length - 1];
              if (top?.height) setQuality(`${top.height}p`);
            }
            setState('playing');
            video.play().catch(() => {});
          });

          hls.on(Hls.Events.ERROR, (_: unknown, data: { fatal: boolean; type: string }) => {
            if (!data.fatal || cancelled) return;
            if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
              hls.startLoad();
            } else if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
              hls.recoverMediaError();
            } else {
              setState('error');
            }
          });
        } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
          video.src = proxyUrl(channel.url);
          video.onloadedmetadata = () => {
            if (cancelled) return;
            setState('playing');
            video.play().catch(() => {});
          };
          video.onerror = () => { if (!cancelled) setState('error'); };
        } else {
          setState('error');
        }
      } catch {
        if (!cancelled) setState('error');
      }
    })();

    return () => {
      cancelled = true;
      if (hlsRef.current) { hlsRef.current.destroy(); hlsRef.current = null; }
      video.src = '';
    };
  }, [channel, retryCount]);

  const toggleFullscreen = () => {
    const el = containerRef.current;
    if (!el) return;
    if (!document.fullscreenElement) el.requestFullscreen().catch(() => {});
    else document.exitFullscreen();
  };

  if (!channel) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[300px] sm:min-h-[400px] bg-gray-950 rounded-2xl border border-gray-800">
        <div className="text-7xl mb-5 animate-bounce">📺</div>
        <p className="text-gray-300 text-lg font-semibold">Select a channel to watch</p>
        <p className="text-gray-600 text-sm mt-2 hidden sm:block">Browse channels in the sidebar</p>
        <p className="text-gray-600 text-sm mt-2 sm:hidden">Select a channel below</p>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      {/* Video container */}
      <div ref={containerRef} className="relative bg-black rounded-2xl overflow-hidden aspect-video border border-gray-800 shadow-2xl group">
        {state === 'loading' && (
          <div className="absolute inset-0 flex flex-col items-center justify-center bg-black/90 z-10 gap-4">
            <div className="w-14 h-14 border-4 border-blue-500 border-t-transparent rounded-full animate-spin" />
            <p className="text-gray-400 text-sm">Connecting to stream…</p>
          </div>
        )}
        {state === 'error' && (
          <div className="absolute inset-0 flex flex-col items-center justify-center bg-gray-950 z-10 gap-3">
            <div className="text-6xl">📡</div>
            <p className="text-red-400 font-semibold text-lg">Stream unavailable</p>
            <p className="text-gray-500 text-sm text-center max-w-xs">
              This channel may be offline or geo-restricted
            </p>
            <button
              onClick={() => setRetryCount(c => c + 1)}
              className="mt-2 px-5 py-2 bg-blue-600 hover:bg-blue-500 active:scale-95 text-white text-sm font-semibold rounded-lg transition-all"
            >
              ↺ Retry
            </button>
          </div>
        )}
        <video ref={videoRef} className="w-full h-full" controls playsInline />

        {/* Hover overlay: quality badge + fullscreen button (always visible on touch devices) */}
        <div className="absolute top-3 right-3 flex items-center gap-2 z-20 opacity-0 group-hover:opacity-100 transition-opacity duration-200 touch-visible">
          {quality && (
            <span className="bg-black/75 backdrop-blur-sm text-green-400 text-[11px] font-bold px-2.5 py-1 rounded-lg pointer-events-none">
              {quality}
            </span>
          )}
          <button
            onClick={toggleFullscreen}
            className="bg-black/75 backdrop-blur-sm hover:bg-black text-white p-2 rounded-lg transition-colors"
            title="Fullscreen (F)"
          >
            <svg width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24">
              <path d="M8 3H5a2 2 0 00-2 2v3m18 0V5a2 2 0 00-2-2h-3m0 18h3a2 2 0 002-2v-3M3 16v3a2 2 0 002 2h3"/>
            </svg>
          </button>
        </div>
      </div>

      {/* Now Playing bar */}
      <div className="flex items-center gap-3 bg-gray-900/60 rounded-xl px-4 py-3 border border-gray-800">
        {channel.logo && !logoError ? (
          <img
            src={channel.logo}
            alt={channel.name}
            loading="lazy"
            className="w-11 h-11 rounded-lg object-contain bg-gray-800 p-1 flex-shrink-0"
            onError={() => setLogoError(true)}
          />
        ) : (
          <div className="w-11 h-11 rounded-lg bg-gray-800 flex-shrink-0 flex items-center justify-center text-lg">📡</div>
        )}
        <div className="flex-1 min-w-0">
          <p className="text-white font-semibold text-base truncate">{channel.name}</p>
          <p className="text-xs text-gray-500 mt-0.5 truncate">{channel.group}</p>
        </div>
        <div className="flex items-center gap-2.5 flex-shrink-0">
          {viewerLabel != null && (
            <div className="flex items-center gap-1.5 bg-gray-800/70 border border-gray-700/40 px-2.5 py-1 rounded-full">
              <span className="text-sm leading-none">👥</span>
              <span className="text-xs font-semibold text-gray-300">{viewerLabel}</span>
              <span className="hidden sm:inline text-xs text-gray-500">watching</span>
            </div>
          )}
          <div className="flex items-center gap-1.5">
            <span className="w-2 h-2 bg-red-500 rounded-full animate-pulse" />
            <span className="text-red-400 text-xs font-bold uppercase tracking-widest">Live</span>
          </div>
        </div>
      </div>
    </div>
  );
}
