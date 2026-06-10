'use client';

import { useEffect, useRef, useState } from 'react';
import type { Channel } from '@/lib/parseM3U';

interface Props {
  channel: Channel | null;
  /** viewers on this specific channel */
  channelViewerCount?: number | null;
  channels?: Channel[];
  onSelectChannel?: (ch: Channel) => void;
}

function proxyUrl(url: string) {
  return `/api/proxy?url=${encodeURIComponent(url)}`;
}

// ── Channel carousel (shown when no channel is selected) ─────────────────────

function CarouselLogo({ logo, name }: { logo: string; name: string }) {
  const [err, setErr] = useState(false);
  if (!logo || err) {
    return (
      <div className="w-16 h-16 sm:w-20 sm:h-20 rounded-2xl bg-gray-800 flex items-center justify-center text-2xl font-bold text-gray-400 flex-shrink-0">
        {name.slice(0, 2).toUpperCase()}
      </div>
    );
  }
  return (
    <img
      src={logo}
      alt={name}
      loading="lazy"
      onError={() => setErr(true)}
      className="w-16 h-16 sm:w-20 sm:h-20 rounded-2xl object-contain bg-gray-800/60 p-2 flex-shrink-0"
    />
  );
}

function ChannelCarousel({ channels, onSelect }: { channels: Channel[]; onSelect: (ch: Channel) => void }) {
  const [idx, setIdx] = useState(0);
  const [animKey, setAnimKey] = useState(0);
  const [slideDir, setSlideDir] = useState<'right' | 'left'>('right');
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const total = channels.length;
  const featured = total > 0 ? channels[idx % total] : null;

  const advance = (dir: 1 | -1) => {
    setSlideDir(dir === 1 ? 'right' : 'left');
    setAnimKey(k => k + 1);
    setIdx(i => (i + dir + total) % total);
  };

  const resetTimer = () => {
    if (timerRef.current) clearInterval(timerRef.current);
    timerRef.current = setInterval(() => {
      setSlideDir('right');
      setAnimKey(k => k + 1);
      setIdx(i => (i + 1) % total);
    }, 4000);
  };

  useEffect(() => {
    if (total === 0) return;
    timerRef.current = setInterval(() => {
      setSlideDir('right');
      setAnimKey(k => k + 1);
      setIdx(i => (i + 1) % total);
    }, 4000);
    return () => { if (timerRef.current) clearInterval(timerRef.current); };
  }, [total]);

  if (!featured) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[300px] sm:min-h-[400px] bg-gray-950 rounded-2xl border border-gray-800">
        <div className="text-7xl mb-5 animate-bounce">📺</div>
        <p className="text-gray-300 text-lg font-semibold">Loading channels…</p>
      </div>
    );
  }

  return (
    <div className="relative bg-gray-950 rounded-2xl border border-gray-800 overflow-hidden min-h-[240px] sm:min-h-[340px]">

      {/* Background glow — decorative only */}
      <div className="absolute inset-0 bg-gradient-to-br from-blue-950/30 via-transparent to-purple-950/20 pointer-events-none" />

      {/* Animated content — key changes on every slide to restart the animation */}
      <div
        key={animKey}
        className={`flex flex-col items-center justify-center gap-4 px-6 py-10 min-h-[240px] sm:min-h-[340px] ${slideDir === 'right' ? 'slide-from-right' : 'slide-from-left'}`}
      >
        <CarouselLogo logo={featured.logo} name={featured.name} />
        <div className="text-center">
          <p className="text-white font-bold text-xl sm:text-2xl leading-tight">{featured.name}</p>
          <p className="text-gray-500 text-sm mt-1">{featured.group}</p>
        </div>
        <button
          onClick={() => onSelect(featured)}
          className="relative z-10 flex items-center gap-2 cursor-pointer bg-blue-600 hover:bg-blue-500 active:scale-95 text-white text-sm font-semibold px-6 py-2.5 rounded-full transition-all mt-1 shadow-lg shadow-blue-900/40"
        >
          <span className="w-2 h-2 bg-red-400 rounded-full animate-pulse" />
          Watch Live
        </button>
      </div>

      {/* Prev arrow — z-20 so it's always above the content layer */}
      <button
        onClick={() => { advance(-1); resetTimer(); }}
        className="absolute left-3 top-1/2 -translate-y-1/2 z-20 cursor-pointer bg-black/60 hover:bg-black/90 text-white rounded-full w-9 h-9 flex items-center justify-center transition-all backdrop-blur-sm shadow-md"
        aria-label="Previous channel"
      >
        <svg width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2.5" viewBox="0 0 24 24">
          <polyline points="15 18 9 12 15 6"/>
        </svg>
      </button>
      <button
        onClick={() => { advance(1); resetTimer(); }}
        className="absolute right-3 top-1/2 -translate-y-1/2 z-20 cursor-pointer bg-black/60 hover:bg-black/90 text-white rounded-full w-9 h-9 flex items-center justify-center transition-all backdrop-blur-sm shadow-md"
        aria-label="Next channel"
      >
        <svg width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2.5" viewBox="0 0 24 24">
          <polyline points="9 18 15 12 9 6"/>
        </svg>
      </button>

      {/* Dot indicators — z-20 */}
      {total > 1 && (
        <div className="absolute bottom-3 left-1/2 -translate-x-1/2 flex gap-1.5 z-20">
          {Array.from({ length: Math.min(total, 10) }).map((_, i) => (
            <button
              key={i}
              onClick={() => { setSlideDir(i > idx ? 'right' : 'left'); setAnimKey(k => k + 1); setIdx(i); resetTimer(); }}
              className={`cursor-pointer rounded-full transition-all ${
                i === idx % Math.min(total, 10)
                  ? 'w-5 h-1.5 bg-blue-500'
                  : 'w-1.5 h-1.5 bg-gray-600 hover:bg-gray-400'
              }`}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function formatViewers(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`;
  return `${n}`;
}

export default function VideoPlayer({ channel, channelViewerCount, channels = [], onSelectChannel }: Props) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const hlsRef = useRef<InstanceType<typeof import('hls.js')['default']> | null>(null);
  const [state, setState] = useState<'idle' | 'loading' | 'playing' | 'error'>('idle');
  const [logoError, setLogoError] = useState(false);
  const [quality, setQuality] = useState('');
  const [retryCount, setRetryCount] = useState(0);

  const viewerLabel = channelViewerCount != null ? formatViewers(channelViewerCount) : null;

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
            enableWorker: true,                  // off main thread — accurate bandwidth measurement
            lowLatencyMode: false,               // standard HLS streams, not LL-HLS — low latency mode shrinks buffer and hurts quality
            capLevelToPlayerSize: false,         // never cap quality to player element size
            startLevel: -1,                      // ABR picks start, then we lock to highest after manifest
            startFragPrefetch: true,             // begin loading first fragment during manifest parse — faster first frame
            maxMaxBufferLength: 60,              // larger buffer → smoother ABR decisions
            backBufferLength: 0,                 // live TV — no need to keep back buffer; frees memory and avoids GC pauses
            abrEwmaDefaultEstimate: 5_000_000,  // assume 5 Mbps upfront so ABR starts at high quality immediately
          });
          hlsRef.current = hls;
          hls.loadSource(proxyUrl(channel.url));
          hls.attachMedia(video);

          const updateQualityBadge = (levelIndex: number) => {
            const level = hls.levels[levelIndex];
            if (!level) return;
            if (level.height) setQuality(`${level.height}p`);
            else if (level.bitrate) setQuality(`${Math.round(level.bitrate / 1000)}k`);
          };

          hls.once(Hls.Events.MANIFEST_PARSED, () => {
            if (cancelled) return;
            if (hls.levels.length > 0) {
              const maxLevel = hls.levels.length - 1;
              hls.currentLevel = maxLevel;
              hls.nextAutoLevel = maxLevel;      // prevent ABR from drifting back down after error recovery
              updateQualityBadge(maxLevel);
            }
            setState('playing');
            video.play().catch(() => {});
          });

          hls.on(Hls.Events.LEVEL_SWITCHED, (_: unknown, data: { level: number }) => {
            if (!cancelled) updateQualityBadge(data.level);
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
    return <ChannelCarousel channels={channels} onSelect={onSelectChannel ?? (() => {})} />;
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
              className="mt-2 px-5 py-2 cursor-pointer bg-blue-600 hover:bg-blue-500 active:scale-95 text-white text-sm font-semibold rounded-lg transition-all"
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
            className="bg-black/75 backdrop-blur-sm cursor-pointer hover:bg-black text-white p-2 rounded-lg transition-colors"
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
