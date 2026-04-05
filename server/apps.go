package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// ── ANSI strip ────────────────────────────────────────────────────────────────

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// ── HTTP client for downloads (no global timeout — large files) ───────────────

var dlClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

// ── App definitions ───────────────────────────────────────────────────────────

type AppDef struct {
	ID            string
	Name          string
	Desc          string
	Category      string // "ai" | "remote" | "tools"
	InstallMethod string // "dmg" | "zip-app" | "bin" | "pkg" | "pip" | "brew"
	DownloadURL   string // direct or redirecting URL (takes priority)
	GitHubOwner   string // for GitHub API latest-release resolution
	GitHubRepo    string
	GitHubAsset   string // substring to match in asset filename (case-insensitive)
	InstallPkg    string // pip package name
	CheckBinary   string // checked in binaryPaths
	CheckBundle   string // checked via os.Stat
	ServicePort   int    // optional TCP port to check if running
}

// binaryPaths are checked in order when looking for installed binaries.
// Using direct stat avoids `which` failing under launchd restricted PATH.
var binaryPaths = []string{
	"/opt/homebrew/bin", // Apple Silicon
	"/usr/local/bin",    // Intel / manual installs
	"/usr/bin",
	"/bin",
}

var appRegistry = []AppDef{
	// ── AI ────────────────────────────────────────────────────────────────────
	{
		ID: "ollama", Name: "Ollama",
		Desc:          "Execute LLMs localmente no Mac Mini",
		Category:      "ai",
		InstallMethod: "zip-app",
		DownloadURL:   "https://github.com/ollama/ollama/releases/latest/download/Ollama-darwin.zip",
		CheckBundle:   "/Applications/Ollama.app",
		ServicePort:   11434,
	},
	{
		ID: "open-webui", Name: "Open WebUI",
		Desc:          "Interface web para modelos locais (compatível com Ollama)",
		Category:      "ai",
		InstallMethod: "brew",
		InstallPkg:    "open-webui",
		CheckBinary:   "open-webui",
		ServicePort:   3000,
	},
	{
		ID: "lm-studio", Name: "LM Studio",
		Desc:          "Baixe, execute e faça chat com LLMs localmente",
		Category:      "ai",
		InstallMethod: "dmg",
		GitHubOwner:   "lmstudio-ai",
		GitHubRepo:    "lmstudio-app",
		GitHubAsset:   "arm64.dmg",
		CheckBundle:   "/Applications/LM Studio.app",
	},
	{
		ID: "jan", Name: "Jan",
		Desc:          "Alternativa open-source ao ChatGPT que roda offline",
		Category:      "ai",
		InstallMethod: "dmg",
		GitHubOwner:   "janhq",
		GitHubRepo:    "jan",
		GitHubAsset:   "arm64.dmg",
		CheckBundle:   "/Applications/Jan.app",
	},
	{
		ID: "gpt4all", Name: "GPT4All",
		Desc:          "Chatbot local open-source sem GPU necessária",
		Category:      "ai",
		InstallMethod: "dmg",
		GitHubOwner:   "nomic-ai",
		GitHubRepo:    "gpt4all",
		GitHubAsset:   "darwin",
		CheckBundle:   "/Applications/GPT4All.app",
	},
	{
		ID: "localai", Name: "LocalAI",
		Desc:          "API REST compatível com OpenAI para modelos locais",
		Category:      "ai",
		InstallMethod: "bin",
		GitHubOwner:   "mudler",
		GitHubRepo:    "LocalAI",
		GitHubAsset:   "Darwin-arm64",
		CheckBinary:   "local-ai",
		ServicePort:   8080,
	},
	// ── Remote ────────────────────────────────────────────────────────────────
	{
		ID: "tailscale", Name: "Tailscale",
		Desc:          "VPN mesh segura baseada em WireGuard",
		Category:      "remote",
		InstallMethod: "pkg",
		DownloadURL:   "https://pkgs.tailscale.com/stable/tailscale-macos.pkg",
		CheckBundle:   "/Applications/Tailscale.app",
	},
	{
		ID: "rustdesk", Name: "RustDesk",
		Desc:          "Acesso remoto open-source, alternativa ao TeamViewer",
		Category:      "remote",
		InstallMethod: "dmg",
		GitHubOwner:   "rustdesk",
		GitHubRepo:    "rustdesk",
		GitHubAsset:   "aarch64.dmg",
		CheckBundle:   "/Applications/RustDesk.app",
	},
	{
		ID: "zerotier", Name: "ZeroTier",
		Desc:          "Rede virtual peer-to-peer sem configuração de roteador",
		Category:      "remote",
		InstallMethod: "pkg",
		DownloadURL:   "https://download.zerotier.com/dist/ZeroTier%20One.pkg",
		CheckBundle:   "/Applications/ZeroTier One.app",
	},
	// ── Tools ─────────────────────────────────────────────────────────────────
	{
		ID: "docker", Name: "Docker Desktop",
		Desc:          "Plataforma de containers para desenvolvimento",
		Category:      "tools",
		InstallMethod: "dmg",
		DownloadURL:   "https://desktop.docker.com/mac/main/arm64/Docker.dmg",
		CheckBinary:   "docker",
	},
	{
		ID: "vscode", Name: "VS Code",
		Desc:          "Editor de código open-source da Microsoft",
		Category:      "tools",
		InstallMethod: "zip-app",
		DownloadURL:   "https://update.code.visualstudio.com/latest/darwin-arm64/stable",
		CheckBundle:   "/Applications/Visual Studio Code.app",
	},
	{
		ID: "iterm2", Name: "iTerm2",
		Desc:          "Terminal avançado com split panes e perfis",
		Category:      "tools",
		InstallMethod: "zip-app",
		GitHubOwner:   "gnachman",
		GitHubRepo:    "iTerm2",
		GitHubAsset:   ".zip",
		CheckBundle:   "/Applications/iTerm.app",
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

func checkAppInstalled(app AppDef) bool {
	if app.CheckBinary != "" {
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
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", app.ServicePort), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ── GitHub latest-release resolver ───────────────────────────────────────────

func resolveDownloadURL(app *AppDef, send func(string)) (string, error) {
	if app.DownloadURL != "" {
		return app.DownloadURL, nil
	}
	if app.GitHubOwner == "" {
		return "", fmt.Errorf("sem URL de download configurada")
	}
	send(fmt.Sprintf("Consultando versão mais recente de %s/%s...", app.GitHubOwner, app.GitHubRepo))
	return resolveGitHubAsset(app.GitHubOwner, app.GitHubRepo, app.GitHubAsset)
}

func resolveGitHubAsset(owner, repo, assetMatch string) (string, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "M4Connect/1.0")

	resp, err := dlClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("erro ao consultar GitHub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API retornou status %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("erro ao decodificar resposta da API: %w", err)
	}

	match := strings.ToLower(assetMatch)
	for _, a := range release.Assets {
		if strings.Contains(strings.ToLower(a.Name), match) {
			return a.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("asset com %q não encontrado na versão %s", assetMatch, release.TagName)
}

// ── Download with progress streaming ─────────────────────────────────────────

func downloadFile(rawURL, dest string, send func(string)) bool {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		send("Erro ao criar requisição: " + err.Error())
		return false
	}
	req.Header.Set("User-Agent", "M4Connect/1.0")

	resp, err := dlClient.Do(req)
	if err != nil {
		send("Erro no download: " + err.Error())
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		send(fmt.Sprintf("Servidor retornou HTTP %d", resp.StatusCode))
		return false
	}

	total := resp.ContentLength
	if total > 0 {
		send(fmt.Sprintf("Tamanho: %.1f MB", float64(total)/1024/1024))
	}

	f, err := os.Create(dest)
	if err != nil {
		send("Erro ao criar arquivo temporário: " + err.Error())
		return false
	}
	defer f.Close()

	buf := make([]byte, 128*1024)
	var downloaded int64
	lastPct := -1

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				send("Erro ao escrever: " + werr.Error())
				return false
			}
			downloaded += int64(n)
			if total > 0 {
				pct := int(downloaded * 100 / total)
				p10 := (pct / 10) * 10
				if p10 != lastPct {
					lastPct = p10
					send(fmt.Sprintf("Baixando... %d%% (%.1f / %.1f MB)", pct,
						float64(downloaded)/1024/1024, float64(total)/1024/1024))
				}
			} else {
				// No Content-Length — report every 10 MB
				mb10 := int(downloaded / (10 * 1024 * 1024))
				if mb10 != lastPct {
					lastPct = mb10
					send(fmt.Sprintf("Baixando... %.1f MB", float64(downloaded)/1024/1024))
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			send("Erro durante download: " + err.Error())
			return false
		}
	}

	send(fmt.Sprintf("Download concluído (%.1f MB).", float64(downloaded)/1024/1024))
	return true
}

// ── Find .app inside a directory (first level + one level deep) ───────────────

func findDotApp(dir string) string {
	// Check root of dir first
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			return filepath.Join(dir, e.Name())
		}
	}
	// One level deeper
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".app") {
			continue
		}
		sub := filepath.Join(dir, e.Name())
		subs, _ := os.ReadDir(sub)
		for _, se := range subs {
			if se.IsDir() && strings.HasSuffix(se.Name(), ".app") {
				return filepath.Join(sub, se.Name())
			}
		}
	}
	return ""
}

// ── Install methods ───────────────────────────────────────────────────────────

func installApp(app *AppDef, send func(string)) bool {
	tmpDir := fmt.Sprintf("/tmp/m4install-%s-%d", app.ID, time.Now().Unix())
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		send("Erro ao criar diretório temporário: " + err.Error())
		return false
	}
	defer os.RemoveAll(tmpDir)

	switch app.InstallMethod {
	case "dmg":
		return installDMG(app, tmpDir, send)
	case "zip-app":
		return installZipApp(app, tmpDir, send)
	case "bin":
		return installBin(app, tmpDir, send)
	case "pkg":
		return installPKG(app, tmpDir, send)
	case "pip":
		return installPip(app, send)
	case "brew":
		return installBrew(app, send)
	default:
		send("Método de instalação desconhecido: " + app.InstallMethod)
		return false
	}
}

func installDMG(app *AppDef, tmpDir string, send func(string)) bool {
	dlURL, err := resolveDownloadURL(app, send)
	if err != nil {
		send("Erro ao resolver URL: " + err.Error())
		return false
	}

	dmgPath := filepath.Join(tmpDir, "install.dmg")
	if !downloadFile(dlURL, dmgPath, send) {
		return false
	}

	mountPoint := filepath.Join(tmpDir, "mnt")
	os.MkdirAll(mountPoint, 0755)
	send("Montando imagem...")
	out, err := exec.Command("hdiutil", "attach", dmgPath,
		"-mountpoint", mountPoint, "-nobrowse", "-quiet", "-noverify").CombinedOutput()
	if err != nil {
		send("Erro ao montar DMG: " + strings.TrimSpace(string(out)))
		return false
	}

	appPath := findDotApp(mountPoint)
	if appPath == "" {
		send("Erro: nenhum .app encontrado na imagem")
		exec.Command("hdiutil", "detach", mountPoint, "-quiet").Run()
		return false
	}

	appName := filepath.Base(appPath)
	dest := "/Applications/" + appName
	send("Instalando " + appName + "...")
	os.RemoveAll(dest)
	out2, err := exec.Command("cp", "-r", appPath, dest).CombinedOutput()
	exec.Command("hdiutil", "detach", mountPoint, "-quiet").Run()
	if err != nil {
		send("Erro ao copiar: " + strings.TrimSpace(string(out2)))
		return false
	}

	// Remove quarantine flag so macOS doesn't block the app
	exec.Command("xattr", "-cr", dest).Run()
	send("Instalado em " + dest)
	return true
}

func installZipApp(app *AppDef, tmpDir string, send func(string)) bool {
	dlURL, err := resolveDownloadURL(app, send)
	if err != nil {
		send("Erro ao resolver URL: " + err.Error())
		return false
	}

	zipPath := filepath.Join(tmpDir, "install.zip")
	if !downloadFile(dlURL, zipPath, send) {
		return false
	}

	extractDir := filepath.Join(tmpDir, "extracted")
	os.MkdirAll(extractDir, 0755)
	send("Extraindo...")
	out, err := exec.Command("unzip", "-o", "-q", zipPath, "-d", extractDir).CombinedOutput()
	if err != nil {
		send("Erro ao extrair: " + strings.TrimSpace(string(out)))
		return false
	}

	appPath := findDotApp(extractDir)
	if appPath == "" {
		send("Erro: nenhum .app encontrado no ZIP")
		return false
	}

	appName := filepath.Base(appPath)
	dest := "/Applications/" + appName
	send("Instalando " + appName + "...")
	os.RemoveAll(dest)
	out2, err := exec.Command("cp", "-r", appPath, dest).CombinedOutput()
	if err != nil {
		send("Erro ao copiar: " + strings.TrimSpace(string(out2)))
		return false
	}

	exec.Command("xattr", "-cr", dest).Run()
	send("Instalado em " + dest)
	return true
}

func installBin(app *AppDef, tmpDir string, send func(string)) bool {
	dlURL, err := resolveDownloadURL(app, send)
	if err != nil {
		send("Erro ao resolver URL: " + err.Error())
		return false
	}

	binPath := filepath.Join(tmpDir, app.CheckBinary)
	if !downloadFile(dlURL, binPath, send) {
		return false
	}

	if err := os.Chmod(binPath, 0755); err != nil {
		send("Erro ao definir permissões: " + err.Error())
		return false
	}

	dest := "/usr/local/bin/" + app.CheckBinary
	send("Instalando binário em " + dest + "...")
	out, err := exec.Command("cp", binPath, dest).CombinedOutput()
	if err != nil {
		send("Erro ao copiar: " + strings.TrimSpace(string(out)))
		return false
	}

	send("Instalado: " + dest)
	return true
}

func installPKG(app *AppDef, tmpDir string, send func(string)) bool {
	dlURL, err := resolveDownloadURL(app, send)
	if err != nil {
		send("Erro ao resolver URL: " + err.Error())
		return false
	}

	pkgPath := filepath.Join(tmpDir, "install.pkg")
	if !downloadFile(dlURL, pkgPath, send) {
		return false
	}

	send("Instalando pacote (pode demorar)...")
	out, err := exec.Command("installer", "-pkg", pkgPath, "-target", "/").CombinedOutput()
	if err != nil {
		send("Erro: " + strings.TrimSpace(string(out)))
		return false
	}

	send("Pacote instalado com sucesso.")
	return true
}

// findBrew returns the path to the Homebrew binary, or empty string if not found.
func findBrew() string {
	for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func installBrew(app *AppDef, send func(string)) bool {
	brewPath := findBrew()
	if brewPath == "" {
		send("Homebrew não encontrado. Instale em https://brew.sh e tente novamente.")
		return false
	}

	u, err := user.Current()
	if err != nil {
		send("Erro ao obter usuário: " + err.Error())
		return false
	}

	env := append(os.Environ(),
		"HOME="+u.HomeDir,
		"PATH=/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin",
		"HOMEBREW_NO_AUTO_UPDATE=1",
		"HOMEBREW_NO_ENV_HINTS=1",
	)

	// Check if already installed via brew to decide between install and upgrade
	checkCmd := exec.Command(brewPath, "list", "--formula", app.InstallPkg)
	checkCmd.Env = env
	alreadyInstalled := checkCmd.Run() == nil

	var cmd *exec.Cmd
	if alreadyInstalled {
		send(fmt.Sprintf("Atualizando %s via Homebrew...", app.InstallPkg))
		cmd = exec.Command(brewPath, "upgrade", app.InstallPkg)
	} else {
		send(fmt.Sprintf("Instalando %s via Homebrew...", app.InstallPkg))
		cmd = exec.Command(brewPath, "install", app.InstallPkg)
	}
	cmd.Env = env

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	var cmdErr error
	go func() { cmdErr = cmd.Run(); pw.Close() }()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		line := stripANSI(scanner.Text())
		if line != "" {
			send(line)
		}
	}
	return cmdErr == nil
}

// findPython returns the path to the best available Python 3.11+ interpreter.
func findPython() string {
	candidates := []string{
		"/opt/homebrew/bin/python3.13",
		"/opt/homebrew/bin/python3.12",
		"/opt/homebrew/bin/python3.11",
		"/usr/local/bin/python3.13",
		"/usr/local/bin/python3.12",
		"/usr/local/bin/python3.11",
		"/opt/homebrew/bin/python3",
		"/usr/local/bin/python3",
		"/usr/bin/python3",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func installPip(app *AppDef, send func(string)) bool {
	u, err := user.Current()
	if err != nil {
		send("Erro ao obter usuário: " + err.Error())
		return false
	}

	python := findPython()
	if python == "" {
		send("Python 3 não encontrado. Instale via Homebrew: brew install python@3.11")
		return false
	}
	send(fmt.Sprintf("Usando: %s", python))
	send(fmt.Sprintf("Instalando %s via pip...", app.InstallPkg))

	cmd := exec.Command(python, "-m", "pip", "install", "--upgrade",
		"--break-system-packages", app.InstallPkg)
	cmd.Env = append(os.Environ(),
		"HOME="+u.HomeDir,
		"PATH=/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin",
	)

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	var cmdErr error
	go func() { cmdErr = cmd.Run(); pw.Close() }()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		line := stripANSI(scanner.Text())
		if line != "" {
			send(line)
		}
	}
	return cmdErr == nil
}

// ── Uninstall ─────────────────────────────────────────────────────────────────

func uninstallApp(app *AppDef, send func(string)) bool {
	switch app.InstallMethod {
	case "dmg", "zip-app", "pkg":
		if app.CheckBundle == "" {
			send("Erro: bundle path desconhecido")
			return false
		}
		send("Removendo " + app.CheckBundle + "...")
		if err := os.RemoveAll(app.CheckBundle); err != nil {
			send("Erro: " + err.Error())
			return false
		}
		send("Removido com sucesso.")
		return true

	case "bin":
		for _, dir := range binaryPaths {
			path := dir + "/" + app.CheckBinary
			if _, err := os.Stat(path); err == nil {
				send("Removendo " + path + "...")
				if err := os.Remove(path); err != nil {
					send("Erro: " + err.Error())
					return false
				}
				send("Removido.")
				return true
			}
		}
		send("Binário não encontrado.")
		return false

	case "pip":
		u, err := user.Current()
		if err != nil {
			send("Erro ao obter usuário: " + err.Error())
			return false
		}
		send("Desinstalando " + app.InstallPkg + " via pip3...")
		cmd := exec.Command("pip3", "uninstall", "-y", app.InstallPkg)
		cmd.Env = append(os.Environ(),
			"HOME="+u.HomeDir,
			"PATH=/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			send("Erro: " + strings.TrimSpace(string(out)))
			return false
		}
		send("Desinstalado.")
		return true

	case "brew":
		brewPath := findBrew()
		if brewPath == "" {
			send("Homebrew não encontrado.")
			return false
		}
		u, _ := user.Current()
		send("Desinstalando " + app.InstallPkg + " via Homebrew...")
		cmd := exec.Command(brewPath, "uninstall", "--force", app.InstallPkg)
		cmd.Env = append(os.Environ(),
			"HOME="+u.HomeDir,
			"PATH=/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			send("Erro: " + strings.TrimSpace(string(out)))
			return false
		}
		send("Desinstalado.")
		return true
	}
	return false
}

// ── Install lock (only one install at a time) ─────────────────────────────────

var appInstallBusy atomic.Bool

// ── SSE install/uninstall helper ──────────────────────────────────────────────

func sseInstallStream(w http.ResponseWriter, r *http.Request, action func(*AppDef, func(string)) bool) {
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	send := func(line string) {
		sseWrite(w, flusher, "output", map[string]string{"line": line})
	}

	success := action(app, send)
	sseWrite(w, flusher, "done", map[string]bool{"ok": success})
}

// ── HTTP Handlers ─────────────────────────────────────────────────────────────

func handleListApps(w http.ResponseWriter, r *http.Request) {
	type appJSON struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Desc      string `json:"desc"`
		Category  string `json:"category"`
		Installed bool   `json:"installed"`
		Running   bool   `json:"running"`
	}

	result := make([]appJSON, 0, len(appRegistry))
	for _, app := range appRegistry {
		installed := checkAppInstalled(app)
		running := false
		if installed {
			running = checkAppRunning(app)
		}
		result = append(result, appJSON{
			ID:        app.ID,
			Name:      app.Name,
			Desc:      app.Desc,
			Category:  app.Category,
			Installed: installed,
			Running:   running,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleAppInstall(w http.ResponseWriter, r *http.Request) {
	sseInstallStream(w, r, installApp)
}

func handleAppUninstall(w http.ResponseWriter, r *http.Request) {
	sseInstallStream(w, r, uninstallApp)
}
