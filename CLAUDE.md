# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

M4-Server is a peer-to-peer monitoring and remote access system connecting a Windows PC to a Mac Mini M4 via direct Ethernet cable (no internet/VPN). Communication uses a UDP handshake + HTTP/WebSocket portal, authenticated with a shared token.

## Build Commands

### Server (Mac Mini — must build on macOS)
```bash
cd server
go build -o m4server .

# Create installer package (requires pkgbuild / Xcode CLT)
bash pkg/build_pkg.sh   # outputs pkg/M4Server.pkg
```

### Client (Windows)
```bash
cd client

# Production build (no console window)
go build -ldflags="-H windowsgui" -o M4Connect.exe .

# Debug build (with console output)
go build -o M4Connect_debug.exe .

# Rebuild icon/manifest resources (requires rsrc tool)
rsrc -manifest M4Connect.exe.manifest -ico "ui/icon/favicon.ico" -o rsrc.syso
go build -ldflags="-H windowsgui" -o M4Connect.exe .
```

No test suite or linter is configured. Use `go vet ./...` for basic static checks.

## Architecture

### Connection Flow
1. **Cable detection** — both sides detect a new Ethernet interface
2. **IP assignment** — Mac configures `10.10.10.1/24`; Windows configures `10.10.10.2/32` via `netsh` (requires admin)
3. **UDP handshake** — Windows sends `M4HELLO:{token}` to `10.10.10.1:54321`; Mac replies `M4ACK:{mac_ip}:{client_ip}:{portal_port}:{hostname}`
4. **Portal** — Mac serves a web dashboard on `http://10.10.10.1:8080` with live stats via WebSocket

### Server (`server/`)
| File | Responsibility |
|------|---------------|
| `main.go` | Entry point; session lifecycle, panic recovery, cable plug/unplug loop |
| `portal.go` | HTTP server, static file serving, WebSocket stats broadcaster (~1 Hz), REST `/api/status` |
| `network.go` | macOS interface detection, IP configuration via `ifconfig`/`networksetup`, link monitoring |
| `services.go` | VNC (ARD) and SSH enable/disable via `launchctl` |
| `config.go` | JSON config load/save at `/Library/Application Support/M4Server/config.json` |
| `static/` | Embedded web UI (index.html, style.css) served by the portal |
| `pkg/` | macOS packaging: `build_pkg.sh`, launchd plist, postinstall hook |

### Client (`client/`)
| File | Responsibility |
|------|---------------|
| `main.go` | System tray app; local HTTP server on `localhost:12345`; SSE hub for UI updates; opens browser |
| `network.go` | Windows Ethernet detection, IP config via `netsh`, UDP handshake sender, connection state machine |
| `ui/` | Embedded web UI: `index.html` (main), `setup.html` (token wizard), `app.js` (SSE + events), `style.css` |

### Client Local API (`localhost:12345`)
- `GET /events` — SSE stream for real-time UI updates
- `POST /api/connect` — trigger connection flow
- `GET /api/status` — current connection state
- `GET /api/mac-stats` — proxies to Mac portal `/api/status`
- `POST /api/disconnect` — release IP, cleanup
- `GET /api/config` + `POST /api/config` — read/write token config

### Configuration Files
- **Mac:** `/Library/Application Support/M4Server/config.json` — token, subnet, ports, IP suffixes
- **Windows:** `~/.m4connect/config.json` — token, subnet, ports

### Network Addressing
- Mac: `10.10.10.1` (configurable suffix)
- Windows: `10.10.10.2` (configurable suffix)
- Handshake port: `54321` (UDP)
- Portal port: `8080` (HTTP/WebSocket)
