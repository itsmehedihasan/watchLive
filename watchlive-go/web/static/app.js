/* LiveTV front-end — vanilla JS port of the original React app.
   State lives in module scope; render functions re-paint the affected DOM. */
(function () {
  'use strict';

  // ── State ────────────────────────────────────────────────────────────────
  var channels = [];            // fetched from /api/channels (gzipped, ETag-cached)
  var channelsLoading = true;
  var selected = null;          // currently playing channel or null
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
    channelCount: $('channelCount'),
    carousel: $('carousel'), carouselContent: $('carouselContent'),
    carouselPrev: $('carouselPrev'), carouselNext: $('carouselNext'), carouselDots: $('carouselDots'),
    playerSection: $('playerSection'), videoContainer: $('videoContainer'), video: $('video'),
    loadingOverlay: $('loadingOverlay'), errorOverlay: $('errorOverlay'), retryBtn: $('retryBtn'),
    qualityBadge: $('qualityBadge'), fsBtn: $('fsBtn'),
    npLogo: $('npLogo'), npLogoFallback: $('npLogoFallback'),
    npName: $('npName'), npGroup: $('npGroup'),
    viewerPill: $('viewerPill'), viewerCount: $('viewerCount'),
    copyBtn: $('copyBtn'), moreChannels: $('moreChannels'),
  };

  function proxyUrl(url) { return '/api/proxy?url=' + encodeURIComponent(url); }

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

  // Cap how many channel buttons exist in the DOM at once — with 10k+
  // channels, rendering them all would stall every keystroke. Search
  // narrows into the long tail.
  var RENDER_CAP = 500;

  function renderChannelList() {
    var matches = filteredChannels();
    var list = matches.slice(0, RENDER_CAP);

    // Remove previous channel buttons, keep the loading/empty-state nodes.
    Array.prototype.slice.call(els.channelList.querySelectorAll('.channel-item')).forEach(function (n) { n.remove(); });

    els.listLoading.hidden = !channelsLoading;
    els.emptyState.hidden = channelsLoading || list.length !== 0;

    var frag = document.createDocumentFragment();
    list.forEach(function (ch) {
      var btn = document.createElement('button');
      btn.className = 'channel-item' + (selected && selected.id === ch.id ? ' selected' : '');
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
      frag.appendChild(btn);
    });
    els.channelList.appendChild(frag);

    els.channelCount.textContent = channelsLoading ? ''
      : matches.length > RENDER_CAP
        ? 'Showing first ' + RENDER_CAP + ' of ' + matches.length + ' matches — search to narrow'
        : 'Showing ' + matches.length + ' of ' + channels.length + ' channels';
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
    group.textContent = featured.group;
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

  function startPlayback(channel) {
    destroyPlayer();
    var token = playToken;
    var video = els.video;
    setPlayerState('loading');
    setQuality('');

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
      h.loadSource(proxyUrl(channel.url));
      h.attachMedia(video);

      var updateQualityBadge = function (levelIndex) {
        var level = h.levels[levelIndex];
        if (!level) return;
        if (level.height) setQuality(level.height + 'p');
        else if (level.bitrate) setQuality(Math.round(level.bitrate / 1000) + 'k');
      };

      h.once(Hls.Events.MANIFEST_PARSED, function () {
        if (token !== playToken) return;
        if (h.levels.length > 0) {
          var maxLevel = h.levels.length - 1;
          h.currentLevel = maxLevel;
          h.nextAutoLevel = maxLevel;       // prevent ABR drifting back down after error recovery
          updateQualityBadge(maxLevel);
        }
        setPlayerState('playing');
        video.play().catch(function () {});
      });

      h.on(Hls.Events.LEVEL_SWITCHED, function (_, data) {
        if (token === playToken) updateQualityBadge(data.level);
      });

      h.on(Hls.Events.ERROR, function (_, data) {
        if (!data.fatal || token !== playToken) return;
        if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
          h.startLoad();
        } else if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
          h.recoverMediaError();
        } else {
          setPlayerState('error');
        }
      });
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = proxyUrl(channel.url);
      video.onloadedmetadata = function () {
        if (token !== playToken) return;
        setPlayerState('playing');
        video.play().catch(function () {});
      };
      video.onerror = function () { if (token === playToken) setPlayerState('error'); };
    } else {
      setPlayerState('error');
    }
  }

  els.retryBtn.addEventListener('click', function () { if (selected) startPlayback(selected); });

  function toggleFullscreen() {
    if (!document.fullscreenElement) els.videoContainer.requestFullscreen().catch(function () {});
    else document.exitFullscreen();
  }
  els.fsBtn.addEventListener('click', toggleFullscreen);

  // ── Now Playing bar ──────────────────────────────────────────────────────
  function renderNowPlaying() {
    if (!selected) return;
    els.npName.textContent = selected.name;
    els.npGroup.textContent = selected.group;

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
    if (!selected) return;
    var url = selected.url;
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
    mobileChannelsOpen = false;
    channelViewers = null; // reset immediately on channel switch
    renderLayout();
    renderChannelList();
    renderNowPlaying();
    startPlayback(ch);
    beat(); // immediate heartbeat to get fresh counts
  }

  function goHome() {
    selected = null;
    mobileChannelsOpen = false;
    destroyPlayer();
    setPlayerState('idle');
    renderLayout();
    renderChannelList();
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
})();
