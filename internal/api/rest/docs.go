package rest

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"

	documents "github.com/vectordb/vectordb/docs"
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
		if err != nil || entry.IsDir() || !strings.HasSuffix(name, ".md") {
			return nil
		}
		data, readErr := documents.Files.ReadFile(name)
		if readErr != nil {
			return nil
		}
		section := "Overview"
		if directory := path.Dir(name); directory != "." {
			section = titleCase(strings.Split(directory, "/")[0])
		}
		entries = append(entries, docsIndexEntry{Path: name, Title: markdownTitle(string(data), name), Section: section})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Section == entries[j].Section {
			return entries[i].Title < entries[j].Title
		}
		return entries[i].Section < entries[j].Section
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"documents": entries})
}

func (s *Server) docsContent(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(r.PathValue("path"))
	if name == "." || strings.HasPrefix(name, "../") || !strings.HasSuffix(name, ".md") {
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
