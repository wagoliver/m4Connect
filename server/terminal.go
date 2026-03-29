package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

func shellPath() string {
	if _, err := os.Stat("/bin/zsh"); err == nil {
		return "/bin/zsh"
	}
	return "/bin/bash"
}

func handleTerminal(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	cmd := exec.Command(shellPath())
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("[terminal] pty.Start error: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("erro ao iniciar terminal\r\n"))
		return
	}
	defer func() {
		cmd.Process.Kill()
		ptmx.Close()
	}()

	// PTY stdout → WebSocket
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			}
			if err != nil {
				conn.Close()
				return
			}
		}
	}()

	// WebSocket → PTY stdin / resize
	for {
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if mt == websocket.BinaryMessage {
			ptmx.Write(msg)
		} else if mt == websocket.TextMessage {
			var ev struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(msg, &ev) == nil && ev.Type == "resize" {
				pty.Setsize(ptmx, &pty.Winsize{Cols: ev.Cols, Rows: ev.Rows})
			}
		}
	}
}
