package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/engine"
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
		"/docs/":           "GideonDB Docs",
		"/docs/styles.css": "--accent",
		"/docs/app.js":     "loadDocument",
		"/docs/theme.js":   "gideondb-theme",
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
	expectedSections := []string{"Overview", "Getting Started", "API", "SDK", "Integrations", "Indexing", "Architecture", "Operations"}
	sections := make([]string, 0, len(expectedSections))
	for _, document := range index.Documents {
		if len(sections) == 0 || sections[len(sections)-1] != document.Section {
			sections = append(sections, document.Section)
		}
	}
	if strings.Join(sections, ",") != strings.Join(expectedSections, ",") {
		t.Fatalf("section order=%v", sections)
	}
	if index.Documents[0].Path != "README.md" || index.Documents[1].Path != "glossary.md" {
		t.Fatalf("overview order=%v", index.Documents[:2])
	}
	for _, document := range index.Documents {
		if !publicDocument(document.Path) {
			t.Fatalf("internal document exposed in public index: %s", document.Path)
		}
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

	internal := httptest.NewRecorder()
	handler.ServeHTTP(internal, httptest.NewRequest(http.MethodGet, "/docs/_content/design/decisions/README.md", nil))
	if internal.Code != http.StatusNotFound {
		t.Fatalf("internal document status=%d", internal.Code)
	}
}
