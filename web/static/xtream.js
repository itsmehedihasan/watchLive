import { state, els } from './state.js';
import { loadChannels } from './init.js';

// The Xtream Codes tab of the add modal: a dropdown of saved playlists (with a
// Refresh button) plus an always-visible "add a playlist" sub-form. Saving or
// selecting a not-yet-imported playlist fetches and imports its live channels
// synchronously (spinner shown); errors surface inline.

// loadedPlaylists caches the last GET /api/xtream/playlists response so change
// handlers can look up a selected playlist's imported flag without refetching.
let loadedPlaylists = [];

function showError(msg) {
  els.xtreamError.textContent = msg;
  els.xtreamError.hidden = !msg;
}

function setBusy(btn, busy, idleText) {
  if (busy) {
    btn.disabled = true;
    btn.dataset.idle = idleText || btn.textContent;
    btn.innerHTML = '<span class="btn-spinner"></span>';
  } else {
    btn.disabled = false;
    btn.textContent = btn.dataset.idle || idleText || btn.textContent;
  }
}

// resetXtreamTab clears the sub-form and error; called whenever the modal opens.
export function resetXtreamTab() {
  els.xtreamName.value = '';
  els.xtreamUser.value = '';
  els.xtreamPass.value = '';
  els.xtreamServer.value = '';
  showError('');
}

// initXtreamTab loads the saved-playlist list; called each time the tab is shown
// so a playlist added in this session appears without a full reload.
export function initXtreamTab() {
  showError('');
  fetch('/api/xtream/playlists')
    .then(function (r) { return r.ok ? r.json() : []; })
    .then(function (list) { renderSaved(Array.isArray(list) ? list : []); })
    .catch(function () { renderSaved([]); });
}

function renderSaved(list) {
  loadedPlaylists = list;
  const sel = els.xtreamSaved;
  sel.innerHTML = '';
  if (list.length === 0) {
    els.xtreamSavedWrap.hidden = true;
    return;
  }
  els.xtreamSavedWrap.hidden = false;
  list.forEach(function (p) {
    const opt = document.createElement('option');
    opt.value = p.id;
    opt.textContent = p.name + (p.imported ? '' : ' (not imported)');
    sel.appendChild(opt);
  });
}

function selectedPlaylist() {
  const id = els.xtreamSaved.value;
  for (let i = 0; i < loadedPlaylists.length; i++) {
    if (loadedPlaylists[i].id === id) return loadedPlaylists[i];
  }
  return null;
}

// Selecting a playlist that has never been imported fetches+imports it via a
// refresh (the server upserts by stable id, so refresh doubles as first import).
// An already-imported playlist just stays selected — no network call.
els.xtreamSaved.addEventListener('change', function () {
  const p = selectedPlaylist();
  if (p && !p.imported) importPlaylist(p.id, els.xtreamSaved);
});

els.xtreamRefresh.addEventListener('click', function () {
  const p = selectedPlaylist();
  if (p) importPlaylist(p.id, els.xtreamRefresh);
});

// importPlaylist refreshes (fetches + upserts) a saved playlist's live channels.
function importPlaylist(id, busyBtn) {
  showError('');
  setBusy(busyBtn, true, busyBtn === els.xtreamRefresh ? 'Refresh' : '');
  fetch('/api/xtream/playlists/' + encodeURIComponent(id) + '/refresh', { method: 'POST' })
    .then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t || ('refresh failed: ' + r.status)); });
      return r.json();
    })
    .then(function () {
      setBusy(busyBtn, false);
      loadChannels();
      // Reflect the now-imported state in the dropdown labels.
      initXtreamTab();
    })
    .catch(function (err) {
      setBusy(busyBtn, false);
      showError(friendly(err));
    });
}

els.xtreamSave.addEventListener('click', function () {
  const name = els.xtreamName.value.trim();
  const username = els.xtreamUser.value.trim();
  const password = els.xtreamPass.value;
  const server = els.xtreamServer.value.trim();
  showError('');
  if (!name || !username || !password || !/^https?:\/\//i.test(server)) {
    showError('Enter a name, username, password and a server address starting with http:// or https://.');
    return;
  }
  setBusy(els.xtreamSave, true, 'Save & Import');
  fetch('/api/xtream/playlists', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: name, server: server, username: username, password: password }),
  })
    .then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t || ('save failed: ' + r.status)); });
      return r.json();
    })
    .then(function (d) {
      setBusy(els.xtreamSave, false);
      resetXtreamTab();
      loadChannels();
      // Reload the saved list and select the newly-added playlist.
      fetch('/api/xtream/playlists')
        .then(function (r) { return r.ok ? r.json() : []; })
        .then(function (list) {
          renderSaved(Array.isArray(list) ? list : []);
          if (d && d.playlist && d.playlist.id) els.xtreamSaved.value = d.playlist.id;
        })
        .catch(function () {});
    })
    .catch(function (err) {
      setBusy(els.xtreamSave, false);
      showError(friendly(err));
    });
});

// friendly turns a raw fetch error into a short user-facing message.
function friendly(err) {
  const m = String((err && err.message) || '').trim();
  if (m && m.length < 140) return m;
  return 'Could not reach the panel. Check the address and credentials, then try again.';
}
