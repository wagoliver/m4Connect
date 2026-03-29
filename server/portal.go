package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	psnet "github.com/shirou/gopsutil/v3/net"
)

//go:embed static
var staticFiles embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ── WebSocket hub ─────────────────────────────────────────────────────────────

type hub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]bool
}

func newHub() *hub { return &hub{clients: make(map[*websocket.Conn]bool)} }

func (h *hub) add(c *websocket.Conn) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
}

func (h *hub) remove(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *hub) broadcast(data any) {
	b, _ := json.Marshal(data)
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
			c.Close()
			delete(h.clients, c)
		}
	}
}

// ── Stats ─────────────────────────────────────────────────────────────────────

type Stats struct {
	// CPU
	CPUPercent   float64  `json:"cpu_percent"`
	CPUIdle      float64  `json:"cpu_idle"`
	LoadAvg1     float64  `json:"load_avg_1"`
	LoadAvg5     float64  `json:"load_avg_5"`
	ProcessCount int      `json:"process_count"`
	ThreadCount  int      `json:"thread_count"`
	PowerW       *float64 `json:"power_w"`

	// Memory (GB)
	RAMUsedGB   float64 `json:"ram_used_gb"`
	RAMTotalGB  float64 `json:"ram_total_gb"`
	RAMPercent  float64 `json:"ram_percent"`
	AppMemGB    float64 `json:"app_mem_gb"`
	WiredMemGB  float64 `json:"wired_mem_gb"`
	CompMemGB   float64 `json:"comp_mem_gb"`
	CachedMemGB float64 `json:"cached_mem_gb"`
	SwapUsedGB  float64 `json:"swap_used_gb"`
	SwapTotalGB float64 `json:"swap_total_gb"`

	// Network (bytes/sec)
	NetRxBPS float64 `json:"net_rx_bps"`
	NetTxBPS float64 `json:"net_tx_bps"`

	// Disk
	DiskUsedGB   float64 `json:"disk_used_gb"`
	DiskTotalGB  float64 `json:"disk_total_gb"`
	DiskReadBPS  float64 `json:"disk_read_bps"`
	DiskWriteBPS float64 `json:"disk_write_bps"`

	// System
	UptimeSecs uint64 `json:"uptime_secs"`
	Hostname   string `json:"hostname"`
	OSVersion  string `json:"os_version"`
	ChipInfo   string `json:"chip_info"`
	MacIP      string `json:"mac_ip"`

	Services ServicesStatus `json:"services"`
}

// ── macOS helpers ─────────────────────────────────────────────────────────────

func getOSVersion() string {
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return "macOS"
	}
	return "macOS " + strings.TrimSpace(string(out))
}

func getChipInfo() string {
	out, err := exec.Command("system_profiler", "SPHardwareDataType").Output()
	if err != nil {
		return "Apple Silicon"
	}
	var chip, cores, ramInfo string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Chip:"):
			chip = strings.TrimSpace(strings.TrimPrefix(line, "Chip:"))
		case strings.HasPrefix(line, "Total Number of Cores:"):
			cores = strings.TrimSpace(strings.TrimPrefix(line, "Total Number of Cores:"))
		case strings.HasPrefix(line, "Memory:"):
			ramInfo = strings.TrimSpace(strings.TrimPrefix(line, "Memory:"))
		}
	}
	if chip != "" {
		return fmt.Sprintf("%s · %s · %s Unified Memory", chip, cores, ramInfo)
	}
	return "Apple Silicon"
}

func getMemBreakdown() (appGB, wiredGB, compGB, cachedGB float64) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return
	}
	s := string(out)
	pageSize := uint64(16384)
	rePSize := regexp.MustCompile(`page size of (\d+) bytes`)
	if m := rePSize.FindStringSubmatch(s); m != nil {
		fmt.Sscanf(m[1], "%d", &pageSize)
	}
	getPages := func(key string) uint64 {
		re := regexp.MustCompile(regexp.QuoteMeta(key) + `\s+(\d+)`)
		if m := re.FindStringSubmatch(s); m != nil {
			var v uint64
			fmt.Sscanf(m[1], "%d", &v)
			return v
		}
		return 0
	}
	toGB := func(pages uint64) float64 {
		return float64(pages*pageSize) / 1e9
	}
	appGB = toGB(getPages("Pages active:"))
	wiredGB = toGB(getPages("Pages wired down:"))
	compGB = toGB(getPages("Pages stored in compressor:"))
	cachedGB = toGB(getPages("File-backed pages:"))
	return
}

func getProcessStats() (procs, threads int) {
	out, err := exec.Command("sh", "-c", "ps -Ax | wc -l").Output()
	if err == nil {
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &procs)
		procs-- // remove header line
	}
	out, err = exec.Command("sh", "-c", "ps -Ao nlwp | awk 'NR>1{s+=$1}END{print s+0}'").Output()
	if err == nil {
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &threads)
	}
	return
}

// ── CPU Power poller (cached, runs async) ────────────────────────────────────

var (
	cachedPower     *float64
	cachedPowerMu   sync.RWMutex
	powerPollerOnce sync.Once
)

func startPowerPoller() {
	powerPollerOnce.Do(func() {
		poll := func() {
			out, err := exec.Command("powermetrics", "--samplers", "cpu_power", "-n", "1", "-i", "1").Output()
			if err != nil {
				log.Printf("[power] powermetrics error: %v", err)
				return
			}
			re := regexp.MustCompile(`CPU Power:\s*(\d+)\s*mW`)
			if m := re.FindSubmatch(out); m != nil {
				var mw float64
				fmt.Sscanf(string(m[1]), "%f", &mw)
				w := mw / 1000.0
				cachedPowerMu.Lock()
				cachedPower = &w
				cachedPowerMu.Unlock()
			}
		}
		go func() {
			for {
				poll()
				time.Sleep(10 * time.Second)
			}
		}()
	})
}

func getCachedPower() *float64 {
	cachedPowerMu.RLock()
	defer cachedPowerMu.RUnlock()
	return cachedPower
}

// ── Rate tracker ──────────────────────────────────────────────────────────────

type rateTracker struct {
	mu       sync.Mutex
	prevNet  []psnet.IOCountersStat
	prevDisk map[string]disk.IOCountersStat
	prevTime time.Time
}

func newRateTracker() *rateTracker {
	return &rateTracker{prevTime: time.Now()}
}

func (rt *rateTracker) sample() (rxBPS, txBPS, rdBPS, wrBPS float64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(rt.prevTime).Seconds()
	if elapsed < 0.1 {
		elapsed = 2.0
	}

	// Network (aggregate all interfaces)
	netCounters, _ := psnet.IOCounters(false)
	if len(rt.prevNet) > 0 && len(netCounters) > 0 {
		rx := float64(netCounters[0].BytesRecv-rt.prevNet[0].BytesRecv) / elapsed
		tx := float64(netCounters[0].BytesSent-rt.prevNet[0].BytesSent) / elapsed
		if rx >= 0 {
			rxBPS = rx
		}
		if tx >= 0 {
			txBPS = tx
		}
	}
	rt.prevNet = netCounters

	// Disk I/O
	diskCounters, _ := disk.IOCounters()
	if rt.prevDisk != nil {
		var totalRd, totalWr uint64
		for name, cur := range diskCounters {
			if prev, ok := rt.prevDisk[name]; ok {
				if cur.ReadBytes >= prev.ReadBytes {
					totalRd += cur.ReadBytes - prev.ReadBytes
				}
				if cur.WriteBytes >= prev.WriteBytes {
					totalWr += cur.WriteBytes - prev.WriteBytes
				}
			}
		}
		rdBPS = float64(totalRd) / elapsed
		wrBPS = float64(totalWr) / elapsed
	}
	rt.prevDisk = diskCounters
	rt.prevTime = now
	return
}

// ── Slow info cache ───────────────────────────────────────────────────────────

var (
	slowInfoOnce    sync.Once
	cachedOSVersion string
	cachedChipInfo  string
)

func initSlowInfo() {
	slowInfoOnce.Do(func() {
		cachedOSVersion = getOSVersion()
		cachedChipInfo = getChipInfo()
	})
}

// ── collectStats ──────────────────────────────────────────────────────────────

func collectStats(rt *rateTracker, bindIP string) Stats {
	initSlowInfo()

	cpuPct, _ := cpu.Percent(200*time.Millisecond, false)
	v, _ := mem.VirtualMemory()
	swap, _ := mem.SwapMemory()
	info, _ := host.Info()
	avg, _ := load.Avg()
	du, _ := disk.Usage("/")

	appGB, wiredGB, compGB, cachedGB := getMemBreakdown()
	procs, threads := getProcessStats()
	rxBPS, txBPS, rdBPS, wrBPS := rt.sample()

	var cpuVal float64
	if len(cpuPct) > 0 {
		cpuVal = cpuPct[0]
	}

	var la1, la5 float64
	if avg != nil {
		la1, la5 = avg.Load1, avg.Load5
	}

	var swapUsed, swapTotal float64
	if swap != nil {
		swapUsed = float64(swap.Used) / 1e9
		swapTotal = float64(swap.Total) / 1e9
	}

	var diskUsed, diskTotal float64
	if du != nil {
		diskUsed = float64(du.Used) / 1e9
		diskTotal = float64(du.Total) / 1e9
	}

	var ramUsed, ramTotal float64
	var ramPct float64
	if v != nil {
		ramUsed = float64(v.Used) / 1e9
		ramTotal = float64(v.Total) / 1e9
		ramPct = v.UsedPercent
	}

	return Stats{
		CPUPercent:   cpuVal,
		CPUIdle:      100 - cpuVal,
		LoadAvg1:     la1,
		LoadAvg5:     la5,
		ProcessCount: procs,
		ThreadCount:  threads,
		PowerW:       getCachedPower(),

		RAMUsedGB:   ramUsed,
		RAMTotalGB:  ramTotal,
		RAMPercent:  ramPct,
		AppMemGB:    appGB,
		WiredMemGB:  wiredGB,
		CompMemGB:   compGB,
		CachedMemGB: cachedGB,
		SwapUsedGB:  swapUsed,
		SwapTotalGB: swapTotal,

		NetRxBPS: rxBPS,
		NetTxBPS: txBPS,

		DiskUsedGB:   diskUsed,
		DiskTotalGB:  diskTotal,
		DiskReadBPS:  rdBPS,
		DiskWriteBPS: wrBPS,

		UptimeSecs: info.Uptime,
		Hostname:   info.Hostname,
		OSVersion:  cachedOSVersion,
		ChipInfo:   cachedChipInfo,
		MacIP:      bindIP,

		Services: GetAllServices(),
	}
}

// ── Portal server ─────────────────────────────────────────────────────────────

func startPortal(ctx context.Context, bindIP string, port int, store *Storage, convStore *ConvStore, embedQueue *EmbedQueue) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC no portal: %v", r)
		}
	}()

	h := newHub()
	rt := newRateTracker()
	startPowerPoller()

	go initSlowInfo()

	mux := http.NewServeMux()

	sub, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFiles.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html")
		w.Write(data)
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(collectStats(rt, bindIP))
	})

	mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(GetAllServices())
	})

	mux.HandleFunc("/api/services/vnc/on", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"ok": EnableVNC()})
	})
	mux.HandleFunc("/api/services/vnc/off", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"ok": DisableVNC()})
	})
	mux.HandleFunc("/api/services/ssh/on", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"ok": EnableSSH()})
	})
	mux.HandleFunc("/api/services/ssh/off", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"ok": DisableSSH()})
	})

	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"version": Version,
			"build":   BuildHash,
		})
	})

	mux.HandleFunc("/api/inference", handleInference)

	mux.HandleFunc("/api/speedtest/ping", handleSpeedtestPing)
	mux.HandleFunc("/api/speedtest/download", handleSpeedtestDownload)
	mux.HandleFunc("/api/speedtest/upload", handleSpeedtestUpload)

	mux.HandleFunc("/api/ollama", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CheckOllama())
	})

	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if store == nil {
			json.NewEncoder(w).Encode(HistoryResponse{Period: "–", Points: []StatPoint{}})
			return
		}
		period := r.URL.Query().Get("period")
		if period == "" {
			period = "24h"
		}
		json.NewEncoder(w).Encode(store.QueryHistory(period))
	})

	// ── Conversations ─────────────────────────────────────────────────────────

	mux.HandleFunc("GET /api/conversations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if convStore == nil {
			json.NewEncoder(w).Encode([]Conversation{})
			return
		}
		list, _ := convStore.ListConversations()
		json.NewEncoder(w).Encode(list)
	})

	mux.HandleFunc("POST /api/conversations", func(w http.ResponseWriter, r *http.Request) {
		if convStore == nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Title string `json:"title"`
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Title == "" {
			req.Title = "Nova conversa"
		}
		conv, err := convStore.CreateConversation(req.Title, req.Model)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(conv)
	})

	mux.HandleFunc("GET /api/conversations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if convStore == nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("id")
		conv, err := convStore.GetConversation(id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		messages, _ := convStore.GetMessages(id)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"conversation": conv,
			"messages":     messages,
			"model":        conv.Model,
		})
	})

	mux.HandleFunc("DELETE /api/conversations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if convStore == nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		convStore.DeleteConversation(r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("PATCH /api/conversations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if convStore == nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Title string `json:"title"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Title != "" {
			convStore.UpdateTitle(r.PathValue("id"), req.Title)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/conversations/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		if convStore == nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		convID := r.PathValue("id")
		var req struct {
			ID       string `json:"id"`
			Role     string `json:"role"`
			Content  string `json:"content"`
			Position int    `json:"position"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.ID == "" {
			req.ID = newID()
		}
		if err := convStore.SaveMessage(convID, req.ID, req.Role, req.Content, req.Position); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Queue embedding for assistant messages (best-effort)
		if embedQueue != nil && req.Role == "assistant" && req.Content != "" {
			embedQueue.Submit(convID, req.ID, req.Content)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": req.ID})
	})

	// ── RAG ───────────────────────────────────────────────────────────────────

	mux.HandleFunc("POST /api/rag/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if convStore == nil || embedQueue == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"results": []RAGResult{}, "context": "",
			})
			return
		}
		var req struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Limit <= 0 {
			req.Limit = 5
		}
		vec, err := embedQueue.fetchEmbedding(req.Query)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"results": []RAGResult{}, "context": "", "error": err.Error(),
			})
			return
		}
		results, err := SearchRAG(convStore, vec, req.Limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": results,
			"context": FormatRAGContext(results),
		})
	})

	// ── Apps ──────────────────────────────────────────────────────────────────

	mux.HandleFunc("GET /api/apps", handleListApps)
	mux.HandleFunc("GET /api/apps/{id}/install", handleAppInstall)
	mux.HandleFunc("GET /api/apps/{id}/uninstall", handleAppUninstall)

	mux.HandleFunc("GET /api/rag/status", func(w http.ResponseWriter, r *http.Request) {
		model := "nomic-embed-text"
		var total, qlen int
		if convStore != nil {
			total = convStore.CountEmbeddings()
		}
		if embedQueue != nil {
			qlen = embedQueue.Len()
			model = embedQueue.model
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"model":            model,
			"queue_len":        qlen,
			"total_embeddings": total,
		})
	})

	mux.HandleFunc("/ws/terminal", handleTerminal)

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		h.add(conn)
		defer h.remove(conn)
		b, _ := json.Marshal(collectStats(rt, bindIP))
		conn.WriteMessage(websocket.TextMessage, b)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	})

	// Stats broadcast — para quando a sessão encerra
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.broadcast(collectStats(rt, bindIP))
			}
		}
	}()

	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", bindIP, port),
		Handler: mux,
	}

	// Desliga o servidor quando o cabo for removido (ctx cancelado)
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
		log.Println("Portal encerrado.")
	}()

	log.Printf("Portal: http://%s:%d", bindIP, port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("Portal erro: %v", err)
	}
}
