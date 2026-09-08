package rest

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"time"
)

const dashboardSessionCookie = "gideondb_dashboard_session"

type dashboardSessionRecord struct {
	csrf    string
	expires time.Time
}

type dashboardLoginAttempt struct {
	count int
	until time.Time
}

type dashboardAuthenticator struct {
	username string
	salt     [32]byte
	hash     [32]byte
	mu       sync.Mutex
	sessions map[string]dashboardSessionRecord
	attempts map[string]dashboardLoginAttempt
}

func newDashboardAuthenticator(username, password string) *dashboardAuthenticator {
	a := &dashboardAuthenticator{username: username, sessions: map[string]dashboardSessionRecord{}, attempts: map[string]dashboardLoginAttempt{}}
	_, _ = rand.Read(a.salt[:])
	a.hash = dashboardPasswordHash(password, a.salt[:])
	return a
}

// dashboardPasswordHash deliberately performs substantial work so an in-memory
// credential disclosure does not expose a fast password verifier.
func dashboardPasswordHash(password string, salt []byte) [32]byte {
	value := append(append([]byte(nil), salt...), []byte(password)...)
	for i := 0; i < 200000; i++ {
		digest := sha256.Sum256(value)
		value = digest[:]
	}
	return sha256.Sum256(value)
}

func randomToken() string {
	value := make([]byte, 32)
	_, _ = rand.Read(value)
	return hex.EncodeToString(value)
}

func (a *dashboardAuthenticator) authenticate(username, password string) bool {
	provided := dashboardPasswordHash(password, a.salt[:])
	return subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) == 1 && hmac.Equal(provided[:], a.hash[:])
}

func (a *dashboardAuthenticator) validRequest(r *http.Request) bool {
	_, ok := a.session(r)
	return ok
}

func (a *dashboardAuthenticator) session(r *http.Request) (dashboardSessionRecord, bool) {
	cookie, err := r.Cookie(dashboardSessionCookie)
	if err != nil || cookie.Value == "" {
		return dashboardSessionRecord{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.sessions[cookie.Value]
	if !ok || time.Now().After(record.expires) {
		delete(a.sessions, cookie.Value)
		return dashboardSessionRecord{}, false
	}
	return record, true
}

func (a *dashboardAuthenticator) allowAttempt(remote string) bool {
	remote = dashboardClientAddress(remote)
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	entry := a.attempts[remote]
	if entry.until.IsZero() || !now.Before(entry.until) {
		entry = dashboardLoginAttempt{until: now.Add(time.Minute)}
	}
	entry.count++
	a.attempts[remote] = entry
	return entry.count <= 5
}

func dashboardClientAddress(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
}

func (s *Server) dashboardLogin(w http.ResponseWriter, r *http.Request) {
	if s.dashboardAuth == nil {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "dashboard authentication is not configured"})
		return
	}
	if !s.dashboardAuth.allowAttempt(r.RemoteAddr) {
		writeJSON(w, http.StatusTooManyRequests, apiError{Code: "login_throttled", Message: "too many login attempts"})
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !s.dashboardAuth.authenticate(input.Username, input.Password) {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "invalid_credentials", Message: "invalid username or password"})
		return
	}
	token, csrf := randomToken(), randomToken()
	s.dashboardAuth.mu.Lock()
	s.dashboardAuth.sessions[token] = dashboardSessionRecord{csrf: csrf, expires: time.Now().Add(8 * time.Hour)}
	delete(s.dashboardAuth.attempts, dashboardClientAddress(r.RemoteAddr))
	s.dashboardAuth.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: dashboardSessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 8 * 60 * 60})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": csrf})
}

func (s *Server) dashboardSession(w http.ResponseWriter, r *http.Request) {
	if s.dashboardAuth == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	record, ok := s.dashboardAuth.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "dashboard login required"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": record.csrf})
}

func (s *Server) dashboardLogout(w http.ResponseWriter, r *http.Request) {
	if s.dashboardAuth == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	record, ok := s.dashboardAuth.session(r)
	if !ok || !hmac.Equal([]byte(record.csrf), []byte(r.Header.Get("X-GideonDB-CSRF"))) {
		writeJSON(w, http.StatusForbidden, apiError{Code: "csrf_rejected", Message: "valid CSRF token required"})
		return
	}
	cookie, _ := r.Cookie(dashboardSessionCookie)
	s.dashboardAuth.mu.Lock()
	delete(s.dashboardAuth.sessions, cookie.Value)
	s.dashboardAuth.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: dashboardSessionCookie, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
