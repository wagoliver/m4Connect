package main

import (
	"log"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

func handleVNC(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	tcp, err := net.Dial("tcp", "localhost:5900")
	if err != nil {
		log.Printf("[vnc] dial error: %v", err)
		return
	}
	defer tcp.Close()

	done := make(chan struct{}, 2)

	// TCP → WebSocket
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, err := tcp.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
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
			mt, msg, err := conn.ReadMessage()
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
