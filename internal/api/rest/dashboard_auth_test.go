package rest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/engine"
)

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
