//go:build windows

package native

import (
	"os"
	"os/exec"
	"path/filepath"
)

// ResolveMPV locates the mpv.exe player the native shell drives. Unlike ffmpeg
// (which is embedded and extracted), mpv.exe is large and ships loose beside the
// binary — build.ps1 / run-native.ps1 stage it from .\mpv\. Resolution order:
// mpv.exe next to our exe, then .\mpv\mpv.exe next to our exe, then PATH. Returns
// "" if none is found (the caller shows an install prompt).
func ResolveMPV() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		for _, cand := range []string{
			filepath.Join(dir, "mpv.exe"),
			filepath.Join(dir, "mpv", "mpv.exe"),
		} {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand
			}
		}
	}
	if p, err := exec.LookPath("mpv"); err == nil {
		return p
	}
	return ""
}
