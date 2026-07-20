import { state, cells, els, RENDER_CAP } from './state.js';
import { makeChannelButton } from './channels.js';
import { assignChannel } from './cell.js';

export function openPicker(cellIdx) {
  state.pickerTarget = cellIdx;
  els.pickerTitle.textContent = (cellIdx === 0 && cells.length === 1)
    ? 'Pick your first channel' : ('Pick a channel for Cell ' + (cellIdx + 1));
  els.picker.hidden = false;
  els.scrim.hidden = false;
  renderPicker();
  setTimeout(function () { els.pickerSearch.focus(); }, 0);
}

export function closePicker() {
  els.picker.hidden = true;
  state.pickerTarget = -1;
  // Cancel a queued debounced render and reset the query so it can't fire
  // against the hidden modal, and the next open starts clean.
  if (state.pickerDebounce) { clearTimeout(state.pickerDebounce); state.pickerDebounce = null; }
  els.pickerSearch.value = '';
  els.pickerSearchClear.hidden = true;
  state.pickerSearch = '';
  maybeHideScrim();
}

function pickerChannels() {
  let base = state.channels;
  if (state.pickerSearch) {
    const q = state.pickerSearch.toLowerCase();
    base = base.filter(function (ch) {
      return ch.name.toLowerCase().indexOf(q) !== -1 || ch.group.toLowerCase().indexOf(q) !== -1;
    });
  }
  return base;
}

export function renderPicker() {
  const matches = pickerChannels();
  els.pickerList.innerHTML = '';
  const frag = document.createDocumentFragment();
  matches.slice(0, RENDER_CAP).forEach(function (ch) {
    frag.appendChild(makeChannelButton(ch, function () {
      const target = state.pickerTarget;
      closePicker();
      assignChannel(target, ch);
    }));
  });
  els.pickerList.appendChild(frag);
  els.pickerList.scrollTop = 0;
  const n = matches.length;
  els.pickerCount.textContent = state.channelsLoading ? 'Loading…'
    : n === 0 ? (state.sourceRefreshing ? 'Fetching channels…' : 'No channels found')
    : n > RENDER_CAP ? ('Showing first ' + RENDER_CAP + ' of ' + n + ' — search to narrow')
    : (n + ' live channel' + (n === 1 ? '' : 's'));
}

function maybeHideScrim() {
  const anyOpen = els.sidebar.classList.contains('open') || !els.picker.hidden ||
                !els.addChannel.hidden ||
                !els.importModal.hidden || !els.importConflict.hidden;
  els.scrim.hidden = !anyOpen;
}

els.pickerClose.addEventListener('click', closePicker);
els.pickerSearch.addEventListener('input', function () {
  els.pickerSearchClear.hidden = !els.pickerSearch.value;
  if (state.pickerDebounce) clearTimeout(state.pickerDebounce);
  state.pickerDebounce = setTimeout(function () { state.pickerSearch = els.pickerSearch.value; renderPicker(); }, 150);
});
els.pickerSearchClear.addEventListener('click', function () {
  els.pickerSearch.value = ''; state.pickerSearch = ''; els.pickerSearchClear.hidden = true; renderPicker(); els.pickerSearch.focus();
});
