package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const novncVersion = "1.4.0"
const novncCDN = "https://cdn.jsdelivr.net/npm/@novnc/novnc@" + novncVersion + "/"
const novncCacheBase = configDir + "/novnc"

// serveNoVNCCore serves any noVNC file (core/, vendor/, etc.) from a local
// cache. On cache miss it fetches from the npm CDN, caches, and serves.
func serveNoVNCCore(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/static/novnc/")
	if strings.Contains(rel, "..") || strings.Contains(rel, "\x00") {
		http.NotFound(w, r)
		return
	}
	// vnc.html lives in the embedded FS — serve it directly
	if rel == "" || rel == "vnc.html" {
		data, _ := staticFiles.ReadFile("static/novnc/vnc.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
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
