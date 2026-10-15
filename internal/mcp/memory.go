package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/Bharanipbk/gideondb/pkg/client"
)

// Memories are ordinary records in one collection. Each user is a namespace,
// so memories never leak between users. Metadata carries:
//
//	created_at  unix seconds when the memory was stored
//	expires_at  unix seconds after which it is ignored (neverExpires if no TTL)
//
// and the text lives in payload.text.
const (
	createdAtKey = "created_at"
	expiresAtKey = "expires_at"
	// neverExpires is the largest integer a float64 JSON number holds exactly.
	neverExpires = int64(1<<53 - 1)
	// recallOversampling fetches extra candidates so recency re-ranking can
	// promote recent memories that are slightly less similar.
	recallOversampling = 4
)

func (t *toolset) remember(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		User       string         `json:"user"`
		Text       string         `json:"text"`
		Metadata   map[string]any `json:"metadata"`
		TTLSeconds int64          `json:"ttl_seconds"`
		ID         string         `json:"id"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.User == "" || input.Text == "" {
		return nil, errors.New("user and text are required")
	}
	if input.TTLSeconds < 0 {
		return nil, errors.New("ttl_seconds must not be negative")
	}
	for _, key := range []string{createdAtKey, expiresAtKey} {
		if _, ok := input.Metadata[key]; ok {
			return nil, fmt.Errorf("metadata key %q is reserved", key)
		}
	}
	vectors, err := t.cfg.Embedder.Embed(ctx, []string{input.Text})
	if err != nil {
		return nil, err
	}
	if err := t.ensureMemoryCollection(ctx, len(vectors[0])); err != nil {
		return nil, err
	}
	if input.ID == "" {
		input.ID = newID()
	}
	now := t.cfg.Now().Unix()
	expiresAt := neverExpires
	if input.TTLSeconds > 0 {
		expiresAt = now + input.TTLSeconds
	}
	metadata := cloneMap(input.Metadata)
	metadata[createdAtKey] = now
	metadata[expiresAtKey] = expiresAt
	_, err = t.cfg.Client.Upsert(ctx, t.cfg.MemoryCollection, client.Record{
		ID: input.ID, Vector: vectors[0], Namespace: input.User,
		Metadata: metadata, Payload: map[string]any{"text": input.Text},
	})
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": input.ID, "created_at": now}
	if expiresAt != neverExpires {
		result["expires_at"] = expiresAt
	}
	return result, nil
}

type memory struct {
	ID         string         `json:"id"`
	Text       string         `json:"text"`
	Score      float64        `json:"score"`
	Similarity float32        `json:"similarity"`
	CreatedAt  int64          `json:"created_at"`
	ExpiresAt  int64          `json:"expires_at,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

func (t *toolset) recall(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		User          string         `json:"user"`
		Query         string         `json:"query"`
		TopK          int            `json:"top_k"`
		RecencyWeight *float64       `json:"recency_weight"`
		HalfLifeHours float64        `json:"half_life_hours"`
		Filter        map[string]any `json:"filter"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.User == "" || input.Query == "" {
		return nil, errors.New("user and query are required")
	}
	topK, err := bounded("top_k", input.TopK, 5, 50)
	if err != nil {
		return nil, err
	}
	weight := 0.2
	if input.RecencyWeight != nil {
		weight = *input.RecencyWeight
	}
	if weight < 0 || weight > 1 {
		return nil, errors.New("recency_weight must be between 0 and 1")
	}
	halfLife := input.HalfLifeHours
	if halfLife == 0 {
		halfLife = 168
	}
	if halfLife < 0 || math.IsNaN(halfLife) || math.IsInf(halfLife, 0) {
		return nil, errors.New("half_life_hours must be positive")
	}
	vectors, err := t.cfg.Embedder.Embed(ctx, []string{input.Query})
	if err != nil {
		return nil, err
	}
	now := t.cfg.Now().Unix()
	filter := map[string]any{expiresAtKey: map[string]any{"$gt": now}}
	if len(input.Filter) > 0 {
		filter = map[string]any{"$and": []any{filter, input.Filter}}
	}
	results, err := t.cfg.Client.Search(ctx, t.cfg.MemoryCollection, client.SearchOptions{
		Vector: vectors[0], TopK: min(topK*recallOversampling, 200), Namespace: input.User, Filter: filter,
	})
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
		return map[string]any{"memories": []memory{}}, nil
	}
	if err != nil {
		return nil, err
	}
	memories := make([]memory, 0, len(results))
	for _, result := range results {
		item := memory{ID: result.ID, Similarity: result.Score, Metadata: map[string]any{}}
		item.Text, _ = result.Payload["text"].(string)
		for key, value := range result.Metadata {
			switch key {
			case createdAtKey:
				item.CreatedAt = asInt(value)
			case expiresAtKey:
				if expires := asInt(value); expires != neverExpires {
					item.ExpiresAt = expires
				}
			default:
				item.Metadata[key] = value
			}
		}
		ageHours := math.Max(0, float64(now-item.CreatedAt)/3600)
		recency := math.Exp2(-ageHours / halfLife)
		item.Score = (1-weight)*float64(result.Score) + weight*recency
		memories = append(memories, item)
	}
	sort.SliceStable(memories, func(i, j int) bool { return memories[i].Score > memories[j].Score })
	if len(memories) > topK {
		memories = memories[:topK]
	}
	return map[string]any{"memories": memories}, nil
}

func (t *toolset) forget(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		User string `json:"user"`
		ID   string `json:"id"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.User == "" || input.ID == "" {
		return nil, errors.New("user and id are required")
	}
	if err := t.cfg.Client.Delete(ctx, t.cfg.MemoryCollection, input.User, input.ID); err != nil {
		return nil, err
	}
	return map[string]any{"forgotten": input.ID}, nil
}

func (t *toolset) pruneMemories(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		User string `json:"user"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.User == "" {
		return nil, errors.New("user is required")
	}
	now := t.cfg.Now().Unix()
	var expired []string
	cursor := ""
	for {
		page, err := t.cfg.Client.Scroll(ctx, t.cfg.MemoryCollection, client.ScrollOptions{Namespace: input.User, Limit: 200, Cursor: cursor})
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return map[string]any{"pruned": 0}, nil
		}
		if err != nil {
			return nil, err
		}
		for _, record := range page.Records {
			if record.Namespace == input.User && asInt(record.Metadata[expiresAtKey]) <= now {
				expired = append(expired, record.ID)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	for _, id := range expired {
		if err := t.cfg.Client.Delete(ctx, t.cfg.MemoryCollection, input.User, id); err != nil {
			return nil, err
		}
	}
	return map[string]any{"pruned": len(expired)}, nil
}

func (t *toolset) ensureMemoryCollection(ctx context.Context, dimension int) error {
	description, err := t.cfg.Client.DescribeCollection(ctx, t.cfg.MemoryCollection)
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
		_, err = t.cfg.Client.CreateCollection(ctx, client.CollectionConfig{
			Name: t.cfg.MemoryCollection, Dimension: dimension, Metric: "cosine", ShardCount: 1,
			Index: client.IndexConfig{Type: "hnsw"},
		})
		if errors.As(err, &apiErr) && apiErr.StatusCode == 409 {
			return nil // created concurrently
		}
		return err
	}
	if err != nil {
		return err
	}
	if description.Config.Dimension != dimension {
		return fmt.Errorf("memory collection %q has dimension %d but the embedding model returns %d",
			t.cfg.MemoryCollection, description.Config.Dimension, dimension)
	}
	return nil
}

func asInt(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func newID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}
