import { state, els, MAX_CELLS } from './state.js';
import { addCell, updatePickLabels } from './grid.js';
import { renderChannelList, beat } from './channels.js';
import { updateHealthStatus, observeHealth, stopHealthPolling } from './health.js';
import { updateRecordButton, restoreAudioPrefs } from './audio.js';
import { renderPicker } from './picker.js';

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
      state.health = {};
      let probed = 0;
      state.channels.forEach(function (ch) {
        if (ch.is_working === true) { state.health[ch.id] = true; probed++; }
        else if (ch.is_working === false) { state.health[ch.id] = false; probed++; }
      });
      state.healthDone = state.healthTotal = probed;
      stopHealthPolling();
      renderChannelList();
      if (!els.picker.hidden) renderPicker();
      updateHealthStatus();
      if (state.healthOn && !state.sourceRefreshing) observeHealth();
    })
    .catch(function () { state.channelsLoading = false; renderChannelList(); });
}

// Live sync is disabled (seed.m3u is the source of truth); this only reads the
// server's recording capability so the record button can show/hide.
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

  els.healthToggle.classList.toggle('on', state.healthOn);
  els.healthToggle.setAttribute('aria-checked', state.healthOn ? 'true' : 'false');

  renderChannelList();
  updateHealthStatus();
  loadKeys();
  loadChannels();
  pollSource();
  beat();
  setInterval(beat, 30000);

  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/static/sw.js').catch(function (err) { console.warn('SW registration failed:', err); });
  }
}
