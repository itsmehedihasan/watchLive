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

  // Name — inline editable, click to edit.
  const nameSpan = document.createElement('span');
  nameSpan.className = 'playlist-row-name';
  nameSpan.textContent = p.name;
  nameSpan.tabIndex = 0;
  nameSpan.title = 'Click to rename';
  nameSpan.addEventListener('click', function () {
    startEdit(nameSpan, p.name, 'playlist-row-name-input', function (next) {
      patchPlaylist(p.id, { name: next }, function () { p.name = next; nameSpan.textContent = next; });
    });
  });

  // Server — inline editable, click to edit.
  const serverSpan = document.createElement('span');
  serverSpan.className = 'playlist-row-server';
  serverSpan.textContent = p.server;
  serverSpan.tabIndex = 0;
  serverSpan.title = 'Click to edit server';
  serverSpan.addEventListener('click', function () {
    startEdit(serverSpan, p.server, 'playlist-row-server-input', function (next) {
      patchPlaylist(p.id, { server: next }, function () { p.server = next; serverSpan.textContent = next; });
    });
  });

  // Stream type — inline select.
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
  removeBtn.className = 'add-btn playlist-row-remove';
  removeBtn.title = 'Delete playlist';
  removeBtn.setAttribute('aria-label', 'Delete playlist');
  removeBtn.innerHTML =
    '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" ' +
    'stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<polyline points="3 6 5 6 21 6"></polyline>' +
    '<path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"></path>' +
    '<path d="M10 11v6M14 11v6"></path>' +
    '<path d="M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"></path>' +
    '</svg>';
  removeBtn.addEventListener('click', function () {
    if (!confirm('Delete playlist "' + p.name + '" and all its channels?')) return;
    removePlaylist(p.id);
  });

  // Row 1: playlist name, full width.
  const line1 = document.createElement('div');
  line1.className = 'playlist-row-line1';
  line1.appendChild(nameSpan);

  // Row 2: server (70%) · stream type (20%) · delete (10%).
  const line2 = document.createElement('div');
  line2.className = 'playlist-row-line2';
  line2.appendChild(serverSpan);
  line2.appendChild(typeSelect);
  line2.appendChild(removeBtn);

  row.appendChild(line1);
  row.appendChild(line2);
  return row;
}

// startEdit swaps a display span for a text input in place; Enter/blur saves
// via onSave (unless blank or unchanged, which reverts with no request),
// Escape cancels. Shared by the name and server fields.
function startEdit(span, current, inputClass, onSave) {
  const parent = span.parentNode;
  const input = document.createElement('input');
  input.type = 'text';
  input.className = inputClass;
  input.value = current;
  parent.replaceChild(input, span);
  input.focus();
  input.select();

  let done = false;
  function finish(save) {
    if (done) return;
    done = true;
    const next = input.value.trim();
    if (save && next && next !== current) onSave(next);
    parent.replaceChild(span, input);
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
