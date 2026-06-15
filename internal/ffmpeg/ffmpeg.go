// Package ffmpeg embeds an ffmpeg executable into the binary and extracts it on
// demand, so the whole app can ship as a single self-contained file. You can't
// exec a program straight from embed.FS (it lives in memory), so Resolve writes
// the embedded copy to a cache file once and returns that path. When no binary
// is embedded (e.g. a dev build where bin/ holds only .gitkeep), it falls back
// to an ffmpeg found on PATH, and finally to "" (recording disabled).
package ffmpeg

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

//go:embed all:bin
var binFS embed.FS

func embeddedName() string {
	if runtime.GOOS == "windows" {
		return "ffmpeg.exe"
	}
	return "ffmpeg"
}

// Resolve returns a usable ffmpeg path: the embedded copy (extracted once),
// then a PATH install, then "" when neither is available.
func Resolve() string {
	if path, ok := extractEmbedded(); ok {
		return path
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

func extractEmbedded() (string, bool) {
	data, err := binFS.ReadFile("bin/" + embeddedName())
	if err != nil || len(data) == 0 {
		return "", false
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "watchlive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	// Size in the name doubles as a cheap version check: a new embedded build
	// has a different size and extracts to a fresh file instead of reusing a
	// stale one.
	target := filepath.Join(dir, fmt.Sprintf("ffmpeg-%d%s", len(data), ext))
	if fi, err := os.Stat(target); err == nil && fi.Size() == int64(len(data)) {
		return target, true
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return "", false
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return "", false
	}
	return target, true
}
