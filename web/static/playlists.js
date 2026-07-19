import { state, els } from './state.js';
import { renderChannelList } from './channels.js';

// initPlaylistFilter loads saved Xtream playlists into the sidebar dropdown and
// selects the first one (index [0]) by default. Selection is in-memory only —
// it resets to [0] on every page load. Manual channels stay visible regardless.
export function initPlaylistFilter() {
  const sel = els.playlistFilter;
  const wrap = els.playlistFilterWrap;
  if (!sel || !wrap) return;
  fetch('/api/xtream/playlists')
    .then(function (r) { return r.ok ? r.json() : []; })
    .then(function (list) {
      list = Array.isArray(list) ? list : [];
      sel.innerHTML = '';
      if (list.length === 0) {
        wrap.hidden = true;
        state.selectedPlaylist = '';
        renderChannelList();
        return;
      }
      wrap.hidden = false;
      list.forEach(function (p) {
        const opt = document.createElement('option');
        opt.value = p.id;
        opt.textContent = p.name;
        sel.appendChild(opt);
      });
      state.selectedPlaylist = list[0].id; // index [0] default
      sel.value = state.selectedPlaylist;
      renderChannelList();
    })
    .catch(function () {
      wrap.hidden = true;
      state.selectedPlaylist = '';
      renderChannelList();
    });

  sel.addEventListener('change', function () {
    state.selectedPlaylist = sel.value;
    renderChannelList();
  });
}

// initSidebarTabs wires the Channels/Movies/Sports tab bar. Only Channels shows
// the list today; Movies/Sports render a placeholder (handled in channels.js).
export function initSidebarTabs() {
  const tabs = els.sidebarTabs;
  if (!tabs) return;
  const buttons = [els.tabChannels, els.tabMovies, els.tabSports];
  buttons.forEach(function (btn) {
    if (!btn) return;
    btn.addEventListener('click', function () {
      const tab = btn.dataset.tab || 'channels';
      if (state.activeTab === tab) return;
      state.activeTab = tab;
      buttons.forEach(function (b) {
        if (!b) return;
        const on = b === btn;
        b.classList.toggle('active', on);
        b.setAttribute('aria-selected', on ? 'true' : 'false');
      });
      renderChannelList();
    });
  });
}
