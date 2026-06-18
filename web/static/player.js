import { state, cells } from './state.js';
import { proxyUrl, streamKind } from './util.js';
import { setDead } from './channels.js';

export function destroyCellPlayer(cell) {
  cell.token++;
  if (cell.hls) { cell.hls.destroy(); cell.hls = null; }
  if (cell.shaka) { try { cell.shaka.destroy(); } catch (e) { /* ignore */ } cell.shaka = null; }
  if (cell.mpegts) { try { cell.mpegts.destroy(); } catch (e) { /* ignore */ } cell.mpegts = null; }
  cell.video.removeAttribute('src');
  cell.video.onloadedmetadata = null;
  cell.video.onerror = null;
  try { cell.video.load(); } catch (e) { /* ignore */ }
}

export function setCellState(cell, st) {
  cell.els.loading.hidden = st !== 'loading';
  cell.els.error.hidden = st !== 'error';
}

function currentServer(cell) {
  const ch = cell.channel;
  if (!ch || !ch.servers || ch.servers.length === 0) return null;
  return ch.servers[Math.min(cell.serverIdx, ch.servers.length - 1)];
}

export function currentServerForCell(cell) {
  return currentServer(cell);
}

function failover(cell) {
  if (!cell.channel) return;
  cell.failedServers[cell.serverIdx] = true;
  const servers = cell.channel.servers || [];
  for (let i = 0; i < servers.length; i++) {
    if (!cell.failedServers[i]) {
      cell.serverIdx = i;
      startCellPlayback(cell);
      return;
    }
  }
  setDead(cell.channel, true);
  setCellState(cell, 'error');
}

export function retryCell(cell) {
  if (!cell.channel) return;
  cell.failedServers = {};
  cell.serverIdx = 0;
  startCellPlayback(cell);
}

function onCellPlaying(cell, token) {
  if (token !== cell.token) return;
  setCellState(cell, 'playing');
  if (cell.channel) setDead(cell.channel, false);
  playCell(cell);
  updatePlayIcon(cell);
}

export function updatePlayIcon(cell) {
  cell.els.play.classList.toggle('playing', !cell.video.paused);
}

export function startCellPlayback(cell) {
  const server = currentServer(cell);
  if (!server) { setCellState(cell, 'error'); return; }
  destroyCellPlayer(cell);
  const token = cell.token;
  setCellState(cell, 'loading');

  switch (streamKind(server.url)) {
    case 'dash': playDash(cell, server, token); break;
    case 'ts':   playTs(cell, server, token); break;
    default:     playHls(cell, server, token); break;
  }
}

export function playHls(cell, server, token) {
  const video = cell.video;
  let netRecoveries = 0, mediaRecoveries = 0;

  if (window.Hls && Hls.isSupported()) {
    cell.hls = new Hls({
      enableWorker: true,
      lowLatencyMode: false,
      capLevelToPlayerSize: false,
      startLevel: -1,
      startFragPrefetch: true,
      maxMaxBufferLength: 60,
      backBufferLength: 0,
      abrEwmaDefaultEstimate: 5000000,
    });
    const h = cell.hls;
    h.loadSource(proxyUrl(server.url));
    h.attachMedia(video);

    h.once(Hls.Events.MANIFEST_PARSED, function () { onCellPlaying(cell, token); refreshCellSettings(cell); });
    h.on(Hls.Events.LEVEL_SWITCHED, function () { if (token === cell.token) refreshCellSettings(cell); });
    h.on(Hls.Events.ERROR, function (_, data) {
      if (!data.fatal || token !== cell.token) return;
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR && netRecoveries < 1) {
        netRecoveries++; h.startLoad();
      } else if (data.type === Hls.ErrorTypes.MEDIA_ERROR && mediaRecoveries < 1) {
        mediaRecoveries++; h.recoverMediaError();
      } else {
        failover(cell);
      }
    });
  } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
    video.src = proxyUrl(server.url);
    video.onloadedmetadata = function () { onCellPlaying(cell, token); };
    video.onerror = function () { if (token === cell.token) failover(cell); };
  } else {
    setCellState(cell, 'error');
  }
}

export function playDash(cell, server, token) {
  if (!(window.shaka && shaka.Player.isBrowserSupported())) { failover(cell); return; }
  const player = new shaka.Player();
  cell.shaka = player;
  player.attach(cell.video).then(function () {
    if (token !== cell.token) return;
    const chKeys = (cell.channel && cell.channel.clear_keys) || {};
    const keys = Object.assign({}, state.clearKeys, chKeys);
    if (Object.keys(keys).length) {
      player.configure({ drm: { clearKeys: keys } });
    }
    player.getNetworkingEngine().registerRequestFilter(function (type, req) {
      const RT = shaka.net.NetworkingEngine.RequestType;
      if (type !== RT.MANIFEST && type !== RT.SEGMENT) return;
      const ownApi = location.origin + '/api/';
      req.uris = req.uris.map(function (u) {
        if (!/^https?:/i.test(u)) return u;
        if (u.indexOf(ownApi) === 0) return u;
        return proxyUrl(u);
      });
    });
    player.addEventListener('error', function () { if (token === cell.token) failover(cell); });
    player.addEventListener('adaptation', function () { if (token === cell.token) refreshCellSettings(cell); });
    player.addEventListener('variantchanged', function () { if (token === cell.token) refreshCellSettings(cell); });
    return player.load(server.url);
  }).then(function () {
    onCellPlaying(cell, token);
    refreshCellSettings(cell);
  }).catch(function () {
    if (token === cell.token) failover(cell);
  });
}

export function playTs(cell, server, token) {
  if (!(window.mpegts && mpegts.isSupported())) { failover(cell); return; }
  const video = cell.video;
  const player = mpegts.createPlayer({ type: 'mpegts', isLive: true, url: proxyUrl(server.url) });
  cell.mpegts = player;
  player.attachMediaElement(video);
  player.on(mpegts.Events.ERROR, function () { if (token === cell.token) failover(cell); });
  video.onloadedmetadata = function () { onCellPlaying(cell, token); };
  player.load();
}

export function playCell(cell) {
  const p = cell.video.play();
  if (p && p.catch) {
    p.catch(function () {
      if (!cell.video.muted) { cell.video.muted = true; applyAudio(); }
      cell.video.play().catch(function () {});
    });
  }
}

// Forward declarations for circular refs — resolved at runtime
function refreshCellSettings(cell) {
  // imported lazily to avoid circular at module eval time
  import('./cell.js').then(function(m) { m.refreshCellSettings(cell); });
}

function applyAudio() {
  import('./audio.js').then(function(m) { m.applyAudio(); });
}
