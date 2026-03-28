package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sync/atomic"
	"time"

	"encoding/json"
)

// ── ANSI strip ────────────────────────────────────────────────────────────────

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// ── Brew path (Apple Silicon first, Intel fallback) ───────────────────────────

func brewBin() string {
	if _, err := os.Stat("/opt/homebrew/bin/brew"); err == nil {
		return "/opt/homebrew/bin/brew"
	}
	return "/usr/local/bin/brew"
}

// ── App definitions ───────────────────────────────────────────────────────────

type AppDef struct {
	ID          string
	Name        string
	Desc        string
	Category    string // "ai" | "remote" | "tools"
	InstallType string // "brew" | "cask" | "pip"
	Pkg         string // brew formula / cask name / pip package
	CheckBinary string // checked via `which`
	CheckBundle string // checked via os.Stat in /Applications/
	ServicePort int    // optional: TCP dial to check running
}

type AppInfo struct {
	AppDef
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
}

var appRegistry = []AppDef{
	// ── AI ────────────────────────────────────────────────────────────────────
	{
		ID: "ollama", Name: "Ollama",
		Desc:        "Execute LLMs localmente no Mac Mini",
		Category:    "ai", InstallType: "brew", Pkg: "ollama",
		CheckBinary: "ollama", ServicePort: 11434,
	},
	{
		ID: "open-webui", Name: "Open WebUI",
		Desc:        "Interface web para modelos locais (compatível com Ollama)",
		Category:    "ai", InstallType: "pip", Pkg: "open-webui",
		CheckBinary: "open-webui", ServicePort: 3000,
	},
	{
		ID: "lm-studio", Name: "LM Studio",
		Desc:        "Baixe, execute e faça chat com LLMs localmente",
		Category:    "ai", InstallType: "cask", Pkg: "lm-studio",
		CheckBundle: "/Applications/LM Studio.app",
	},
	{
		ID: "jan", Name: "Jan",
		Desc:        "Alternativa open-source ao ChatGPT que roda offline",
		Category:    "ai", InstallType: "cask", Pkg: "jan",
		CheckBundle: "/Applications/Jan.app",
	},
	{
		ID: "gpt4all", Name: "GPT4All",
		Desc:        "Chatbot local open-source sem GPU necessária",
		Category:    "ai", InstallType: "cask", Pkg: "gpt4all",
		CheckBundle: "/Applications/GPT4All.app",
	},
	{
		ID: "localai", Name: "LocalAI",
		Desc:        "API REST compatível com OpenAI para modelos locais",
		Category:    "ai", InstallType: "brew", Pkg: "localai",
		CheckBinary: "local-ai", ServicePort: 8080,
	},
	// ── Remote ────────────────────────────────────────────────────────────────
	{
		ID: "tailscale", Name: "Tailscale",
		Desc:        "VPN mesh segura baseada em WireGuard",
		Category:    "remote", InstallType: "cask", Pkg: "tailscale",
		CheckBundle: "/Applications/Tailscale.app",
	},
	{
		ID: "rustdesk", Name: "RustDesk",
		Desc:        "Acesso remoto open-source, alternativa ao TeamViewer",
		Category:    "remote", InstallType: "cask", Pkg: "rustdesk",
		CheckBundle: "/Applications/RustDesk.app",
	},
	{
		ID: "zerotier", Name: "ZeroTier",
		Desc:        "Rede virtual peer-to-peer sem configuração de roteador",
		Category:    "remote", InstallType: "cask", Pkg: "zerotier-one",
		CheckBundle: "/Applications/ZeroTier One.app",
	},
	// ── Tools ─────────────────────────────────────────────────────────────────
	{
		ID: "docker", Name: "Docker Desktop",
		Desc:        "Plataforma de containers para desenvolvimento",
		Category:    "tools", InstallType: "cask", Pkg: "docker",
		CheckBinary: "docker",
	},
	{
		ID: "vscode", Name: "VS Code",
		Desc:        "Editor de código open-source da Microsoft",
		Category:    "tools", InstallType: "cask", Pkg: "visual-studio-code",
		CheckBundle: "/Applications/Visual Studio Code.app",
	},
	{
		ID: "iterm2", Name: "iTerm2",
		Desc:        "Terminal avançado com split panes e perfis",
		Category:    "tools", InstallType: "cask", Pkg: "iterm2",
		CheckBundle: "/Applications/iTerm.app",
	},
}

// ── Status helpers ────────────────────────────────────────────────────────────

func findApp(id string) *AppDef {
	for i := range appRegistry {
		if appRegistry[i].ID == id {
			return &appRegistry[i]
		}
	}
	return nil
}

// binaryPaths lists directories where brew-installed binaries live.
var binaryPaths = []string{
	"/opt/homebrew/bin",  // Apple Silicon
	"/usr/local/bin",     // Intel
	"/usr/bin",
	"/bin",
}

func checkAppInstalled(app AppDef) bool {
	if app.CheckBinary != "" {
		// Check known paths directly — `which` fails under launchd restricted PATH.
		for _, dir := range binaryPaths {
			if _, err := os.Stat(dir + "/" + app.CheckBinary); err == nil {
				return true
			}
		}
	}
	if app.CheckBundle != "" {
		_, err := os.Stat(app.CheckBundle)
		return err == nil
	}
	return false
}

func checkAppRunning(app AppDef) bool {
	if app.ServicePort <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(app.ServicePort), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 10)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ── Install lock (only one install at a time) ─────────────────────────────────

var appInstallBusy atomic.Bool

// ── Command streaming helper ──────────────────────────────────────────────────

func streamCommand(w http.ResponseWriter, args []string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	// Combine stdout + stderr into a single pipe
	pr, pw := io.Pipe()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = pw
	cmd.Stderr = pw
	// Ensure brew/pip are in PATH
	cmd.Env = append(os.Environ(),
		"PATH=/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME="+os.Getenv("HOME"),
	)

	var cmdErr error
	go func() {
		cmdErr = cmd.Run()
		pw.Close()
	}()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		line := stripANSI(scanner.Text())
		if line == "" {
			continue
		}
		sseWrite(w, flusher, "output", map[string]string{"line": line})
	}

	ok2 := cmdErr == nil
	sseWrite(w, flusher, "done", map[string]bool{"ok": ok2})
}

// ── HTTP Handlers ─────────────────────────────────────────────────────────────

func handleListApps(w http.ResponseWriter, r *http.Request) {
	type appJSON struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Desc        string `json:"desc"`
		Category    string `json:"category"`
		InstallType string `json:"install_type"`
		Installed   bool   `json:"installed"`
		Running     bool   `json:"running"`
	}

	result := make([]appJSON, 0, len(appRegistry))
	for _, app := range appRegistry {
		installed := checkAppInstalled(app)
		running := false
		if installed {
			running = checkAppRunning(app)
		}
		result = append(result, appJSON{
			ID:          app.ID,
			Name:        app.Name,
			Desc:        app.Desc,
			Category:    app.Category,
			InstallType: app.InstallType,
			Installed:   installed,
			Running:     running,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleAppInstall(w http.ResponseWriter, r *http.Request) {
	if !appInstallBusy.CompareAndSwap(false, true) {
		http.Error(w, "another install is in progress", http.StatusConflict)
		return
	}
	defer appInstallBusy.Store(false)

	id := r.PathValue("id")
	app := findApp(id)
	if app == nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	brew := brewBin()
	var args []string
	switch app.InstallType {
	case "brew":
		args = []string{brew, "install", app.Pkg}
	case "cask":
		args = []string{brew, "install", "--cask", "--no-quarantine", app.Pkg}
	case "pip":
		args = []string{"pip3", "install", "--upgrade", app.Pkg}
	default:
		http.Error(w, "unknown install type", http.StatusBadRequest)
		return
	}

	streamCommand(w, args)
}

func handleAppUninstall(w http.ResponseWriter, r *http.Request) {
	if !appInstallBusy.CompareAndSwap(false, true) {
		http.Error(w, "another operation is in progress", http.StatusConflict)
		return
	}
	defer appInstallBusy.Store(false)

	id := r.PathValue("id")
	app := findApp(id)
	if app == nil {
		http.Error(w, "app not found", http.StatusNotFound)
		return
	}

	brew := brewBin()
	var args []string
	switch app.InstallType {
	case "brew":
		args = []string{brew, "uninstall", app.Pkg}
	case "cask":
		args = []string{brew, "uninstall", "--cask", app.Pkg}
	case "pip":
		args = []string{"pip3", "uninstall", "-y", app.Pkg}
	default:
		http.Error(w, "unknown install type", http.StatusBadRequest)
		return
	}

	streamCommand(w, args)
}
