package rest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/engine"
)

func loginDashboard(t *testing.T, handler http.Handler, password string) (*http.Cookie, string, bool) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/dashboard/session", bytes.NewBufferString(`{"username":"admin","password":"`+password+`"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("login = %d %q", response.Code, response.Body.String())
	}
	var body struct {
		CSRF       string `json:"csrf_token"`
		MustChange bool   `json:"must_change_password"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return response.Result().Cookies()[0], body.CSRF, body.MustChange
}

func dashboardAuthTestEngine(t *testing.T) *engine.Engine {
	t.Helper()
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestDashboardSessionAuthenticationAndCSRF(t *testing.T) {
	db := dashboardAuthTestEngine(t)
	handler := NewWithOptions(db, nil, Options{DashboardUsername: "admin", DashboardPassword: "admin123"}).Handler()

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/dashboard/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Sign in") {
		t.Fatalf("unauthenticated dashboard = %d %q", page.Code, page.Body.String())
	}

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/v1/dashboard/session", bytes.NewBufferString(`{"username":"admin","password":"admin123"}`)))
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login = %d %q", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("insecure session cookie: %#v", cookie)
	}
	var loginBody struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &loginBody); err != nil || loginBody.CSRF == "" {
		t.Fatalf("missing CSRF token: %v %q", err, login.Body.String())
	}

	mutation := httptest.NewRequest(http.MethodPost, "/v1/collections", bytes.NewBufferString(`{}`))
	mutation.AddCookie(cookie)
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, mutation)
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "csrf_rejected") {
		t.Fatalf("mutation without CSRF = %d %q", denied.Code, denied.Body.String())
	}

	page = httptest.NewRecorder()
	authorizedPage := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	authorizedPage.AddCookie(cookie)
	handler.ServeHTTP(page, authorizedPage)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "request-rate-chart") {
		t.Fatalf("authenticated dashboard = %d %q", page.Code, page.Body.String())
	}
}

func TestDashboardLoginThrottle(t *testing.T) {
	db := dashboardAuthTestEngine(t)
	handler := NewWithOptions(db, nil, Options{DashboardUsername: "admin", DashboardPassword: "correct-password"}).Handler()
	for attempt := 1; attempt <= 6; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/dashboard/session", bytes.NewBufferString(`{"username":"admin","password":"wrong-password"}`))
		request.RemoteAddr = "192.0.2.10:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if attempt == 6 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d = %d, want %d", attempt, response.Code, want)
		}
	}
}

func TestDashboardBootstrapPasswordChangePersistsAndRotatesSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard-credentials.json")
	if err := PrepareDashboardCredentials(path, "admin", "admin123", true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %v, err=%v", info.Mode().Perm(), err)
	}
	if data, err := os.ReadFile(path); err != nil || bytes.Contains(data, []byte("admin123")) {
		t.Fatalf("credential file contains plaintext or cannot be read: %v", err)
	}
	handler := NewWithOptions(dashboardAuthTestEngine(t), nil, Options{DashboardCredentialsFile: path}).Handler()
	cookie, csrf, mustChange := loginDashboard(t, handler, "admin123")
	if !mustChange {
		t.Fatal("bootstrap login did not require password replacement")
	}
	blocked := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/collections", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(blocked, request)
	if blocked.Code != http.StatusForbidden || !strings.Contains(blocked.Body.String(), "password_change_required") {
		t.Fatalf("bootstrap API access = %d %q", blocked.Code, blocked.Body.String())
	}
	change := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/dashboard/password", bytes.NewBufferString(`{"current_password":"admin123","new_password":"unique-password-456"}`))
	request.AddCookie(cookie)
	request.Header.Set("X-GideonDB-CSRF", csrf)
	handler.ServeHTTP(change, request)
	if change.Code != http.StatusOK {
		t.Fatalf("password change = %d %q", change.Code, change.Body.String())
	}
	stale := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/v1/collections", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(stale, request)
	if stale.Code != http.StatusUnauthorized {
		t.Fatalf("old session = %d", stale.Code)
	}
	reloaded, err := loadDashboardAuthenticator(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.mustChange || !reloaded.authenticate("admin", "unique-password-456") || reloaded.authenticate("admin", "admin123") {
		t.Fatal("persisted password state is invalid")
	}
}

func TestDashboardPasswordFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard-password")
	if err := os.WriteFile(path, []byte("operator-password-456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	password, err := LoadDashboardPasswordFile(path)
	if err != nil || password != "operator-password-456" {
		t.Fatalf("password=%q err=%v", password, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDashboardPasswordFile(path); err == nil {
		t.Fatal("world-readable password file was accepted")
	}
}

func TestDashboardSessionRefreshRotatesCookieAndCSRF(t *testing.T) {
	handler := NewWithOptions(dashboardAuthTestEngine(t), nil, Options{DashboardUsername: "admin", DashboardPassword: "operator-password-456"}).Handler()
	cookie, csrf, _ := loginDashboard(t, handler, "operator-password-456")
	refresh := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/dashboard/session/refresh", nil)
	request.AddCookie(cookie)
	request.Header.Set("X-GideonDB-CSRF", csrf)
	handler.ServeHTTP(refresh, request)
	if refresh.Code != http.StatusOK || len(refresh.Result().Cookies()) != 1 {
		t.Fatalf("refresh = %d %q", refresh.Code, refresh.Body.String())
	}
	rotatedCookie := refresh.Result().Cookies()[0]
	if rotatedCookie.Value == cookie.Value {
		t.Fatal("session cookie was not rotated")
	}
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(refresh.Body.Bytes(), &body); err != nil || body.CSRF == "" || body.CSRF == csrf {
		t.Fatalf("CSRF was not rotated: %v %q", err, refresh.Body.String())
	}
	stale := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/v1/collections", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(stale, request)
	if stale.Code != http.StatusUnauthorized {
		t.Fatalf("stale session = %d", stale.Code)
	}
	active := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/v1/collections", nil)
	request.AddCookie(rotatedCookie)
	handler.ServeHTTP(active, request)
	if active.Code != http.StatusOK {
		t.Fatalf("rotated session = %d %q", active.Code, active.Body.String())
	}
}
