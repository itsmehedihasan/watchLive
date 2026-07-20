import { els } from './state.js';
import { loadChannels } from './init.js';

// The Playlist tab of the add modal: lists every saved Xtream playlist for
// management (rename, change stream type, delete). Adding a playlist is still
// done from the Xtream Codes tab — this tab never creates one.

// initPlaylistTab loads and renders the saved-playlist list; called each time
// the tab is shown so a playlist added/removed elsewhere in this session is
// reflected without a full page reload.
export function initPlaylistTab() {
  fetch('/api/xtream/playlists')
    .then(function (r) { return r.ok ? r.json() : []; })
    .then(function (list) { render(Array.isArray(list) ? list : []); })
    .catch(function () { render([]); });
}

function render(list) {
  els.playlistList.innerHTML = '';
  els.playlistEmpty.hidden = list.length > 0;
  list.forEach(function (p) { els.playlistList.appendChild(buildRow(p)); });
}

function buildRow(p) {
  const row = document.createElement('div');
  row.className = 'playlist-row';

  const nameSpan = document.createElement('span');
  nameSpan.className = 'playlist-row-name';
  nameSpan.textContent = p.name;
  nameSpan.tabIndex = 0;
  nameSpan.title = 'Click to rename';
  nameSpan.addEventListener('click', function () { startRename(row, nameSpan, p); });

  const typeSelect = document.createElement('select');
  typeSelect.className = 'country-select playlist-row-type';
  ['ts', 'm3u8'].forEach(function (v) {
    const opt = document.createElement('option');
    opt.value = v;
    opt.textContent = v === 'ts' ? 'MPEG-TS (ts)' : 'HLS (m3u8)';
    if (v === (p.stream_type || 'ts')) opt.selected = true;
    typeSelect.appendChild(opt);
  });
  typeSelect.addEventListener('change', function () {
    patchPlaylist(p.id, { stream_type: typeSelect.value });
  });

  const removeBtn = document.createElement('button');
  removeBtn.type = 'button';
  removeBtn.className = 'add-btn add-btn-ghost playlist-row-remove';
  removeBtn.textContent = 'Remove';
  removeBtn.addEventListener('click', function () {
    if (!confirm('Remove playlist "' + p.name + '"?')) return;
    removePlaylist(p.id);
  });

  row.appendChild(nameSpan);
  row.appendChild(typeSelect);
  row.appendChild(removeBtn);
  return row;
}

// startRename swaps the name span for a text input in place; Enter/blur
// saves (unless blank, which reverts with no request), Escape cancels.
function startRename(row, nameSpan, p) {
  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'playlist-row-name-input';
  input.value = p.name;
  row.replaceChild(input, nameSpan);
  input.focus();
  input.select();

  let done = false;
  function finish(save) {
    if (done) return;
    done = true;
    const next = input.value.trim();
    if (save && next && next !== p.name) {
      patchPlaylist(p.id, { name: next }, function () { p.name = next; nameSpan.textContent = next; });
    }
    row.replaceChild(nameSpan, input);
  }
  input.addEventListener('blur', function () { finish(true); });
  input.addEventListener('keydown', function (e) {
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
}

function patchPlaylist(id, body, onSuccess) {
  fetch('/api/xtream/playlists/' + encodeURIComponent(id), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
      return r.json();
    })
    .then(function () { if (onSuccess) onSuccess(); })
    .catch(function () { initPlaylistTab(); }); // reload to discard the failed edit
}

function removePlaylist(id) {
  fetch('/api/xtream/playlists/' + encodeURIComponent(id), { method: 'DELETE' })
    .then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t); });
      return r.json();
    })
    .then(function () {
      loadChannels();
      initPlaylistTab();
    })
    .catch(function () { initPlaylistTab(); });
}
