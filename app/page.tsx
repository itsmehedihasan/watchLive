'use client';

import { useState, useEffect, useMemo, useCallback, useRef } from 'react';
import { parseM3U, type Channel } from '@/lib/parseM3U';
import VideoPlayer from '@/components/VideoPlayer';

// ─── tiny sub-components ────────────────────────────────────────────────────
function ChannelLogo({ logo, name }: { logo: string; name: string }) {
  const [err, setErr] = useState(false);
  if (!logo || err) {
    return (
      <div className="w-8 h-8 rounded-lg bg-gray-800 flex-shrink-0 flex items-center justify-center text-xs font-bold text-gray-400">
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
      className="w-8 h-8 rounded-lg object-contain bg-gray-800 p-0.5 flex-shrink-0"
    />
  );
}


// ─── main page ──────────────────────────────────────────────────────────────
export default function HomePage() {
  const [channels, setChannels] = useState<Channel[]>([]);
  const [loadingPlaylist, setLoadingPlaylist] = useState(true);
  const [selected, setSelected] = useState<Channel | null>(null);
  const [search, setSearch] = useState('');
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [mobileChannelsOpen, setMobileChannelsOpen] = useState(false);
  /** total users currently on the site */
  const [totalViewers, setTotalViewers] = useState<number | null>(null);
  /** viewers watching the currently selected channel */
  const [channelViewers, setChannelViewers] = useState<number | null>(null);
  /** channel IDs ranked by total tune-ins, used to build the carousel */
  const [topChannelIds, setTopChannelIds] = useState<string[]>([]);
  const searchInputRef = useRef<HTMLInputElement>(null);
  // keep a ref so the heartbeat closure always reads the latest selected channel
  const selectedRef = useRef<Channel | null>(null);
  const beatRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    selectedRef.current = selected;
    setChannelViewers(null); // reset immediately on channel switch
    beatRef.current?.();    // fire an immediate heartbeat to get fresh counts
  }, [selected]);

  useEffect(() => {
    fetch('/list.txt')
      .then(r => r.text())
      .then(text => {
        const parsed = parseM3U(text);
        setChannels(parsed);
        setLoadingPlaylist(false);
      })
      .catch(() => setLoadingPlaylist(false));
  }, []);

  // Heartbeat: register session + current channel every 30s
  useEffect(() => {
    let sessionId = sessionStorage.getItem('livetv_sid');
    if (!sessionId) {
      sessionId = Math.random().toString(36).slice(2) + Date.now().toString(36);
      sessionStorage.setItem('livetv_sid', sessionId);
    }
    const sid = sessionId;
    const beat = () => {
      const channelId = selectedRef.current?.id ?? null;
      fetch('/api/viewers', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sessionId: sid, channelId }),
      })
        .then(r => r.json())
        .then((d: { total: number; channelCount: number | null; top?: Array<{ id: string }> }) => {
          setTotalViewers(d.total);
          if (d.channelCount != null) setChannelViewers(d.channelCount);
          if (d.top && d.top.length > 0) setTopChannelIds(d.top.map(x => x.id));
        })
        .catch(() => {});
    };
    beatRef.current = beat;
    beat();
    const t = setInterval(beat, 30_000);
    return () => clearInterval(t);
  }, []);

  // / → focus search, Escape → clear + blur
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === '/' && document.activeElement?.tagName !== 'INPUT') {
        e.preventDefault();
        searchInputRef.current?.focus();
      }
      if (e.key === 'Escape' && document.activeElement === searchInputRef.current) {
        setSearch('');
        searchInputRef.current?.blur();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  const filtered = useMemo(() => {
    if (!search) return channels;
    const q = search.toLowerCase();
    return channels.filter(ch => ch.name.toLowerCase().includes(q) || ch.group.toLowerCase().includes(q));
  }, [channels, search]);

  // Top-5 channels for the carousel; falls back to first 5 until view data arrives
  const carouselChannels = useMemo<Channel[]>(() => {
    if (topChannelIds.length === 0) return channels.slice(0, 5);
    return topChannelIds
      .map(id => channels.find(ch => ch.id === id))
      .filter((ch): ch is Channel => ch != null);
  }, [channels, topChannelIds]);

  const handleSelectChannel = useCallback((ch: Channel) => {
    setSelected(ch);
    setMobileChannelsOpen(false);
  }, []);


  return (
    <div className="flex flex-col h-screen bg-[#0d0d0d] text-white overflow-hidden select-none">

      {/* ── Header ─────────────────────────────────────────────────────── */}
      <header className="flex items-center gap-3 px-4 py-2.5 bg-[#181818] border-b border-[#2a2a2a] flex-shrink-0 z-20">
        <button
          onClick={() => setSidebarOpen(v => !v)}
          title="Toggle sidebar"
          className="hidden sm:flex text-gray-400 hover:text-white p-2 rounded-lg hover:bg-gray-800 transition-colors flex-shrink-0"
        >
          <svg width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24">
            <line x1="3" y1="6" x2="21" y2="6" /><line x1="3" y1="12" x2="21" y2="12" /><line x1="3" y1="18" x2="21" y2="18" />
          </svg>
        </button>

        <button
          onClick={() => { setSelected(null); setMobileChannelsOpen(false); }}
          className="flex items-center gap-2 flex-shrink-0 cursor-pointer hover:opacity-80 transition-opacity"
        >
          <span className="text-2xl">📺</span>
          <span className="font-bold text-base sm:text-lg tracking-tight">LiveTV</span>
        </button>

        {!selected && totalViewers != null && (
          <span className="text-xs text-gray-500 bg-[#252525] px-2 py-0.5 rounded-full flex items-center gap-1">
            <span>👥</span>
            <span>{totalViewers} active</span>
          </span>
        )}
      </header>


      {/* ── Body ───────────────────────────────────────────────────────── */}
      <div className="flex flex-col sm:flex-row flex-1 overflow-hidden min-h-0">

        {/* Sidebar — collapsible on mobile when channel selected, side panel on desktop */}
        <aside className={`order-2 sm:order-1 flex-1 sm:flex-none sm:w-56 bg-[#161616] border-t sm:border-t-0 sm:border-r border-[#222] flex flex-col overflow-hidden min-h-0${!sidebarOpen ? ' sm:hidden' : ''}${selected && !mobileChannelsOpen ? ' hidden sm:flex' : ''}`}>

            {/* Search */}
            <div className="px-3 py-2 border-b border-[#222] flex-shrink-0">
              <div className="relative">
                <svg className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-500 pointer-events-none" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24">
                  <circle cx="11" cy="11" r="8" /><line x1="21" y1="21" x2="16.65" y2="16.65" />
                </svg>
                <input
                  type="text"
                  value={search}
                  onChange={e => setSearch(e.target.value)}
                  ref={searchInputRef}
                  placeholder="Search channels…"
                  className="w-full bg-[#252525] rounded-lg pl-8 pr-8 py-2 text-sm text-white placeholder-gray-600 border border-[#333] focus:border-blue-500 focus:outline-none"
                />
                {search && (
                  <button onClick={() => setSearch('')} className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-500 hover:text-white">✕</button>
                )}
              </div>
            </div>

            {/* Channel list */}
            <div className="flex-1 overflow-y-auto min-h-0 pb-12">
              {loadingPlaylist ? (
                <div className="flex flex-col items-center justify-center h-40 gap-3 text-gray-500">
                  <div className="w-8 h-8 border-2 border-gray-600 border-t-blue-500 rounded-full animate-spin" />
                  <span className="text-sm">Loading playlist…</span>
                </div>
              ) : filtered.length === 0 ? (
                <div className="flex flex-col items-center justify-center h-40 gap-2 text-gray-500">
                  <span className="text-3xl">😕</span>
                  <span className="text-sm">No channels found</span>
                  <button onClick={() => setSearch('')} className="text-xs text-blue-400 hover:text-blue-300 mt-1">Clear search</button>
                </div>
              ) : (
                filtered.map(ch => (
                  <button
                    key={ch.id}
                    onClick={() => handleSelectChannel(ch)}
                    className={`w-full flex items-center gap-2.5 px-3 py-3 sm:py-2 transition-colors text-left group ${
                      selected?.id === ch.id
                        ? 'bg-blue-900/30 border-r-2 border-blue-500'
                        : 'hover:bg-[#222] border-r-2 border-transparent'
                    }`}
                  >
                    <ChannelLogo logo={ch.logo} name={ch.name} />
                    <span className={`text-sm truncate leading-tight ${
                      selected?.id === ch.id ? 'text-blue-300 font-medium' : 'text-gray-300 group-hover:text-white'
                    }`}>
                      {ch.name}
                    </span>
                    {selected?.id === ch.id && (
                      <span className="ml-auto flex-shrink-0 w-1.5 h-1.5 bg-red-500 rounded-full animate-pulse" />
                    )}
                  </button>
                ))
              )}
            </div>

            {/* Footer count */}
            {!loadingPlaylist && (
              <div className="flex-shrink-0 px-3 py-2 border-t border-[#222] text-[11px] text-gray-600 text-center">
                Showing {filtered.length} of {channels.length} channels
              </div>
            )}
        </aside>

        {/* Main player area */}
        <main className="order-1 sm:order-2 flex-shrink-0 sm:flex-1 overflow-y-auto p-4 sm:p-6 bg-[#0d0d0d]">
          <div className="max-w-5xl mx-auto">
            <VideoPlayer channel={selected} channelViewerCount={channelViewers} channels={carouselChannels} onSelectChannel={handleSelectChannel} />
            {selected && (
              <button
                onClick={() => setMobileChannelsOpen(v => !v)}
                className="sm:hidden mt-3 w-full py-2.5 rounded-lg bg-[#1e1e1e] hover:bg-[#252525] text-sm text-gray-300 border border-[#2a2a2a] transition-colors font-medium"
              >
                More Channels
              </button>
            )}
          </div>
        </main>
      </div>

    </div>
  );
}
