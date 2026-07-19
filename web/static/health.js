import { state, els } from './state.js';
import { renderChannelList } from './channels.js';
import { renderPicker } from './picker.js';

export function countAlive() {
  let n = 0;
  for (const id in state.health) { if (state.health[id]) n++; }
  return n;
}

export function renderHealthLists() {
  renderChannelList();
  if (!els.picker.hidden) renderPicker();
}

export function updateHealthStatus() {
  const el = els.healthStatus;
  if (!state.healthOn) { el.hidden = true; el.textContent = ''; return; }
  el.hidden = false;
  if (state.healthProbing) el.textContent = 'Re-checking ' + state.healthDone + ' / ' + state.healthTotal + ' · ' + countAlive() + ' live';
  else if (state.healthTotal) el.textContent = countAlive() + ' working of ' + state.healthTotal;
  else el.textContent = '';
}

export function applyHealthSnapshot(snap) {
  if (!snap) return;
  state.healthDone = snap.done || 0;
  state.healthTotal = snap.total || 0;
  let changed = false;
  if (snap.status) {
    for (const id in snap.status) {
      const v = snap.status[id];
      if (state.health[id] !== v) { state.health[id] = v; changed = true; }
    }
  }
  const wasProbing = state.healthProbing;
  state.healthProbing = !!snap.running;
  if (!snap.running) stopHealthPolling();
  if (state.healthOn && (changed || (wasProbing && !snap.running))) renderHealthLists();
  updateHealthStatus();
}

export function pollHealth() {
  fetch('/api/health').then(function (r) { return r.json(); }).then(applyHealthSnapshot).catch(function () {});
}

export function startHealthProbe(force) {
  state.healthProbing = true;
  updateHealthStatus();
  fetch('/api/health' + (force ? '?force=1' : ''), { method: 'POST' })
    .then(function (r) { return r.json(); }).then(applyHealthSnapshot).catch(function () {});
  if (!state.healthPoll) state.healthPoll = setInterval(pollHealth, 1500);
}

export function observeHealth() {
  fetch('/api/health')
    .then(function (r) { return r.json(); })
    .then(function (snap) {
      applyHealthSnapshot(snap);
      if (snap && snap.running && !state.healthPoll) state.healthPoll = setInterval(pollHealth, 1500);
    })
    .catch(function () {});
}

export function stopHealthPolling() {
  if (state.healthPoll) { clearInterval(state.healthPoll); state.healthPoll = null; }
  state.healthProbing = false;
}

els.healthToggle.addEventListener('click', function () {
  state.healthOn = !state.healthOn;
  try { localStorage.setItem('livetv_health_on', state.healthOn ? '1' : '0'); } catch (e) { /* quota */ }
  els.healthToggle.classList.toggle('on', state.healthOn);
  els.healthToggle.setAttribute('aria-checked', state.healthOn ? 'true' : 'false');
  if (state.healthOn) startHealthProbe(true); else stopHealthPolling();
  renderHealthLists();
  updateHealthStatus();
});
