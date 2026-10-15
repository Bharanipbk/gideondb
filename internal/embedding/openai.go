// Package embedding turns text into vectors through external model servers,
// with a content-hash cache so repeated text is embedded once.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Embedder turns a batch of texts into one vector each, in order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// maxResponseBytes bounds an embedding response: 2,048 inputs of 4,096
// float32 values in JSON fit comfortably.
const maxResponseBytes = 256 << 20

// OpenAIEmbedder calls an OpenAI-compatible /embeddings endpoint, which
// covers OpenAI, Azure OpenAI, Ollama (/v1), vLLM, LM Studio and others.
type OpenAIEmbedder struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
}

func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(map[string]any{"model": e.Model, "input": texts})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.BaseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	httpClient := e.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	reply, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer reply.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(reply.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("embedding response: %w", err)
	}
	if reply.StatusCode < 200 || reply.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding endpoint returned HTTP %d: %s", reply.StatusCode, truncate(string(payload), 300))
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("embedding endpoint returned %d vectors for %d inputs", len(parsed.Data), len(texts))
	}
	vectors := make([][]float32, len(texts))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(texts) || vectors[item.Index] != nil || len(item.Embedding) == 0 {
			return nil, errors.New("embedding response has invalid indexes")
		}
		vectors[item.Index] = item.Embedding
	}
	return vectors, nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
