/* LiveTV front-end — vanilla JS port of the original React app.
   State lives in module scope; render functions re-paint the affected DOM. */
(function () {
  'use strict';

  // ── Country code → full name ─────────────────────────────────────────────
  var COUNTRY_NAMES = {
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
    // common 3-letter / non-standard codes seen in iptv-org playlists
    UAE:'UAE',EUR:'Europe',INT:'International',
  };

  function countryLabel(g) {
    if (!g) return g;
    var up = g.trim().toUpperCase();
    if (COUNTRY_NAMES[up]) return COUNTRY_NAMES[up];
    // Also check the original casing key (e.g. "United Kingdom" passes through)
    return g;
  }

  // ── State ────────────────────────────────────────────────────────────────
  var channels = [];            // fetched from /api/channels (gzipped, ETag-cached)
  var channelsLoading = true;
  var selected = null;          // currently playing channel or null
  var serverIdx = 0;            // index into selected.servers
  var failedServers = {};       // server indexes that hit fatal errors this selection
  var search = '';
  var sidebarOpen = true;       // desktop sidebar visibility
  var mobileChannelsOpen = false;
  var topChannelIds = [];       // channel ids ranked by tune-ins (carousel order)
  var channelViewers = null;    // viewers on the selected channel
  var hls = null;               // active Hls.js instance
  var playToken = 0;            // invalidates async player work on channel switch
  var copiedTimer = null;

  // ── DOM ──────────────────────────────────────────────────────────────────
  var $ = function (id) { return document.getElementById(id); };
  var els = {
    sidebar: $('sidebar'), sidebarToggle: $('sidebarToggle'), homeBtn: $('homeBtn'),
    activeBadge: $('activeBadge'), totalViewers: $('totalViewers'),
    search: $('search'), searchClear: $('searchClear'),
    channelList: $('channelList'), listLoading: $('listLoading'),
    emptyState: $('emptyState'), emptyClear: $('emptyClear'),
    channelCount: $('channelCount'), syncBtn: $('syncBtn'),
    carousel: $('carousel'), carouselContent: $('carouselContent'),
    carouselPrev: $('carouselPrev'), carouselNext: $('carouselNext'), carouselDots: $('carouselDots'),
    playerSection: $('playerSection'), videoContainer: $('videoContainer'), video: $('video'),
    loadingOverlay: $('loadingOverlay'), errorOverlay: $('errorOverlay'), retryBtn: $('retryBtn'),
    qualityBadge: $('qualityBadge'), fsBtn: $('fsBtn'),
    streamOptions: $('streamOptions'), serverRow: $('serverRow'), serverButtons: $('serverButtons'),
    qualityWrap: $('qualityWrap'), qualitySelect: $('qualitySelect'),
    npLogo: $('npLogo'), npLogoFallback: $('npLogoFallback'),
    npName: $('npName'), npGroup: $('npGroup'),
    viewerPill: $('viewerPill'), viewerCount: $('viewerCount'),
    copyBtn: $('copyBtn'), moreChannels: $('moreChannels'),
  };

  function proxyUrl(url) { return '/api/proxy?url=' + encodeURIComponent(url); }

  // ── Dead-channel marks ───────────────────────────────────────────────────
  // A channel is marked dead when playback exhausted every server. Keyed by
  // name (IDs shift when the playlist is re-synced); persisted per browser.
  var deadMarks = {};
  try { deadMarks = JSON.parse(localStorage.getItem('livetv_dead')) || {}; } catch (e) { /* fresh start */ }

  function deadKey(ch) { return ch.name.toLowerCase(); }
  function isDead(ch) { return !!deadMarks[deadKey(ch)]; }

  function setDead(ch, dead) {
    var key = deadKey(ch);
    if (dead === !!deadMarks[key]) return;
    if (dead) deadMarks[key] = Date.now();
    else delete deadMarks[key];
    try { localStorage.setItem('livetv_dead', JSON.stringify(deadMarks)); } catch (e) { /* quota — marks stay in memory */ }
    // Update the existing sidebar button in place.
    Array.prototype.slice.call(els.channelList.querySelectorAll('.channel-item')).forEach(function (btn) {
      if (btn.dataset.id === ch.id) btn.classList.toggle('dead', dead);
    });
  }

  function formatViewers(n) { return n >= 1000 ? (n / 1000).toFixed(1) + 'K' : String(n); }

  // ── Channel list (sidebar) ───────────────────────────────────────────────
  function filteredChannels() {
    if (!search) return channels;
    var q = search.toLowerCase();
    return channels.filter(function (ch) {
      return ch.name.toLowerCase().indexOf(q) !== -1 || ch.group.toLowerCase().indexOf(q) !== -1;
    });
  }

  function logoOrFallback(ch, imgClass, fbClass) {
    var fallback = document.createElement('div');
    fallback.className = fbClass;
    fallback.textContent = ch.name.slice(0, 2).toUpperCase();
    if (!ch.logo) return fallback;
    var img = document.createElement('img');
    img.className = imgClass;
    img.src = ch.logo;
    img.alt = ch.name;
    img.loading = 'lazy';
    img.onerror = function () { if (img.parentNode) img.parentNode.replaceChild(fallback, img); };
    return img;
  }

  // Cap how many channel buttons exist in the DOM at once during search —
  // with 10k+ channels, rendering them all would stall every keystroke.
  var RENDER_CAP = 500;

  // Country/group sections the user has expanded (group name → true).
  var expandedGroups = {};

  function makeChannelButton(ch) {
    var btn = document.createElement('button');
    btn.className = 'channel-item' + (selected && selected.id === ch.id ? ' selected' : '') + (isDead(ch) ? ' dead' : '');
    btn.dataset.id = ch.id;
    btn.appendChild(logoOrFallback(ch, 'channel-logo', 'channel-logo-fallback'));
    var name = document.createElement('span');
    name.className = 'channel-name';
    name.textContent = ch.name;
    btn.appendChild(name);
    if (selected && selected.id === ch.id) {
      var dot = document.createElement('span');
      dot.className = 'channel-live-dot';
      btn.appendChild(dot);
    }
    btn.addEventListener('click', function () { selectChannel(ch); });
    return btn;
  }

  function renderChannelList() {
    var matches = filteredChannels();
    var keepScroll = els.channelList.scrollTop; // survive accordion re-renders

    // Remove previous dynamic nodes, keep the loading/empty-state nodes.
    Array.prototype.slice.call(els.channelList.querySelectorAll('.channel-item, .group-section')).forEach(function (n) { n.remove(); });

    els.listLoading.hidden = !channelsLoading;
    els.emptyState.hidden = channelsLoading || matches.length !== 0;

    var frag = document.createDocumentFragment();

    if (search) {
      // Searching: flat capped result list across all groups.
      matches.slice(0, RENDER_CAP).forEach(function (ch) { frag.appendChild(makeChannelButton(ch)); });
      els.channelList.appendChild(frag);
      els.channelList.scrollTop = 0; // new result set starts at the top
      els.channelCount.textContent = channelsLoading ? ''
        : matches.length > RENDER_CAP
          ? 'Showing first ' + RENDER_CAP + ' of ' + matches.length + ' matches — search to narrow'
          : matches.length + ' match' + (matches.length === 1 ? '' : 'es');
      return;
    }

    // Browsing: collapsible sections per group (country). Only expanded
    // sections render their channel buttons, which keeps the DOM small.
    var byGroup = {};
    var groupNames = [];
    matches.forEach(function (ch) {
      var g = ch.group || 'Other';
      if (!byGroup[g]) { byGroup[g] = []; groupNames.push(g); }
      byGroup[g].push(ch);
    });
    groupNames.sort(function (a, b) {
      a = a.toLowerCase(); b = b.toLowerCase();
      return a < b ? -1 : a > b ? 1 : 0;
    });

    groupNames.forEach(function (g) {
      var section = document.createElement('div');
      section.className = 'group-section';

      var head = document.createElement('button');
      head.className = 'group-header' + (expandedGroups[g] ? ' open' : '');
      var caret = document.createElement('span');
      caret.className = 'group-caret';
      caret.textContent = '▸';
      var title = document.createElement('span');
      title.className = 'group-title';
      title.textContent = countryLabel(g);
      var count = document.createElement('span');
      count.className = 'group-count';
      count.textContent = String(byGroup[g].length);
      head.appendChild(caret);
      head.appendChild(title);
      head.appendChild(count);
      head.addEventListener('click', function () {
        expandedGroups[g] = !expandedGroups[g];
        renderChannelList();
      });
      section.appendChild(head);

      if (expandedGroups[g]) {
        byGroup[g].forEach(function (ch) { section.appendChild(makeChannelButton(ch)); });
      }
      frag.appendChild(section);
    });

    els.channelList.appendChild(frag);
    els.channelList.scrollTop = keepScroll;
    els.channelCount.textContent = channelsLoading ? ''
      : channels.length + ' channels · ' + groupNames.length + ' countries';
  }

  // Update which sidebar button is highlighted without rebuilding the list —
  // a rebuild would reset the user's scroll position.
  function updateListSelection() {
    Array.prototype.slice.call(els.channelList.querySelectorAll('.channel-item')).forEach(function (btn) {
      var isSel = !!selected && btn.dataset.id === selected.id;
      btn.classList.toggle('selected', isSel);
      var dot = btn.querySelector('.channel-live-dot');
      if (isSel && !dot) {
        dot = document.createElement('span');
        dot.className = 'channel-live-dot';
        btn.appendChild(dot);
      } else if (!isSel && dot) {
        dot.remove();
      }
    });
  }

  function renderLayout() {
    // Desktop: hide sidebar when collapsed. Mobile: hide unless no channel
    // selected or "More Channels" was tapped.
    els.sidebar.classList.toggle('collapsed', !sidebarOpen);
    els.sidebar.classList.toggle('mobile-hidden', !!selected && !mobileChannelsOpen);
    els.carousel.hidden = !!selected;
    els.playerSection.hidden = !selected;
    els.activeBadge.hidden = !!selected || els.totalViewers.textContent === '';
  }

  // ── Carousel ─────────────────────────────────────────────────────────────
  var carouselIdx = 0;
  var carouselDir = 'right';
  var carouselTimer = null;

  function carouselChannels() {
    var byId = {};
    channels.forEach(function (ch) { byId[ch.id] = ch; });
    var top = topChannelIds.map(function (id) { return byId[id]; }).filter(Boolean);
    if (top.length < 5) {
      var inTop = {};
      top.forEach(function (ch) { inTop[ch.id] = true; });
      for (var i = 0; i < channels.length && top.length < 5; i++) {
        if (!inTop[channels[i].id]) top.push(channels[i]);
      }
    }
    return top;
  }

  function renderCarousel() {
    var list = carouselChannels();
    var total = list.length;
    els.carouselPrev.hidden = els.carouselNext.hidden = total === 0;

    if (total === 0) return; // keep the loading placeholder

    var featured = list[carouselIdx % total];
    var content = els.carouselContent;
    content.innerHTML = '';
    content.className = 'carousel-content ' + (carouselDir === 'right' ? 'slide-from-right' : 'slide-from-left');

    content.appendChild(logoOrFallback(featured, 'carousel-logo', 'carousel-logo-fallback'));

    var meta = document.createElement('div');
    var name = document.createElement('p');
    name.className = 'carousel-name';
    name.textContent = featured.name;
    var group = document.createElement('p');
    group.className = 'carousel-group';
    group.textContent = countryLabel(featured.group);
    meta.appendChild(name);
    meta.appendChild(group);
    content.appendChild(meta);

    var watch = document.createElement('button');
    watch.className = 'watch-btn';
    watch.innerHTML = '<span class="watch-dot"></span>Watch Live';
    watch.addEventListener('click', function () { selectChannel(featured); });
    content.appendChild(watch);

    // Dots (max 10)
    var dotCount = Math.min(total, 10);
    els.carouselDots.innerHTML = '';
    els.carouselDots.hidden = total <= 1;
    for (var i = 0; i < dotCount; i++) {
      (function (i) {
        var dot = document.createElement('button');
        dot.className = 'carousel-dot' + (i === carouselIdx % dotCount ? ' active' : '');
        dot.addEventListener('click', function () {
          carouselDir = i > carouselIdx ? 'right' : 'left';
          carouselIdx = i;
          renderCarousel();
          resetCarouselTimer();
        });
        els.carouselDots.appendChild(dot);
      })(i);
    }
  }

  function advanceCarousel(dir) {
    var total = carouselChannels().length;
    if (total === 0) return;
    carouselDir = dir === 1 ? 'right' : 'left';
    carouselIdx = (carouselIdx + dir + total) % total;
    renderCarousel();
  }

  function resetCarouselTimer() {
    if (carouselTimer) clearInterval(carouselTimer);
    carouselTimer = setInterval(function () {
      if (!selected) advanceCarousel(1);
    }, 4000);
  }

  els.carouselPrev.addEventListener('click', function () { advanceCarousel(-1); resetCarouselTimer(); });
  els.carouselNext.addEventListener('click', function () { advanceCarousel(1); resetCarouselTimer(); });

  // ── Player ───────────────────────────────────────────────────────────────
  function setPlayerState(state) {
    els.loadingOverlay.hidden = state !== 'loading';
    els.errorOverlay.hidden = state !== 'error';
  }

  function setQuality(text) {
    els.qualityBadge.textContent = text || '';
    els.qualityBadge.hidden = !text;
  }

  function destroyPlayer() {
    playToken++;
    if (hls) { hls.destroy(); hls = null; }
    els.video.removeAttribute('src');
    els.video.onloadedmetadata = null;
    els.video.onerror = null;
  }

  function currentServer() {
    if (!selected || !selected.servers || selected.servers.length === 0) return null;
    return selected.servers[Math.min(serverIdx, selected.servers.length - 1)];
  }

  // Try the next server that hasn't failed yet; show the error screen when
  // every server for this channel has been exhausted.
  function failover() {
    if (!selected) return;
    failedServers[serverIdx] = true;
    renderServerRow();
    for (var i = 0; i < selected.servers.length; i++) {
      if (!failedServers[i]) {
        serverIdx = i;
        renderServerRow();
        startPlayback();
        return;
      }
    }
    setDead(selected, true);
    setPlayerState('error');
  }

  function populateQualityOptions(h) {
    var sel = els.qualitySelect;
    sel.innerHTML = '';
    var auto = document.createElement('option');
    auto.value = '-1';
    auto.textContent = 'Auto';
    sel.appendChild(auto);
    // hls.js orders levels ascending by bitrate; show highest first.
    for (var i = h.levels.length - 1; i >= 0; i--) {
      var level = h.levels[i];
      var opt = document.createElement('option');
      opt.value = String(i);
      opt.textContent = level.height ? level.height + 'p' : Math.round(level.bitrate / 1000) + 'k';
      sel.appendChild(opt);
    }
    sel.value = '-1';
    els.qualityWrap.hidden = h.levels.length < 2;
    renderStreamOptionsBar();
  }

  els.qualitySelect.addEventListener('change', function () {
    if (!hls) return;
    // -1 = Auto (adaptive); any other index pins that level.
    hls.currentLevel = parseInt(els.qualitySelect.value, 10);
  });

  function startPlayback() {
    var server = currentServer();
    if (!server) { setPlayerState('error'); return; }
    destroyPlayer();
    var token = playToken;
    var video = els.video;
    setPlayerState('loading');
    setQuality('');
    els.qualityWrap.hidden = true;
    renderStreamOptionsBar();
    // One recovery attempt per error type, then move on to the next server —
    // retrying a dead stream forever would never surface the failover.
    var netRecoveries = 0;
    var mediaRecoveries = 0;

    if (window.Hls && Hls.isSupported()) {
      hls = new Hls({
        enableWorker: true,                 // off main thread — accurate bandwidth measurement
        lowLatencyMode: false,              // standard HLS streams, not LL-HLS
        capLevelToPlayerSize: false,        // never cap quality to player element size
        startLevel: -1,                     // ABR picks start, then we lock to highest after manifest
        startFragPrefetch: true,            // begin loading first fragment during manifest parse
        maxMaxBufferLength: 60,             // larger buffer → smoother ABR decisions
        backBufferLength: 0,                // live TV — no back buffer needed
        abrEwmaDefaultEstimate: 5000000,    // assume 5 Mbps upfront so ABR starts high
      });
      var h = hls;
      h.loadSource(proxyUrl(server.url));
      h.attachMedia(video);

      var updateQualityBadge = function (levelIndex) {
        var level = h.levels[levelIndex];
        if (!level) return;
        if (level.height) setQuality(level.height + 'p');
        else if (level.bitrate) setQuality(Math.round(level.bitrate / 1000) + 'k');
      };

      h.once(Hls.Events.MANIFEST_PARSED, function () {
        if (token !== playToken) return;
        populateQualityOptions(h);
        setPlayerState('playing');
        if (selected) setDead(selected, false); // it played — not dead after all
        video.play().catch(function () {});
      });

      h.on(Hls.Events.LEVEL_SWITCHED, function (_, data) {
        if (token === playToken) updateQualityBadge(data.level);
      });

      h.on(Hls.Events.ERROR, function (_, data) {
        if (!data.fatal || token !== playToken) return;
        if (data.type === Hls.ErrorTypes.NETWORK_ERROR && netRecoveries < 1) {
          netRecoveries++;
          h.startLoad();
        } else if (data.type === Hls.ErrorTypes.MEDIA_ERROR && mediaRecoveries < 1) {
          mediaRecoveries++;
          h.recoverMediaError();
        } else {
          failover();
        }
      });
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = proxyUrl(server.url);
      video.onloadedmetadata = function () {
        if (token !== playToken) return;
        setPlayerState('playing');
        if (selected) setDead(selected, false);
        video.play().catch(function () {});
      };
      video.onerror = function () { if (token === playToken) failover(); };
    } else {
      setPlayerState('error');
    }
  }

  els.retryBtn.addEventListener('click', function () {
    if (!selected) return;
    // Fresh start: forget which servers failed and retry from the first.
    failedServers = {};
    serverIdx = 0;
    renderServerRow();
    startPlayback();
  });

  function toggleFullscreen() {
    if (!document.fullscreenElement) els.videoContainer.requestFullscreen().catch(function () {});
    else document.exitFullscreen();
  }
  els.fsBtn.addEventListener('click', toggleFullscreen);

  // ── Now Playing bar ──────────────────────────────────────────────────────
  function renderNowPlaying() {
    if (!selected) return;
    els.npName.textContent = selected.name;
    els.npGroup.textContent = countryLabel(selected.group);

    els.npLogo.hidden = true;
    els.npLogoFallback.hidden = false;
    if (selected.logo) {
      els.npLogo.onerror = function () { els.npLogo.hidden = true; els.npLogoFallback.hidden = false; };
      els.npLogo.onload = function () { els.npLogo.hidden = false; els.npLogoFallback.hidden = true; };
      els.npLogo.src = selected.logo;
    }

    setCopied(false);
    renderViewerPill();
  }

  // The options bar collapses entirely when neither picker is relevant
  // (single-server channel playing a single-resolution stream).
  function renderStreamOptionsBar() {
    els.streamOptions.hidden = els.serverRow.hidden && els.qualityWrap.hidden;
  }

  function renderServerRow() {
    var servers = selected && selected.servers ? selected.servers : [];
    els.serverRow.hidden = servers.length < 2;
    els.serverButtons.innerHTML = '';
    if (servers.length >= 2) {
      servers.forEach(function (server, i) {
        var btn = document.createElement('button');
        btn.className = 'server-btn' + (i === serverIdx ? ' active' : '') + (failedServers[i] ? ' failed' : '');
        btn.textContent = String(i + 1);
        if (server.label) {
          var lbl = document.createElement('span');
          lbl.className = 'server-label';
          lbl.textContent = server.label;
          btn.appendChild(lbl);
        }
        btn.title = server.url;
        btn.addEventListener('click', function () {
          // A manual pick is a fresh vote of confidence — clear failed marks
          // so auto-failover can walk the full list again from here.
          failedServers = {};
          serverIdx = i;
          renderServerRow();
          startPlayback();
        });
        els.serverButtons.appendChild(btn);
      });
    }
    renderStreamOptionsBar();
  }

  function renderViewerPill() {
    if (channelViewers == null) { els.viewerPill.hidden = true; return; }
    els.viewerPill.hidden = false;
    els.viewerCount.textContent = formatViewers(channelViewers);
  }

  function setCopied(on) {
    els.copyBtn.classList.toggle('copied', on);
    els.copyBtn.querySelector('.copy-icon').hidden = on;
    els.copyBtn.querySelector('.copied-icon').hidden = !on;
    els.copyBtn.querySelector('.copy-label').textContent = on ? 'Copied!' : 'Copy URL';
  }

  els.copyBtn.addEventListener('click', function () {
    var server = currentServer();
    if (!server) return;
    var url = server.url;
    var done = function () {
      setCopied(true);
      if (copiedTimer) clearTimeout(copiedTimer);
      copiedTimer = setTimeout(function () { setCopied(false); }, 2000);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(url).then(done).catch(function () { legacyCopy(url); done(); });
    } else {
      legacyCopy(url);
      done();
    }
  });

  function legacyCopy(text) {
    // fallback for older Android WebViews
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    try { document.execCommand('copy'); } catch (e) { /* nothing else to try */ }
    document.body.removeChild(ta);
  }

  // ── Selection ────────────────────────────────────────────────────────────
  function selectChannel(ch) {
    selected = ch;
    serverIdx = 0;
    failedServers = {};
    mobileChannelsOpen = false;
    channelViewers = null; // reset immediately on channel switch
    renderLayout();
    updateListSelection();
    renderNowPlaying();
    renderServerRow();
    startPlayback();
    beat(); // immediate heartbeat to get fresh counts
  }

  function goHome() {
    selected = null;
    mobileChannelsOpen = false;
    destroyPlayer();
    setPlayerState('idle');
    renderLayout();
    updateListSelection();
    renderCarousel();
    beat();
  }

  els.homeBtn.addEventListener('click', goHome);
  els.sidebarToggle.addEventListener('click', function () { sidebarOpen = !sidebarOpen; renderLayout(); });
  els.moreChannels.addEventListener('click', function () { mobileChannelsOpen = !mobileChannelsOpen; renderLayout(); });

  // ── Search ───────────────────────────────────────────────────────────────
  function setSearch(value) {
    search = value;
    els.search.value = value;
    els.searchClear.hidden = !value;
    renderChannelList();
  }
  var searchDebounce = null;
  els.search.addEventListener('input', function () {
    if (searchDebounce) clearTimeout(searchDebounce);
    searchDebounce = setTimeout(function () { setSearch(els.search.value); }, 150);
  });
  els.searchClear.addEventListener('click', function () { setSearch(''); });
  els.emptyClear.addEventListener('click', function () { setSearch(''); });

  // ── Keyboard shortcuts ───────────────────────────────────────────────────
  window.addEventListener('keydown', function (e) {
    var inInput = document.activeElement && document.activeElement.tagName === 'INPUT';
    if (e.key === '/' && !inInput) {
      e.preventDefault();
      els.search.focus();
    }
    if (e.key === 'Escape' && document.activeElement === els.search) {
      setSearch('');
      els.search.blur();
    }
    if ((e.key === 'f' || e.key === 'F') && !inInput && selected) {
      toggleFullscreen();
    }
  });

  // ── Heartbeat ────────────────────────────────────────────────────────────
  var sessionId = sessionStorage.getItem('livetv_sid');
  if (!sessionId) {
    sessionId = Math.random().toString(36).slice(2) + Date.now().toString(36);
    sessionStorage.setItem('livetv_sid', sessionId);
  }

  function beat() {
    var channelId = selected ? selected.id : null;
    fetch('/api/viewers', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ sessionId: sessionId, channelId: channelId }),
    })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        els.totalViewers.textContent = String(d.total);
        els.activeBadge.hidden = !!selected;
        if (d.channelCount != null && selected && selected.id === channelId) {
          channelViewers = d.channelCount;
          renderViewerPill();
        }
        if (d.top && d.top.length > 0) {
          var ids = d.top.map(function (x) { return x.id; });
          if (ids.join(',') !== topChannelIds.join(',')) {
            topChannelIds = ids;
            if (!selected) renderCarousel();
          }
        }
      })
      .catch(function () {});
  }

  // ── Sync ─────────────────────────────────────────────────────────────────
  els.syncBtn.addEventListener('click', function () {
    var btn = els.syncBtn;
    btn.disabled = true;
    btn.textContent = 'Syncing…';
    fetch('/api/sync', { method: 'POST' })
      .then(function (r) {
        if (!r.ok) throw new Error('sync failed: ' + r.status);
        return r.json();
      })
      .then(function () {
        btn.textContent = '⟳ Sync';
        btn.disabled = false;
        loadChannels(); // pull the refreshed list into the UI
      })
      .catch(function () {
        btn.textContent = 'Sync failed';
        setTimeout(function () {
          btn.textContent = '⟳ Sync';
          btn.disabled = false;
        }, 3000);
      });
  });

  // ── Init ─────────────────────────────────────────────────────────────────
  function loadChannels() {
    fetch('/api/channels')
      .then(function (r) {
        if (!r.ok) throw new Error('channels fetch failed: ' + r.status);
        return r.json();
      })
      .then(function (data) {
        channels = Array.isArray(data) ? data : [];
        channelsLoading = false;
        renderChannelList();
        if (!selected) renderCarousel();
      })
      .catch(function () {
        channelsLoading = false;
        renderChannelList(); // falls through to the empty state
      });
  }

  renderLayout();
  renderChannelList();
  renderCarousel();
  resetCarouselTimer();
  loadChannels();
  beat();
  setInterval(beat, 30000);

  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/static/sw.js').catch(function (err) {
      console.warn('SW registration failed:', err);
    });
  }
})();
