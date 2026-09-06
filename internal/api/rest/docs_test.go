package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vectordb/vectordb/internal/engine"
)

func TestDocumentationWebsite(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler := New(db, nil).Handler()

	redirect := httptest.NewRecorder()
	handler.ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if redirect.Code != http.StatusTemporaryRedirect || redirect.Header().Get("Location") != "/docs/" {
		t.Fatalf("redirect=%d %q", redirect.Code, redirect.Header().Get("Location"))
	}

	for target, marker := range map[string]string{
		"/docs/":           "VectorDB Docs",
		"/docs/styles.css": "--accent",
		"/docs/app.js":     "loadDocument",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), marker) {
			t.Fatalf("%s status=%d missing %q", target, response.Code, marker)
		}
	}

	indexResponse := httptest.NewRecorder()
	handler.ServeHTTP(indexResponse, httptest.NewRequest(http.MethodGet, "/docs/_index", nil))
	var index struct {
		Documents []docsIndexEntry `json:"documents"`
	}
	if err := json.Unmarshal(indexResponse.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if indexResponse.Code != http.StatusOK || len(index.Documents) < 10 {
		t.Fatalf("index status=%d documents=%d", indexResponse.Code, len(index.Documents))
	}

	content := httptest.NewRecorder()
	handler.ServeHTTP(content, httptest.NewRequest(http.MethodGet, "/docs/_content/getting-started/quickstart.md", nil))
	if content.Code != http.StatusOK || !strings.Contains(content.Body.String(), "# Phase 1 quickstart") {
		t.Fatalf("content status=%d body=%q", content.Code, content.Body.String())
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/docs/_content/missing.md", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d", missing.Code)
	}
}
