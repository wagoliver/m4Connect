package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const novncVersion = "1.4.0"
const novncCDN = "https://cdn.jsdelivr.net/npm/@novnc/novnc@" + novncVersion + "/core/"
const novncCacheBase = configDir + "/novnc"

// serveNoVNCCore serves noVNC core files from a local cache.
// On cache miss it fetches from CDN, caches, and serves.
// This avoids embedding large JS files in the binary while still
// working offline after the first load.
func serveNoVNCCore(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/static/novnc/core/")
	if rel == "" || strings.Contains(rel, "..") || strings.Contains(rel, "\x00") {
		http.NotFound(w, r)
		return
	}

	cachePath := filepath.Join(novncCacheBase, rel)

	// Serve from cache if available
	if data, err := os.ReadFile(cachePath); err == nil {
		setNoVNCContentType(w, rel)
		w.Write(data)
		return
	}

	// Fetch from CDN
	resp, err := http.Get(novncCDN + rel)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.NotFound(w, r)
		return
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "upstream read error", http.StatusInternalServerError)
		return
	}

	// Cache for next time
	_ = os.MkdirAll(filepath.Dir(cachePath), 0755)
	_ = os.WriteFile(cachePath, data, 0644)

	setNoVNCContentType(w, rel)
	w.Write(data)
}

func setNoVNCContentType(w http.ResponseWriter, path string) {
	switch filepath.Ext(path) {
	case ".js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".wasm":
		w.Header().Set("Content-Type", "application/wasm")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
}
