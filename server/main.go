package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

func main() {
	logDir := "/Library/Logs/M4Server"
	os.MkdirAll(logDir, 0755)
	logFile, err := os.OpenFile(
		filepath.Join(logDir, "m4server.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644,
	)
	if err == nil {
		log.SetOutput(logFile)
	}
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cfg, err := loadOrCreateConfig()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	store, err := openStorage()
	if err != nil {
		log.Printf("Warning: could not open stats storage: %v", err)
	}
	defer store.Close()

	log.Println("M4Server daemon started")
	fmt.Printf("\n╔══════════════════════════════════════╗\n")
	fmt.Printf("║         M4Server — Online            ║\n")
	fmt.Printf("╚══════════════════════════════════════╝\n\n")

	wifi := getWiFiInterfaces()
	log.Printf("WiFi interfaces (ignored): %v", wifi)

	for {
		log.Println("Aguardando cabo Ethernet...")
		iface := waitForNewCable(wifi)
		log.Printf("Cabo detectado: %s", iface)

		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("PANIC recuperado em sessão: %v", r)
				}
			}()
			handleSession(cfg, iface, store)
		}()

		log.Println("Sessão encerrada. Aguardando próxima conexão...")
		time.Sleep(2 * time.Second)
	}
}

func handleSession(cfg Config, iface string, store *Storage) {
	subnet := findFreeSubnet(cfg.PreferredSubnet)
	macIP := fmt.Sprintf("%s.%s", subnet, cfg.MacSuffix)

	log.Printf("Configurando %s em %s...", macIP, iface)
	if err := configureInterface(iface, macIP, "255.255.255.0"); err != nil {
		log.Printf("Erro ao configurar IP: %v — abortando", err)
		return
	}
	defer releaseInterface(iface)

	conn, err := net.ListenPacket("udp", fmt.Sprintf("0.0.0.0:%d", cfg.HandshakePort))
	if err != nil {
		log.Printf("Erro ao abrir UDP: %v", err)
		return
	}
	defer conn.Close()

	// Handshake: aceita retentativas por 30s (cobre ACK perdido)
	log.Printf("Aguardando handshake UDP :%d ...", cfg.HandshakePort)
	if pc, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		pc.SetDeadline(time.Now().Add(30 * time.Second))
	}

	buf := make([]byte, 512)
	expected := "M4HELLO:" + cfg.Token
	var clientAddr net.Addr

	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			log.Printf("Handshake timeout/erro: %v", err)
			return
		}
		if string(buf[:n]) != expected {
			log.Printf("Handshake inválido de %s — ignorando", addr)
			continue
		}
		clientAddr = addr
		break
	}

	log.Printf("Handshake válido de %s", clientAddr)

	hostname, _ := os.Hostname()
	clientIP := fmt.Sprintf("%s.%s", subnet, cfg.ClientSuffix)
	ack := fmt.Sprintf("M4ACK:%s:%s:%d:%s", macIP, clientIP, cfg.PortalPort, hostname)
	conn.WriteTo([]byte(ack), clientAddr)

	EnableVNC()
	EnableSSH()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // desliga o portal quando o cabo for removido

	go startPortal(ctx, macIP, cfg.PortalPort, store)

	log.Printf("Portal iniciado em http://%s:%d", macIP, cfg.PortalPort)
	log.Println("Aguardando desconexão do cabo...")
	waitForLinkLoss(iface)
}
