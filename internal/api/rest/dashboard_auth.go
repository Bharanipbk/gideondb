package rest

import (
	"bytes"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

const dashboardSessionCookie = "gideondb_dashboard_session"
const dashboardPasswordIterations = 600000

type dashboardSessionRecord struct {
	username   string
	role       string
	mustChange bool
	csrf       string
	expires    time.Time
}
type dashboardLoginAttempt struct {
	count int
	until time.Time
}
type dashboardCredentials struct {
	Username           string `json:"username"`
	Algorithm          string `json:"algorithm"`
	Iterations         int    `json:"iterations"`
	Salt               string `json:"salt"`
	PasswordHash       string `json:"password_hash"`
	MustChangePassword bool   `json:"must_change_password"`
}
type dashboardAuthenticator struct {
	path     string
	users    map[string]dashboardUser
	settings dashboardSettings
	mu       sync.Mutex
	sessions map[string]dashboardSessionRecord
	attempts map[string]dashboardLoginAttempt
}

func newDashboardAuthenticator(username, password, path string, bootstrap bool) *dashboardAuthenticator {
	a := &dashboardAuthenticator{path: path, users: map[string]dashboardUser{}, settings: defaultDashboardSettings(), sessions: map[string]dashboardSessionRecord{}, attempts: map[string]dashboardLoginAttempt{}}
	u := dashboardUser{Username: username, Role: "admin", MustChangePassword: bootstrap}
	u.setPassword(password)
	a.users[username] = u
	return a
}

func dashboardPasswordHash(password string, salt []byte) []byte {
	value, _ := pbkdf2.Key(sha256.New, password, salt, dashboardPasswordIterations, 32)
	return value
}

// LoadDashboardPasswordFile reads an operator-mounted secret without accepting
// files visible to group or other users.
func LoadDashboardPasswordFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("dashboard password file permissions must be 0600 or stricter")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	password := string(bytes.TrimSpace(data))
	if len(password) < 12 {
		return "", fmt.Errorf("dashboard password must contain at least 12 characters")
	}
	return password, nil
}
func randomToken() string {
	value := make([]byte, 32)
	_, _ = rand.Read(value)
	return hex.EncodeToString(value)
}
func (a *dashboardAuthenticator) authenticate(username, password string) bool {
	a.mu.Lock()
	u, ok := a.users[username]
	a.mu.Unlock()
	// Always perform a password derivation, including unknown users.
	provided := dashboardPasswordHash(password, u.Salt)
	return ok && !u.Disabled && hmac.Equal(provided, u.Hash)
}
func (a *dashboardAuthenticator) validRequest(r *http.Request) bool {
	record, ok := a.session(r)
	return ok && !record.mustChange
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
	u, exists := a.users[record.username]
	if !exists || u.Disabled {
		delete(a.sessions, cookie.Value)
		return dashboardSessionRecord{}, false
	}
	record.role, record.mustChange = u.Role, u.MustChangePassword
	return record, true
}
func dashboardClientAddress(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
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
func setDashboardCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: dashboardSessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func (s *Server) issueDashboardSession(w http.ResponseWriter, r *http.Request, username string) string {
	token, csrf := randomToken(), randomToken()
	s.dashboardAuth.sessions[token] = dashboardSessionRecord{username: username, csrf: csrf, expires: time.Now().Add(time.Duration(s.dashboardAuth.settings.SessionHours) * time.Hour)}
	setDashboardCookie(w, r, token, s.dashboardAuth.settings.SessionHours*60*60)
	return csrf
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
	s.dashboardAuth.mu.Lock()
	u, exists := s.dashboardAuth.users[input.Username]
	if !exists || u.Disabled || !hmac.Equal(dashboardPasswordHash(input.Password, u.Salt), u.Hash) {
		s.dashboardAuth.mu.Unlock()
		writeJSON(w, 401, apiError{Code: "invalid_credentials", Message: "invalid username or password"})
		return
	}
	csrf := s.issueDashboardSession(w, r, input.Username)
	delete(s.dashboardAuth.attempts, dashboardClientAddress(r.RemoteAddr))
	mustChange := s.dashboardAuth.users[input.Username].MustChangePassword
	s.dashboardAuth.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": csrf, "must_change_password": mustChange})
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
	mustChange := record.mustChange
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": record.csrf, "must_change_password": mustChange, "username": record.username, "role": record.role})
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
	setDashboardCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) dashboardRefreshSession(w http.ResponseWriter, r *http.Request) {
	if s.dashboardAuth == nil {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "dashboard authentication is not configured"})
		return
	}
	record, ok := s.dashboardAuth.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "dashboard login required"})
		return
	}
	if !hmac.Equal([]byte(record.csrf), []byte(r.Header.Get("X-GideonDB-CSRF"))) {
		writeJSON(w, http.StatusForbidden, apiError{Code: "csrf_rejected", Message: "valid CSRF token required"})
		return
	}
	cookie, _ := r.Cookie(dashboardSessionCookie)
	s.dashboardAuth.mu.Lock()
	if _, active := s.dashboardAuth.sessions[cookie.Value]; !active {
		s.dashboardAuth.mu.Unlock()
		writeJSON(w, 401, apiError{Code: "unauthorized", Message: "session expired"})
		return
	}
	delete(s.dashboardAuth.sessions, cookie.Value)
	csrf := s.issueDashboardSession(w, r, record.username)
	s.dashboardAuth.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": csrf})
}
func (s *Server) dashboardChangePassword(w http.ResponseWriter, r *http.Request) {
	if s.dashboardAuth == nil {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "dashboard authentication is not configured"})
		return
	}
	record, ok := s.dashboardAuth.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "dashboard login required"})
		return
	}
	if !hmac.Equal([]byte(record.csrf), []byte(r.Header.Get("X-GideonDB-CSRF"))) {
		writeJSON(w, http.StatusForbidden, apiError{Code: "csrf_rejected", Message: "valid CSRF token required"})
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.NewPassword) < 12 {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "weak_password", Message: "new password must contain at least 12 characters"})
		return
	}
	if !s.dashboardAuth.authenticate(record.username, input.CurrentPassword) {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "invalid_credentials", Message: "current password is incorrect"})
		return
	}
	s.dashboardAuth.mu.Lock()
	old := s.dashboardAuth.users[record.username]
	cookie, _ := r.Cookie(dashboardSessionCookie)
	_, active := s.dashboardAuth.sessions[cookie.Value]
	// Recheck under the mutation lock so concurrent resets cannot be overwritten.
	if !active || !hmac.Equal(dashboardPasswordHash(input.CurrentPassword, old.Salt), old.Hash) || old.Disabled {
		s.dashboardAuth.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "invalid_credentials", Message: "current password is incorrect"})
		return
	}
	u := old
	u.setPassword(input.NewPassword)
	u.MustChangePassword = false
	s.dashboardAuth.users[record.username] = u
	if err := s.dashboardAuth.persist(); err != nil {
		s.dashboardAuth.users[record.username] = old
		s.dashboardAuth.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "credential_persist_failed", Message: "password change could not be persisted"})
		return
	}
	s.dashboardAuth.revokeUserSessions(record.username)
	csrf := s.issueDashboardSession(w, r, record.username)
	s.dashboardAuth.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": csrf, "must_change_password": false})
}
