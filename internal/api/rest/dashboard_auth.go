package rest

import (
	"bytes"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const dashboardSessionCookie = "gideondb_dashboard_session"
const dashboardPasswordIterations = 600000

type dashboardSessionRecord struct {
	csrf    string
	expires time.Time
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
	username   string
	salt, hash []byte
	path       string
	mustChange bool
	mu         sync.Mutex
	sessions   map[string]dashboardSessionRecord
	attempts   map[string]dashboardLoginAttempt
}

func newDashboardAuthenticator(username, password, path string, bootstrap bool) *dashboardAuthenticator {
	a := &dashboardAuthenticator{username: username, path: path, mustChange: bootstrap, salt: make([]byte, 32), sessions: map[string]dashboardSessionRecord{}, attempts: map[string]dashboardLoginAttempt{}}
	_, _ = rand.Read(a.salt)
	a.hash = dashboardPasswordHash(password, a.salt)
	return a
}
func dashboardPasswordHash(password string, salt []byte) []byte {
	value, _ := pbkdf2.Key(sha256.New, password, salt, dashboardPasswordIterations, 32)
	return value
}

// PrepareDashboardCredentials creates a restrictive persistent verifier once,
// or validates the existing verifier without rewriting it.
func PrepareDashboardCredentials(path, username, password string, bootstrap bool) error {
	if info, err := os.Stat(path); err == nil {
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("dashboard credentials file permissions must be 0600 or stricter")
		}
		_, err = loadDashboardAuthenticator(path)
		return err
	} else if !os.IsNotExist(err) {
		return err
	}
	if username == "" || password == "" {
		return fmt.Errorf("dashboard credentials are required")
	}
	return newDashboardAuthenticator(username, password, path, bootstrap).persist()
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
func loadDashboardAuthenticator(path string) (*dashboardAuthenticator, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("dashboard credentials file permissions must be 0600 or stricter")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var saved dashboardCredentials
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return nil, fmt.Errorf("decode dashboard credentials: %w", err)
	}
	if saved.Username == "" || saved.Algorithm != "pbkdf2-sha256" || saved.Iterations != dashboardPasswordIterations {
		return nil, fmt.Errorf("unsupported dashboard credential format")
	}
	salt, err := hex.DecodeString(saved.Salt)
	if err != nil || len(salt) < 16 {
		return nil, fmt.Errorf("invalid dashboard credential salt")
	}
	hash, err := hex.DecodeString(saved.PasswordHash)
	if err != nil || len(hash) != 32 {
		return nil, fmt.Errorf("invalid dashboard password hash")
	}
	return &dashboardAuthenticator{username: saved.Username, salt: salt, hash: hash, path: path, mustChange: saved.MustChangePassword, sessions: map[string]dashboardSessionRecord{}, attempts: map[string]dashboardLoginAttempt{}}, nil
}
func (a *dashboardAuthenticator) persist() error {
	saved := dashboardCredentials{Username: a.username, Algorithm: "pbkdf2-sha256", Iterations: dashboardPasswordIterations, Salt: hex.EncodeToString(a.salt), PasswordHash: hex.EncodeToString(a.hash), MustChangePassword: a.mustChange}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(a.path), ".dashboard-credentials-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(append(data, '\n'))
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, a.path)
}
func randomToken() string {
	value := make([]byte, 32)
	_, _ = rand.Read(value)
	return hex.EncodeToString(value)
}
func (a *dashboardAuthenticator) authenticate(username, password string) bool {
	a.mu.Lock()
	expectedUsername := a.username
	salt := append([]byte(nil), a.salt...)
	expectedHash := append([]byte(nil), a.hash...)
	a.mu.Unlock()
	provided := dashboardPasswordHash(password, salt)
	return subtle.ConstantTimeCompare([]byte(username), []byte(expectedUsername)) == 1 && hmac.Equal(provided, expectedHash)
}
func (a *dashboardAuthenticator) validRequest(r *http.Request) bool {
	_, ok := a.session(r)
	a.mu.Lock()
	mustChange := a.mustChange
	a.mu.Unlock()
	return ok && !mustChange
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
func (s *Server) issueDashboardSession(w http.ResponseWriter, r *http.Request) string {
	token, csrf := randomToken(), randomToken()
	s.dashboardAuth.sessions[token] = dashboardSessionRecord{csrf: csrf, expires: time.Now().Add(8 * time.Hour)}
	setDashboardCookie(w, r, token, 8*60*60)
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
	csrf := s.issueDashboardSession(w, r)
	delete(s.dashboardAuth.attempts, dashboardClientAddress(r.RemoteAddr))
	mustChange := s.dashboardAuth.mustChange
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
	s.dashboardAuth.mu.Lock()
	mustChange := s.dashboardAuth.mustChange
	s.dashboardAuth.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": record.csrf, "must_change_password": mustChange})
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
	delete(s.dashboardAuth.sessions, cookie.Value)
	csrf := s.issueDashboardSession(w, r)
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
	if !s.dashboardAuth.authenticate(s.dashboardAuth.username, input.CurrentPassword) {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "invalid_credentials", Message: "current password is incorrect"})
		return
	}
	s.dashboardAuth.mu.Lock()
	oldSalt, oldHash, oldMust := s.dashboardAuth.salt, s.dashboardAuth.hash, s.dashboardAuth.mustChange
	s.dashboardAuth.salt = make([]byte, 32)
	_, _ = rand.Read(s.dashboardAuth.salt)
	s.dashboardAuth.hash = dashboardPasswordHash(input.NewPassword, s.dashboardAuth.salt)
	s.dashboardAuth.mustChange = false
	if s.dashboardAuth.path != "" {
		if err := s.dashboardAuth.persist(); err != nil {
			s.dashboardAuth.salt, s.dashboardAuth.hash, s.dashboardAuth.mustChange = oldSalt, oldHash, oldMust
			s.dashboardAuth.mu.Unlock()
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "credential_persist_failed", Message: "password change could not be persisted"})
			return
		}
	}
	s.dashboardAuth.sessions = map[string]dashboardSessionRecord{}
	csrf := s.issueDashboardSession(w, r)
	s.dashboardAuth.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": csrf, "must_change_password": false})
}
