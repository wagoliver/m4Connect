package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

const maxDownloadSize = 500 * 1024 * 1024 // 500 MB cap

func handleSpeedtestPing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]int64{"ts": time.Now().UnixNano()})
}

func handleSpeedtestDownload(w http.ResponseWriter, r *http.Request) {
	size := 100 * 1024 * 1024 // 100 MB default
	if s, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil && s > 0 && s <= maxDownloadSize {
		size = s
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(size))
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64*1024)
	written := 0
	for written < size {
		n := len(buf)
		if written+n > size {
			n = size - written
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return
		}
		written += n
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func handleSpeedtestUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	n, _ := io.Copy(io.Discard, r.Body)
	elapsed := time.Since(start).Seconds()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"bytes":   n,
		"elapsed": elapsed,
	})
}
