package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os/exec"
	"sync"
	"time"
)

// authenticate verifies username/password using macOS Directory Services.
func authenticate(user, pass string) bool {
	cmd := exec.Command("dscl", ".", "-authonly", user, pass)
	err := cmd.Run()
	return err == nil
}

// ── Sessions ──────────────────────────────────────────────────────────────────

const cookieName = "m4sid"
const sessionTTL = 24 * time.Hour

type sessionMap struct {
	mu   sync.Mutex
	data map[string]time.Time
}

var sessions = &sessionMap{data: make(map[string]time.Time)}

func newSession() string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	sessions.mu.Lock()
	sessions.data[tok] = time.Now().Add(sessionTTL)
	sessions.mu.Unlock()
	return tok
}

func validSession(tok string) bool {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	exp, ok := sessions.data[tok]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(sessions.data, tok)
		return false
	}
	return true
}

func deleteSession(tok string) {
	sessions.mu.Lock()
	delete(sessions.data, tok)
	sessions.mu.Unlock()
}

// ── Middleware ─────────────────────────────────────────────────────────────────

func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || r.URL.Path == "/logout" {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || !validSession(c.Value) {
			if r.Header.Get("Upgrade") == "websocket" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}
