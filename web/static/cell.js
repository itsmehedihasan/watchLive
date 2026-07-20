import { state, cells, els } from './state.js';
import { destroyCellPlayer, setCellState, updatePlayIcon, startCellPlayback, retryCell, playCell } from './player.js';
import { applyAudio, renderAudioButtons, stopRecording } from './audio.js';
import { refreshHighlights, beat } from './channels.js';
import { openPicker } from './picker.js';
import { NATIVE, openScreen, focusScreen } from './native.js';

// nextNid hands each cell a stable native id used as the mpv-screen key in the
// bridge protocol. Monotonic (never reused) so a closing window can't collide
// with a freshly-added tile.
let nextNid = 1;

export function makeCell(idx) {
  const cell = {
    idx: idx,
    nid: nextNid++,
    channel: null,
    serverIdx: 0,
    failedServers: {},
    hls: null,
    shaka: null,
    mpegts: null,
    token: 0,
    root: null,
    video: null,
  };

  // Native shell: a cell is a video-less "screen tile" 1:1 with an external mpv
  // window (video lives in mpv, auto-tiled by Go). Build the tile and return.
  if (NATIVE) return makeScreenTile(cell);

  const root = document.createElement('section');
  root.className = 'cell';
  root.dataset.idx = String(idx);

  const stage = document.createElement('div');
  stage.className = 'cell-stage';

  const video = document.createElement('video');
  video.playsInline = true;
  video.muted = true;
  stage.appendChild(video);

  const loading = document.createElement('div');
  loading.className = 'cell-overlay cell-loading';
  loading.innerHTML = '<div class="spinner"></div><p class="overlay-muted">Connecting…</p>';
  loading.hidden = true;
  stage.appendChild(loading);

  // Per-cell buffering gauge (PotPlayer-style). Each cell owns its own <video>,
  // so its buffered ranges — and therefore this %  — are independent of the others.
  const buffering = document.createElement('div');
  buffering.className = 'cell-buffering';
  buffering.hidden = true;
  buffering.innerHTML = '<span class="buf-text">Buffering : 0% complete</span>';
  stage.appendChild(buffering);

  const error = document.createElement('div');
  error.className = 'cell-overlay cell-error';
  error.hidden = true;
  const errEmoji = document.createElement('div'); errEmoji.className = 'overlay-emoji'; errEmoji.textContent = '📡';
  const errTitle = document.createElement('p'); errTitle.className = 'error-title'; errTitle.textContent = 'Stream unavailable';
  const errBtn = document.createElement('button'); errBtn.className = 'primary-btn'; errBtn.textContent = '↺ Retry';
  errBtn.addEventListener('click', function () { retryCell(cell); });
  error.appendChild(errEmoji); error.appendChild(errTitle); error.appendChild(errBtn);
  stage.appendChild(error);

  const mic = document.createElement('button');
  mic.className = 'cell-mic';
  mic.title = 'Use this cell’s audio';
  mic.innerHTML =
    '<svg class="ico-on" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">' +
      '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/></svg>' +
    '<svg class="ico-off" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" hidden>' +
      '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><line x1="22" y1="9" x2="16" y2="15"/><line x1="16" y1="9" x2="22" y2="15"/></svg>';
  mic.addEventListener('click', function () { setAudioCell(cell.idx); });
  stage.appendChild(mic);

  const play = document.createElement('button');
  play.className = 'cell-play';
  play.title = 'Play / pause';
  play.innerHTML =
    '<svg class="ico-play" width="16" height="16" fill="currentColor" stroke="none" viewBox="0 0 24 24"><path d="M8 5v14l11-7z"/></svg>' +
    '<svg class="ico-pause" width="16" height="16" fill="currentColor" stroke="none" viewBox="0 0 24 24"><rect x="6" y="5" width="4" height="14" rx="1"/><rect x="14" y="5" width="4" height="14" rx="1"/></svg>';
  play.addEventListener('click', function () {
    if (cell.video.paused) cell.video.play().catch(function () {});
    else cell.video.pause();
  });
  video.addEventListener('play', function () { updatePlayIcon(cell); });
  video.addEventListener('pause', function () { updatePlayIcon(cell); });
  stage.appendChild(play);

  const label = document.createElement('div');
  label.className = 'cell-label';
  stage.appendChild(label);

  const controls = document.createElement('div');
  controls.className = 'cell-controls';
  const gear = document.createElement('button');
  gear.className = 'cell-ctl cell-gear';
  gear.title = 'Settings — quality & server';
  gear.innerHTML = '<svg width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 11-2.83 2.83l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 11-2.83-2.83l.06-.06a1.65 1.65 0 00.33-1.82 1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 112.83-2.83l.06.06a1.65 1.65 0 001.82.33H9a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 112.83 2.83l-.06.06a1.65 1.65 0 00-.33 1.82V9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z"/></svg>';
  gear.addEventListener('click', function (e) { e.stopPropagation(); toggleCellSettings(cell); });
  const expand = document.createElement('button');
  expand.className = 'cell-ctl';
  expand.title = 'Expand (fullscreen)';
  expand.innerHTML = '<svg width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path d="M8 3H5a2 2 0 00-2 2v3m18 0V5a2 2 0 00-2-2h-3m0 18h3a2 2 0 002-2v-3M3 16v3a2 2 0 002 2h3"/></svg>';
  expand.addEventListener('click', function () { toggleCellFullscreen(cell); });
  const close = document.createElement('button');
  close.className = 'cell-ctl';
  close.title = 'Close this screen';
  close.innerHTML = '<svg width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
  close.addEventListener('click', function () { clearCell(cell); });
  controls.appendChild(gear);
  controls.appendChild(expand);
  controls.appendChild(close);
  stage.appendChild(controls);

  const settings = document.createElement('div');
  settings.className = 'cell-settings';
  settings.hidden = true;
  settings.addEventListener('click', function (e) { e.stopPropagation(); });
  stage.appendChild(settings);

  root.appendChild(stage);

  const empty = document.createElement('div');
  empty.className = 'cell-empty';
  const pick = document.createElement('button');
  pick.className = 'cell-pick';
  pick.innerHTML =
    '<svg width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">' +
      '<rect x="2" y="6" width="14" height="12" rx="2"/><path d="M16 10l6-3v10l-6-3"/><line x1="9" y1="9" x2="9" y2="15"/><line x1="6" y1="12" x2="12" y2="12"/></svg>' +
    '<span class="cell-pick-label"></span>';
  pick.addEventListener('click', function () { openPicker(cell.idx); });
  empty.appendChild(pick);
  root.appendChild(empty);

  // --- Buffering monitor (PotPlayer-style %) ---------------------------------
  // A live stream plays right at the live edge, so the forward buffer naturally
  // stays near zero — a raw "buffer ahead" % would just sit at ~2% forever.
  // Instead, on a stall we FREEZE the playhead (auto-pause), let the underlying
  // player keep downloading into the buffer, and show how full it is getting
  // toward FILL_TARGET seconds. With the playhead frozen the buffer actually
  // grows, so the % climbs 0→100, then we resume — exactly like PotPlayer.
  const FILL_TARGET = 3;    // seconds to queue up before resuming after a stall
  const MAX_WAIT = 7000;    // never hold the playhead frozen longer than this
  const PLATEAU_MS = 2500;  // if the buffer stops growing, stop waiting for more
  const bufText = buffering.querySelector('.buf-text');
  let bufTimer = 0;
  let filling = false;   // currently buffering with the playhead frozen
  let autoPaused = false; // the pause was ours (a refill), not the user's
  let fillStart = 0, bestAhead = 0, lastGrowAt = 0;

  function nowMs() { return (window.performance && performance.now) ? performance.now() : 0; }
  function bufferedAhead() {
    const b = video.buffered, t = video.currentTime;
    for (let i = 0; i < b.length; i++) {
      // Tolerate tiny gaps so a 1-frame hole at the playhead doesn't read as 0.
      if (b.start(i) <= t + 0.25 && t < b.end(i) + 0.25) return Math.max(0, b.end(i) - t);
    }
    return 0;
  }
  function paintBuf() {
    const ahead = bufferedAhead();
    const pct = Math.max(0, Math.min(100, Math.round((ahead / FILL_TARGET) * 100)));
    bufText.textContent = 'Buffering : ' + pct + '% complete';
    if (!filling) return;
    const t = nowMs();
    if (ahead > bestAhead + 0.05) { bestAhead = ahead; lastGrowAt = t; }
    // Resume the moment we have enough — OR bail out so we can NEVER freeze
    // forever: a hard timeout, or the buffer plateauing (upstream can't get
    // ahead of realtime). Whatever we have, we resume and let playback / the
    // player's own error handling take over.
    const enough = ahead >= FILL_TARGET || video.readyState >= 4;
    const timedOut = (t - fillStart) > MAX_WAIT;
    const plateaued = ahead > 0.3 && (t - lastGrowAt) > PLATEAU_MS;
    if (enough || timedOut || plateaued) resumeFromBuffering();
  }
  function startFilling() {
    if (filling || !cell.channel || !loading.hidden || video.ended) return;
    filling = true;
    autoPaused = true;
    fillStart = nowMs(); bestAhead = bufferedAhead(); lastGrowAt = fillStart;
    try { video.pause(); } catch (e) { /* ignore */ }
    buffering.hidden = false;
    paintBuf();
    if (!bufTimer) bufTimer = window.setInterval(paintBuf, 200);
  }
  function stopOverlay() {
    buffering.hidden = true;
    if (bufTimer) { clearInterval(bufTimer); bufTimer = 0; }
  }
  function resumeFromBuffering() {
    if (!filling) return;
    filling = false;
    stopOverlay();
    autoPaused = false;
    video.play().catch(function () { /* will retry on next event */ });
  }
  function hideBuffering() { filling = false; autoPaused = false; stopOverlay(); }
  cell.hideBuffering = hideBuffering;

  // A real stall while we WANT to be playing → start a PotPlayer-style refill.
  video.addEventListener('waiting', function () { if (!video.paused || autoPaused) startFilling(); });
  video.addEventListener('stalled', function () { if (!video.paused || autoPaused) startFilling(); });
  // The player recovered on its own (enough data) → drop the overlay.
  video.addEventListener('playing', function () { if (filling) { filling = false; } stopOverlay(); });
  video.addEventListener('pause', function () {
    if (autoPaused) { autoPaused = false; return; } // our own refill pause — keep filling
    hideBuffering(); // user paused → abort refill, no auto-resume
  });

  cell.root = root;
  cell.video = video;
  cell.els = { stage: stage, empty: empty, loading: loading, buffering: buffering, error: error, mic: mic, play: play, label: label, settings: settings, pickLabel: pick.querySelector('.cell-pick-label') };

  return cell;
}

// makeScreenTile builds the native-mode cell: a control tile for one mpv window.
// No <video>, players, or buffering monitor — mpv owns playback and its own OSC.
// It reuses the loading/error overlays, label, mic, and pick button so the shared
// assignChannel / clearCell / setCellState / audio machinery works unchanged.
function makeScreenTile(cell) {
  const root = document.createElement('section');
  root.className = 'cell cell-screen';
  root.dataset.idx = String(cell.idx);

  const stage = document.createElement('div');
  stage.className = 'cell-stage';

  const logo = document.createElement('img');
  logo.className = 'screen-logo';
  logo.alt = '';
  logo.hidden = true;
  stage.appendChild(logo);

  const label = document.createElement('div');
  label.className = 'cell-label screen-name';
  stage.appendChild(label);

  const hint = document.createElement('div');
  hint.className = 'screen-hint';
  hint.textContent = 'Playing in its own window';
  stage.appendChild(hint);

  const loading = document.createElement('div');
  loading.className = 'cell-overlay cell-loading';
  loading.innerHTML = '<div class="spinner"></div><p class="overlay-muted">Opening…</p>';
  loading.hidden = true;
  stage.appendChild(loading);

  const error = document.createElement('div');
  error.className = 'cell-overlay cell-error';
  error.hidden = true;
  const errEmoji = document.createElement('div'); errEmoji.className = 'overlay-emoji'; errEmoji.textContent = '📡';
  const errTitle = document.createElement('p'); errTitle.className = 'error-title'; errTitle.textContent = 'Stream unavailable';
  const errBtn = document.createElement('button'); errBtn.className = 'primary-btn'; errBtn.textContent = '↺ Retry';
  errBtn.addEventListener('click', function () { retryCell(cell); });
  error.appendChild(errEmoji); error.appendChild(errTitle); error.appendChild(errBtn);
  stage.appendChild(error);

  const mic = document.createElement('button');
  mic.className = 'cell-mic';
  mic.title = 'Use this screen’s audio';
  mic.innerHTML =
    '<svg class="ico-on" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">' +
      '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/></svg>' +
    '<svg class="ico-off" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" hidden>' +
      '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><line x1="22" y1="9" x2="16" y2="15"/><line x1="16" y1="9" x2="22" y2="15"/></svg>';
  mic.addEventListener('click', function () { setAudioCell(cell.idx); });
  stage.appendChild(mic);

  const controls = document.createElement('div');
  controls.className = 'cell-controls';
  const focusBtn = document.createElement('button');
  focusBtn.className = 'cell-ctl';
  focusBtn.title = 'Bring this player window to the front';
  focusBtn.innerHTML = '<svg width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/></svg>';
  focusBtn.addEventListener('click', function () { focusScreen(cell.nid); });
  const close = document.createElement('button');
  close.className = 'cell-ctl';
  close.title = 'Stop this screen';
  close.innerHTML = '<svg width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
  close.addEventListener('click', function () { clearCell(cell); });
  controls.appendChild(focusBtn);
  controls.appendChild(close);
  stage.appendChild(controls);

  root.appendChild(stage);

  const empty = document.createElement('div');
  empty.className = 'cell-empty';
  const pick = document.createElement('button');
  pick.className = 'cell-pick';
  pick.innerHTML =
    '<svg width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">' +
      '<rect x="2" y="6" width="14" height="12" rx="2"/><path d="M16 10l6-3v10l-6-3"/><line x1="9" y1="9" x2="9" y2="15"/><line x1="6" y1="12" x2="12" y2="12"/></svg>' +
    '<span class="cell-pick-label"></span>';
  pick.addEventListener('click', function () { openPicker(cell.idx); });
  empty.appendChild(pick);
  root.appendChild(empty);

  cell.root = root;
  cell.video = null;
  cell.hideBuffering = function () {};
  cell.els = {
    stage: stage, empty: empty, loading: loading, error: error,
    mic: mic, label: label, logo: logo, hint: hint,
    settings: null, play: null, buffering: null,
    pickLabel: pick.querySelector('.cell-pick-label'),
  };

  // Open the mpv window immediately (idle, ready) — "+ opens a window now".
  openScreen(cell.nid);
  return cell;
}

function setAudioCell(idx) {
  const cell = cells[idx];
  if (!cell || !cell.channel) return;
  state.audioCell = idx;
  applyAudio();
  renderAudioButtons();
  beat();
}

function toggleCellFullscreen(cell) {
  // Browser only — native screen tiles have no expand button (mpv owns fullscreen).
  if (!document.fullscreenElement) cell.els.stage.requestFullscreen().catch(function () {});
  else document.exitFullscreen();
}

export function toggleCellSettings(cell) {
  if (state.openSettingsCell === cell) { closeCellSettings(); return; }
  closeCellSettings();
  if (!cell.channel) return;
  renderCellSettings(cell);
  cell.els.settings.hidden = false;
  cell.root.classList.add('settings-open');
  state.openSettingsCell = cell;
}

export function closeCellSettings() {
  if (!state.openSettingsCell) return;
  state.openSettingsCell.els.settings.hidden = true;
  state.openSettingsCell.root.classList.remove('settings-open');
  state.openSettingsCell = null;
}

export function refreshCellSettings(cell) {
  if (state.openSettingsCell === cell) renderCellSettings(cell);
}

function levelLabel(lv) {
  if (lv.height) return lv.height + 'p';
  if (lv.name) return String(lv.name);
  if (lv.bitrate) {
    return lv.bitrate >= 1000000
      ? (lv.bitrate / 1000000).toFixed(1) + ' Mbps'
      : Math.round(lv.bitrate / 1000) + ' kbps';
  }
  return 'Auto';
}

function cellQuality(cell) {
  if (cell.hls && cell.hls.levels && cell.hls.levels.length > 1) {
    const h = cell.hls;
    const opts = [{ value: -1, label: 'Auto' }];
    for (let i = h.levels.length - 1; i >= 0; i--) {
      opts.push({ value: i, label: levelLabel(h.levels[i]) });
    }
    return {
      opts: opts,
      current: h.autoLevelEnabled ? -1 : h.currentLevel,
      set: function (v) { h.currentLevel = v; },
    };
  }
  if (cell.shaka) {
    let tracks = [];
    try { tracks = cell.shaka.getVariantTracks() || []; } catch (e) { return null; }
    const byHeight = {};
    tracks.forEach(function (t) {
      if (!t.height) return;
      if (!byHeight[t.height] || t.bandwidth > byHeight[t.height].bandwidth) byHeight[t.height] = t;
    });
    const heights = Object.keys(byHeight).map(Number).sort(function (a, b) { return b - a; });
    if (heights.length < 2) return null;
    const opts = [{ value: -1, label: 'Auto' }];
    heights.forEach(function (hgt) { opts.push({ value: hgt, label: hgt + 'p' }); });
    const cfg = cell.shaka.getConfiguration();
    const active = tracks.find(function (t) { return t.active; });
    return {
      opts: opts,
      current: (cfg.abr && cfg.abr.enabled) ? -1 : (active && active.height ? active.height : -1),
      set: function (v) {
        if (v === -1) { cell.shaka.configure({ abr: { enabled: true } }); return; }
        cell.shaka.configure({ abr: { enabled: false } });
        if (byHeight[v]) cell.shaka.selectVariantTrack(byHeight[v], true);
      },
    };
  }
  return null;
}

function renderCellSettings(cell) {
  const panel = cell.els.settings;
  panel.innerHTML = '';
  const q = cellQuality(cell);
  const servers = (cell.channel && cell.channel.servers && cell.channel.servers.length > 1)
    ? cell.channel.servers : null;

  if (q) {
    panel.appendChild(settingsSection('Quality', q.opts.map(function (o) {
      return settingsOpt(o.label, o.value === q.current, function () {
        q.set(o.value);
        renderCellSettings(cell);
      });
    })));
  }
  if (servers) {
    panel.appendChild(settingsSection('Server', servers.map(function (s, i) {
      return settingsOpt(s.label || ('Server ' + (i + 1)), i === cell.serverIdx, function () {
        if (i === cell.serverIdx) return;
        cell.serverIdx = i;
        cell.failedServers = {};
        startCellPlayback(cell);
        renderCellSettings(cell);
      });
    })));
  }
  if (!q && !servers) {
    const empty = document.createElement('div');
    empty.className = 'cell-settings-empty';
    empty.textContent = 'No options available';
    panel.appendChild(empty);
  }
}

function settingsSection(title, buttons) {
  const sec = document.createElement('div');
  sec.className = 'cell-settings-section';
  const head = document.createElement('div');
  head.className = 'cell-settings-label';
  head.textContent = title;
  sec.appendChild(head);
  const row = document.createElement('div');
  row.className = 'cell-settings-opts';
  buttons.forEach(function (b) { row.appendChild(b); });
  sec.appendChild(row);
  return sec;
}

function settingsOpt(label, active, onClick) {
  const b = document.createElement('button');
  b.className = 'cell-settings-opt' + (active ? ' active' : '');
  b.textContent = label;
  b.addEventListener('click', function (e) { e.stopPropagation(); onClick(); });
  return b;
}

export function assignChannel(cellIdx, ch) {
  const cell = cells[cellIdx];
  if (!cell) return;
  if (state.recId && cellIdx === state.recCellIdx) stopRecording();
  cell.channel = ch;
  cell.serverIdx = 0;
  cell.failedServers = {};
  cell.root.classList.add('filled');
  cell.els.label.textContent = ch.name;
  // Native screen tiles show the channel logo (no video preview).
  if (cell.els.logo) {
    if (ch.logo) { cell.els.logo.src = ch.logo; cell.els.logo.hidden = false; }
    else { cell.els.logo.removeAttribute('src'); cell.els.logo.hidden = true; }
  }
  if (state.audioCell === -1) state.audioCell = cellIdx;
  if (ch.resolver) {
    // Dynamic channel: fetch a fresh signed URL before playing. The backend caches
    // it into ch.servers and updates the proxy header map for the (rotated) host.
    setCellState(cell, 'loading');
    resolveChannel(ch).then(function (ok) {
      if (cell.channel !== ch) return; // user already switched this cell away
      if (ok) startCellPlayback(cell);
      else setCellState(cell, 'error');
    });
  } else {
    startCellPlayback(cell);
  }
  applyAudio();
  renderAudioButtons();
  refreshHighlights();
  beat();
}

// resolveChannel asks the backend for a fresh playable URL for a dynamic channel
// and patches it onto ch.servers in place. Returns a promise of success.
function resolveChannel(ch) {
  return fetch('/api/resolve?id=' + encodeURIComponent(ch.id))
    .then(function (r) { if (!r.ok) throw new Error('resolve ' + r.status); return r.json(); })
    .then(function (d) {
      if (!d || !d.url) return false;
      ch.servers = [{ url: d.url }];
      if (d.referer) ch.http_referer = d.referer;
      return true;
    })
    .catch(function (e) { console.warn('resolve failed for', ch.name, e); return false; });
}

export function clearCell(cell) {
  if (state.openSettingsCell === cell) closeCellSettings();
  if (state.recId && cell.idx === state.recCellIdx) stopRecording();
  destroyCellPlayer(cell);
  cell.channel = null;
  cell.root.classList.remove('filled');
  cell.els.label.textContent = '';
  if (cell.els.logo) { cell.els.logo.removeAttribute('src'); cell.els.logo.hidden = true; }
  setCellState(cell, 'idle');
  if (state.audioCell === cell.idx) {
    state.audioCell = -1;
    for (let i = 0; i < cells.length; i++) {
      if (cells[i].channel) { state.audioCell = i; break; }
    }
  }
  applyAudio();
  renderAudioButtons();
  refreshHighlights();
  beat();
}

document.addEventListener('click', function () { closeCellSettings(); });
