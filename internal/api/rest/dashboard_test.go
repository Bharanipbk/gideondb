package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vectordb/vectordb/internal/engine"
)

func TestDashboardAssetsAndSecurityPolicy(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler := New(db, nil).Handler()
	rootRedirect := httptest.NewRecorder()
	handler.ServeHTTP(rootRedirect, httptest.NewRequest(http.MethodGet, "/", nil))
	if rootRedirect.Code != http.StatusTemporaryRedirect || rootRedirect.Header().Get("Location") != "/dashboard/" {
		t.Fatalf("root redirect=%d %q", rootRedirect.Code, rootRedirect.Header().Get("Location"))
	}
	redirect := httptest.NewRecorder()
	handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if redirect.Code != http.StatusTemporaryRedirect || redirect.Header().Get("Location") != "/dashboard/" {
		t.Fatalf("redirect=%d %q", redirect.Code, redirect.Header().Get("Location"))
	}
	for asset, expected := range map[string][2]string{
		"/dashboard/":                  {"text/html", "request-rate-chart"},
		"/dashboard/app.js":            {"text/javascript", "recordPerformance"},
		"/dashboard/styles.css":        {"text/css", "--accent"},
		"/dashboard/performance.css":   {"text/css", ".chart-grid"},
		"/dashboard/explorer.css":      {"text/css", ".explorer-tools"},
		"/dashboard/configuration.css": {"text/css", ".configuration-details"},
		"/dashboard/logs.css":          {"text/css", ".log-row"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, asset, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", asset, response.Code)
		}
		if !strings.Contains(response.Header().Get("Content-Type"), expected[0]) {
			t.Fatalf("%s content-type=%q", asset, response.Header().Get("Content-Type"))
		}
		if !strings.Contains(response.Body.String(), expected[1]) {
			t.Fatalf("%s missing marker", asset)
		}
		policy := response.Header().Get("Content-Security-Policy")
		if !strings.Contains(policy, "default-src 'self'") || !strings.Contains(policy, "connect-src 'self'") {
			t.Fatalf("%s policy=%q", asset, policy)
		}
	}
}

func TestNodeInfoReportsPostureWithoutSecrets(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler := NewWithOptions(db, nil, Options{APIKey: "0123456789abcdef", NodeID: "11111111111111111111111111111111", ClusterID: "22222222222222222222222222222222", AdvertiseAddress: "node-a:6333", RequireInternalMTLS: true, EnableStaticRouting: true}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/v1/node", nil)
	request.Header.Set("Authorization", "Bearer 0123456789abcdef")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"authentication_required":true`) || !strings.Contains(body, `"internal_mtls_required":true`) || !strings.Contains(body, `"static_routing":true`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	if strings.Contains(body, "0123456789abcdef") {
		t.Fatal("node info leaked API key")
	}
}
