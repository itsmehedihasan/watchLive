import { state, cells, els } from './state.js';
import { currentServerForCell } from './player.js';

export function applyAudio() {
  cells.forEach(function (c, i) {
    const isAudio = i === state.audioCell && !state.globalMuted;
    c.video.muted = !isAudio;
    c.video.volume = state.volume;
    const on = c.els.mic.querySelector('.ico-on');
    const off = c.els.mic.querySelector('.ico-off');
    if (on && off) { on.hidden = !isAudio; off.hidden = isAudio; }
    c.els.mic.classList.toggle('active', isAudio);
  });
  els.muteBtn.querySelector('.ico-vol').hidden = state.globalMuted;
  els.muteBtn.querySelector('.ico-muted').hidden = !state.globalMuted;
  els.muteBtn.setAttribute('aria-pressed', state.globalMuted ? 'true' : 'false');
  if (state.recId && state.audioCell !== state.recCellIdx) stopRecording();
  updateRecordButton();
}

export function updateRecordButton() {
  const btn = els.recordBtn;
  if (!state.recordingAvailable) { btn.hidden = true; return; }
  btn.hidden = false;
  const canStart = state.audioCell >= 0 && cells[state.audioCell] && cells[state.audioCell].channel;
  btn.disabled = !(state.recId || canStart);
  btn.title = state.recId ? 'Stop recording' : 'Record the screen with audio';
}

function updateRecordTime() {
  const s = Math.max(0, Math.floor((Date.now() - state.recStartMs) / 1000));
  els.recordTime.textContent = Math.floor(s / 60) + ':' + ('0' + (s % 60)).slice(-2);
}

export function toggleRecording() {
  if (state.recId) stopRecording();
  else startRecording();
}

export function startRecording() {
  if (state.recId || state.audioCell < 0) return;
  const cell = cells[state.audioCell];
  if (!cell || !cell.channel) return;
  const server = currentServerForCell(cell);
  if (!server) return;
  state.recCellIdx = cell.idx;
  els.recordBtn.disabled = true;
  fetch('/api/record/start', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url: server.url, name: cell.channel.name }),
  })
    .then(function (r) { if (!r.ok) throw new Error('start failed'); return r.json(); })
    .then(function (d) {
      state.recId = d.id;
      state.recStartMs = Date.now();
      els.recordBtn.classList.add('recording');
      els.recordSavedRow.hidden = true;
      els.recordTime.hidden = false;
      updateRecordTime();
      state.recTimer = setInterval(updateRecordTime, 1000);
      updateRecordButton();
    })
    .catch(function () {
      state.recCellIdx = -1;
      updateRecordButton();
    });
}

export function stopRecording() {
  if (!state.recId) return;
  const id = state.recId;
  state.recId = null;
  state.recCellIdx = -1;
  if (state.recTimer) { clearInterval(state.recTimer); state.recTimer = null; }
  els.recordBtn.classList.remove('recording');
  els.recordTime.hidden = true;
  els.recordBtn.disabled = true;
  fetch('/api/record/stop', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id: id }),
  })
    .then(function (r) { return r.json(); })
    .then(function (d) {
      if (d && d.file) {
        els.recordSaved.href = '/api/record/file?name=' + encodeURIComponent(d.file);
        els.recordSavedRow.hidden = false;
      }
    })
    .catch(function () {})
    .then(function () { updateRecordButton(); });
}

export function renderAudioButtons() {
  els.audioButtons.innerHTML = '';
  for (let i = 0; i < cells.length; i++) {
    (function (i) {
      const btn = document.createElement('button');
      const cell = cells[i];
      btn.className = 'rail-num' +
        (i === state.audioCell ? ' active' : '') +
        (cell.channel ? '' : ' empty');
      btn.textContent = String(i + 1);
      btn.title = cell.channel ? ('Audio from screen ' + (i + 1) + ' — ' + cell.channel.name)
                               : ('Screen ' + (i + 1) + ' is empty');
      btn.addEventListener('click', function () {
        if (cell.channel) {
          state.audioCell = i;
          applyAudio();
          renderAudioButtons();
          beat();
        } else {
          openPicker(i);
        }
      });
      els.audioButtons.appendChild(btn);
    })(i);
  }
}

function applyVolumeFill() {
  els.volume.style.setProperty('--vol-fill', Math.round(state.volume * 100) + '%');
}

export function setVolume(v) {
  state.volume = Math.max(0, Math.min(1, v));
  els.volume.value = String(Math.round(state.volume * 100));
  if (state.volume > 0 && state.globalMuted) state.globalMuted = false;
  applyVolumeFill();
  persistAudio();
  applyAudio();
}

export function persistAudio() {
  try {
    localStorage.setItem('livetv_volume', String(Math.round(state.volume * 100)));
    localStorage.setItem('livetv_muted', state.globalMuted ? '1' : '0');
  } catch (e) { /* quota */ }
}

export function restoreAudioPrefs() {
  const v = parseInt(localStorage.getItem('livetv_volume'), 10);
  if (!isNaN(v)) state.volume = Math.max(0, Math.min(1, v / 100));
  state.globalMuted = localStorage.getItem('livetv_muted') === '1';
  els.volume.value = String(Math.round(state.volume * 100));
  applyVolumeFill();
}

// forward ref
function beat() { import('./channels.js').then(function(m) { m.beat(); }); }
function openPicker(i) { import('./picker.js').then(function(m) { m.openPicker(i); }); }

els.muteBtn.addEventListener('click', function () {
  state.globalMuted = !state.globalMuted;
  persistAudio();
  applyAudio();
});
els.volume.addEventListener('input', function () {
  setVolume(parseInt(els.volume.value, 10) / 100);
});
els.recordBtn.addEventListener('click', toggleRecording);
els.recordSavedClose.addEventListener('click', function () { els.recordSavedRow.hidden = true; });
els.volUp.addEventListener('click', function () { setVolume(state.volume + 0.1); });
els.volDown.addEventListener('click', function () { setVolume(state.volume - 0.1); });
