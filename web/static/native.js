// native.js — bridge to the native Windows shell (cmd/watchlive-native).
//
// The WebView2 window is a "remote": it drives external mpv player windows over
// a JSON bridge. Each screen tile maps 1:1 to an mpv window, keyed by a stable
// id. mpv owns playback and its own on-screen controls (play/pause/seek/mute/
// fullscreen), so this module only sends commands and receives screen state —
// no rect-tracking, no fullscreen plumbing, no <video>.
//
// It imports nothing from the rest of the app (other modules import from here),
// so there's no import cycle.

export const NATIVE = !!(window.chrome && window.chrome.webview && window.chrome.webview.postMessage);

// Tag the document so native-only CSS (screen-tile styling) applies.
if (NATIVE) {
  try { document.documentElement.classList.add('native'); } catch (e) { /* pre-DOM */ }
}

// postNative sends one message to Go as a JSON string (WebView2 delivers it via
// TryGetWebMessageAsString).
export function postNative(msg) {
  if (!NATIVE) return;
  try { window.chrome.webview.postMessage(JSON.stringify(msg)); } catch (e) { /* host gone */ }
}

// Screen commands — id is a cell's stable native id (cell.nid).
export function openScreen(id) { postNative({ t: 'open', id: id }); }
export function playScreen(id, url, referer, ua) {
  postNative({ t: 'play', id: id, url: url, referer: referer || '', ua: ua || '' });
}
export function stopScreen(id) { postNative({ t: 'stop', id: id }); }
export function closeScreen(id) { postNative({ t: 'close', id: id }); }
export function audioScreen(id) { postNative({ t: 'audio', id: id }); }
export function focusScreen(id) { postNative({ t: 'focus', id: id }); }

// Go→JS: window.__native.onScreen({id,state}) reports mpv state per screen;
// window.__native.onClosed({id}) fires when the user closes an mpv window.
const screenHandlers = [];
const closedHandlers = [];
export function onNativeScreen(fn) { screenHandlers.push(fn); }
export function onNativeClosed(fn) { closedHandlers.push(fn); }

if (NATIVE) {
  window.__native = window.__native || {};
  window.__native.onScreen = function (ev) {
    screenHandlers.forEach(function (fn) { try { fn(ev); } catch (e) { /* isolate */ } });
  };
  window.__native.onClosed = function (ev) {
    closedHandlers.forEach(function (fn) { try { fn(ev); } catch (e) { /* isolate */ } });
  };
}
