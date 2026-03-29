package main

import (
	"io"
	"log"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

// handleVNC proxies WebSocket ↔ TCP:5900 with RFB handshake interception.
// It forces security type 2 (VNC auth) so noVNC uses the VNC password
// instead of trying Apple ARD (type 30), which hangs on macOS.
func handleVNC(w http.ResponseWriter, r *http.Request) {
	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer wsConn.Close()

	tcp, err := net.Dial("tcp", "localhost:5900")
	if err != nil {
		log.Printf("[vnc] dial error: %v", err)
		return
	}
	defer tcp.Close()

	// ── RFB handshake interception ────────────────────────────────────────────

	// 1. Server → client: version string (12 bytes)
	serverVer := make([]byte, 12)
	if _, err := io.ReadFull(tcp, serverVer); err != nil {
		log.Printf("[vnc] read server version: %v", err)
		return
	}
	if err := wsConn.WriteMessage(websocket.BinaryMessage, serverVer); err != nil {
		return
	}
	log.Printf("[vnc] server version: %s", serverVer)

	// 2. Client → server: version string
	_, clientVer, err := wsConn.ReadMessage()
	if err != nil {
		return
	}
	if _, err := tcp.Write(clientVer); err != nil {
		return
	}
	log.Printf("[vnc] client version: %s", clientVer)

	// 3. Server → client: security types list
	nBuf := make([]byte, 1)
	if _, err := io.ReadFull(tcp, nBuf); err != nil {
		log.Printf("[vnc] read sec type count: %v", err)
		return
	}
	n := int(nBuf[0])
	if n == 0 {
		// Server sent a connection-failed error — read and forward the reason
		lenBuf := make([]byte, 4)
		io.ReadFull(tcp, lenBuf)
		reason := make([]byte, int(lenBuf[0])<<24|int(lenBuf[1])<<16|int(lenBuf[2])<<8|int(lenBuf[3]))
		io.ReadFull(tcp, reason)
		log.Printf("[vnc] server refused: %s", reason)
		return
	}
	secTypes := make([]byte, n)
	if _, err := io.ReadFull(tcp, secTypes); err != nil {
		log.Printf("[vnc] read sec types: %v", err)
		return
	}
	log.Printf("[vnc] server security types: %v", secTypes)

	// Force type 2 (VNC auth) if available; otherwise forward original list
	hasType2 := false
	for _, t := range secTypes {
		if t == 2 {
			hasType2 = true
			break
		}
	}

	if hasType2 {
		// Tell noVNC only VNC auth is available
		if err := wsConn.WriteMessage(websocket.BinaryMessage, []byte{1, 2}); err != nil {
			return
		}
		log.Printf("[vnc] forced type 2 (VNC auth)")
	} else {
		// Forward original list unchanged
		msg := append([]byte{nBuf[0]}, secTypes...)
		if err := wsConn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			return
		}
		log.Printf("[vnc] forwarding original security types (type 2 not available)")
	}

	// 4. Client → server: chosen security type
	_, clientSec, err := wsConn.ReadMessage()
	if err != nil {
		return
	}
	if hasType2 {
		// Always tell server to use type 2 regardless of what client sent
		if _, err := tcp.Write([]byte{2}); err != nil {
			return
		}
	} else {
		if _, err := tcp.Write(clientSec); err != nil {
			return
		}
	}

	// ── Bidirectional proxy for the rest of the session ───────────────────────

	done := make(chan struct{}, 2)

	// TCP → WebSocket
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, err := tcp.Read(buf)
			if n > 0 {
				if werr := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WebSocket → TCP
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			mt, msg, err := wsConn.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.BinaryMessage {
				if _, werr := tcp.Write(msg); werr != nil {
					return
				}
			}
		}
	}()

	<-done
}
