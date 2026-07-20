import { state, els } from './state.js';
import { loadKeys, loadChannels } from './init.js';
import { initXtreamTab, resetXtreamTab } from './xtream.js';
import { initPlaylistTab } from './playlist-tab.js';
import { initChannelListTab } from './channel-list-tab.js';

function maybeHideScrim() {
  const anyOpen = els.sidebar.classList.contains('open') || !els.picker.hidden ||
                !els.addChannel.hidden ||
                !els.importModal.hidden || !els.importConflict.hidden;
  els.scrim.hidden = !anyOpen;
}

function closeDrawers() {
  els.sidebar.classList.remove('open');
  // Cancel a queued search render so it can't fire against the closed drawer.
  if (state.searchDebounce) { clearTimeout(state.searchDebounce); state.searchDebounce = null; }
  maybeHideScrim();
}

// openDrawer opens the single browse sidebar. The `which` argument is retained
// for call-site compatibility but the left drawer no longer exists.
function openDrawer() {
  els.sidebar.hidden = false;
  els.scrim.hidden = false;
  requestAnimationFrame(function () { els.sidebar.classList.add('open'); });
}

export { openDrawer, closeDrawers, maybeHideScrim };

function setLicenseOpen(on) {
  els.addChannelLicToggle.classList.toggle('open', on);
  els.addChannelLicToggle.setAttribute('aria-expanded', on ? 'true' : 'false');
  els.addChannelLicWrap.classList.toggle('show', on);
  els.addChannelUaWrap.classList.toggle('show', on);
  els.addChannelLicWrap2.classList.toggle('show', on);
}

// setAddTab switches between the Manual, Xtream Codes, Playlist, and Channel
// List tabs of the add modal.
function setAddTab(tab) {
  els.addTabManual.classList.toggle('active', tab === 'manual');
  els.addTabXtream.classList.toggle('active', tab === 'xtream');
  els.addTabPlaylist.classList.toggle('active', tab === 'playlist');
  els.addTabChannels.classList.toggle('active', tab === 'channels');
  els.addChannelForm.hidden = tab !== 'manual';
  els.xtreamPanel.hidden = tab !== 'xtream';
  els.playlistPanel.hidden = tab !== 'playlist';
  els.channelListPanel.hidden = tab !== 'channels';
  els.addChannelTitle.textContent =
    tab === 'xtream' ? 'Add from Xtream Codes' :
    tab === 'playlist' ? 'Manage playlists' :
    tab === 'channels' ? 'Channel list' : 'Add a channel';
  if (tab === 'xtream') initXtreamTab();
  if (tab === 'playlist') initPlaylistTab();
  if (tab === 'channels') initChannelListTab();
}

els.addTabManual.addEventListener('click', function () { setAddTab('manual'); });
els.addTabXtream.addEventListener('click', function () { setAddTab('xtream'); });
els.addTabPlaylist.addEventListener('click', function () { setAddTab('playlist'); });
els.addTabChannels.addEventListener('click', function () { setAddTab('channels'); });

export function openChannelModal(mode, ch) {
  closeDrawers();
  state.channelModalMode = mode;
  state.channelModalId = (mode === 'edit' && ch) ? ch.id : null;
  els.addChannelError.hidden = true;
  els.addChannelForm.reset();
  setLicenseOpen(false);
  const editing = mode === 'edit' && ch;
  // Tabs only make sense for adding; editing an existing channel is Manual-only.
  els.addTabs.hidden = !!editing;
  setAddTab('manual');
  resetXtreamTab();
  els.addChannelTitle.textContent = editing ? 'Update stream link' : 'Add a channel';
  els.addChannelSave.textContent = editing ? 'Update' : 'Save';
  els.addChannelSave.disabled = false;
  if (editing) {
    els.addChannelName.value = ch.name || '';
    els.addChannelUrl.value = (ch.servers && ch.servers[0] && ch.servers[0].url) || '';
    els.addChannelReferer.value = ch.http_referer || '';
    els.addChannelUserAgent.value = ch.http_user_agent || '';
    // Reveal the advanced area when the channel already carries headers, so the
    // user sees what's there instead of silently editing hidden fields.
    if (ch.http_referer || ch.http_user_agent) setLicenseOpen(true);
  }
  els.addChannel.hidden = false;
  els.scrim.hidden = false;
  setTimeout(function () {
    if (editing) { els.addChannelUrl.focus(); els.addChannelUrl.select(); }
    else els.addChannelName.focus();
  }, 0);
}

export function openAddChannel() { openChannelModal('add', null); }
export function openEditChannel(ch) { openChannelModal('edit', ch); }

export function closeAddChannel() {
  els.addChannel.hidden = true;
  els.addChannelSave.disabled = false;
  els.addChannelSave.textContent = state.channelModalMode === 'edit' ? 'Update' : 'Save';
  state.channelModalId = null;
  maybeHideScrim();
}

export function closeImport() {
  els.importModal.hidden = true;
  els.importList.innerHTML = '';
  els.importError.hidden = true;
  els.importSave.disabled = false;
  els.importSave.textContent = 'Save to library';
  maybeHideScrim();
}

function updateImportCount() {
  const n = els.importList.querySelectorAll('.import-row').length;
  els.importCount.textContent = n + ' channel' + (n === 1 ? '' : 's');
}

function addImportRow(entry) {
  const row = document.createElement('div');
  row.className = 'import-row';
  if (entry.clear_keys && Object.keys(entry.clear_keys).length) row._clearKeys = entry.clear_keys;
  const name = document.createElement('input');
  name.className = 'import-name'; name.type = 'text'; name.placeholder = 'Name';
  name.value = entry.name || '';
  const url = document.createElement('input');
  url.className = 'import-url'; url.type = 'text'; url.placeholder = 'https://…';
  url.value = entry.url || '';
  const rm = document.createElement('button');
  rm.className = 'import-row-remove'; rm.type = 'button'; rm.title = 'Remove this channel'; rm.textContent = '✕';
  rm.addEventListener('click', function () { row.remove(); updateImportCount(); });
  row.appendChild(name); row.appendChild(url); row.appendChild(rm);
  els.importList.appendChild(row);
}

export function openImportReview(entries) {
  closeDrawers();
  els.importList.innerHTML = '';
  els.importError.hidden = true;
  entries.forEach(addImportRow);
  updateImportCount();
  els.importModal.hidden = false;
  els.scrim.hidden = false;
}

function btnLoading(btn) {
  const text = btn.textContent;
  btn.disabled = true;
  btn.innerHTML = '<span class="btn-spinner"></span>';
  return function () { btn.disabled = false; btn.textContent = text; };
}

function commitImport(entries, restore) {
  fetch('/api/import/save', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ entries: entries }),
  })
    .then(function (r) { if (!r.ok) throw new Error('save failed: ' + r.status); return r.json(); })
    .then(function () { closeImportConflict(); closeImport(); loadKeys(); loadChannels(); })
    .catch(function () {
      restore();
      els.importError.textContent = 'Could not save the channels. Please try again.';
      els.importError.hidden = false;
    });
}

function collectImportRows() {
  const entries = [];
  Array.prototype.slice.call(els.importList.querySelectorAll('.import-row')).forEach(function (row) {
    const name = row.querySelector('.import-name').value.trim();
    const url = row.querySelector('.import-url').value.trim();
    if (name && /^https?:\/\//i.test(url)) {
      const e = { name: name, url: url };
      if (row._clearKeys) e.clear_keys = row._clearKeys;
      entries.push(e);
    }
  });
  return entries;
}

export function openImportConflict(duplicates, fresh) {
  state.pendingImportNew = fresh;
  els.importModal.hidden = true;
  els.importConflictList.innerHTML = '';
  duplicates.forEach(function (d) {
    const row = document.createElement('div');
    row.className = 'conflict-row';
    const imp = document.createElement('div');
    imp.className = 'conflict-imported';
    const nm = document.createElement('div'); nm.className = 'c-name'; nm.textContent = d.imported.name;
    const u = document.createElement('div'); u.className = 'c-url'; u.textContent = d.imported.url; u.title = d.imported.url;
    imp.appendChild(nm); imp.appendChild(u);
    const arrow = document.createElement('div'); arrow.className = 'conflict-arrow'; arrow.textContent = '→';
    const ex = document.createElement('div');
    ex.className = 'conflict-existing';
    ex.innerHTML = 'already in library as <span class="c-tag"></span>';
    ex.querySelector('.c-tag').textContent = d.existingName || d.existingId;
    row.appendChild(imp); row.appendChild(arrow); row.appendChild(ex);
    els.importConflictList.appendChild(row);
  });
  els.importConflictSummary.textContent = duplicates.length + ' duplicate' + (duplicates.length === 1 ? '' : 's') +
    ' · ' + fresh.length + ' new to add';
  els.importConflictAdd.disabled = fresh.length === 0;
  els.importConflictAdd.textContent = fresh.length === 0 ? 'No new channels' : ('Add ' + fresh.length + ' new channel' + (fresh.length === 1 ? '' : 's'));
  els.importConflict.hidden = false;
  els.scrim.hidden = false;
}

export function closeImportConflict() {
  els.importConflict.hidden = true;
  els.importConflictAdd.disabled = false;
  maybeHideScrim();
}

els.addChannelBtn.addEventListener('click', openAddChannel);
els.addChannelClose.addEventListener('click', closeAddChannel);
els.addChannelCancel.addEventListener('click', closeAddChannel);
els.xtreamCancel.addEventListener('click', closeAddChannel);
els.addChannelLicToggle.addEventListener('click', function () {
  setLicenseOpen(!els.addChannelLicWrap.classList.contains('show'));
});
els.addChannelForm.addEventListener('submit', function (e) {
  e.preventDefault();
  const name = els.addChannelName.value.trim();
  const url = els.addChannelUrl.value.trim();
  const license = els.addChannelLicense.value.trim();
  const referer = els.addChannelReferer.value.trim();
  const userAgent = els.addChannelUserAgent.value.trim();
  if (!name || !/^https?:\/\//i.test(url)) {
    els.addChannelError.textContent = 'Enter a name and an http(s) stream link.';
    els.addChannelError.hidden = false;
    return;
  }
  const editing = state.channelModalMode === 'edit';
  const endpoint = editing ? '/api/channels/update' : '/api/channels/add';
  const payload = editing
    ? { id: state.channelModalId, name: name, url: url, license: license, referer: referer, userAgent: userAgent }
    : { name: name, url: url, license: license, referer: referer, userAgent: userAgent };
  els.addChannelSave.disabled = true;
  els.addChannelSave.textContent = editing ? 'Updating…' : 'Saving…';
  fetch(endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
    .then(function (r) { if (!r.ok) throw new Error('save failed: ' + r.status); return r.json(); })
    .then(function () { closeAddChannel(); loadKeys(); loadChannels(); })
    .catch(function () {
      els.addChannelError.textContent = editing
        ? 'Could not update the channel. Check the link and try again.'
        : 'Could not add the channel. Check the link and try again.';
      els.addChannelError.hidden = false;
      els.addChannelSave.disabled = false;
      els.addChannelSave.textContent = editing ? 'Update' : 'Save';
    });
});

els.importBtn.addEventListener('click', function () { els.importFile.click(); });
els.importFile.addEventListener('change', function () {
  const file = els.importFile.files && els.importFile.files[0];
  if (!file) return;
  const reader = new FileReader();
  reader.onload = function () {
    fetch('/api/import/parse', { method: 'POST', body: reader.result })
      .then(function (r) {
        if (!r.ok) return r.text().then(function (t) { throw new Error(t || ('parse failed: ' + r.status)); });
        return r.json();
      })
      .then(function (d) { openImportReview((d && d.entries) || []); })
      .catch(function (err) {
        openImportReview([]);
        els.importError.textContent = String(err.message || 'Could not read that playlist.');
        els.importError.hidden = false;
      });
  };
  reader.readAsText(file);
  els.importFile.value = '';
});
els.importClose.addEventListener('click', closeImport);
els.importCancel.addEventListener('click', closeImport);

els.importSave.addEventListener('click', function () {
  const entries = collectImportRows();
  els.importError.hidden = true;
  if (entries.length === 0) {
    els.importError.textContent = 'Nothing to save — each channel needs a name and an http(s) link.';
    els.importError.hidden = false;
    return;
  }
  const restore = btnLoading(els.importSave);
  fetch('/api/import/check', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ entries: entries }),
  })
    .then(function (r) { if (!r.ok) throw new Error('check failed: ' + r.status); return r.json(); })
    .then(function (d) {
      const dups = (d && d.duplicates) || [];
      const fresh = (d && d.new) || [];
      if (dups.length === 0) {
        commitImport(fresh, restore);
        return;
      }
      restore();
      openImportConflict(dups, fresh);
    })
    .catch(function () {
      restore();
      els.importError.textContent = 'Could not check the playlist. Please try again.';
      els.importError.hidden = false;
    });
});

els.importConflictClose.addEventListener('click', closeImport);
els.importConflictCancel.addEventListener('click', function () {
  closeImportConflict();
  els.importModal.hidden = false;
  els.scrim.hidden = false;
});
els.importConflictAdd.addEventListener('click', function () {
  if (state.pendingImportNew.length === 0) return;
  const restore = btnLoading(els.importConflictAdd);
  commitImport(state.pendingImportNew, restore);
});

els.rightDrawerToggle.addEventListener('click', function () { openDrawer(); });
els.scrim.addEventListener('click', function () { closeDrawers(); closePicker(); closeAddChannel(); closeImport(); closeImportConflict(); });
Array.prototype.slice.call(document.querySelectorAll('[data-close-drawer]')).forEach(function (b) {
  b.addEventListener('click', closeDrawers);
});

window.addEventListener('keydown', function (e) {
  const inInput = document.activeElement && document.activeElement.tagName === 'INPUT';
  if (e.key === 'Escape') { closeDrawers(); closePicker(); closeAddChannel(); closeImport(); closeImportConflict(); }
  if (e.key === '/' && !inInput) { e.preventDefault(); openDrawer(); setTimeout(function () { els.search.focus(); }, 0); }
});

// forward refs
function closePicker() { import('./picker.js').then(function(m) { m.closePicker(); }); }
