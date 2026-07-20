# Third-party notices

WatchLive's native Windows build (`watchlive-native.exe`) dynamically links a
few third-party components that are distributed alongside it. Their licenses are
reproduced/linked below.

## mpv player (`mpv.exe`)

The native shell plays video by launching the **[mpv media player](https://mpv.io/)**
as a separate process (`mpv.exe`) and controlling it over mpv's JSON IPC. The
binary is the prebuilt Windows **player** build from **shinchiro / mpv-player-windows**
(<https://sourceforge.net/projects/mpv-player-windows/files/64bit/>).

The mpv **player** build is licensed under the **GNU General Public License,
version 2 or later (GPL-2.0-or-later)** (unlike the LGPL libmpv library). It also
bundles **FFmpeg**. WatchLive does not modify or statically link mpv — it ships
the unmodified upstream binary and invokes it as a standalone process — so you
may replace `mpv.exe` with your own compatible build. Source for mpv is at
<https://github.com/mpv-player/mpv> and for FFmpeg at <https://ffmpeg.org/>.

Full license text: <https://www.gnu.org/licenses/old-licenses/gpl-2.0.html>

## FFmpeg (`ffmpeg.exe`, recording)

The optional screen-recording feature shells out to **FFmpeg** (`ffmpeg.exe`),
fetched at build time from the gyan.dev builds. FFmpeg is licensed under
LGPL-2.1-or-later / GPL depending on the build; see <https://ffmpeg.org/legal.html>.

## Microsoft Edge WebView2

The UI is hosted in the **Microsoft Edge WebView2** runtime, which is installed
on the system (Evergreen Runtime, bundled with Windows 10 22H2) and is **not**
redistributed with WatchLive. The `WebView2Loader` shim is provided by the
[jchv/go-webview2](https://github.com/jchv/go-webview2) Go module (MIT) and is
loaded from memory, so no `WebView2Loader.dll` is shipped.

## Go modules

See `go.mod` / `go.sum` for the full dependency set. Notable native-shell deps:

- `github.com/jchv/go-webview2` — MIT
- `github.com/jchv/go-winloader` — MIT
- `github.com/Microsoft/go-winio` — MIT (named-pipe client for mpv's IPC)
- `golang.org/x/sys` — BSD-3-Clause
