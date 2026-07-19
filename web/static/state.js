export const state = {
  channels: [],
  channelsLoading: true,
  sourceRefreshing: false,
  search: '',
  country: '',
  countryOptionsKey: '',
  topChannelIds: [],
  clearKeys: {},
  audioCell: -1,
  recordingAvailable: false,
  recId: null,
  recCellIdx: -1,
  recStartMs: 0,
  recTimer: null,
  globalMuted: false,
  volume: 1,
  pickerTarget: -1,
  deadMarks: {},
  healthOn: true,
  health: {},
  healthProbing: false,
  healthDone: 0,
  healthTotal: 0,
  healthPoll: null,
  openSettingsCell: null,
  channelModalMode: 'add',
  channelModalId: null,
  pickerSearch: '',
  lastRenderedSearch: null,
  expandedCats: {},
  favOpen: true,
  pendingImportNew: [],
  sessionId: null,
  searchDebounce: null,
  pickerDebounce: null,
};

export const cells = [];
export const MAX_CELLS = 4;
export const RENDER_CAP = 500;
export const CATEGORY_ORDER = ['News', 'Sports', 'Movies', 'Music', 'Kids', 'Religious', 'Entertainment'];

export const $ = id => document.getElementById(id);

export const els = {
  grid: $('grid'),
  muteBtn: $('muteBtn'), volume: $('volume'), volUp: $('volUp'), volDown: $('volDown'),
  gridAdd: $('gridAdd'), gridRemove: $('gridRemove'), audioButtons: $('audioButtons'),
  recordBtn: $('recordBtn'), recordTime: $('recordTime'),
  recordSaved: $('recordSaved'), recordSavedRow: $('recordSavedRow'), recordSavedClose: $('recordSavedClose'),
  scrim: $('scrim'),
  rightDrawerToggle: $('rightDrawerToggle'),
  sidebar: $('sidebar'),
  search: $('search'), searchClear: $('searchClear'),
  countryFilter: $('countryFilter'),
  channelList: $('channelList'), listLoading: $('listLoading'),
  emptyState: $('emptyState'), emptyClear: $('emptyClear'),
  channelCount: $('channelCount'),
  healthToggle: $('healthToggle'), healthStatus: $('healthStatus'),
  picker: $('picker'), pickerTitle: $('pickerTitle'), pickerClose: $('pickerClose'),
  pickerSearch: $('pickerSearch'), pickerSearchClear: $('pickerSearchClear'),
  pickerList: $('pickerList'), pickerCount: $('pickerCount'),
  addChannelBtn: $('addChannelBtn'), addChannel: $('addChannel'),
  addChannelTitle: $('addChannelTitle'),
  addChannelForm: $('addChannelForm'), addChannelName: $('addChannelName'),
  addChannelUrl: $('addChannelUrl'), addChannelLicense: $('addChannelLicense'),
  addChannelReferer: $('addChannelReferer'), addChannelUserAgent: $('addChannelUserAgent'),
  addChannelLicToggle: $('addChannelLicToggle'), addChannelLicWrap: $('addChannelLicWrap'),
  addChannelUaWrap: $('addChannelUaWrap'), addChannelLicWrap2: $('addChannelLicWrap2'),
  addChannelError: $('addChannelError'),
  addChannelClose: $('addChannelClose'), addChannelCancel: $('addChannelCancel'),
  addChannelSave: $('addChannelSave'),
  addTabs: $('addTabs'), addTabManual: $('addTabManual'), addTabXtream: $('addTabXtream'),
  xtreamPanel: $('xtreamPanel'),
  xtreamSavedWrap: $('xtreamSavedWrap'), xtreamSaved: $('xtreamSaved'), xtreamRefresh: $('xtreamRefresh'),
  xtreamName: $('xtreamName'), xtreamUser: $('xtreamUser'), xtreamPass: $('xtreamPass'), xtreamServer: $('xtreamServer'),
  xtreamError: $('xtreamError'), xtreamCancel: $('xtreamCancel'), xtreamSave: $('xtreamSave'),
  importBtn: $('importBtn'), importFile: $('importFile'), importModal: $('importModal'),
  importList: $('importList'), importCount: $('importCount'), importError: $('importError'),
  importClose: $('importClose'), importCancel: $('importCancel'), importSave: $('importSave'),
  importConflict: $('importConflict'), importConflictSummary: $('importConflictSummary'),
  importConflictList: $('importConflictList'), importConflictClose: $('importConflictClose'),
  importConflictCancel: $('importConflictCancel'), importConflictAdd: $('importConflictAdd'),
};

try { state.deadMarks = JSON.parse(localStorage.getItem('livetv_dead')) || {}; } catch(e) {}
state.healthOn = localStorage.getItem('livetv_health_on') !== '0';

// Session ID
state.sessionId = sessionStorage.getItem('livetv_sid');
if (!state.sessionId) {
  state.sessionId = Math.random().toString(36).slice(2) + Date.now().toString(36);
  sessionStorage.setItem('livetv_sid', state.sessionId);
}
