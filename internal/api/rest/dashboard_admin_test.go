package rest

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func adminRequest(h http.Handler, method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	r.Header.Set("X-GideonDB-CSRF", csrf)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestDashboardSQLiteMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin.sqlite")
	old := newDashboardAuthenticator("existing-admin", "existing-password", "", false).users["existing-admin"]
	legacy := dashboardCredentials{Username: old.Username, Algorithm: "pbkdf2-sha256", Iterations: dashboardPasswordIterations, Salt: hex.EncodeToString(old.Salt), PasswordHash: hex.EncodeToString(old.Hash)}
	data, _ := json.Marshal(legacy)
	os.WriteFile(filepath.Join(dir, "dashboard-credentials.json"), data, 0600)
	if err := PrepareDashboardCredentials(path, "admin", "ignored-password", true); err != nil {
		t.Fatal(err)
	}
	a, err := loadDashboardAuthenticator(path)
	if err != nil {
		t.Fatal(err)
	}
	if !a.authenticate("existing-admin", "existing-password") || a.authenticate("admin", "ignored-password") {
		t.Fatal("migration changed login")
	}
	if _, err = os.Stat(filepath.Join(dir, "dashboard-credentials.json")); !os.IsNotExist(err) {
		t.Fatal("legacy JSON remains")
	}
	data, err = os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(data, []byte("SQLite format 3")) {
		t.Fatal("not SQLite")
	}
	if err = PrepareDashboardCredentials(path, "admin", "replacement-password", true); err != nil {
		t.Fatal(err)
	}
	a, err = loadDashboardAuthenticator(path)
	if err != nil || !a.authenticate("existing-admin", "existing-password") {
		t.Fatal("second initialization reset credentials")
	}
}
func TestDashboardUsersAndSettingsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.sqlite")
	if err := PrepareDashboardCredentials(path, "admin", "administrator-password", false); err != nil {
		t.Fatal(err)
	}
	h := NewWithOptions(dashboardAuthTestEngine(t), nil, Options{DashboardCredentialsFile: path}).Handler()
	cookie, csrf, _ := loginDashboard(t, h, "administrator-password")
	request := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := adminRequest(h, method, path, body, cookie, csrf)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	request("POST", "/v1/dashboard/users", `{"username":"reader","role":"reader","password":"reader-password"}`, 201)
	request("POST", "/v1/dashboard/users", `{"username":"reader","role":"admin","password":"reader-password"}`, 409)
	request("POST", "/v1/dashboard/users", `{"username":"bad","role":"admin","password":"short"}`, 400)
	request("POST", "/v1/dashboard/users", `{"username":"bad","role":"superuser","password":"long-password"}`, 400)
	request("DELETE", "/v1/dashboard/users/admin", "", 409)
	request("PUT", "/v1/dashboard/users/admin", `{"role":"reader"}`, 409)
	request("PUT", "/v1/dashboard/settings", `{"site_name":"My Console","session_hours":2,"refresh_seconds":15}`, 200)
	request("PUT", "/v1/dashboard/settings", `{"site_name":"","session_hours":0,"refresh_seconds":1}`, 400)
	if w := adminRequest(h, "POST", "/v1/dashboard/users", `{"username":"csrf","role":"admin","password":"long-password"}`, cookie, ""); w.Code != 403 {
		t.Fatal("CSRF bypass")
	}
	if w := adminRequest(h, "GET", "/v1/dashboard/users", "", nil, ""); w.Code != 401 {
		t.Fatal("anonymous access")
	}
	a, err := loadDashboardAuthenticator(path)
	if err != nil {
		t.Fatal(err)
	}
	if !a.authenticate("reader", "reader-password") || a.settings.SiteName != "My Console" || a.settings.SessionHours != 2 {
		t.Fatal("user/settings not persisted")
	}
	if strings.Contains(request("GET", "/v1/dashboard/users", "", 200).Body.String(), "password_hash") {
		t.Fatal("hash leaked")
	}
	request("PUT", "/v1/dashboard/users/reader", `{"role":"writer","disabled":true}`, 200)
	a, _ = loadDashboardAuthenticator(path)
	if a.authenticate("reader", "reader-password") {
		t.Fatal("disabled user authenticated")
	}
	request("PUT", "/v1/dashboard/users/reader", `{"role":"writer","password":"replacement-password"}`, 200)
	a, _ = loadDashboardAuthenticator(path)
	if !a.authenticate("reader", "replacement-password") || a.authenticate("reader", "reader-password") {
		t.Fatal("reset failed")
	}
	request("DELETE", "/v1/dashboard/users/reader", "", 204)
	a, _ = loadDashboardAuthenticator(path)
	if _, ok := a.users["reader"]; ok {
		t.Fatal("deletion not persisted")
	}
}
func TestDashboardReaderAuthorizationAndRevocation(t *testing.T) {
	s := NewWithOptions(dashboardAuthTestEngine(t), nil, Options{DashboardUsername: "admin", DashboardPassword: "administrator-password"})
	h := s.Handler()
	adminCookie, adminCSRF, _ := loginDashboard(t, h, "administrator-password")
	w := adminRequest(h, "POST", "/v1/dashboard/users", `{"username":"reader","role":"reader","password":"reader-password"}`, adminCookie, adminCSRF)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	w = adminRequest(h, "POST", "/v1/dashboard/session", `{"username":"reader","password":"reader-password"}`, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &session)
	if w = adminRequest(h, "GET", "/v1/collections", "", cookie, ""); w.Code != 403 {
		t.Fatal("bootstrap reader bypassed password requirement")
	}
	w = adminRequest(h, "POST", "/v1/dashboard/password", `{"current_password":"reader-password","new_password":"changed-reader-password"}`, cookie, session.CSRF)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookie = w.Result().Cookies()[0]
	json.Unmarshal(w.Body.Bytes(), &session)
	for _, path := range []string{"/v1/dashboard/users", "/v1/dashboard/settings"} {
		if w = adminRequest(h, "GET", path, "", cookie, ""); w.Code != 403 {
			t.Fatalf("reader accessed %s", path)
		}
	}
	if w = adminRequest(h, "GET", "/v1/collections", "", cookie, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = adminRequest(h, "POST", "/v1/collections", `{}`, cookie, session.CSRF); w.Code != 403 {
		t.Fatal("reader created collection")
	}
	if w = adminRequest(h, "PUT", "/v1/dashboard/users/reader", `{"role":"reader","disabled":true}`, adminCookie, adminCSRF); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = adminRequest(h, "GET", "/v1/collections", "", cookie, ""); w.Code != 401 {
		t.Fatal("disabled session still active")
	}
	if w = adminRequest(h, "GET", "/v1/collections", "", adminCookie, ""); w.Code != 200 {
		t.Fatal("unrelated admin session revoked")
	}
}
func TestDashboardInvalidStoreFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.sqlite")
	os.WriteFile(path, []byte("corrupt"), 0600)
	h := NewWithOptions(dashboardAuthTestEngine(t), nil, Options{DashboardCredentialsFile: path}).Handler()
	if w := adminRequest(h, "GET", "/v1/collections", "", nil, ""); w.Code != 401 {
		t.Fatal("invalid credentials enabled anonymous access")
	}
}

func TestDashboardPersistenceFailureRollsBack(t *testing.T) {
	s := NewWithOptions(dashboardAuthTestEngine(t), nil, Options{DashboardUsername: "admin", DashboardPassword: "administrator-password"})
	h := s.Handler()
	cookie, csrf, _ := loginDashboard(t, h, "administrator-password")
	// A directory cannot be opened as a SQLite database.
	s.dashboardAuth.path = t.TempDir()
	w := adminRequest(h, "POST", "/v1/dashboard/users", `{"username":"lost","role":"reader","password":"reader-password"}`, cookie, csrf)
	if w.Code != 500 {
		t.Fatalf("failed persistence returned %d", w.Code)
	}
	if _, ok := s.dashboardAuth.users["lost"]; ok {
		t.Fatal("uncommitted user remained active")
	}
	w = adminRequest(h, "PUT", "/v1/dashboard/settings", `{"site_name":"Unsaved","session_hours":1,"refresh_seconds":10}`, cookie, csrf)
	if w.Code != 500 || s.dashboardAuth.settings.SiteName != "GideonDB" {
		t.Fatal("settings were changed without persistence")
	}
}
