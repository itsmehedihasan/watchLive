import { state, cells, els, MAX_CELLS } from './state.js';
import { makeCell, closeCellSettings } from './cell.js';
import { setCellState, destroyCellPlayer } from './player.js';
import { applyAudio, renderAudioButtons, stopRecording } from './audio.js';
import { refreshHighlights, beat } from './channels.js';

export function renderGridControls() {
  els.gridAdd.disabled = cells.length >= MAX_CELLS;
  els.gridRemove.disabled = cells.length <= 1;
  els.grid.dataset.count = String(cells.length);
}

export function updatePickLabels() {
  cells.forEach(function (c, i) {
    c.els.pickLabel.textContent = (i === 0 && cells.length === 1) ? 'Pick your first channel' : ('Set Cell ' + (i + 1));
  });
}

export function persistGrid() {
  try { localStorage.setItem('livetv_grid', String(cells.length)); } catch (e) { /* quota */ }
}

export function addCell() {
  if (cells.length >= MAX_CELLS) return;
  const cell = makeCell(cells.length);
  cells.push(cell);
  els.grid.appendChild(cell.root);
  setCellState(cell, 'idle');
  updatePickLabels();
  renderGridControls();
  renderAudioButtons();
  applyAudio();
  persistGrid();
}

export function removeCell() {
  if (cells.length <= 1) return;
  const cell = cells.pop();
  if (state.openSettingsCell === cell) closeCellSettings();
  if (state.recId && cell.idx === state.recCellIdx) stopRecording();
  destroyCellPlayer(cell);
  if (cell.root.parentNode) cell.root.parentNode.removeChild(cell.root);
  if (state.audioCell >= cells.length) {
    state.audioCell = -1;
    for (let i = 0; i < cells.length; i++) { if (cells[i].channel) { state.audioCell = i; break; } }
  }
  updatePickLabels();
  renderGridControls();
  renderAudioButtons();
  applyAudio();
  refreshHighlights();
  persistGrid();
  beat();
}

els.gridAdd.addEventListener('click', addCell);
els.gridRemove.addEventListener('click', removeCell);
