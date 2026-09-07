package rest

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"

	documents "github.com/Bharanipbk/gideondb/docs"
)

//go:embed docsweb/*
var docsWebAssets embed.FS

type docsIndexEntry struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Section string `json:"section"`
}

func (s *Server) docsWeb(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/docs" {
		http.Redirect(w, r, "/docs/", http.StatusTemporaryRedirect)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/docs/")), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	assets, err := fs.Sub(docsWebAssets, "docsweb")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	clone := r.Clone(r.Context())
	if name == "index.html" {
		clone.URL.Path = "/"
	} else {
		clone.URL.Path = "/" + name
	}
	http.FileServer(http.FS(assets)).ServeHTTP(w, clone)
}

func (s *Server) docsIndex(w http.ResponseWriter, _ *http.Request) {
	entries := make([]docsIndexEntry, 0, 64)
	_ = fs.WalkDir(documents.Files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !publicDocument(name) {
			return nil
		}
		data, readErr := documents.Files.ReadFile(name)
		if readErr != nil {
			return nil
		}
		section := "Overview"
		if directory := path.Dir(name); directory != "." {
			section = documentationSection(strings.Split(directory, "/")[0])
		}
		entries = append(entries, docsIndexEntry{Path: name, Title: markdownTitle(string(data), name), Section: section})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool {
		leftSection, rightSection := documentationSectionOrder(entries[i].Section), documentationSectionOrder(entries[j].Section)
		if leftSection == rightSection {
			leftDocument, rightDocument := documentationDocumentOrder(entries[i].Path), documentationDocumentOrder(entries[j].Path)
			if leftDocument == rightDocument {
				return entries[i].Title < entries[j].Title
			}
			return leftDocument < rightDocument
		}
		return leftSection < rightSection
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"documents": entries})
}

func documentationSectionOrder(section string) int {
	order := map[string]int{
		"Overview":        0,
		"Getting Started": 1,
		"API":             2,
		"SDK":             3,
		"Integrations":    4,
		"Indexing":        5,
		"Architecture":    6,
		"Operations":      7,
	}
	if position, exists := order[section]; exists {
		return position
	}
	return len(order)
}

func documentationDocumentOrder(name string) int {
	order := map[string]int{
		"README.md":                           0,
		"glossary.md":                         1,
		"getting-started/quickstart.md":       0,
		"getting-started/adoption.md":         1,
		"api/rest.md":                         0,
		"sdk/go.md":                           0,
		"sdk/python.md":                       1,
		"sdk/typescript.md":                   2,
		"sdk/java.md":                         3,
		"sdk/rust.md":                         4,
		"sdk/dotnet.md":                       5,
		"integrations/ollama.md":              0,
		"integrations/huggingface.md":         1,
		"integrations/openai-compatible.md":   2,
		"integrations/cohere.md":              3,
		"integrations/langchain.md":           4,
		"integrations/llamaindex.md":          5,
		"indexing/hnsw.md":                    0,
		"indexing/filtering.md":               1,
		"indexing/sparse-hybrid.md":           2,
		"architecture/segments.md":            0,
		"architecture/wal.md":                 1,
		"architecture/mmap-vectors.md":        2,
		"architecture/cluster-foundations.md": 3,
		"architecture/replication.md":         4,
		"architecture/rebalancing.md":         5,
		"operations/configuration.md":         0,
		"operations/security.md":              1,
		"operations/docker.md":                2,
		"operations/kubernetes.md":            3,
		"operations/dashboard.md":             4,
		"operations/observability.md":         5,
		"operations/backup-restore.md":        6,
		"operations/rolling-upgrades.md":      7,
		"operations/compatibility.md":          8,
		"operations/network-partitions.md":    9,
	}
	if position, exists := order[name]; exists {
		return position
	}
	return len(order)
}

func documentationSection(directory string) string {
	switch directory {
	case "api":
		return "API"
	case "sdk":
		return "SDK"
	default:
		return titleCase(directory)
	}
}

func (s *Server) docsContent(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(r.PathValue("path"))
	if name == "." || strings.HasPrefix(name, "../") || !publicDocument(name) {
		http.NotFound(w, r)
		return
	}
	data, err := documents.Files.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write(data)
}

func publicDocument(name string) bool {
	if name == "README.md" || name == "glossary.md" {
		return true
	}
	if name == "api/rest.md" {
		return true
	}
	if name == "architecture/phase-1.md" {
		return false
	}
	for _, directory := range []string{"getting-started/", "architecture/", "indexing/", "integrations/", "operations/", "sdk/"} {
		if strings.HasPrefix(name, directory) && strings.HasSuffix(name, ".md") {
			return true
		}
	}
	return false
}

func markdownTitle(content, fallback string) string {
	for _, line := range strings.Split(content, "\n") {
		if title := strings.TrimSpace(strings.TrimPrefix(line, "# ")); strings.HasPrefix(line, "# ") && title != "" {
			return title
		}
	}
	return titleCase(strings.TrimSuffix(path.Base(fallback), path.Ext(fallback)))
}

func titleCase(value string) string {
	words := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(value, "-", " "), "_", " "))
	for index, word := range words {
		if word != "" {
			words[index] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}
