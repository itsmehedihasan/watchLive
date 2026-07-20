import { els, state } from './state.js';

// The Channel List tab of the add modal: pick a saved Xtream playlist from the
// dropdown, then browse a read-only list of that playlist's channels with every
// stream address. Clicking an address copies it to the clipboard. Data comes
// from the already-loaded state.channels — no dedicated endpoint.

let dropdownBound = false;

// initChannelListTab (re)builds the playlist dropdown and renders the channel
// list; called each time the tab is shown so it reflects the latest channels
// and saved playlists without a full page reload.
export function initChannelListTab() {
  fetch('/api/xtream/playlists')
    .then(function (r) { return r.ok ? r.json() : []; })
    .then(function (list) { populateDropdown(Array.isArray(list) ? list : []); })
    .catch(function () { populateDropdown([]); })
    .then(function () { render(); });

  if (!dropdownBound) {
    dropdownBound = true;
    els.channelListFilter.addEventListener('change', render);
  }
}

// populateDropdown rebuilds the options as a "Select Playlist" placeholder + one
// per saved playlist, preserving the current selection when it still exists.
function populateDropdown(list) {
  const sel = els.channelListFilter;
  const current = sel.value;
  sel.innerHTML = '';
  const placeholder = document.createElement('option');
  placeholder.value = ''; placeholder.textContent = 'Select Playlist';
  placeholder.disabled = true;
  sel.appendChild(placeholder);
  const ids = {};
  list.forEach(function (p) {
    ids[p.id] = true;
    const opt = document.createElement('option');
    opt.value = p.id; opt.textContent = p.name;
    sel.appendChild(opt);
  });
  // Keep the prior selection if it still exists; otherwise fall back to the
  // (disabled) placeholder so nothing is shown until the user picks a playlist.
  sel.value = current && ids[current] ? current : '';
}

// render fills the list with the channels of the selected playlist. With no
// playlist chosen (placeholder), the list stays empty and no hint is shown.
function render() {
  const pid = els.channelListFilter.value;
  els.channelListItems.innerHTML = '';

  if (!pid) {                       // placeholder selected — prompt, no list
    els.channelListEmpty.hidden = true;
    return;
  }

  const channels = state.channels.filter(function (ch) {
    return ch.xtream_playlist_id === pid;
  });
  els.channelListEmpty.hidden = channels.length > 0;
  channels.forEach(function (ch) {
    els.channelListItems.appendChild(buildRow(ch));
  });
}

function buildRow(ch) {
  const row = document.createElement('div');
  row.className = 'channel-addr-row';

  const name = document.createElement('div');
  name.className = 'channel-addr-name';
  name.textContent = ch.name;
  row.appendChild(name);

  (ch.servers || []).forEach(function (s) {
    if (!s || !s.url) return;
    row.appendChild(buildAddr(s.url));
  });
  return row;
}

// buildAddr renders one read-only stream URL; clicking copies it to the
// clipboard and briefly shows "Copied!".
function buildAddr(url) {
  const el = document.createElement('div');
  el.className = 'channel-addr-url';
  el.textContent = url;
  el.title = 'Click to copy';

  let reverting = null;
  el.addEventListener('click', function () {
    if (!navigator.clipboard) return;             // no-op where clipboard is unavailable
    navigator.clipboard.writeText(url).then(function () {
      el.textContent = 'Copied!';
      el.classList.add('copied');
      if (reverting) clearTimeout(reverting);
      reverting = setTimeout(function () {
        el.textContent = url;
        el.classList.remove('copied');
      }, 1200);
    }).catch(function () { /* leave the address unchanged on failure */ });
  });
  return el;
}
