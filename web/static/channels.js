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
  renderChannelList();
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

// workingSet applies the browse filter pipeline in order: country facet →
// health → text search. Category grouping happens in the renderer. The country
// facet matches ch.group (which holds the country code) against state.country.
function workingSet() {
  let base = state.channels;
  if (state.selectedPlaylist) {
    const pid = state.selectedPlaylist;
    base = base.filter(function (ch) {
      return ch.xtream_playlist_id === pid || isManual(ch);
    });
  }
  if (state.country) {
    const c = state.country.toLowerCase();
    base = base.filter(function (ch) { return (ch.group || '').toLowerCase() === c; });
  }
  if (state.healthOn) base = base.filter(passesHealth);
  if (state.search) {
    const q = state.search.toLowerCase();
    base = base.filter(function (ch) {
      return ch.name.toLowerCase().indexOf(q) !== -1 || ch.group.toLowerCase().indexOf(q) !== -1;
    });
  }
  return base;
}

// populateCountryFilter (re)builds the dropdown from the country codes present in
// the catalog, keeping the current selection. It rebuilds only when the set of
// countries actually changed, so it doesn't stomp the user's open dropdown.
export function populateCountryFilter() {
  const sel = els.countryFilter;
  if (!sel) return;
  const seen = {};
  state.channels.forEach(function (ch) { const g = ch.group; if (g) seen[g] = true; });
  const codes = Object.keys(seen).sort(function (a, b) {
    a = countryLabel(a).toLowerCase(); b = countryLabel(b).toLowerCase();
    return a < b ? -1 : a > b ? 1 : 0;
  });
  const key = codes.join(',');
  if (key === state.countryOptionsKey) return;
  state.countryOptionsKey = key;
  const current = state.country;
  sel.innerHTML = '';
  const all = document.createElement('option');
  all.value = ''; all.textContent = 'All countries';
  sel.appendChild(all);
  codes.forEach(function (code) {
    const opt = document.createElement('option');
    opt.value = code; opt.textContent = countryLabel(code);
    sel.appendChild(opt);
  });
  // Restore selection if it still exists; otherwise fall back to "All".
  if (current && seen[current]) sel.value = current;
  else { state.country = ''; sel.value = ''; }
}

function buildFavSection(searching) {
  let favList = state.channels.filter(isFav);
  if (state.selectedPlaylist) {
    const pid = state.selectedPlaylist;
    favList = favList.filter(function (ch) {
      return ch.xtream_playlist_id === pid || isManual(ch);
    });
  }
  if (state.healthOn) favList = favList.filter(passesHealth);
  const open = state.favOpen || searching;
  const section = document.createElement('div');
  section.className = 'group-section fav-section';

  const head = document.createElement('button');
  head.className = 'group-header' + (open ? ' open' : '');
  const caret = document.createElement('span'); caret.className = 'group-caret'; caret.textContent = '▸';
  const title = document.createElement('span'); title.className = 'group-title'; title.textContent = '★ Favourites';
  const count = document.createElement('span'); count.className = 'group-count'; count.textContent = String(favList.length);
  head.appendChild(caret); head.appendChild(title); head.appendChild(count);
  head.addEventListener('click', function () { state.favOpen = !state.favOpen; renderChannelList(); });
  section.appendChild(head);

  if (open) {
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

// renderChannelList is the single browse renderer: it applies the country +
// health + search pipeline, then lays the results out as category-grouped
// sections with a Favourites section on top. While a search is active the
// category sections auto-expand so matches are visible without a click.
export function renderChannelList() {
  populateCountryFilter();
  if (state.activeTab !== 'channels') {
    // Movies/Sports: hide the list + counts, show the placeholder.
    Array.prototype.slice.call(els.channelList.querySelectorAll('.channel-item, .group-section')).forEach(function (n) { n.remove(); });
    els.listLoading.hidden = true;
    els.emptyState.hidden = true;
    els.channelCount.textContent = '';
    if (els.tabPlaceholder) els.tabPlaceholder.hidden = false;
    return;
  }
  if (els.tabPlaceholder) els.tabPlaceholder.hidden = true;
  const matches = workingSet();
  const searching = !!state.search;
  const keepScroll = els.channelList.scrollTop;
  Array.prototype.slice.call(els.channelList.querySelectorAll('.channel-item, .group-section')).forEach(function (n) { n.remove(); });

  const awaitingFirstList = state.sourceRefreshing && state.channels.length === 0;
  els.listLoading.hidden = !(state.channelsLoading || awaitingFirstList);
  els.emptyState.hidden = state.channelsLoading || awaitingFirstList || matches.length !== 0;
  if (state.channelsLoading || awaitingFirstList) { els.channelCount.textContent = ''; return; }

  const byCat = {};
  matches.forEach(function (ch) {
    const t = ch.type || 'Entertainment';
    (byCat[t] || (byCat[t] = [])).push(ch);
  });

  const frag = document.createDocumentFragment();
  frag.appendChild(buildFavSection(searching));

  // Known categories first (in CATEGORY_ORDER). Then "straggler" groups: Xtream
  // category groups (channels carry a panel index in cat_order) sorted by that
  // index to preserve the panel's order, followed by any remaining groups
  // (cat_order 0 — non-Xtream) alphabetically.
  const known = {};
  CATEGORY_ORDER.forEach(function (c) { known[c] = true; });
  // Group's ordering key = the smallest cat_order among its channels.
  const groupOrder = {};
  Object.keys(byCat).forEach(function (c) {
    let min = 0;
    byCat[c].forEach(function (ch) {
      const o = ch.cat_order || 0;
      if (o > 0 && (min === 0 || o < min)) min = o;
    });
    groupOrder[c] = min; // 0 = no panel ordering signal
  });
  const extra = Object.keys(byCat).filter(function (c) { return !known[c]; })
    .sort(function (a, b) {
      const oa = groupOrder[a], ob = groupOrder[b];
      if (oa !== ob) {
        // Ordered (non-zero) groups come before unordered (zero) ones; among
        // ordered groups, ascending panel index.
        if (oa === 0) return 1;
        if (ob === 0) return -1;
        return oa - ob;
      }
      a = a.toLowerCase(); b = b.toLowerCase();
      return a < b ? -1 : a > b ? 1 : 0;
    });
  const cats = CATEGORY_ORDER.concat(extra);

  cats.forEach(function (cat) {
    const list = byCat[cat];
    if (!list || list.length === 0) return;
    const open = searching || state.expandedCats[cat];
    const section = document.createElement('div');
    section.className = 'group-section';
    const head = document.createElement('button');
    head.className = 'group-header' + (open ? ' open' : '');
    const caret = document.createElement('span'); caret.className = 'group-caret'; caret.textContent = '▸';
    const title = document.createElement('span'); title.className = 'group-title'; title.textContent = cat;
    const count = document.createElement('span'); count.className = 'group-count'; count.textContent = String(list.length);
    head.appendChild(caret); head.appendChild(title); head.appendChild(count);
    head.addEventListener('click', function () { state.expandedCats[cat] = !state.expandedCats[cat]; renderChannelList(); });
    section.appendChild(head);
    if (open) {
      list.slice(0, RENDER_CAP).forEach(function (ch) { section.appendChild(makeChannelButton(ch, browseSelect)); });
      if (list.length > RENDER_CAP) {
        const more = document.createElement('div');
        more.className = 'group-more';
        more.textContent = 'Showing first ' + RENDER_CAP + ' of ' + list.length + ' — search to narrow';
        section.appendChild(more);
      }
    }
    frag.appendChild(section);
  });

  els.channelList.appendChild(frag);
  // Reset scroll when the search term changes so matches aren't scrolled past.
  els.channelList.scrollTop = (state.search === state.lastRenderedSearch) ? keepScroll : 0;
  state.lastRenderedSearch = state.search;

  const shown = matches.length;
  els.channelCount.textContent = searching
    ? shown + ' match' + (shown === 1 ? '' : 'es')
    : shown + (state.healthOn ? ' working' : ' channels');
}

function browseSelect(ch) {
  const idx = targetCellForBrowse();
  assignChannel(idx, ch);
}

function targetCellForBrowse() {
  for (let i = 0; i < cells.length; i++) { if (!cells[i].channel) return i; }
  return state.audioCell >= 0 ? state.audioCell : 0;
}

// beat is retained as a no-op: the live-viewers heartbeat (/api/viewers) was
// removed from the client. Call sites remain harmless.
export function beat() {}

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

if (els.countryFilter) {
  els.countryFilter.addEventListener('change', function () {
    state.country = els.countryFilter.value;
    renderChannelList();
  });
}

// forward ref
function assignChannel(cellIdx, ch) {
  import('./cell.js').then(function(m) { m.assignChannel(cellIdx, ch); });
}
