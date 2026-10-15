package embedding

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config describes one named embedding model, loaded from the server's
// embedders file. Keys are read from APIKeyFile so they never live in the
// collection catalog, WAL or API responses.
type Config struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Model          string `json:"model"`
	APIKeyFile     string `json:"api_key_file,omitempty"`
	Dimension      int    `json:"dimension,omitempty"`
	BatchSize      int    `json:"batch_size,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	CacheEntries   int    `json:"cache_entries,omitempty"`
}

type fileFormat struct {
	Embedders []Config `json:"embedders"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidName reports whether name can identify an embedder.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// Registry holds the server's embedders by name.
type Registry struct {
	models map[string]*Model
}

// Model is a named embedder with batching and a content-hash cache.
type Model struct {
	config   Config
	embedder Embedder
	cache    *lru
	hits     atomic.Uint64
	misses   atomic.Uint64
}

// Stats reports cache effectiveness for one model.
type Stats struct {
	Name        string `json:"name"`
	Model       string `json:"model"`
	Dimension   int    `json:"dimension,omitempty"`
	CacheHits   uint64 `json:"cache_hits"`
	CacheMisses uint64 `json:"cache_misses"`
}

// LoadFile reads {"embedders":[...]} from path.
func LoadFile(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read embedders file: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file fileFormat
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse embedders file: %w", err)
	}
	return NewRegistry(file.Embedders)
}

// NewRegistry validates configs and builds OpenAI-compatible clients for them.
func NewRegistry(configs []Config) (*Registry, error) {
	registry := &Registry{models: make(map[string]*Model, len(configs))}
	for _, config := range configs {
		if !ValidName(config.Name) {
			return nil, fmt.Errorf("embedder name %q must match %s", config.Name, namePattern)
		}
		if _, exists := registry.models[config.Name]; exists {
			return nil, fmt.Errorf("duplicate embedder %q", config.Name)
		}
		parsed, err := url.Parse(config.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("embedder %q: url must be an absolute http(s) URL", config.Name)
		}
		if config.Model == "" {
			return nil, fmt.Errorf("embedder %q: model is required", config.Name)
		}
		if config.Dimension < 0 || config.BatchSize < 0 || config.TimeoutSeconds < 0 || config.CacheEntries < 0 {
			return nil, fmt.Errorf("embedder %q: numeric settings must not be negative", config.Name)
		}
		apiKey := ""
		if config.APIKeyFile != "" {
			data, err := os.ReadFile(config.APIKeyFile)
			if err != nil {
				return nil, fmt.Errorf("embedder %q: read api_key_file: %w", config.Name, err)
			}
			apiKey = strings.TrimSpace(string(data))
		}
		timeout := 30 * time.Second
		if config.TimeoutSeconds > 0 {
			timeout = time.Duration(config.TimeoutSeconds) * time.Second
		}
		embedder := &OpenAIEmbedder{BaseURL: config.URL, Model: config.Model, APIKey: apiKey, HTTP: &http.Client{Timeout: timeout}}
		registry.models[config.Name] = NewModel(config, embedder)
	}
	return registry, nil
}

// NewModel wraps embedder with batching and caching; tests use it directly.
func NewModel(config Config, embedder Embedder) *Model {
	if config.BatchSize == 0 {
		config.BatchSize = 64
	}
	if config.CacheEntries == 0 {
		config.CacheEntries = 10_000
	}
	return &Model{config: config, embedder: embedder, cache: newLRU(config.CacheEntries)}
}

// NewRegistryWithModels builds a registry from prepared models.
func NewRegistryWithModels(models ...*Model) *Registry {
	registry := &Registry{models: make(map[string]*Model, len(models))}
	for _, model := range models {
		registry.models[model.config.Name] = model
	}
	return registry
}

// Get returns the named model.
func (r *Registry) Get(name string) (*Model, bool) {
	if r == nil {
		return nil, false
	}
	model, ok := r.models[name]
	return model, ok
}

// Stats lists every model's cache counters, sorted by name.
func (r *Registry) Stats() []Stats {
	if r == nil {
		return nil
	}
	stats := make([]Stats, 0, len(r.models))
	for _, model := range r.models {
		stats = append(stats, Stats{Name: model.config.Name, Model: model.config.Model, Dimension: model.config.Dimension, CacheHits: model.hits.Load(), CacheMisses: model.misses.Load()})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Name < stats[j].Name })
	return stats
}

// Dimension is the configured output dimension, or 0 when not declared.
func (m *Model) Dimension() int { return m.config.Dimension }

// ErrDimension reports a model that returned vectors of the wrong size.
var ErrDimension = errors.New("embedding dimension mismatch")

// Embed returns one vector per text. Cached texts are not re-sent; the rest
// go to the model in batches. Every vector must have dimension when it is
// positive.
func (m *Model) Embed(ctx context.Context, texts []string, dimension int) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	keys := make([][32]byte, len(texts))
	var pending []int
	for i, text := range texts {
		keys[i] = sha256.Sum256([]byte(m.config.Model + "\x00" + text))
		if vector, ok := m.cache.get(keys[i]); ok {
			vectors[i] = vector
			m.hits.Add(1)
			continue
		}
		pending = append(pending, i)
	}
	m.misses.Add(uint64(len(pending)))
	for start := 0; start < len(pending); start += m.config.BatchSize {
		batch := pending[start:min(start+m.config.BatchSize, len(pending))]
		inputs := make([]string, len(batch))
		for i, position := range batch {
			inputs[i] = texts[position]
		}
		embedded, err := m.embedder.Embed(ctx, inputs)
		if err != nil {
			return nil, fmt.Errorf("embedder %q: %w", m.config.Name, err)
		}
		if len(embedded) != len(batch) {
			return nil, fmt.Errorf("embedder %q returned %d vectors for %d texts", m.config.Name, len(embedded), len(batch))
		}
		for i, position := range batch {
			if dimension > 0 && len(embedded[i]) != dimension {
				return nil, fmt.Errorf("%w: embedder %q returned %d values, collection expects %d", ErrDimension, m.config.Name, len(embedded[i]), dimension)
			}
			vectors[position] = embedded[i]
			m.cache.put(keys[position], embedded[i])
		}
	}
	return vectors, nil
}

// lru is a fixed-capacity least-recently-used cache of vectors. Cached
// slices are shared, so callers must not modify returned vectors.
type lru struct {
	mu       sync.Mutex
	capacity int
	order    *list.List
	items    map[[32]byte]*list.Element
}

type lruEntry struct {
	key    [32]byte
	vector []float32
}

func newLRU(capacity int) *lru {
	return &lru{capacity: capacity, order: list.New(), items: make(map[[32]byte]*list.Element, capacity)}
}

func (c *lru) get(key [32]byte) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*lruEntry).vector, true
}

func (c *lru) put(key [32]byte, vector []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		element.Value.(*lruEntry).vector = vector
		c.order.MoveToFront(element)
		return
	}
	c.items[key] = c.order.PushFront(&lruEntry{key: key, vector: vector})
	if c.order.Len() > c.capacity {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*lruEntry).key)
	}
}
