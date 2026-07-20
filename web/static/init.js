import { state, els, MAX_CELLS } from './state.js';
import { addCell, updatePickLabels } from './grid.js';
import { renderChannelList } from './channels.js';
import { updateRecordButton, restoreAudioPrefs } from './audio.js';
import { renderPicker } from './picker.js';
import { initPlaylistFilter, initSidebarTabs } from './playlists.js';

export function loadKeys() {
  fetch('/api/keys')
    .then(function (r) { return r.ok ? r.json() : {}; })
    .then(function (data) { state.clearKeys = (data && typeof data === 'object') ? data : {}; })
    .catch(function () { /* keep whatever we had */ });
}

export function loadChannels() {
  fetch('/api/channels')
    .then(function (r) {
      if (!r.ok) throw new Error('channels fetch failed: ' + r.status);
      return r.json();
    })
    .then(function (data) {
      state.channels = Array.isArray(data) ? data : [];
      state.channelsLoading = false;
      renderChannelList();
      if (!els.picker.hidden) renderPicker();
    })
    .catch(function () { state.channelsLoading = false; renderChannelList(); });
}

// Live sync is disabled; this only reads the server's recording capability so
// the record button can show/hide.
function pollSource() {
  fetch('/api/source')
    .then(function (r) { return r.json(); })
    .then(function (d) {
      if (d.recordingAvailable != null) { state.recordingAvailable = !!d.recordingAvailable; updateRecordButton(); }
    })
    .catch(function () { /* old build — ignore */ });
}

export function init() {
  restoreAudioPrefs();

  const saved = parseInt(localStorage.getItem('livetv_grid'), 10);
  const count = (!isNaN(saved) && saved >= 1 && saved <= MAX_CELLS) ? saved : 1;
  for (let i = 0; i < count; i++) addCell();
  updatePickLabels();

  renderChannelList();
  loadKeys();
  loadChannels();
  initPlaylistFilter();
  initSidebarTabs();
  pollSource();

  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/static/sw.js').catch(function (err) { console.warn('SW registration failed:', err); });
  }
}
