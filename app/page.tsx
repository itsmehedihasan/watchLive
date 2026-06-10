'use client';

import { useState, useEffect, useMemo, useCallback, useRef } from 'react';
import { parseM3U, type Channel } from '@/lib/parseM3U';
import VideoPlayer from '@/components/VideoPlayer';

// ─── constants ─────────────────────────────────────────────────────────────
const TYPE_EMOJI: Record<string, string> = {
  All: '📋',
  Entertainment: '📺',
  News: '📰',
  Sports: '⚽',
  Movies: '🎬',
  Music: '🎵',
  Kids: '🧸',
  Religious: '🕌',
};

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

function FilterPill({
  active,
  onClick,
  children,
  color = 'blue',
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
  color?: 'blue' | 'purple';
}) {
  const activeClass =
    color === 'blue'
      ? 'bg-blue-600 text-white shadow-lg shadow-blue-900/40'
      : 'bg-purple-600 text-white shadow-lg shadow-purple-900/40';
  return (
    <button
      onClick={onClick}
      className={`flex-shrink-0 flex items-center gap-1 text-xs px-3 py-2 sm:py-1.5 rounded-full font-medium transition-all duration-150 ${
        active ? activeClass : 'bg-[#252525] text-gray-400 hover:bg-[#333] hover:text-white'
      }`}
    >
      {children}
    </button>
  );
}

// ─── main page ──────────────────────────────────────────────────────────────
export default function HomePage() {
  const [channels, setChannels] = useState<Channel[]>([]);
  const [loadingPlaylist, setLoadingPlaylist] = useState(true);
  const [selected, setSelected] = useState<Channel | null>(null);
  const [search, setSearch] = useState('');
  const [typeFilter, setTypeFilter] = useState('All');
  const [groupFilter, setGroupFilter] = useState('All');
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [isMobile, setIsMobile] = useState(false);
  const searchInputRef = useRef<HTMLInputElement>(null);

  // Detect mobile; on first mobile detection close sidebar so player shows first
  useEffect(() => {
    const check = () => window.innerWidth < 640;
    const mobile = check();
    setIsMobile(mobile);
    if (mobile) setSidebarOpen(false);
    const onResize = () => setIsMobile(check());
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);

  useEffect(() => {
    fetch('/list.txt')
      .then(r => r.text())
      .then(text => {
        const parsed = parseM3U(text);
        setChannels(parsed);
        setLoadingPlaylist(false);
        if (parsed.length > 0) setSelected(parsed[0]);
      })
      .catch(() => setLoadingPlaylist(false));
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

  // Derive filter options from the full channel list
  const types = useMemo(
    () => ['All', ...Array.from(new Set(channels.map(c => c.type))).sort()],
    [channels],
  );
  const groups = useMemo(
    () =>
      ['All', ...Array.from(new Set(channels.map(c => c.group))).sort((a, b) => a.localeCompare(b))],
    [channels],
  );

  // Filtered channels
  const filtered = useMemo(() => {
    return channels.filter(ch => {
      if (typeFilter !== 'All' && ch.type !== typeFilter) return false;
      if (groupFilter !== 'All' && ch.group !== groupFilter) return false;
      if (search) {
        const q = search.toLowerCase();
        return ch.name.toLowerCase().includes(q) || ch.group.toLowerCase().includes(q);
      }
      return true;
    });
  }, [channels, typeFilter, groupFilter, search]);

  // Grouped for sidebar display
  const grouped = useMemo(() => {
    const map = new Map<string, Channel[]>();
    filtered.forEach(ch => {
      if (!map.has(ch.group)) map.set(ch.group, []);
      map.get(ch.group)!.push(ch);
    });
    return [...map.entries()];
  }, [filtered]);

  const resetFilters = useCallback(() => {
    setTypeFilter('All');
    setGroupFilter('All');
    setSearch('');
  }, []);

  // Select channel and auto-close sidebar on mobile
  const handleSelectChannel = useCallback((ch: Channel) => {
    setSelected(ch);
    if (isMobile) setSidebarOpen(false);
  }, [isMobile]);

  const hasActiveFilter =
    typeFilter !== 'All' || groupFilter !== 'All' || search !== '';

  return (
    <div className="flex flex-col h-screen bg-[#0d0d0d] text-white overflow-hidden select-none">

      {/* ── Header ─────────────────────────────────────────────────────── */}
      <header className="flex items-center gap-3 px-4 py-2.5 bg-[#181818] border-b border-[#2a2a2a] flex-shrink-0 z-20">
        <button
          onClick={() => setSidebarOpen(v => !v)}
          title="Toggle sidebar"
          className="text-gray-400 hover:text-white p-2 rounded-lg hover:bg-gray-800 transition-colors flex-shrink-0"
        >
          <svg width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24">
            <line x1="3" y1="6" x2="21" y2="6" /><line x1="3" y1="12" x2="21" y2="12" /><line x1="3" y1="18" x2="21" y2="18" />
          </svg>
        </button>

        <div className="flex items-center gap-2 flex-shrink-0">
          <span className="text-2xl">📺</span>
          <span className="font-bold text-base sm:text-lg tracking-tight">LiveTV</span>
          {!loadingPlaylist && (
            <span className="hidden sm:inline text-xs text-gray-500 bg-[#252525] px-2 py-0.5 rounded-full">
              {channels.length} channels
            </span>
          )}
        </div>

        {/* Mobile: show currently playing channel name when sidebar is closed */}
        {selected && !sidebarOpen && (
          <p className="flex-1 min-w-0 text-xs text-gray-400 truncate sm:hidden ml-1">
            {selected.name}
          </p>
        )}

        <div className="ml-auto flex items-center gap-2 flex-shrink-0">
          <div className="relative">
            <svg className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-500 pointer-events-none" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24">
              <circle cx="11" cy="11" r="8" /><line x1="21" y1="21" x2="16.65" y2="16.65" />
            </svg>
            <input
              type="text"
              value={search}
              onChange={e => setSearch(e.target.value)}
              ref={searchInputRef}
              placeholder="Search…"
              className="bg-[#252525] rounded-full pl-8 pr-4 py-2 sm:py-1.5 text-sm text-white placeholder-gray-600 border border-[#333] focus:border-blue-500 focus:outline-none w-32 sm:w-56 transition-colors"
            />
            {search && (
              <button onClick={() => setSearch('')} className="absolute right-3 top-1/2 -translate-y-1/2 text-gray-500 hover:text-white">✕</button>
            )}
          </div>
          {hasActiveFilter && (
            <button onClick={resetFilters} className="text-xs text-red-400 hover:text-red-300 px-2 py-1.5 rounded-lg hover:bg-red-900/20 transition-colors whitespace-nowrap">
              Clear
            </button>
          )}
        </div>
      </header>

      {/* ── Filter bar ─────────────────────────────────────────────────── */}
      <div className="flex-shrink-0 bg-[#141414] border-b border-[#222] px-4 py-2 z-10">
        <div className="flex items-center gap-2 overflow-x-auto scrollbar-hide">
          <span className="text-[11px] text-gray-600 font-medium uppercase tracking-wider flex-shrink-0 w-14 hidden sm:block">Type</span>
          <div className="flex items-center gap-1.5">
            {types.map(t => (
              <FilterPill key={t} active={typeFilter === t} onClick={() => setTypeFilter(t)} color="purple">
                {TYPE_EMOJI[t] ?? '📺'} {t}
              </FilterPill>
            ))}
          </div>
        </div>
      </div>

      {/* ── Body ───────────────────────────────────────────────────────── */}
      <div className="flex flex-1 overflow-hidden">

        {/* Mobile backdrop — tapping it closes the sidebar */}
        {sidebarOpen && (
          <div
            className="fixed inset-0 bg-black/60 z-40 sm:hidden"
            onClick={() => setSidebarOpen(false)}
          />
        )}

        {/* Sidebar — full-screen overlay on mobile, side panel on desktop */}
        {sidebarOpen && (
          <aside className="fixed inset-0 z-50 w-full sm:relative sm:inset-auto sm:w-72 flex-shrink-0 bg-[#161616] border-r border-[#222] flex flex-col overflow-hidden">

            {/* Mobile-only header with close button */}
            <div className="sm:hidden flex items-center justify-between px-4 py-3 bg-[#181818] border-b border-[#2a2a2a] flex-shrink-0">
              <div className="flex items-center gap-2">
                <span className="font-semibold text-white text-sm">Channel Guide</span>
                {!loadingPlaylist && (
                  <span className="text-xs text-gray-500 bg-[#252525] px-2 py-0.5 rounded-full">
                    {channels.length}
                  </span>
                )}
              </div>
              <button
                onClick={() => setSidebarOpen(false)}
                aria-label="Close channel guide"
                className="text-gray-400 hover:text-white p-2 rounded-lg hover:bg-gray-800 -mr-1"
              >
                <svg width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24">
                  <line x1="18" y1="6" x2="6" y2="18" /><line x1="6" y1="6" x2="18" y2="18" />
                </svg>
              </button>
            </div>

            {/* Mobile-only: now playing strip */}
            {selected && (
              <div className="sm:hidden flex items-center gap-2 px-4 py-2 bg-blue-950/40 border-b border-blue-900/30 flex-shrink-0">
                <span className="w-1.5 h-1.5 bg-red-500 rounded-full animate-pulse flex-shrink-0" />
                <span className="text-xs text-blue-300 truncate">Now: {selected.name}</span>
              </div>
            )}

            {/* Group filter */}
            <div className="px-3 py-2 border-b border-[#222] flex-shrink-0">
              <select
                value={groupFilter}
                onChange={e => setGroupFilter(e.target.value)}
                className="w-full bg-[#252525] text-gray-300 text-xs rounded-lg px-3 py-2 border border-[#333] focus:border-blue-500 focus:outline-none"
              >
                <option value="All">All groups ({channels.length})</option>
                {groups.slice(1).map(g => {
                  const count = channels.filter(c => c.group === g).length;
                  return <option key={g} value={g}>{g} ({count})</option>;
                })}
              </select>
            </div>

            {/* Channel list */}
            <div className="flex-1 overflow-y-auto">
              {loadingPlaylist ? (
                <div className="flex flex-col items-center justify-center h-40 gap-3 text-gray-500">
                  <div className="w-8 h-8 border-2 border-gray-600 border-t-blue-500 rounded-full animate-spin" />
                  <span className="text-sm">Loading playlist…</span>
                </div>
              ) : filtered.length === 0 ? (
                <div className="flex flex-col items-center justify-center h-40 gap-2 text-gray-500">
                  <span className="text-3xl">😕</span>
                  <span className="text-sm">No channels found</span>
                  <button onClick={resetFilters} className="text-xs text-blue-400 hover:text-blue-300 mt-1">Reset filters</button>
                </div>
              ) : (
                grouped.map(([group, chs]) => (
                  <div key={group}>
                    <div className="sticky top-0 bg-[#1c1c1c] px-3 py-1.5 border-b border-[#252525] z-10 flex items-center justify-between">
                      <span className="text-[11px] font-semibold text-gray-500 uppercase tracking-wider truncate">{group}</span>
                      <span className="text-[10px] text-gray-700 bg-[#252525] px-1.5 py-0.5 rounded-full ml-2 flex-shrink-0">{chs.length}</span>
                    </div>
                    {chs.map(ch => (
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
                    ))}
                  </div>
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
        )}

        {/* Main player area */}
        <main className="flex-1 overflow-y-auto p-4 sm:p-6 bg-[#0d0d0d]">
          <div className="max-w-5xl mx-auto">
            <VideoPlayer channel={selected} />

            {/* Stats row — only when no channel selected */}
            {!selected && !loadingPlaylist && (
              <div className="mt-6 grid grid-cols-2 sm:grid-cols-4 gap-3">
                {(['Entertainment', 'News', 'Sports', 'Movies', 'Music', 'Kids', 'Religious'] as const).map(t => {
                  const count = channels.filter(c => c.type === t).length;
                  if (!count) return null;
                  return (
                    <div key={t} className="bg-gray-900/60 rounded-xl p-4 border border-gray-800 text-center">
                      <div className="text-2xl mb-1">{TYPE_EMOJI[t] ?? '📺'}</div>
                      <div className="text-xl font-bold text-white">{count}</div>
                      <div className="text-xs text-gray-500 mt-0.5">{t}</div>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </main>
      </div>

      {/* Mobile floating Guide button — shown on mobile when sidebar is closed */}
      {!sidebarOpen && (
        <button
          className="fixed bottom-5 right-4 z-30 sm:hidden flex items-center gap-2 bg-blue-600 hover:bg-blue-500 active:scale-95 text-white pl-4 pr-5 py-3 rounded-full shadow-lg shadow-blue-900/50 font-medium text-sm transition-all"
          onClick={() => setSidebarOpen(true)}
          aria-label="Open channel guide"
        >
          <svg width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2.5" viewBox="0 0 24 24">
            <line x1="3" y1="6" x2="21" y2="6" /><line x1="3" y1="12" x2="21" y2="12" /><line x1="3" y1="18" x2="21" y2="18" />
          </svg>
          Guide
        </button>
      )}
    </div>
  );
}
