package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/getlantern/systray"
)

// logWriter redireciona log.Printf para o painel de log no browser via SSE
type logWriter struct{}

func (lw logWriter) Write(p []byte) (n int, err error) {
	line := strings.TrimSpace(string(p))
	if line != "" {
		select {
		case broadcast <- Event{Step: "log", Status: "info", Detail: line}:
		default:
		}
	}
	return len(p), nil
}

//go:embed ui
var uiFiles embed.FS

//go:embed ui/icon/Ionic-Ionicons-Logo-apple.32.png
var trayIconPNG []byte

const localPort = 12345

// ── Config ────────────────────────────────────────────────────────────────────

var configFile = filepath.Join(os.Getenv("USERPROFILE"), ".m4connect", "config.json")

type Config struct {
	Token         string `json:"token"`
	DefaultSubnet string `json:"default_subnet"`
	ClientSuffix  string `json:"client_suffix"`
	HandshakePort int    `json:"handshake_port"`
	LastIface     string `json:"last_iface,omitempty"`
}

func defaultCfg() Config {
	return Config{DefaultSubnet: "10.10.10", ClientSuffix: "2", HandshakePort: 54321}
}

func loadConfig() Config {
	cfg := defaultCfg()
	data, err := os.ReadFile(configFile)
	if err != nil {
		return cfg
	}
	json.Unmarshal(data, &cfg)
	return cfg
}

func saveConfigFile(cfg Config) {
	os.MkdirAll(filepath.Dir(configFile), 0755)
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(configFile, data, 0600)
}

func hasToken() bool { return loadConfig().Token != "" }

// ── SSE ───────────────────────────────────────────────────────────────────────

type Event struct {
	Step   string `json:"step"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

var sseClients = make(map[chan Event]bool)
var addClient = make(chan chan Event)
var removeClient = make(chan chan Event)
var broadcast = make(chan Event, 16)

var connecting bool

func runSSEHub() {
	for {
		select {
		case c := <-addClient:
			sseClients[c] = true
		case c := <-removeClient:
			delete(sseClients, c)
			close(c)
		case ev := <-broadcast:
			for c := range sseClients {
				select {
				case c <- ev:
				default:
				}
			}
		}
	}
}

func emit(step, status, detail string) {
	broadcast <- Event{Step: step, Status: status, Detail: detail}
}

// ── Connection state ──────────────────────────────────────────────────────────

type AppStatus struct {
	Connected   bool   `json:"connected"`
	PortalURL   string `json:"portal_url,omitempty"`
	HostnameURL string `json:"hostname_url,omitempty"`
	MacIP       string `json:"mac_ip,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
}

var appStatus AppStatus
var currentIface string

// ── Connection flow ───────────────────────────────────────────────────────────

func connectionFlow() {
	cfg := loadConfig()
	subnet := cfg.DefaultSubnet
	clientIP := fmt.Sprintf("%s.%s", subnet, cfg.ClientSuffix)
	serverIP := fmt.Sprintf("%s.1", subnet)

	iface := knownIfaceIfActive(cfg.LastIface)
	if iface != "" {
		log.Printf("Interface salva já ativa: %s", iface)
		emit("cable", "done", fmt.Sprintf("Interface: %s (salva)", iface))
	} else {
		emit("cable", "waiting", "Aguardando cabo Ethernet...")
		iface = waitForEthernetCable()
		log.Printf("Cabo detectado: %s", iface)
		emit("cable", "done", fmt.Sprintf("Interface: %s", iface))
	}
	currentIface = iface

	emit("ip", "waiting", fmt.Sprintf("Configurando %s...", clientIP))
	_, netshErr := configureIPWindowsDebug(iface, clientIP, "255.255.255.252")
	if netshErr != nil {
		log.Printf("Erro ao configurar IP: %v", netshErr)
		emit("ip", "error", fmt.Sprintf("Erro: %v", netshErr))
		return
	}
	actualIP := getInterfaceIP(iface)
	if !strings.Contains(actualIP, clientIP) {
		log.Printf("IP não aplicado (atual: %s) — rode como Administrador", actualIP)
		emit("ip", "error", fmt.Sprintf("netsh não aplicou o IP (atual: %s) — rode como Administrador", actualIP))
		return
	}
	log.Printf("IP configurado: %s em %s", clientIP, iface)
	emit("ip", "done", fmt.Sprintf("IP: %s", clientIP))

	connected := false
	defer func() {
		if !connected {
			releaseIPWindows(iface)
		}
	}()

	emit("handshake", "waiting", fmt.Sprintf("Procurando Mac Mini em %s...", serverIP))
	res, err := sendHandshake(serverIP, clientIP, cfg.Token, cfg.HandshakePort, 30*time.Second)
	if err != nil {
		log.Printf("Handshake falhou: %v", err)
		emit("handshake", "error", fmt.Sprintf("Falha: %v", err))
		return
	}
	log.Printf("Conectado: %s (%s) portal:%d", res.Hostname, res.MacIP, res.PortalPort)
	emit("handshake", "done", fmt.Sprintf("%s — %s", res.Hostname, res.MacIP))
	if cfg.LastIface != iface {
		cfg.LastIface = iface
		saveConfigFile(cfg)
	}

	emit("portal", "waiting", "Verificando portal...")
	rtt, err := pingRTT(res.MacIP)
	if err != nil {
		emit("portal", "done", "Portal disponível")
	} else {
		emit("portal", "done", fmt.Sprintf("Latência: %.0f ms", rtt))
	}

	portalURL   := fmt.Sprintf("http://%s:%d", res.MacIP, res.PortalPort)
	hostnameURL := fmt.Sprintf("http://%s.local:%d/", strings.TrimSuffix(res.Hostname, ".local"), res.PortalPort)
	emit("ready", "done", portalURL)

	connected = true
	appStatus = AppStatus{
		Connected:   true,
		PortalURL:   portalURL,
		HostnameURL: hostnameURL,
		MacIP:       res.MacIP,
		Hostname:    res.Hostname,
	}
}

// ── HTTP server ───────────────────────────────────────────────────────────────

func startLocalServer() {
	mux := http.NewServeMux()

	// UI files
	sub, _ := fs.Sub(uiFiles, "ui")
	mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(sub))))

	// Index — redirect so relative CSS/JS paths resolve under /ui/
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if !hasToken() {
			http.Redirect(w, r, "/ui/setup.html", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/ui/index.html", http.StatusFound)
	})

	// SSE endpoint
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch := make(chan Event, 8)
		addClient <- ch
		defer func() { removeClient <- ch }()

		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}

		for {
			select {
			case ev, open := <-ch:
				if !open {
					return
				}
				data, _ := json.Marshal(ev)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})

	// Config API
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var cfg Config
			json.NewDecoder(r.Body).Decode(&cfg)
			saveConfigFile(cfg)
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			return
		}
		json.NewEncoder(w).Encode(loadConfig())
	})

	// Connection status
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(appStatus)
	})

	// Disconnect
	mux.HandleFunc("/api/disconnect", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if currentIface != "" {
			releaseIPWindows(currentIface)
			currentIface = ""
		}
		appStatus = AppStatus{}
		connecting = false
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	// Ping — measures RTT to Mac Mini portal
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !appStatus.Connected || appStatus.PortalURL == "" {
			json.NewEncoder(w).Encode(map[string]interface{}{"ms": nil})
			return
		}
		start := time.Now()
		resp, err := http.Get(appStatus.PortalURL + "/api/status")
		ms := time.Since(start).Milliseconds()
		if err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"ms": nil})
			return
		}
		resp.Body.Close()
		json.NewEncoder(w).Encode(map[string]interface{}{"ms": ms})
	})

	// Proxy: Mac Mini live stats → browser (avoids CORS)
	mux.HandleFunc("/api/mac-stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !appStatus.Connected || appStatus.PortalURL == "" {
			w.Write([]byte("{}"))
			return
		}
		resp, err := http.Get(appStatus.PortalURL + "/api/status")
		if err != nil {
			w.Write([]byte("{}"))
			return
		}
		defer resp.Body.Close()
		var v interface{}
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			w.Write([]byte("{}"))
			return
		}
		json.NewEncoder(w).Encode(v)
	})

	// Start connection
	mux.HandleFunc("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		if !connecting {
			connecting = true
			appStatus = AppStatus{}
			go func() {
				connectionFlow()
				connecting = false
			}()
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	addr := fmt.Sprintf("127.0.0.1:%d", localPort)
	log.Printf("Local server: http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

const appWidth = 900
const appHeight = 640

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		openAppWindow(url)
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

// openAppWindow tries to open Edge or Chrome in --app mode with fixed dimensions.
// Falls back to the default browser if neither is found.
func openAppWindow(url string) {
	appFlag := fmt.Sprintf("--app=%s", url)
	sizeFlag := fmt.Sprintf("--window-size=%d,%d", appWidth, appHeight)

	candidates := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		os.ExpandEnv(`${LOCALAPPDATA}\Microsoft\Edge\Application\msedge.exe`),
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		os.ExpandEnv(`${LOCALAPPDATA}\Google\Chrome\Application\chrome.exe`),
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			exec.Command(path, appFlag, sizeFlag, "--disable-extensions").Start()
			return
		}
	}

	// Fallback: default browser, no size control
	exec.Command("cmd", "/c", "start", "", url).Start()
}

// ── Tray ──────────────────────────────────────────────────────────────────────

// pngToICO wraps PNG bytes in a minimal ICO container.
// Windows Vista+ supports PNG-in-ICO (PNG data embedded directly).
func pngToICO(pngData []byte) []byte {
	size := uint32(len(pngData))
	const imgOffset = uint32(22) // 6 (ICONDIR) + 16 (ICONDIRENTRY)
	ico := make([]byte, 0, 22+len(pngData))
	// ICONDIR: reserved=0, type=1 (ICO), count=1
	ico = append(ico, 0, 0, 1, 0, 1, 0)
	// ICONDIRENTRY: width=32, height=32, colorCount=0, reserved=0, planes=1, bitCount=32
	ico = append(ico, 32, 32, 0, 0, 1, 0, 32, 0)
	// BytesInRes (uint32 LE)
	ico = append(ico, byte(size), byte(size>>8), byte(size>>16), byte(size>>24))
	// ImageOffset (uint32 LE)
	ico = append(ico, byte(imgOffset), byte(imgOffset>>8), byte(imgOffset>>16), byte(imgOffset>>24))
	ico = append(ico, pngData...)
	return ico
}

func onTrayReady() {
	systray.SetIcon(pngToICO(trayIconPNG))
	systray.SetTooltip("M4Connect — Mac Mini M4")

	mOpen := systray.AddMenuItem("Open M4Connect", "Show the app window")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Exit M4Connect")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(fmt.Sprintf("http://127.0.0.1:%d", localPort))
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

func onTrayExit() {
	os.Exit(0)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	log.SetOutput(logWriter{})
	log.SetFlags(log.Ltime)
	go runSSEHub()
	go startLocalServer()

	// Give server a moment to start
	time.Sleep(300 * time.Millisecond)

	openBrowser(fmt.Sprintf("http://127.0.0.1:%d", localPort))

	systray.Run(onTrayReady, onTrayExit)
}
