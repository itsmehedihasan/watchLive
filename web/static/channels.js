import { state, cells, els, RENDER_CAP, CATEGORY_ORDER } from './state.js';
import { logoOrFallback } from './util.js';
import { countryLabel } from './countries.js';
import { openEditChannel } from './modals.js';

export function isDead(ch) { return !!state.deadMarks[ch.name.toLowerCase()]; }
export function isFav(ch) { return !!ch.is_favourite; }
export function isManual(ch) { return ch.id.indexOf('manual:') === 0; }

export function setDead(ch, dead) {
  const key = ch.name.toLowerCase();
  if (dead === !!state.deadMarks[key]) return;
  if (dead) state.deadMarks[key] = Date.now();
  else delete state.deadMarks[key];
  try { localStorage.setItem('livetv_dead', JSON.stringify(state.deadMarks)); } catch (e) { /* quota */ }
  Array.prototype.slice.call(document.querySelectorAll('.channel-item')).forEach(function (btn) {
    if (btn.dataset.id === ch.id) btn.classList.toggle('dead', dead);
  });
}

export function setFav(ch, on) {
  if (!!ch.is_favourite === on) return;
  ch.is_favourite = on;
  fetch('/api/favourite', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id: ch.id, on: on }),
  })
    .then(function (r) { if (!r.ok) throw new Error('favourite failed: ' + r.status); })
    .catch(function () { ch.is_favourite = !on; onFavChanged(); });
}

export function onFavChanged() {
  renderCategorySidebar();
  const favById = {};
  state.channels.forEach(function (ch) { if (ch.is_favourite) favById[ch.id] = true; });
  Array.prototype.slice.call(document.querySelectorAll('.pin-btn')).forEach(function (pin) {
    const on = !!favById[pin.dataset.favid];
    pin.classList.toggle('faved', on);
    pin.title = on ? 'Remove from Favourites' : 'Add to Favourites';
  });
}

export function passesHealth(ch) {
  if (!state.healthOn) return true;
  return state.health[ch.id] !== false;
}

export function activeIds() {
  const ids = {};
  cells.forEach(function (c) { if (c.channel) ids[c.channel.id] = true; });
  return ids;
}

export function isActive(ch) {
  for (let i = 0; i < cells.length; i++) {
    if (cells[i].channel && cells[i].channel.id === ch.id) return true;
  }
  return false;
}

export function makeChannelButton(ch, onClick) {
  const btn = document.createElement('button');
  btn.className = 'channel-item' + (isActive(ch) ? ' selected' : '') + (isDead(ch) ? ' dead' : '');
  btn.dataset.id = ch.id;
  btn.appendChild(logoOrFallback(ch, 'channel-logo', 'channel-logo-fallback'));
  const name = document.createElement('span');
  name.className = 'channel-name';
  name.textContent = ch.name;
  btn.appendChild(name);
  const pin = document.createElement('span');
  pin.className = 'pin-btn' + (isFav(ch) ? ' faved' : '');
  pin.dataset.favid = ch.id;
  pin.setAttribute('role', 'button');
  pin.title = isFav(ch) ? 'Remove from Favourites' : 'Add to Favourites';
  pin.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round">' +
    '<path d="M9 4h6l-1 5 3 3v2h-4v5l-1 1-1-1v-5H6v-2l3-3z"/></svg>';
  pin.addEventListener('click', function (e) {
    e.stopPropagation();
    setFav(ch, !isFav(ch));
    onFavChanged();
  });
  btn.appendChild(pin);
  if (isManual(ch)) {
    const edit = document.createElement('span');
    edit.className = 'edit-btn';
    edit.setAttribute('role', 'button');
    edit.title = 'Update stream link';
    edit.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
      '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4z"/></svg>';
    edit.addEventListener('click', function (e) {
      e.stopPropagation();
      openEditChannel(ch);
    });
    btn.appendChild(edit);
  }
  btn.addEventListener('click', function () { onClick(ch); });
  return btn;
}

export function refreshHighlights() {
  const ids = activeIds();
  Array.prototype.slice.call(document.querySelectorAll('.channel-item')).forEach(function (btn) {
    btn.classList.toggle('selected', ids[btn.dataset.id] === true);
  });
}

function filteredChannels() {
  let base = state.channels;
  if (state.search) {
    const q = state.search.toLowerCase();
    base = state.channels.filter(function (ch) {
      return ch.name.toLowerCase().indexOf(q) !== -1 || ch.group.toLowerCase().indexOf(q) !== -1;
    });
  }
  if (state.healthOn) base = base.filter(passesHealth);
  return base;
}

export function renderChannelList() {
  const matches = filteredChannels();
  const keepScroll = els.channelList.scrollTop;
  Array.prototype.slice.call(els.channelList.querySelectorAll('.channel-item, .group-section')).forEach(function (n) { n.remove(); });

  const awaitingFirstList = state.sourceRefreshing && state.channels.length === 0;
  els.listLoading.hidden = !(state.channelsLoading || awaitingFirstList);
  els.emptyState.hidden = state.channelsLoading || awaitingFirstList || matches.length !== 0;

  const frag = document.createDocumentFragment();

  if (state.search) {
    matches.slice(0, RENDER_CAP).forEach(function (ch) { frag.appendChild(makeChannelButton(ch, browseSelect)); });
    els.channelList.appendChild(frag);
    els.channelList.scrollTop = (state.search === state.lastRenderedSearch) ? keepScroll : 0;
    state.lastRenderedSearch = state.search;
    els.channelCount.textContent = state.channelsLoading ? ''
      : matches.length > RENDER_CAP
        ? 'Showing first ' + RENDER_CAP + ' of ' + matches.length + ' matches — search to narrow'
        : matches.length + ' match' + (matches.length === 1 ? '' : 'es');
    return;
  }

  const byGroup = {};
  const groupNames = [];
  matches.forEach(function (ch) {
    const g = ch.group || 'Other';
    if (!byGroup[g]) { byGroup[g] = []; groupNames.push(g); }
    byGroup[g].push(ch);
  });
  groupNames.sort(function (a, b) { a = a.toLowerCase(); b = b.toLowerCase(); return a < b ? -1 : a > b ? 1 : 0; });

  groupNames.forEach(function (g) {
    const section = document.createElement('div');
    section.className = 'group-section';
    const head = document.createElement('button');
    head.className = 'group-header' + (state.expandedGroups[g] ? ' open' : '');
    const caret = document.createElement('span'); caret.className = 'group-caret'; caret.textContent = '▸';
    const title = document.createElement('span'); title.className = 'group-title'; title.textContent = countryLabel(g);
    const count = document.createElement('span'); count.className = 'group-count'; count.textContent = String(byGroup[g].length);
    head.appendChild(caret); head.appendChild(title); head.appendChild(count);
    head.addEventListener('click', function () { state.expandedGroups[g] = !state.expandedGroups[g]; renderChannelList(); });
    section.appendChild(head);
    if (state.expandedGroups[g]) byGroup[g].forEach(function (ch) { section.appendChild(makeChannelButton(ch, browseSelect)); });
    frag.appendChild(section);
  });

  els.channelList.appendChild(frag);
  els.channelList.scrollTop = keepScroll;
  state.lastRenderedSearch = '';
  const shown = state.healthOn ? matches.length : state.channels.length;
  els.channelCount.textContent = state.channelsLoading ? ''
    : shown + (state.healthOn ? ' working' : ' channels') + ' · ' + groupNames.length + ' countries';
}

function buildFavSection() {
  const favList = state.channels.filter(isFav);
  const section = document.createElement('div');
  section.className = 'group-section fav-section';

  const head = document.createElement('button');
  head.className = 'group-header' + (state.favOpen ? ' open' : '');
  const caret = document.createElement('span'); caret.className = 'group-caret'; caret.textContent = '▸';
  const title = document.createElement('span'); title.className = 'group-title'; title.textContent = '★ Favourites';
  const count = document.createElement('span'); count.className = 'group-count'; count.textContent = String(favList.length);
  head.appendChild(caret); head.appendChild(title); head.appendChild(count);
  head.addEventListener('click', function () { state.favOpen = !state.favOpen; renderCategorySidebar(); });
  section.appendChild(head);

  if (state.favOpen) {
    if (favList.length === 0) {
      const hint = document.createElement('div');
      hint.className = 'group-more';
      hint.textContent = 'No favourites yet — tap the pin on any channel to add it here.';
      section.appendChild(hint);
    } else {
      favList.slice(0, RENDER_CAP).forEach(function (ch) { section.appendChild(makeChannelButton(ch, browseSelect)); });
    }
  }
  return section;
}

export function renderCategorySidebar() {
  const keepScroll = els.categoryList.scrollTop;
  Array.prototype.slice.call(els.categoryList.querySelectorAll('.channel-item, .group-section')).forEach(function (n) { n.remove(); });
  const awaitingFirstList = state.sourceRefreshing && state.channels.length === 0;
  els.catLoading.hidden = !(state.channelsLoading || awaitingFirstList);
  if (state.channelsLoading || awaitingFirstList) return;

  const byCat = {};
  state.channels.forEach(function (ch) {
    if (!passesHealth(ch)) return;
    const t = ch.type || 'Entertainment';
    (byCat[t] || (byCat[t] = [])).push(ch);
  });

  const frag = document.createDocumentFragment();
  frag.appendChild(buildFavSection());
  CATEGORY_ORDER.forEach(function (cat) {
    const list = byCat[cat];
    if (!list || list.length === 0) return;
    const section = document.createElement('div');
    section.className = 'group-section';
    const head = document.createElement('button');
    head.className = 'group-header' + (state.expandedCats[cat] ? ' open' : '');
    const caret = document.createElement('span'); caret.className = 'group-caret'; caret.textContent = '▸';
    const title = document.createElement('span'); title.className = 'group-title'; title.textContent = cat;
    const count = document.createElement('span'); count.className = 'group-count'; count.textContent = String(list.length);
    head.appendChild(caret); head.appendChild(title); head.appendChild(count);
    head.addEventListener('click', function () { state.expandedCats[cat] = !state.expandedCats[cat]; renderCategorySidebar(); });
    section.appendChild(head);
    if (state.expandedCats[cat]) {
      list.slice(0, RENDER_CAP).forEach(function (ch) { section.appendChild(makeChannelButton(ch, browseSelect)); });
      if (list.length > RENDER_CAP) {
        const more = document.createElement('div');
        more.className = 'group-more';
        more.textContent = 'Showing first ' + RENDER_CAP + ' of ' + list.length + ' — search on the right to narrow';
        section.appendChild(more);
      }
    }
    frag.appendChild(section);
  });

  els.categoryList.appendChild(frag);
  els.categoryList.scrollTop = keepScroll;
}

function browseSelect(ch) {
  const idx = targetCellForBrowse();
  assignChannel(idx, ch);
}

function targetCellForBrowse() {
  for (let i = 0; i < cells.length; i++) { if (!cells[i].channel) return i; }
  return state.audioCell >= 0 ? state.audioCell : 0;
}

export function beat() {
  fetch('/api/viewers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sessionId: state.sessionId, channelId: watchedChannelId() }),
  })
    .then(function (r) { return r.json(); })
    .then(function (d) {
      if (d.top && d.top.length > 0) {
        const ids = d.top.map(function (x) { return x.id; });
        if (ids.join(',') !== state.topChannelIds.join(',')) state.topChannelIds = ids;
      }
    })
    .catch(function () {});
}

function watchedChannelId() {
  if (state.audioCell >= 0 && cells[state.audioCell] && cells[state.audioCell].channel) return cells[state.audioCell].channel.id;
  for (let i = 0; i < cells.length; i++) { if (cells[i].channel) return cells[i].channel.id; }
  return null;
}

function setSearch(value) {
  state.search = value;
  els.search.value = value;
  els.searchClear.hidden = !value;
  renderChannelList();
}

els.search.addEventListener('input', function () {
  if (state.searchDebounce) clearTimeout(state.searchDebounce);
  state.searchDebounce = setTimeout(function () { setSearch(els.search.value); }, 150);
});
els.searchClear.addEventListener('click', function () { setSearch(''); });
els.emptyClear.addEventListener('click', function () { setSearch(''); });

// forward ref
function assignChannel(cellIdx, ch) {
  import('./cell.js').then(function(m) { m.assignChannel(cellIdx, ch); });
}
