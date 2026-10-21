// Package rerank calls external cross-encoder rerank endpoints. The request
// and response follow the shape shared by Cohere, Jina, Voyage, vLLM, TEI
// (with /rerank routing) and Infinity: POST {url}/rerank with model, query
// and documents; the reply lists {index, relevance_score}.
package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Config describes one named reranker, loaded from the server's rerankers
// file. Keys are read from APIKeyFile so they never live in the catalog, WAL
// or API responses.
type Config struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Model          string `json:"model"`
	APIKeyFile     string `json:"api_key_file,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	// MaxDocuments bounds how many candidates one request may send.
	MaxDocuments int `json:"max_documents,omitempty"`
	// MaxDocumentBytes truncates each document before it is sent.
	MaxDocumentBytes int `json:"max_document_bytes,omitempty"`
}

const (
	defaultTimeout          = 5 * time.Second
	defaultMaxDocuments     = 200
	defaultMaxDocumentBytes = 8 << 10
	maxResponseBytes        = 16 << 20
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type fileFormat struct {
	Rerankers []Config `json:"rerankers"`
}

// Registry holds the server's rerankers by name. A nil registry has none.
type Registry struct {
	models map[string]*Model
}

// Model is one configured reranker.
type Model struct {
	config Config
	apiKey string
	http   *http.Client
}

// Score is one reranked document: its position in the request and score.
type Score struct {
	Index int     `json:"index"`
	Score float32 `json:"relevance_score"`
}

// LoadFile reads {"rerankers":[...]} from path.
func LoadFile(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rerankers file: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file fileFormat
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse rerankers file: %w", err)
	}
	return NewRegistry(file.Rerankers)
}

// NewRegistry validates configs and builds HTTP clients for them.
func NewRegistry(configs []Config) (*Registry, error) {
	registry := &Registry{models: make(map[string]*Model, len(configs))}
	for _, config := range configs {
		if !namePattern.MatchString(config.Name) {
			return nil, fmt.Errorf("reranker name %q must match %s", config.Name, namePattern)
		}
		if _, exists := registry.models[config.Name]; exists {
			return nil, fmt.Errorf("duplicate reranker %q", config.Name)
		}
		parsed, err := url.Parse(config.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("reranker %q: url must be an absolute http(s) URL", config.Name)
		}
		if config.Model == "" {
			return nil, fmt.Errorf("reranker %q: model is required", config.Name)
		}
		if config.TimeoutSeconds < 0 || config.MaxDocuments < 0 || config.MaxDocumentBytes < 0 {
			return nil, fmt.Errorf("reranker %q: numeric settings must not be negative", config.Name)
		}
		apiKey := ""
		if config.APIKeyFile != "" {
			data, err := os.ReadFile(config.APIKeyFile)
			if err != nil {
				return nil, fmt.Errorf("reranker %q: read api_key_file: %w", config.Name, err)
			}
			apiKey = strings.TrimSpace(string(data))
		}
		registry.models[config.Name] = NewModel(config, apiKey, nil)
	}
	return registry, nil
}

// NewModel builds a reranker; a nil client uses one with the configured
// timeout. Tests use it directly.
func NewModel(config Config, apiKey string, client *http.Client) *Model {
	if config.MaxDocuments == 0 {
		config.MaxDocuments = defaultMaxDocuments
	}
	if config.MaxDocumentBytes == 0 {
		config.MaxDocumentBytes = defaultMaxDocumentBytes
	}
	if client == nil {
		client = &http.Client{}
	}
	return &Model{config: config, apiKey: apiKey, http: client}
}

// NewRegistryWithModels builds a registry from prepared models.
func NewRegistryWithModels(models ...*Model) *Registry {
	registry := &Registry{models: make(map[string]*Model, len(models))}
	for _, model := range models {
		registry.models[model.config.Name] = model
	}
	return registry
}

// Get returns the named reranker.
func (r *Registry) Get(name string) (*Model, bool) {
	if r == nil {
		return nil, false
	}
	model, ok := r.models[name]
	return model, ok
}

// Names lists configured rerankers, sorted.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.models))
	for name := range r.models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MaxDocuments is the most candidates one Rerank call accepts.
func (m *Model) MaxDocuments() int { return m.config.MaxDocuments }

// Timeout is the deadline applied to one Rerank call.
func (m *Model) Timeout() time.Duration {
	if m.config.TimeoutSeconds > 0 {
		return time.Duration(m.config.TimeoutSeconds) * time.Second
	}
	return defaultTimeout
}

// Rerank scores every document against query. The result has one score per
// document, ordered by document position.
func (m *Model) Rerank(ctx context.Context, query string, documents []string) ([]float32, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	if len(documents) > m.config.MaxDocuments {
		return nil, fmt.Errorf("reranker %q accepts at most %d documents", m.config.Name, m.config.MaxDocuments)
	}
	ctx, cancel := context.WithTimeout(ctx, m.Timeout())
	defer cancel()
	truncated := make([]string, len(documents))
	for i, document := range documents {
		truncated[i] = truncateUTF8(document, m.config.MaxDocumentBytes)
	}
	body, _ := json.Marshal(map[string]any{"model": m.config.Model, "query": query, "documents": truncated, "top_n": len(truncated)})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.config.URL, "/")+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
	reply, err := m.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("rerank request: %w", err)
	}
	defer reply.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(reply.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("rerank response: %w", err)
	}
	if reply.StatusCode < 200 || reply.StatusCode >= 300 {
		return nil, fmt.Errorf("rerank endpoint returned HTTP %d: %s", reply.StatusCode, truncateUTF8(string(payload), 300))
	}
	var parsed struct {
		Results []Score `json:"results"`
		Data    []Score `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("decode rerank response: %w", err)
	}
	items := parsed.Results
	if items == nil {
		items = parsed.Data
	}
	if len(items) != len(documents) {
		return nil, fmt.Errorf("rerank endpoint returned %d scores for %d documents", len(items), len(documents))
	}
	scores := make([]float32, len(documents))
	seen := make([]bool, len(documents))
	for _, item := range items {
		if item.Index < 0 || item.Index >= len(documents) || seen[item.Index] {
			return nil, fmt.Errorf("rerank response has invalid indexes")
		}
		seen[item.Index] = true
		scores[item.Index] = item.Score
	}
	return scores, nil
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && (value[cut]&0xc0) == 0x80 {
		cut--
	}
	return value[:cut]
}
