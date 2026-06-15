/* LiveTV — multi-view video wall.
   Up to four independent player cells share a global volume and a single
   "audio cell" (only one cell is audible at a time). Channels are assigned per
   cell from a picker of working/live channels, or by browsing the floating
   category / country drawers. State lives in module scope; render functions
   re-paint the affected DOM. */
(function () {
  'use strict';

  // ── Country code → full name ─────────────────────────────────────────────
  const COUNTRY_NAMES = {
    AF:'Afghanistan',AX:'Åland Islands',AL:'Albania',DZ:'Algeria',AS:'American Samoa',
    AD:'Andorra',AO:'Angola',AI:'Anguilla',AQ:'Antarctica',AG:'Antigua and Barbuda',
    AR:'Argentina',AM:'Armenia',AW:'Aruba',AU:'Australia',AT:'Austria',
    AZ:'Azerbaijan',BS:'Bahamas',BH:'Bahrain',BD:'Bangladesh',BB:'Barbados',
    BY:'Belarus',BE:'Belgium',BZ:'Belize',BJ:'Benin',BM:'Bermuda',
    BT:'Bhutan',BO:'Bolivia',BQ:'Bonaire',BA:'Bosnia and Herzegovina',BW:'Botswana',
    BV:'Bouvet Island',BR:'Brazil',IO:'British Indian Ocean Territory',BN:'Brunei',
    BG:'Bulgaria',BF:'Burkina Faso',BI:'Burundi',CV:'Cabo Verde',KH:'Cambodia',
    CM:'Cameroon',CA:'Canada',KY:'Cayman Islands',CF:'Central African Republic',
    TD:'Chad',CL:'Chile',CN:'China',CX:'Christmas Island',CC:'Cocos Islands',
    CO:'Colombia',KM:'Comoros',CG:'Congo',CD:'DR Congo',CK:'Cook Islands',
    CR:'Costa Rica',CI:"Côte d'Ivoire",HR:'Croatia',CU:'Cuba',CW:'Curaçao',
    CY:'Cyprus',CZ:'Czech Republic',DK:'Denmark',DJ:'Djibouti',DM:'Dominica',
    DO:'Dominican Republic',EC:'Ecuador',EG:'Egypt',SV:'El Salvador',GQ:'Equatorial Guinea',
    ER:'Eritrea',EE:'Estonia',SZ:'Eswatini',ET:'Ethiopia',FK:'Falkland Islands',
    FO:'Faroe Islands',FJ:'Fiji',FI:'Finland',FR:'France',GF:'French Guiana',
    PF:'French Polynesia',TF:'French Southern Territories',GA:'Gabon',GM:'Gambia',
    GE:'Georgia',DE:'Germany',GH:'Ghana',GI:'Gibraltar',GR:'Greece',
    GL:'Greenland',GD:'Grenada',GP:'Guadeloupe',GU:'Guam',GT:'Guatemala',
    GG:'Guernsey',GN:'Guinea',GW:'Guinea-Bissau',GY:'Guyana',HT:'Haiti',
    HM:'Heard Island',VA:'Vatican City',HN:'Honduras',HK:'Hong Kong',HU:'Hungary',
    IS:'Iceland',IN:'India',ID:'Indonesia',IR:'Iran',IQ:'Iraq',
    IE:'Ireland',IM:'Isle of Man',IL:'Israel',IT:'Italy',JM:'Jamaica',
    JP:'Japan',JE:'Jersey',JO:'Jordan',KZ:'Kazakhstan',KE:'Kenya',
    KI:'Kiribati',KP:'North Korea',KR:'South Korea',KW:'Kuwait',KG:'Kyrgyzstan',
    LA:'Laos',LV:'Latvia',LB:'Lebanon',LS:'Lesotho',LR:'Liberia',
    LY:'Libya',LI:'Liechtenstein',LT:'Lithuania',LU:'Luxembourg',MO:'Macao',
    MG:'Madagascar',MW:'Malawi',MY:'Malaysia',MV:'Maldives',ML:'Mali',
    MT:'Malta',MH:'Marshall Islands',MQ:'Martinique',MR:'Mauritania',MU:'Mauritius',
    YT:'Mayotte',MX:'Mexico',FM:'Micronesia',MD:'Moldova',MC:'Monaco',
    MN:'Mongolia',ME:'Montenegro',MS:'Montserrat',MA:'Morocco',MZ:'Mozambique',
    MM:'Myanmar',NA:'Namibia',NR:'Nauru',NP:'Nepal',NL:'Netherlands',
    NC:'New Caledonia',NZ:'New Zealand',NI:'Nicaragua',NE:'Niger',NG:'Nigeria',
    NU:'Niue',NF:'Norfolk Island',MK:'North Macedonia',MP:'Northern Mariana Islands',
    NO:'Norway',OM:'Oman',PK:'Pakistan',PW:'Palau',PS:'Palestine',
    PA:'Panama',PG:'Papua New Guinea',PY:'Paraguay',PE:'Peru',PH:'Philippines',
    PN:'Pitcairn',PL:'Poland',PT:'Portugal',PR:'Puerto Rico',QA:'Qatar',
    RE:'Réunion',RO:'Romania',RU:'Russia',RW:'Rwanda',BL:'Saint Barthélemy',
    SH:'Saint Helena',KN:'Saint Kitts and Nevis',LC:'Saint Lucia',MF:'Saint Martin',
    PM:'Saint Pierre and Miquelon',VC:'Saint Vincent and the Grenadines',WS:'Samoa',
    SM:'San Marino',ST:'Sao Tome and Principe',SA:'Saudi Arabia',SN:'Senegal',
    RS:'Serbia',SC:'Seychelles',SL:'Sierra Leone',SG:'Singapore',SX:'Sint Maarten',
    SK:'Slovakia',SI:'Slovenia',SB:'Solomon Islands',SO:'Somalia',ZA:'South Africa',
    GS:'South Georgia',SS:'South Sudan',ES:'Spain',LK:'Sri Lanka',SD:'Sudan',
    SR:'Suriname',SJ:'Svalbard and Jan Mayen',SE:'Sweden',CH:'Switzerland',
    SY:'Syria',TW:'Taiwan',TJ:'Tajikistan',TZ:'Tanzania',TH:'Thailand',
    TL:'Timor-Leste',TG:'Togo',TK:'Tokelau',TO:'Tonga',TT:'Trinidad and Tobago',
    TN:'Tunisia',TR:'Turkey',TM:'Turkmenistan',TC:'Turks and Caicos Islands',
    TV:'Tuvalu',UG:'Uganda',UA:'Ukraine',AE:'UAE',GB:'United Kingdom',
    US:'United States',UM:'US Minor Outlying Islands',UY:'Uruguay',UZ:'Uzbekistan',
    VU:'Vanuatu',VE:'Venezuela',VN:'Vietnam',VG:'British Virgin Islands',
    VI:'US Virgin Islands',WF:'Wallis and Futuna',EH:'Western Sahara',YE:'Yemen',
    ZM:'Zambia',ZW:'Zimbabwe',XK:'Kosovo',
    UAE:'UAE',EUR:'Europe',INT:'International',
  };

  function countryLabel(g) {
    if (!g) return g;
    const up = g.trim().toUpperCase();
    return COUNTRY_NAMES[up] || g;
  }

  // ── State ────────────────────────────────────────────────────────────────
  let channels = [];            // fetched from /api/channels (gzipped, ETag-cached)
  let channelsLoading = true;
  let sourceRefreshing = false; // server is fetching the catalog from the API
  let search = '';              // country-drawer search
  let topChannelIds = [];       // channel ids ranked by tune-ins

  const MAX_CELLS = 4;
  const cells = [];               // one entry per grid cell (see makeCell)
  let audioCell = -1;           // index of the cell whose audio is unmuted
  let globalMuted = false;
  let volume = 1;               // 0..1, applied to every cell's <video>
  let pickerTarget = -1;        // cell index the picker is currently filling

  // ── DOM ──────────────────────────────────────────────────────────────────
  const $ = function (id) { return document.getElementById(id); };
  const els = {
    grid: $('grid'),
    muteBtn: $('muteBtn'), volume: $('volume'), volUp: $('volUp'), volDown: $('volDown'),
    gridAdd: $('gridAdd'), gridRemove: $('gridRemove'), audioButtons: $('audioButtons'),
    scrim: $('scrim'),
    leftDrawerToggle: $('leftDrawerToggle'), rightDrawerToggle: $('rightDrawerToggle'),
    categorySidebar: $('categorySidebar'), sidebar: $('sidebar'),
    search: $('search'), searchClear: $('searchClear'),
    channelList: $('channelList'), listLoading: $('listLoading'),
    emptyState: $('emptyState'), emptyClear: $('emptyClear'),
    channelCount: $('channelCount'), syncBtn: $('syncBtn'),
    categoryList: $('categoryList'), catLoading: $('catLoading'),
    healthToggle: $('healthToggle'), healthStatus: $('healthStatus'),
    healthOverlay: $('healthOverlay'), healthOverlayProgress: $('healthOverlayProgress'),
    healthOverlayFill: $('healthOverlayFill'),
    picker: $('picker'), pickerTitle: $('pickerTitle'), pickerClose: $('pickerClose'),
    pickerSearch: $('pickerSearch'), pickerSearchClear: $('pickerSearchClear'),
    pickerList: $('pickerList'), pickerCount: $('pickerCount'),
  };

  function proxyUrl(url) { return '/api/proxy?url=' + encodeURIComponent(url); }
  // Pick a playback engine from the stream URL. The browser can only decode a
  // fixed set of formats; we route .mpd → Shaka (DASH), raw .ts → mpegts.js,
  // everything else → Hls.js / native HLS.
  function streamKind(url) {
    const u = String(url).split('#')[0];
    if (/\.mpd(\?|$)/i.test(u)) return 'dash';
    if (/\.m3u8(\?|$)/i.test(u)) return 'hls';
    if (/\.ts(\?|$)/i.test(u)) return 'ts';
    return 'hls';
  }
  function formatViewers(n) { return n >= 1000 ? (n / 1000).toFixed(1) + 'K' : String(n); }

  // ── Dead-channel marks ───────────────────────────────────────────────────
  // A channel is marked dead when playback exhausted every server. Keyed by
  // name (IDs shift when the playlist is re-synced); persisted per browser.
  let deadMarks = {};
  try { deadMarks = JSON.parse(localStorage.getItem('livetv_dead')) || {}; } catch (e) { /* fresh start */ }

  function deadKey(ch) { return ch.name.toLowerCase(); }
  function isDead(ch) { return !!deadMarks[deadKey(ch)]; }

  // ── Favourites ───────────────────────────────────────────────────────────
  // User-pinned channels, shown in a "Favourites" section at the top of the
  // category sidebar. Keyed by name (IDs shift on re-sync) and persisted per
  // browser, like dead marks. Favourites ignore the "Working only" filter.
  let favMarks = {};
  try { favMarks = JSON.parse(localStorage.getItem('livetv_fav')) || {}; } catch (e) { /* fresh start */ }
  function favKey(ch) { return ch.name.toLowerCase(); }
  function isFav(ch) { return !!favMarks[favKey(ch)]; }
  function setFav(ch, on) {
    const k = favKey(ch);
    if (on === !!favMarks[k]) return;
    if (on) favMarks[k] = Date.now(); else delete favMarks[k];
    try { localStorage.setItem('livetv_fav', JSON.stringify(favMarks)); } catch (e) { /* quota */ }
  }
  // After a pin toggle: rebuild the category sidebar (Favourites membership
  // changed) and fix the pin icons in the other lists in place (no scroll jump).
  function onFavChanged() {
    renderCategorySidebar();
    Array.prototype.slice.call(document.querySelectorAll('.pin-btn')).forEach(function (pin) {
      const on = !!favMarks[pin.dataset.favkey];
      pin.classList.toggle('faved', on);
      pin.title = on ? 'Remove from Favourites' : 'Add to Favourites';
    });
  }

  function setDead(ch, dead) {
    const key = deadKey(ch);
    if (dead === !!deadMarks[key]) return;
    if (dead) deadMarks[key] = Date.now();
    else delete deadMarks[key];
    try { localStorage.setItem('livetv_dead', JSON.stringify(deadMarks)); } catch (e) { /* quota */ }
    Array.prototype.slice.call(document.querySelectorAll('.channel-item')).forEach(function (btn) {
      if (btn.dataset.id === ch.id) btn.classList.toggle('dead', dead);
    });
  }

  // ── Server-side health filter ("Working only") ──────────────────────────
  // When on, the server probes every stream and we hide ones it can't reach.
  // The per-cell picker always lists working channels only. Default ON.
  let healthOn = localStorage.getItem('livetv_health_on') !== '0';
  let health = {};
  let healthProbing = false;
  let healthDone = 0, healthTotal = 0;
  let healthPoll = null;
  let healthOverlayDismissed = false;
  let channelsEtag = null; // content hash of the current list; gates the health cache

  function passesHealth(ch) {
    if (!healthOn) return true;
    return health[ch.id] !== false;
  }
  function countAlive() {
    let n = 0;
    for (const id in health) { if (health[id]) n++; }
    return n;
  }

  // ── Active-channel highlighting ──────────────────────────────────────────
  // Channels currently shown in any cell are highlighted in the browse lists.
  function activeIds() {
    const ids = {};
    cells.forEach(function (c) { if (c.channel) ids[c.channel.id] = true; });
    return ids;
  }
  function isActive(ch) {
    for (let i = 0; i < cells.length; i++) {
      if (cells[i].channel && cells[i].channel.id === ch.id) return true;
    }
    return false;
  }

  // ── Cell model ───────────────────────────────────────────────────────────
  // Each cell owns its own <video> + Hls.js instance so re-laying-out the grid
  // (a pure CSS change) never restarts playback.
  function makeCell(idx) {
    const cell = {
      idx: idx,
      channel: null,
      serverIdx: 0,
      failedServers: {},
      hls: null,
      shaka: null,
      mpegts: null,
      token: 0,
      root: null,
      video: null,
    };

    const root = document.createElement('section');
    root.className = 'cell';
    root.dataset.idx = String(idx);

    // Playing stage
    const stage = document.createElement('div');
    stage.className = 'cell-stage';

    const video = document.createElement('video');
    video.playsInline = true;
    video.muted = true; // every cell starts muted; the audio cell is unmuted on assign
    stage.appendChild(video);

    const loading = document.createElement('div');
    loading.className = 'cell-overlay cell-loading';
    loading.innerHTML = '<div class="spinner"></div><p class="overlay-muted">Connecting…</p>';
    loading.hidden = true;
    stage.appendChild(loading);

    const error = document.createElement('div');
    error.className = 'cell-overlay cell-error';
    error.hidden = true;
    const errEmoji = document.createElement('div'); errEmoji.className = 'overlay-emoji'; errEmoji.textContent = '📡';
    const errTitle = document.createElement('p'); errTitle.className = 'error-title'; errTitle.textContent = 'Stream unavailable';
    const errBtn = document.createElement('button'); errBtn.className = 'primary-btn'; errBtn.textContent = '↺ Retry';
    errBtn.addEventListener('click', function () { retryCell(cell); });
    error.appendChild(errEmoji); error.appendChild(errTitle); error.appendChild(errBtn);
    stage.appendChild(error);

    // Mic (audio source) — top-left, always visible while filled
    const mic = document.createElement('button');
    mic.className = 'cell-mic';
    mic.title = 'Use this cell’s audio';
    mic.innerHTML =
      '<svg class="ico-on" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">' +
        '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/></svg>' +
      '<svg class="ico-off" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" hidden>' +
        '<polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"/><line x1="22" y1="9" x2="16" y2="15"/><line x1="16" y1="9" x2="22" y2="15"/></svg>';
    mic.addEventListener('click', function () { setAudioCell(cell.idx); });
    stage.appendChild(mic);

    // Play / pause — top-left, beside the mic
    const play = document.createElement('button');
    play.className = 'cell-play';
    play.title = 'Play / pause';
    play.innerHTML =
      '<svg class="ico-play" width="16" height="16" fill="currentColor" stroke="none" viewBox="0 0 24 24"><path d="M8 5v14l11-7z"/></svg>' +
      '<svg class="ico-pause" width="16" height="16" fill="currentColor" stroke="none" viewBox="0 0 24 24"><rect x="6" y="5" width="4" height="14" rx="1"/><rect x="14" y="5" width="4" height="14" rx="1"/></svg>';
    play.addEventListener('click', function () {
      if (cell.video.paused) cell.video.play().catch(function () {});
      else cell.video.pause();
    });
    video.addEventListener('play', function () { updatePlayIcon(cell); });
    video.addEventListener('pause', function () { updatePlayIcon(cell); });
    stage.appendChild(play);

    // Channel name label
    const label = document.createElement('div');
    label.className = 'cell-label';
    stage.appendChild(label);

    // Hover controls — bottom center
    const controls = document.createElement('div');
    controls.className = 'cell-controls';
    const gear = document.createElement('button');
    gear.className = 'cell-ctl cell-gear';
    gear.title = 'Settings — quality & server';
    gear.innerHTML = '<svg width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 00.33 1.82l.06.06a2 2 0 11-2.83 2.83l-.06-.06a1.65 1.65 0 00-1.82-.33 1.65 1.65 0 00-1 1.51V21a2 2 0 01-4 0v-.09A1.65 1.65 0 009 19.4a1.65 1.65 0 00-1.82.33l-.06.06a2 2 0 11-2.83-2.83l.06-.06a1.65 1.65 0 00.33-1.82 1.65 1.65 0 00-1.51-1H3a2 2 0 010-4h.09A1.65 1.65 0 004.6 9a1.65 1.65 0 00-.33-1.82l-.06-.06a2 2 0 112.83-2.83l.06.06a1.65 1.65 0 001.82.33H9a1.65 1.65 0 001-1.51V3a2 2 0 014 0v.09a1.65 1.65 0 001 1.51 1.65 1.65 0 001.82-.33l.06-.06a2 2 0 112.83 2.83l-.06.06a1.65 1.65 0 00-.33 1.82V9a1.65 1.65 0 001.51 1H21a2 2 0 010 4h-.09a1.65 1.65 0 00-1.51 1z"/></svg>';
    gear.addEventListener('click', function (e) { e.stopPropagation(); toggleCellSettings(cell); });
    const expand = document.createElement('button');
    expand.className = 'cell-ctl';
    expand.title = 'Expand (fullscreen)';
    expand.innerHTML = '<svg width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><path d="M8 3H5a2 2 0 00-2 2v3m18 0V5a2 2 0 00-2-2h-3m0 18h3a2 2 0 002-2v-3M3 16v3a2 2 0 002 2h3"/></svg>';
    expand.addEventListener('click', function () { toggleCellFullscreen(cell); });
    const close = document.createElement('button');
    close.className = 'cell-ctl';
    close.title = 'Close this screen';
    close.innerHTML = '<svg width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>';
    close.addEventListener('click', function () { clearCell(cell); });
    controls.appendChild(gear);
    controls.appendChild(expand);
    controls.appendChild(close);
    stage.appendChild(controls);

    // Settings popover (quality + server) — toggled by the gear, content built lazily.
    const settings = document.createElement('div');
    settings.className = 'cell-settings';
    settings.hidden = true;
    settings.addEventListener('click', function (e) { e.stopPropagation(); });
    stage.appendChild(settings);

    root.appendChild(stage);

    // Empty stage
    const empty = document.createElement('div');
    empty.className = 'cell-empty';
    const pick = document.createElement('button');
    pick.className = 'cell-pick';
    pick.innerHTML =
      '<svg width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">' +
        '<rect x="2" y="6" width="14" height="12" rx="2"/><path d="M16 10l6-3v10l-6-3"/><line x1="9" y1="9" x2="9" y2="15"/><line x1="6" y1="12" x2="12" y2="12"/></svg>' +
      '<span class="cell-pick-label"></span>';
    pick.addEventListener('click', function () { openPicker(cell.idx); });
    empty.appendChild(pick);
    root.appendChild(empty);

    cell.root = root;
    cell.video = video;
    cell.els = { stage: stage, empty: empty, loading: loading, error: error, mic: mic, play: play, label: label, settings: settings, pickLabel: pick.querySelector('.cell-pick-label') };
    return cell;
  }

  function setCellState(cell, state) {
    cell.els.loading.hidden = state !== 'loading';
    cell.els.error.hidden = state !== 'error';
  }

  // Swap the play/pause glyph to match the <video>'s state. Driven by a class
  // (CSS display) rather than the `hidden` attribute — `hidden` is an
  // HTMLElement property and doesn't reflect onto SVG child elements.
  function updatePlayIcon(cell) {
    cell.els.play.classList.toggle('playing', !cell.video.paused);
  }

  // ── Per-cell playback ────────────────────────────────────────────────────
  function currentServer(cell) {
    const ch = cell.channel;
    if (!ch || !ch.servers || ch.servers.length === 0) return null;
    return ch.servers[Math.min(cell.serverIdx, ch.servers.length - 1)];
  }

  function destroyCellPlayer(cell) {
    cell.token++;
    if (cell.hls) { cell.hls.destroy(); cell.hls = null; }
    // Shaka's destroy() is async; fire-and-forget — the token bump already
    // invalidates any in-flight load callbacks for this cell.
    if (cell.shaka) { try { cell.shaka.destroy(); } catch (e) { /* ignore */ } cell.shaka = null; }
    if (cell.mpegts) { try { cell.mpegts.destroy(); } catch (e) { /* ignore */ } cell.mpegts = null; }
    cell.video.removeAttribute('src');
    cell.video.onloadedmetadata = null;
    cell.video.onerror = null;
    try { cell.video.load(); } catch (e) { /* ignore */ }
  }

  function failover(cell) {
    if (!cell.channel) return;
    cell.failedServers[cell.serverIdx] = true;
    const servers = cell.channel.servers || [];
    for (let i = 0; i < servers.length; i++) {
      if (!cell.failedServers[i]) {
        cell.serverIdx = i;
        startCellPlayback(cell);
        return;
      }
    }
    setDead(cell.channel, true);
    setCellState(cell, 'error');
  }

  function retryCell(cell) {
    if (!cell.channel) return;
    cell.failedServers = {};
    cell.serverIdx = 0;
    startCellPlayback(cell);
  }

  function startCellPlayback(cell) {
    const server = currentServer(cell);
    if (!server) { setCellState(cell, 'error'); return; }
    destroyCellPlayer(cell);
    const token = cell.token;
    setCellState(cell, 'loading');

    switch (streamKind(server.url)) {
      case 'dash': playDash(cell, server, token); break;
      case 'ts':   playTs(cell, server, token); break;
      default:     playHls(cell, server, token); break;
    }
  }

  // Shared success handler: only acts if this cell hasn't been re-tasked since.
  function onCellPlaying(cell, token) {
    if (token !== cell.token) return;
    setCellState(cell, 'playing');
    if (cell.channel) setDead(cell.channel, false);
    playCell(cell);
    updatePlayIcon(cell);
  }

  // ── HLS (.m3u8) — Hls.js with native-HLS fallback ──
  function playHls(cell, server, token) {
    const video = cell.video;
    let netRecoveries = 0, mediaRecoveries = 0;

    if (window.Hls && Hls.isSupported()) {
      cell.hls = new Hls({
        enableWorker: true,
        lowLatencyMode: false,
        capLevelToPlayerSize: false,
        startLevel: -1,
        startFragPrefetch: true,
        maxMaxBufferLength: 60,
        backBufferLength: 0,
        abrEwmaDefaultEstimate: 5000000,
      });
      const h = cell.hls;
      h.loadSource(proxyUrl(server.url));
      h.attachMedia(video);

      h.once(Hls.Events.MANIFEST_PARSED, function () { onCellPlaying(cell, token); refreshCellSettings(cell); });
      h.on(Hls.Events.LEVEL_SWITCHED, function () { if (token === cell.token) refreshCellSettings(cell); });
      h.on(Hls.Events.ERROR, function (_, data) {
        if (!data.fatal || token !== cell.token) return;
        if (data.type === Hls.ErrorTypes.NETWORK_ERROR && netRecoveries < 1) {
          netRecoveries++; h.startLoad();
        } else if (data.type === Hls.ErrorTypes.MEDIA_ERROR && mediaRecoveries < 1) {
          mediaRecoveries++; h.recoverMediaError();
        } else {
          failover(cell);
        }
      });
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = proxyUrl(server.url);
      video.onloadedmetadata = function () { onCellPlaying(cell, token); };
      video.onerror = function () { if (token === cell.token) failover(cell); };
    } else {
      setCellState(cell, 'error');
    }
  }

  // ── DASH (.mpd) — Shaka Player. Load the ORIGINAL url so relative segment
  // URIs resolve correctly, then route every request through our proxy via a
  // request filter (handles CORS + header-spoofing uniformly). ──
  function playDash(cell, server, token) {
    if (!(window.shaka && shaka.Player.isBrowserSupported())) { failover(cell); return; }
    const player = new shaka.Player();
    cell.shaka = player;
    player.attach(cell.video).then(function () {
      if (token !== cell.token) return;
      player.getNetworkingEngine().registerRequestFilter(function (_, req) {
        req.uris = req.uris.map(function (u) { return proxyUrl(u); });
      });
      player.addEventListener('error', function () { if (token === cell.token) failover(cell); });
      player.addEventListener('adaptation', function () { if (token === cell.token) refreshCellSettings(cell); });
      player.addEventListener('variantchanged', function () { if (token === cell.token) refreshCellSettings(cell); });
      return player.load(server.url);
    }).then(function () {
      onCellPlaying(cell, token);
      refreshCellSettings(cell);
    }).catch(function () {
      if (token === cell.token) failover(cell);
    });
  }

  // ── Raw MPEG-TS (continuous .ts) — mpegts.js drives MSE itself. ──
  function playTs(cell, server, token) {
    if (!(window.mpegts && mpegts.isSupported())) { failover(cell); return; }
    const video = cell.video;
    const player = mpegts.createPlayer({ type: 'mpegts', isLive: true, url: proxyUrl(server.url) });
    cell.mpegts = player;
    player.attachMediaElement(video);
    player.on(mpegts.Events.ERROR, function () { if (token === cell.token) failover(cell); });
    video.onloadedmetadata = function () { onCellPlaying(cell, token); };
    player.load();
  }

  // Start playback honoring autoplay policy: an unmuted play() may be blocked
  // unless it follows a user gesture — fall back to muted so video keeps going.
  function playCell(cell) {
    const p = cell.video.play();
    if (p && p.catch) {
      p.catch(function () {
        if (!cell.video.muted) { cell.video.muted = true; applyAudio(); }
        cell.video.play().catch(function () {});
      });
    }
  }

  function toggleCellFullscreen(cell) {
    if (!document.fullscreenElement) cell.els.stage.requestFullscreen().catch(function () {});
    else document.exitFullscreen();
  }

  // ── Per-cell settings popover (quality + server) ──────────────────────────
  let openSettingsCell = null;

  function toggleCellSettings(cell) {
    if (openSettingsCell === cell) { closeCellSettings(); return; }
    closeCellSettings();
    if (!cell.channel) return;
    renderCellSettings(cell);
    cell.els.settings.hidden = false;
    cell.root.classList.add('settings-open');
    openSettingsCell = cell;
  }
  function closeCellSettings() {
    if (!openSettingsCell) return;
    openSettingsCell.els.settings.hidden = true;
    openSettingsCell.root.classList.remove('settings-open');
    openSettingsCell = null;
  }
  // Re-render the open panel when the live stream's tracks/levels change.
  function refreshCellSettings(cell) {
    if (openSettingsCell === cell) renderCellSettings(cell);
  }

  // Prefer the real resolution; many IPTV manifests omit RESOLUTION and carry
  // only BANDWIDTH, so fall back to a human-readable bitrate (Mbps/kbps).
  function levelLabel(lv) {
    if (lv.height) return lv.height + 'p';
    if (lv.name) return String(lv.name);
    if (lv.bitrate) {
      return lv.bitrate >= 1000000
        ? (lv.bitrate / 1000000).toFixed(1) + ' Mbps'
        : Math.round(lv.bitrate / 1000) + ' kbps';
    }
    return 'Auto';
  }

  // Quality options for whichever engine is driving this cell. Returns null when
  // there's nothing to choose (single rendition, or mpegts which has no levels).
  function cellQuality(cell) {
    if (cell.hls && cell.hls.levels && cell.hls.levels.length > 1) {
      const h = cell.hls;
      const opts = [{ value: -1, label: 'Auto' }];
      for (let i = h.levels.length - 1; i >= 0; i--) {
        opts.push({ value: i, label: levelLabel(h.levels[i]) });
      }
      return {
        opts: opts,
        current: h.autoLevelEnabled ? -1 : h.currentLevel,
        set: function (v) { h.currentLevel = v; },
      };
    }
    if (cell.shaka) {
      let tracks = [];
      try { tracks = cell.shaka.getVariantTracks() || []; } catch (e) { return null; }
      const byHeight = {};
      tracks.forEach(function (t) {
        if (!t.height) return;
        if (!byHeight[t.height] || t.bandwidth > byHeight[t.height].bandwidth) byHeight[t.height] = t;
      });
      const heights = Object.keys(byHeight).map(Number).sort(function (a, b) { return b - a; });
      if (heights.length < 2) return null;
      const opts = [{ value: -1, label: 'Auto' }];
      heights.forEach(function (hgt) { opts.push({ value: hgt, label: hgt + 'p' }); });
      const cfg = cell.shaka.getConfiguration();
      const active = tracks.find(function (t) { return t.active; });
      return {
        opts: opts,
        current: (cfg.abr && cfg.abr.enabled) ? -1 : (active && active.height ? active.height : -1),
        set: function (v) {
          if (v === -1) { cell.shaka.configure({ abr: { enabled: true } }); return; }
          cell.shaka.configure({ abr: { enabled: false } });
          if (byHeight[v]) cell.shaka.selectVariantTrack(byHeight[v], true);
        },
      };
    }
    return null;
  }

  function renderCellSettings(cell) {
    const panel = cell.els.settings;
    panel.innerHTML = '';
    const q = cellQuality(cell);
    const servers = (cell.channel && cell.channel.servers && cell.channel.servers.length > 1)
      ? cell.channel.servers : null;

    if (q) {
      panel.appendChild(settingsSection('Quality', q.opts.map(function (o) {
        return settingsOpt(o.label, o.value === q.current, function () {
          q.set(o.value);
          renderCellSettings(cell);
        });
      })));
    }
    if (servers) {
      panel.appendChild(settingsSection('Server', servers.map(function (s, i) {
        return settingsOpt(s.label || ('Server ' + (i + 1)), i === cell.serverIdx, function () {
          if (i === cell.serverIdx) return;
          cell.serverIdx = i;
          cell.failedServers = {};
          startCellPlayback(cell);
          renderCellSettings(cell);
        });
      })));
    }
    if (!q && !servers) {
      const empty = document.createElement('div');
      empty.className = 'cell-settings-empty';
      empty.textContent = 'No options available';
      panel.appendChild(empty);
    }
  }

  function settingsSection(title, buttons) {
    const sec = document.createElement('div');
    sec.className = 'cell-settings-section';
    const head = document.createElement('div');
    head.className = 'cell-settings-label';
    head.textContent = title;
    sec.appendChild(head);
    const row = document.createElement('div');
    row.className = 'cell-settings-opts';
    buttons.forEach(function (b) { row.appendChild(b); });
    sec.appendChild(row);
    return sec;
  }
  function settingsOpt(label, active, onClick) {
    const b = document.createElement('button');
    b.className = 'cell-settings-opt' + (active ? ' active' : '');
    b.textContent = label;
    b.addEventListener('click', function (e) { e.stopPropagation(); onClick(); });
    return b;
  }

  // Close the popover on any click outside it (the gear/opts stop propagation).
  document.addEventListener('click', function () { closeCellSettings(); });

  // ── Channel assignment ───────────────────────────────────────────────────
  function assignChannel(cellIdx, ch) {
    const cell = cells[cellIdx];
    if (!cell) return;
    cell.channel = ch;
    cell.serverIdx = 0;
    cell.failedServers = {};
    cell.root.classList.add('filled');
    cell.els.label.textContent = ch.name;
    // First channel placed becomes the audio source automatically.
    if (audioCell === -1) audioCell = cellIdx;
    startCellPlayback(cell);
    applyAudio();
    renderAudioButtons();
    refreshHighlights();
    beat();
  }

  function clearCell(cell) {
    if (openSettingsCell === cell) closeCellSettings();
    destroyCellPlayer(cell);
    cell.channel = null;
    cell.root.classList.remove('filled');
    cell.els.label.textContent = '';
    setCellState(cell, 'idle');
    if (audioCell === cell.idx) {
      // Hand the audio crown to the next filled cell, if any.
      audioCell = -1;
      for (let i = 0; i < cells.length; i++) {
        if (cells[i].channel) { audioCell = i; break; }
      }
    }
    applyAudio();
    renderAudioButtons();
    refreshHighlights();
    beat();
  }

  // ── Audio / volume ───────────────────────────────────────────────────────
  function setAudioCell(idx) {
    const cell = cells[idx];
    if (!cell || !cell.channel) return;
    audioCell = idx;
    applyAudio();
    renderAudioButtons();
    beat();
  }

  function applyAudio() {
    cells.forEach(function (c, i) {
      const isAudio = i === audioCell && !globalMuted;
      c.video.muted = !isAudio;
      c.video.volume = volume;
      const on = c.els.mic.querySelector('.ico-on');
      const off = c.els.mic.querySelector('.ico-off');
      if (on && off) { on.hidden = !isAudio; off.hidden = isAudio; }
      c.els.mic.classList.toggle('active', isAudio);
    });
    els.muteBtn.querySelector('.ico-vol').hidden = globalMuted;
    els.muteBtn.querySelector('.ico-muted').hidden = !globalMuted;
    els.muteBtn.setAttribute('aria-pressed', globalMuted ? 'true' : 'false');
  }

  function renderAudioButtons() {
    els.audioButtons.innerHTML = '';
    for (let i = 0; i < cells.length; i++) {
      (function (i) {
        const btn = document.createElement('button');
        const cell = cells[i];
        btn.className = 'rail-num' +
          (i === audioCell ? ' active' : '') +
          (cell.channel ? '' : ' empty');
        btn.textContent = String(i + 1);
        btn.title = cell.channel ? ('Audio from screen ' + (i + 1) + ' — ' + cell.channel.name)
                                 : ('Screen ' + (i + 1) + ' is empty');
        btn.addEventListener('click', function () {
          if (cell.channel) setAudioCell(i);
          else openPicker(i);
        });
        els.audioButtons.appendChild(btn);
      })(i);
    }
  }

  els.muteBtn.addEventListener('click', function () {
    globalMuted = !globalMuted;
    persistAudio();
    applyAudio();
  });
  // Paint the slider's filled (bottom → thumb) portion to match the level.
  function applyVolumeFill() {
    els.volume.style.setProperty('--vol-fill', Math.round(volume * 100) + '%');
  }

  // Single entry point for every volume change (slider, +/- buttons, load):
  // clamp, sync the slider + fill, un-mute when raised above zero, persist, apply.
  function setVolume(v) {
    volume = Math.max(0, Math.min(1, v));
    els.volume.value = String(Math.round(volume * 100));
    if (volume > 0 && globalMuted) globalMuted = false;
    applyVolumeFill();
    persistAudio();
    applyAudio();
  }

  els.volume.addEventListener('input', function () {
    setVolume(parseInt(els.volume.value, 10) / 100);
  });
  els.volUp.addEventListener('click', function () { setVolume(volume + 0.1); });
  els.volDown.addEventListener('click', function () { setVolume(volume - 0.1); });

  function persistAudio() {
    try {
      localStorage.setItem('livetv_volume', String(Math.round(volume * 100)));
      localStorage.setItem('livetv_muted', globalMuted ? '1' : '0');
    } catch (e) { /* quota */ }
  }

  // ── Grid sizing ──────────────────────────────────────────────────────────
  function renderGridControls() {
    els.gridAdd.disabled = cells.length >= MAX_CELLS;
    els.gridRemove.disabled = cells.length <= 1;
    els.grid.dataset.count = String(cells.length);
  }

  function addCell() {
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

  function removeCell() {
    if (cells.length <= 1) return;
    const cell = cells.pop();
    if (openSettingsCell === cell) closeCellSettings();
    destroyCellPlayer(cell);
    if (cell.root.parentNode) cell.root.parentNode.removeChild(cell.root);
    if (audioCell >= cells.length) {
      audioCell = -1;
      for (let i = 0; i < cells.length; i++) { if (cells[i].channel) { audioCell = i; break; } }
    }
    updatePickLabels();
    renderGridControls();
    renderAudioButtons();
    applyAudio();
    refreshHighlights();
    persistGrid();
    beat();
  }

  function updatePickLabels() {
    cells.forEach(function (c, i) {
      c.els.pickLabel.textContent = (i === 0 && cells.length === 1) ? 'Pick your first channel' : ('Set Cell ' + (i + 1));
    });
  }

  function persistGrid() {
    try { localStorage.setItem('livetv_grid', String(cells.length)); } catch (e) { /* quota */ }
  }

  els.gridAdd.addEventListener('click', addCell);
  els.gridRemove.addEventListener('click', removeCell);

  // ── Channel picker (per cell) ────────────────────────────────────────────
  let pickerSearch = '';

  function openPicker(cellIdx) {
    pickerTarget = cellIdx;
    els.pickerTitle.textContent = (cellIdx === 0 && cells.length === 1)
      ? 'Pick your first channel' : ('Pick a channel for Cell ' + (cellIdx + 1));
    els.picker.hidden = false;
    els.scrim.hidden = false;
    renderPicker();
    setTimeout(function () { els.pickerSearch.focus(); }, 0);
  }
  function closePicker() {
    els.picker.hidden = true;
    pickerTarget = -1;
    maybeHideScrim();
  }

  // Live/working channels only, optionally narrowed by the picker search box.
  function pickerChannels() {
    let base = channels.filter(passesHealth);
    if (pickerSearch) {
      const q = pickerSearch.toLowerCase();
      base = base.filter(function (ch) {
        return ch.name.toLowerCase().indexOf(q) !== -1 || ch.group.toLowerCase().indexOf(q) !== -1;
      });
    }
    return base;
  }

  function renderPicker() {
    const matches = pickerChannels();
    els.pickerList.innerHTML = '';
    const frag = document.createDocumentFragment();
    matches.slice(0, RENDER_CAP).forEach(function (ch) {
      frag.appendChild(makeChannelButton(ch, function () {
        const target = pickerTarget;
        closePicker();
        assignChannel(target, ch);
      }));
    });
    els.pickerList.appendChild(frag);
    els.pickerList.scrollTop = 0;
    const n = matches.length;
    els.pickerCount.textContent = channelsLoading ? 'Loading…'
      : n === 0 ? (sourceRefreshing ? 'Fetching channels…' : (healthOn ? 'No working channels found yet — health check may still be running' : 'No channels found'))
      : n > RENDER_CAP ? ('Showing first ' + RENDER_CAP + ' of ' + n + ' — search to narrow')
      : (n + ' live channel' + (n === 1 ? '' : 's'));
  }

  els.pickerClose.addEventListener('click', closePicker);
  let pickerDebounce = null;
  els.pickerSearch.addEventListener('input', function () {
    els.pickerSearchClear.hidden = !els.pickerSearch.value;
    if (pickerDebounce) clearTimeout(pickerDebounce);
    pickerDebounce = setTimeout(function () { pickerSearch = els.pickerSearch.value; renderPicker(); }, 150);
  });
  els.pickerSearchClear.addEventListener('click', function () {
    els.pickerSearch.value = ''; pickerSearch = ''; els.pickerSearchClear.hidden = true; renderPicker(); els.pickerSearch.focus();
  });

  // ── Browse drawers (floating sidebars) ───────────────────────────────────
  function targetCellForBrowse() {
    for (let i = 0; i < cells.length; i++) { if (!cells[i].channel) return i; }
    return audioCell >= 0 ? audioCell : 0;
  }

  function openDrawer(which) {
    const drawer = which === 'left' ? els.categorySidebar : els.sidebar;
    const other = which === 'left' ? els.sidebar : els.categorySidebar;
    other.classList.remove('open');
    drawer.hidden = false; // first open: leave display:flex from here on (slides via .open)
    els.scrim.hidden = false;
    // next frame so the slide-in transition runs from the off-screen transform
    requestAnimationFrame(function () { drawer.classList.add('open'); });
  }
  function closeDrawers() {
    // Drop only the .open class so the drawer slides back out; it stays in the
    // DOM (translated off-screen) so the close animates too.
    els.categorySidebar.classList.remove('open');
    els.sidebar.classList.remove('open');
    maybeHideScrim();
  }
  function maybeHideScrim() {
    const anyOpen = els.categorySidebar.classList.contains('open') ||
                  els.sidebar.classList.contains('open') || !els.picker.hidden;
    els.scrim.hidden = !anyOpen;
  }

  els.leftDrawerToggle.addEventListener('click', function () { openDrawer('left'); });
  els.rightDrawerToggle.addEventListener('click', function () { openDrawer('right'); });
  els.scrim.addEventListener('click', function () { closeDrawers(); closePicker(); });
  Array.prototype.slice.call(document.querySelectorAll('[data-close-drawer]')).forEach(function (b) {
    b.addEventListener('click', closeDrawers);
  });

  // Selecting a channel from a browse drawer drops it into the next empty cell
  // (or the audio cell). The drawer stays open so several channels can be
  // picked in a row; it only closes via the close button or a click outside.
  function browseSelect(ch) {
    const idx = targetCellForBrowse();
    assignChannel(idx, ch);
  }

  // ── Browse list rendering (shared by both drawers) ───────────────────────
  function makeChannelButton(ch, onClick) {
    const btn = document.createElement('button');
    btn.className = 'channel-item' + (isActive(ch) ? ' selected' : '') + (isDead(ch) ? ' dead' : '');
    btn.dataset.id = ch.id;
    btn.appendChild(logoOrFallback(ch, 'channel-logo', 'channel-logo-fallback'));
    const name = document.createElement('span');
    name.className = 'channel-name';
    name.textContent = ch.name;
    btn.appendChild(name);
    // Pin toggle (a span, not a button — a <button> can't nest in a <button>).
    const pin = document.createElement('span');
    pin.className = 'pin-btn' + (isFav(ch) ? ' faved' : '');
    pin.dataset.favkey = favKey(ch);
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
    btn.addEventListener('click', function () { onClick(ch); });
    return btn;
  }

  function logoOrFallback(ch, imgClass, fbClass) {
    const fallback = document.createElement('div');
    fallback.className = fbClass;
    fallback.textContent = ch.name.slice(0, 2).toUpperCase();
    if (!ch.logo) return fallback;
    const img = document.createElement('img');
    img.className = imgClass;
    img.src = ch.logo;
    img.alt = ch.name;
    img.loading = 'lazy';
    img.onerror = function () { if (img.parentNode) img.parentNode.replaceChild(fallback, img); };
    return img;
  }

  const RENDER_CAP = 500;
  const expandedGroups = {};
  let lastRenderedSearch = null;

  function filteredChannels() {
    let base = channels;
    if (search) {
      const q = search.toLowerCase();
      base = channels.filter(function (ch) {
        return ch.name.toLowerCase().indexOf(q) !== -1 || ch.group.toLowerCase().indexOf(q) !== -1;
      });
    }
    if (healthOn) base = base.filter(passesHealth);
    return base;
  }

  function renderChannelList() {
    const matches = filteredChannels();
    const keepScroll = els.channelList.scrollTop;
    Array.prototype.slice.call(els.channelList.querySelectorAll('.channel-item, .group-section')).forEach(function (n) { n.remove(); });

    const awaitingFirstList = sourceRefreshing && channels.length === 0;
    els.listLoading.hidden = !(channelsLoading || awaitingFirstList);
    els.emptyState.hidden = channelsLoading || awaitingFirstList || matches.length !== 0;

    const frag = document.createDocumentFragment();

    if (search) {
      matches.slice(0, RENDER_CAP).forEach(function (ch) { frag.appendChild(makeChannelButton(ch, browseSelect)); });
      els.channelList.appendChild(frag);
      els.channelList.scrollTop = (search === lastRenderedSearch) ? keepScroll : 0;
      lastRenderedSearch = search;
      els.channelCount.textContent = channelsLoading ? ''
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
      head.className = 'group-header' + (expandedGroups[g] ? ' open' : '');
      const caret = document.createElement('span'); caret.className = 'group-caret'; caret.textContent = '▸';
      const title = document.createElement('span'); title.className = 'group-title'; title.textContent = countryLabel(g);
      const count = document.createElement('span'); count.className = 'group-count'; count.textContent = String(byGroup[g].length);
      head.appendChild(caret); head.appendChild(title); head.appendChild(count);
      head.addEventListener('click', function () { expandedGroups[g] = !expandedGroups[g]; renderChannelList(); });
      section.appendChild(head);
      if (expandedGroups[g]) byGroup[g].forEach(function (ch) { section.appendChild(makeChannelButton(ch, browseSelect)); });
      frag.appendChild(section);
    });

    els.channelList.appendChild(frag);
    els.channelList.scrollTop = keepScroll;
    lastRenderedSearch = '';
    const shown = healthOn ? matches.length : channels.length;
    els.channelCount.textContent = channelsLoading ? ''
      : shown + (healthOn ? ' working' : ' channels') + ' · ' + groupNames.length + ' countries';
  }

  const CATEGORY_ORDER = ['News', 'Sports', 'Movies', 'Music', 'Kids', 'Religious', 'Entertainment'];
  const expandedCats = {};
  let favOpen = true; // Favourites section starts expanded

  // Builds the always-present "Favourites" section for the top of the category
  // sidebar. Pinned channels bypass the health filter (the user chose them).
  function buildFavSection() {
    const favList = channels.filter(isFav);
    const section = document.createElement('div');
    section.className = 'group-section fav-section';

    const head = document.createElement('button');
    head.className = 'group-header' + (favOpen ? ' open' : '');
    const caret = document.createElement('span'); caret.className = 'group-caret'; caret.textContent = '▸';
    const title = document.createElement('span'); title.className = 'group-title'; title.textContent = '★ Favourites';
    const count = document.createElement('span'); count.className = 'group-count'; count.textContent = String(favList.length);
    head.appendChild(caret); head.appendChild(title); head.appendChild(count);
    head.addEventListener('click', function () { favOpen = !favOpen; renderCategorySidebar(); });
    section.appendChild(head);

    if (favOpen) {
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

  function renderCategorySidebar() {
    const keepScroll = els.categoryList.scrollTop;
    Array.prototype.slice.call(els.categoryList.querySelectorAll('.channel-item, .group-section')).forEach(function (n) { n.remove(); });
    const awaitingFirstList = sourceRefreshing && channels.length === 0;
    els.catLoading.hidden = !(channelsLoading || awaitingFirstList);
    if (channelsLoading || awaitingFirstList) return;

    const byCat = {};
    channels.forEach(function (ch) {
      if (!passesHealth(ch)) return;
      const t = ch.type || 'Entertainment';
      (byCat[t] || (byCat[t] = [])).push(ch);
    });

    const frag = document.createDocumentFragment();
    frag.appendChild(buildFavSection()); // always first
    CATEGORY_ORDER.forEach(function (cat) {
      const list = byCat[cat];
      if (!list || list.length === 0) return;
      const section = document.createElement('div');
      section.className = 'group-section';
      const head = document.createElement('button');
      head.className = 'group-header' + (expandedCats[cat] ? ' open' : '');
      const caret = document.createElement('span'); caret.className = 'group-caret'; caret.textContent = '▸';
      const title = document.createElement('span'); title.className = 'group-title'; title.textContent = cat;
      const count = document.createElement('span'); count.className = 'group-count'; count.textContent = String(list.length);
      head.appendChild(caret); head.appendChild(title); head.appendChild(count);
      head.addEventListener('click', function () { expandedCats[cat] = !expandedCats[cat]; renderCategorySidebar(); });
      section.appendChild(head);
      if (expandedCats[cat]) {
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

  // Re-highlight active channels in the browse lists without rebuilding them.
  function refreshHighlights() {
    const ids = activeIds();
    Array.prototype.slice.call(document.querySelectorAll('.channel-item')).forEach(function (btn) {
      btn.classList.toggle('selected', ids[btn.dataset.id] === true);
    });
  }

  // ── Search (country drawer) ──────────────────────────────────────────────
  function setSearch(value) {
    search = value;
    els.search.value = value;
    els.searchClear.hidden = !value;
    renderChannelList();
  }
  let searchDebounce = null;
  els.search.addEventListener('input', function () {
    if (searchDebounce) clearTimeout(searchDebounce);
    searchDebounce = setTimeout(function () { setSearch(els.search.value); }, 150);
  });
  els.searchClear.addEventListener('click', function () { setSearch(''); });
  els.emptyClear.addEventListener('click', function () { setSearch(''); });

  // ── Keyboard shortcuts ───────────────────────────────────────────────────
  window.addEventListener('keydown', function (e) {
    const inInput = document.activeElement && document.activeElement.tagName === 'INPUT';
    if (e.key === 'Escape') { closeDrawers(); closePicker(); }
    if (e.key === '/' && !inInput) { e.preventDefault(); openDrawer('right'); setTimeout(function () { els.search.focus(); }, 0); }
  });


  // ── Heartbeat (reports the audio cell's channel) ─────────────────────────
  let sessionId = sessionStorage.getItem('livetv_sid');
  if (!sessionId) {
    sessionId = Math.random().toString(36).slice(2) + Date.now().toString(36);
    sessionStorage.setItem('livetv_sid', sessionId);
  }

  function watchedChannelId() {
    if (audioCell >= 0 && cells[audioCell] && cells[audioCell].channel) return cells[audioCell].channel.id;
    for (let i = 0; i < cells.length; i++) { if (cells[i].channel) return cells[i].channel.id; }
    return null;
  }

  function beat() {
    fetch('/api/viewers', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sessionId: sessionId, channelId: watchedChannelId() }),
    })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (d.top && d.top.length > 0) {
          const ids = d.top.map(function (x) { return x.id; });
          if (ids.join(',') !== topChannelIds.join(',')) topChannelIds = ids;
        }
      })
      .catch(function () {});
  }

  // ── Health filter lifecycle ──────────────────────────────────────────────
  function renderHealthLists() {
    renderCategorySidebar();
    renderChannelList();
    if (!els.picker.hidden) renderPicker();
  }

  function updateHealthStatus() {
    const el = els.healthStatus;
    if (!healthOn) { el.hidden = true; el.textContent = ''; }
    else {
      el.hidden = false;
      if (healthProbing) el.textContent = 'Checking ' + healthDone + ' / ' + healthTotal + ' · ' + countAlive() + ' live';
      else if (healthTotal) el.textContent = countAlive() + ' working of ' + healthTotal;
      else el.textContent = 'Starting…';
    }
    updateHealthOverlay();
  }

  function updateHealthOverlay() {
    const show = healthOn && healthProbing && !healthOverlayDismissed;
    els.healthOverlay.hidden = !show;
    if (!show) return;
    els.healthOverlayProgress.textContent = healthTotal
      ? healthDone + ' / ' + healthTotal + ' checked · ' + countAlive() + ' live'
      : 'Starting…';
    const pct = healthTotal ? Math.round((healthDone / healthTotal) * 100) : 0;
    els.healthOverlayFill.style.width = pct + '%';
  }

  function applyHealthSnapshot(snap) {
    if (!snap) return;
    healthDone = snap.done || 0;
    healthTotal = snap.total || 0;
    if (snap.status) { for (const id in snap.status) { health[id] = snap.status[id]; } }
    healthProbing = !!snap.running;
    if (!snap.running) stopHealthPolling();
    if (healthOn) renderHealthLists();
    updateHealthStatus();
  }

  function pollHealth() {
    fetch('/api/health').then(function (r) { return r.json(); }).then(applyHealthSnapshot).catch(function () {});
  }
  function startHealthProbe(force) {
    healthProbing = true;
    healthOverlayDismissed = false;
    updateHealthStatus();
    fetch('/api/health' + (force ? '?force=1' : ''), { method: 'POST' })
      .then(function (r) { return r.json(); }).then(applyHealthSnapshot).catch(function () {});
    if (!healthPoll) healthPoll = setInterval(pollHealth, 1500);
  }

  // On page load, reuse a cached pass instead of re-probing: ask the server what
  // it already has and only probe when there's no finished pass for the current
  // catalog (etag mismatch). The server persists passes to disk, so this stays a
  // cache hit across restarts until the catalog changes or the user re-toggles.
  function ensureHealth() {
    fetch('/api/health')
      .then(function (r) { return r.json(); })
      .then(function (snap) {
        const hit = snap && snap.finished && !snap.running &&
                  snap.etag && snap.etag === channelsEtag &&
                  snap.status && Object.keys(snap.status).length > 0;
        if (hit) {
          applyHealthSnapshot(snap); // restore verdicts — no probe, no overlay
        } else {
          startHealthProbe(false);   // nothing usable for this list → probe
        }
      })
      .catch(function () { startHealthProbe(false); });
  }
  function stopHealthPolling() {
    if (healthPoll) { clearInterval(healthPoll); healthPoll = null; }
    healthProbing = false;
  }

  $('healthOverlayHide').addEventListener('click', function () { healthOverlayDismissed = true; updateHealthOverlay(); });

  els.healthToggle.addEventListener('click', function () {
    healthOn = !healthOn;
    try { localStorage.setItem('livetv_health_on', healthOn ? '1' : '0'); } catch (e) { /* quota */ }
    els.healthToggle.classList.toggle('on', healthOn);
    els.healthToggle.setAttribute('aria-checked', healthOn ? 'true' : 'false');
    // A deliberate off→on flip always re-checks (force), even if the cached pass
    // is for the same catalog — that's the user's "re-probe now" gesture.
    if (healthOn) startHealthProbe(true); else stopHealthPolling();
    renderHealthLists();
    updateHealthStatus();
  });

  // ── Sync ─────────────────────────────────────────────────────────────────
  els.syncBtn.addEventListener('click', function () {
    const btn = els.syncBtn;
    btn.disabled = true; btn.textContent = 'Syncing…';
    fetch('/api/sync', { method: 'POST' })
      .then(function (r) { if (!r.ok) throw new Error('sync failed: ' + r.status); return r.json(); })
      .then(function () { btn.textContent = '⟳ Sync'; btn.disabled = false; loadChannels(); })
      .catch(function () {
        btn.textContent = 'Sync failed';
        setTimeout(function () { btn.textContent = '⟳ Sync'; btn.disabled = false; }, 3000);
      });
  });

  // ── Source refresh (API → list.m3u) ──────────────────────────────────────
  function setLoadingText(text) {
    const a = els.listLoading.querySelector('span');
    const b = els.catLoading.querySelector('span');
    if (a) a.textContent = text;
    if (b) b.textContent = text;
  }
  function pollSource() {
    fetch('/api/source')
      .then(function (r) { return r.json(); })
      .then(function (d) {
        const was = sourceRefreshing;
        sourceRefreshing = !!d.refreshing;
        if (sourceRefreshing && channels.length === 0) {
          setLoadingText('Fetching channels from iptv-org…');
          renderChannelList(); renderCategorySidebar();
          if (!els.picker.hidden) renderPicker();
        }
        if (was && !sourceRefreshing) loadChannels();
        if (sourceRefreshing) setTimeout(pollSource, 2500);
      })
      .catch(function () { /* old build — ignore */ });
  }

  // ── Init ─────────────────────────────────────────────────────────────────
  function loadChannels() {
    fetch('/api/channels')
      .then(function (r) {
        if (!r.ok) throw new Error('channels fetch failed: ' + r.status);
        channelsEtag = r.headers.get('ETag'); // gates the health cache to this list version
        return r.json();
      })
      .then(function (data) {
        channels = Array.isArray(data) ? data : [];
        channelsLoading = false;
        health = {};
        healthDone = healthTotal = 0;
        stopHealthPolling();
        // Reuse a cached pass when possible; only probe a never-seen catalog.
        if (healthOn && !sourceRefreshing) ensureHealth();
        renderCategorySidebar();
        renderChannelList();
        if (!els.picker.hidden) renderPicker();
        updateHealthStatus();
      })
      .catch(function () { channelsLoading = false; renderChannelList(); });
  }

  function restoreAudioPrefs() {
    const v = parseInt(localStorage.getItem('livetv_volume'), 10);
    if (!isNaN(v)) volume = Math.max(0, Math.min(1, v / 100));
    globalMuted = localStorage.getItem('livetv_muted') === '1';
    els.volume.value = String(Math.round(volume * 100));
    applyVolumeFill();
  }

  function init() {
    restoreAudioPrefs();

    // Start with the persisted grid size (default 1).
    const saved = parseInt(localStorage.getItem('livetv_grid'), 10);
    const count = (!isNaN(saved) && saved >= 1 && saved <= MAX_CELLS) ? saved : 1;
    for (let i = 0; i < count; i++) addCell();
    updatePickLabels();

    els.healthToggle.classList.toggle('on', healthOn);
    els.healthToggle.setAttribute('aria-checked', healthOn ? 'true' : 'false');

    renderCategorySidebar();
    renderChannelList();
    updateHealthStatus();
    loadChannels();
    pollSource();
    beat();
    setInterval(beat, 30000);

    if ('serviceWorker' in navigator) {
      navigator.serviceWorker.register('/static/sw.js').catch(function (err) { console.warn('SW registration failed:', err); });
    }
  }

  init();
})();
