# m4Connect

**Monitoramento direto do Mac Mini M4 via cabo Ethernet — sem internet, sem VPN, sem configuração.**

Conecte seu PC Windows ao Mac Mini com um cabo Cat5e/6/7/8 e tenha em segundos um painel completo com CPU, memória, temperatura, disco, rede e histórico de 7 dias.

---

## Como funciona

```
Windows PC ──── Ethernet ──── Mac Mini M4
   M4Connect.exe                m4server (daemon)
   localhost:12345              10.10.10.1:8080
        │                            │
        │   UDP handshake (token)    │
        │ ──────────────────────── ▶ │
        │   WebSocket stats (1 Hz)   │
        │ ◀ ────────────────────── ─ │
        │                            │
   Tray app + browser           Portal web
```

A conexão é **ponto a ponto**: nenhum pacote sai para a internet. O protocolo é:

1. O Mac detecta a interface Ethernet e configura `10.10.10.1/24`
2. O Windows detecta o cabo, configura `10.10.10.2` e envia um handshake UDP com o token
3. O Mac valida o token, ativa VNC + SSH e sobe o portal web
4. O Windows abre o browser no painel em tempo real

---

## Funcionalidades

| Recurso | Detalhe |
|---------|---------|
| **CPU ao vivo** | Percentual de uso, idle, load average, processos e threads |
| **Memória** | Total/usado, breakdown (App, Wired, Compressed, Cached), swap |
| **Temperatura** | Die temperature via `powermetrics` — atualizado a cada 10s |
| **Disco** | Espaço livre/ocupado, read/write I/O em tempo real |
| **Rede** | Download/upload em bytes/s (todas as interfaces agregadas) |
| **Histórico 7 dias** | SQLite no Mac, coleta 1 ponto/min, gráficos com seletor 1h/6h/24h/7d |
| **VNC / SSH** | Liga/desliga Screen Sharing e SSH diretamente do painel |
| **Logs em tempo real** | Terminal embutido no portal |
| **Autenticação** | Token compartilhado — sem usuário/senha, sem TLS, sem internet |

---

## Estrutura do projeto

```
m4Connect/
├── server/              # Daemon macOS (Go)
│   ├── main.go          # Loop de sessão, handshake UDP, lifecyle do daemon
│   ├── portal.go        # HTTP server, WebSocket hub, coleta de stats
│   ├── storage.go       # SQLite — coleta contínua + API de histórico
│   ├── network.go       # Detecção de interface, configuração de IP via ifconfig
│   ├── services.go      # Liga/desliga VNC (ARD) e SSH via launchctl
│   ├── config.go        # Leitura/escrita de config JSON
│   ├── static/          # UI do portal (HTML/CSS/JS, embedded no binário)
│   └── pkg/             # Scripts e plists para o instalador .pkg
│
└── client/              # App Windows (Go)
    ├── main.go          # System tray, servidor local HTTP, hub SSE
    ├── network.go       # Detecção de Ethernet, configuração via netsh, handshake
    └── ui/              # Interface web (embedded no executável)
```

---

## Pré-requisitos para build

**Mac Mini (server):**
- Go 1.22+
- Xcode Command Line Tools (`xcode-select --install`) — para `pkgbuild`
- macOS 14+ (Apple Silicon)

**Windows PC (client):**
- Go 1.22+
- [`rsrc`](https://github.com/akavel/rsrc) — para embutir ícone e manifest (`go install github.com/akavel/rsrc@latest`)

---

## Build

### Servidor (Mac Mini)

```bash
cd server

# Compilar o binário
go get modernc.org/sqlite
go mod tidy
go build -o m4server .

# Criar o instalador .pkg
bash pkg/build_pkg.sh
# → gera pkg/M4Server.pkg
```

### Cliente (Windows)

```bash
cd client

# Build de produção (sem janela de console)
go build -ldflags="-H windowsgui" -o M4Connect.exe .

# Build de debug (com console)
go build -o M4Connect_debug.exe .

# Recompilar recursos (ícone + manifest — só se necessário)
rsrc -manifest M4Connect.exe.manifest -ico "ui/icon/favicon.ico" -o rsrc.syso
go build -ldflags="-H windowsgui" -o M4Connect.exe .
```

---

## Instalação (usuário final)

### 1. Mac Mini

```bash
# Remover quarentena e instalar
xattr -rd com.apple.quarantine ~/Downloads/M4Server.pkg
sudo installer -pkg ~/Downloads/M4Server.pkg -target /

# Copiar o token gerado automaticamente
cat "/Library/Application Support/M4Server/config.json"
```

O daemon sobe automaticamente via launchd e inicia com o sistema.

Logs em: `/Library/Logs/M4Server/m4server.log`

### 2. Windows

1. Clique com botão direito em `M4Connect.exe` → **Executar como administrador**
2. Na primeira execução: clique em ⚙ e cole o token do Mac
3. Pluga o cabo Ethernet — a conexão é automática

---

## Configuração

### Mac (`/Library/Application Support/M4Server/config.json`)

```json
{
  "token": "<gerado automaticamente>",
  "preferred_subnet": "10.10.10",
  "portal_port": 8080,
  "handshake_port": 54321,
  "mac_suffix": "1",
  "client_suffix": "2"
}
```

### Windows (`~/.m4connect/config.json`)

```json
{
  "token": "<copiado do Mac>",
  "default_subnet": "10.10.10",
  "client_suffix": "2",
  "handshake_port": 54321
}
```

---

## Histórico de dados (SQLite)

O daemon coleta stats continuamente — **mesmo sem cabo conectado**. Dados ficam em:

```
~/Library/Application Support/M4Server/stats.db
```

| Período | Resolução | Pontos |
|---------|-----------|--------|
| 1 hora  | 1 min     | ~60    |
| 6 horas | 5 min     | ~72    |
| 24 horas| 15 min    | ~96    |
| 7 dias  | 1 hora    | ~168   |

Retenção automática de 7 dias. API: `GET /api/history?period=1h|6h|24h|7d`

---

## Portas e protocolos

| Porta | Protocolo | Direção | Uso |
|-------|-----------|---------|-----|
| 54321 | UDP | Windows → Mac | Handshake de autenticação |
| 8080  | HTTP/WS | Windows → Mac | Portal web + WebSocket stats |
| 12345 | HTTP/SSE | localhost | Interface local do M4Connect |
| 5900  | TCP | Windows → Mac | VNC (Screen Sharing) — opcional |
| 22    | TCP | Windows → Mac | SSH — opcional |

---

## Segurança

- Token gerado com `crypto/rand` (32 bytes hex) na primeira instalação
- Comunicação **apenas na sub-rede local** `10.10.10.0/24` — sem roteamento
- Sem TLS (tráfego não sai da máquina/cabo)
- VNC e SSH são desativados quando o cabo é removido

---

## Solução de problemas

**Conexão não inicia no Windows**
→ Verifique se está rodando como Administrador (necessário para `netsh`)

**Token inválido**
→ Copie novamente com `cat "/Library/Application Support/M4Server/config.json"` no Mac

**Mac bloqueia a instalação do .pkg**
→ Execute `xattr -rd com.apple.quarantine` antes do `installer`

**Portal não abre após conectar**
→ Verifique os logs: `tail -f /Library/Logs/M4Server/m4server.log`

**Temperatura sempre "–°C"**
→ O daemon precisa rodar como root (launchd daemon) para acessar `powermetrics`

---

## Tecnologias

- **Go 1.22** — server e client
- **gorilla/websocket** — streaming de stats em tempo real
- **gopsutil/v3** — coleta de métricas do sistema
- **modernc.org/sqlite** — banco de dados embedded (pure Go, sem CGo)
- **getlantern/systray** — system tray no Windows
- **Canvas API** — gráficos do portal (sem dependências externas)

---

*M4Connect — conexão P2P direta, zero config, zero internet.*
