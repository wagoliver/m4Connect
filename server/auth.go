package main

/*
#cgo LDFLAGS: -lpam
#include <security/pam_appl.h>
#include <stdlib.h>
#include <string.h>

static int m4conv(int n, const struct pam_message **msg,
                  struct pam_response **resp, void *data) {
	struct pam_response *r = calloc(n, sizeof(struct pam_response));
	if (!r) return PAM_BUF_ERR;
	for (int i = 0; i < n; i++) {
		r[i].resp = strdup((char *)data);
		r[i].resp_retcode = 0;
	}
	*resp = r;
	return PAM_SUCCESS;
}

static int pam_check(const char *user, const char *pass) {
	struct pam_conv c = { m4conv, (void *)pass };
	pam_handle_t *h = NULL;
	int rc = pam_start("login", user, &c, &h);
	if (rc == PAM_SUCCESS) rc = pam_authenticate(h, 0);
	pam_end(h, rc);
	return rc;
}
*/
import "C"

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
	"unsafe"
)

// pamAuthenticate validates username/password via macOS PAM.
func pamAuthenticate(user, pass string) bool {
	cu := C.CString(user)
	cp := C.CString(pass)
	defer C.free(unsafe.Pointer(cu))
	defer C.free(unsafe.Pointer(cp))
	return C.pam_check(cu, cp) == 0
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
