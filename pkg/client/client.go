// Package client provides a dependency-free Go client for the experimental
// GideonDB REST API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 16 << 20

type Options struct {
	APIKey     string
	HTTPClient *http.Client
}

type Client struct {
	baseURL   *url.URL
	http      *http.Client
	apiKey    string
	userAgent string
}

type APIError struct {
	StatusCode int
	Code       string `json:"code"`
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("gideondb: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("gideondb: %s (HTTP %d): %s", e.Code, e.StatusCode, e.Message)
}

type CollectionConfig struct {
	Name       string      `json:"name"`
	Dimension  int         `json:"dimension"`
	Metric     string      `json:"metric"`
	ShardCount int         `json:"shard_count"`
	Index      IndexConfig `json:"index"`
}

type IndexConfig struct {
	Type           string `json:"type"`
	M              int    `json:"m,omitempty"`
	EFConstruction int    `json:"ef_construction,omitempty"`
	EFSearch       int    `json:"ef_search,omitempty"`
}

type CollectionDescription struct {
	Config      CollectionConfig `json:"config"`
	VectorCount int              `json:"vector_count"`
}

type Record struct {
	ID        string         `json:"id"`
	Vector    []float32      `json:"vector"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Timestamp int64          `json:"timestamp,omitempty"`
	Version   uint64         `json:"version,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
}

type ScrollOptions struct {
	Namespace     string
	Limit         int
	Cursor        string
	IncludeVector bool
}

type RecordPage struct {
	Records                []Record `json:"records"`
	NextCursor             string   `json:"next_cursor"`
	VectorsIncluded        bool     `json:"vectors_included"`
	MetadataEpoch          uint64   `json:"metadata_epoch,omitempty"`
	AuthoritativePlacement bool     `json:"authoritative_placement,omitempty"`
}

type SearchOptions struct {
	Vector       []float32
	TopK         int
	EFSearch     int
	Namespace    string
	Filter       map[string]any
	AllowPartial bool
}

type SearchResult struct {
	ID        string         `json:"id"`
	Score     float32        `json:"score"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
}

type ShardFailure struct {
	ShardID uint32 `json:"shard_id"`
	NodeID  string `json:"node_id,omitempty"`
	Error   string `json:"error"`
}

type DistributedSearchResponse struct {
	Results                []SearchResult `json:"results"`
	Partial                bool           `json:"partial"`
	Failures               []ShardFailure `json:"failures"`
	MetadataEpoch          uint64         `json:"metadata_epoch"`
	AuthoritativePlacement bool           `json:"authoritative_placement"`
}

type ShardWriteOutcome struct {
	ShardID              uint32 `json:"shard_id"`
	NodeID               string `json:"node_id,omitempty"`
	Status               string `json:"status"`
	Error                string `json:"error,omitempty"`
	ReplicasAcknowledged int    `json:"replicas_acknowledged,omitempty"`
	ReplicationFactor    int    `json:"replication_factor,omitempty"`
}

type DistributedWriteResponse struct {
	Outcomes               []ShardWriteOutcome `json:"outcomes"`
	Partial                bool                `json:"partial"`
	MetadataEpoch          uint64              `json:"metadata_epoch"`
	AuthoritativePlacement bool                `json:"authoritative_placement"`
}

func New(baseURL string, options Options) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("gideondb: base URL must be an HTTP(S) origin")
	}
	parsed.Path = ""
	transport := options.HTTPClient
	if transport == nil {
		transport = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: parsed, http: transport, apiKey: options.APIKey, userAgent: "gideondb-go/dev"}, nil
}

func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/health", nil, nil)
}

func (c *Client) Ready(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/ready", nil, nil)
}

func (c *Client) ListCollections(ctx context.Context) ([]CollectionConfig, error) {
	var response struct {
		Collections []CollectionConfig `json:"collections"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/collections", nil, &response)
	return response.Collections, err
}

func (c *Client) CreateCollection(ctx context.Context, config CollectionConfig) (CollectionConfig, error) {
	var created CollectionConfig
	err := c.do(ctx, http.MethodPost, "/v1/collections", config, &created)
	return created, err
}

func (c *Client) DescribeCollection(ctx context.Context, name string) (CollectionDescription, error) {
	var description CollectionDescription
	err := c.do(ctx, http.MethodGet, collectionPath(name), nil, &description)
	return description, err
}

func (c *Client) DeleteCollection(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, collectionPath(name), nil, nil)
}

func (c *Client) Upsert(ctx context.Context, collection string, record Record) (Record, error) {
	var stored Record
	err := c.do(ctx, http.MethodPost, collectionPath(collection)+"/vectors", record, &stored)
	return stored, err
}

func (c *Client) BatchUpsert(ctx context.Context, collection string, records []Record) ([]Record, error) {
	var response struct {
		Records []Record `json:"records"`
	}
	err := c.do(ctx, http.MethodPost, collectionPath(collection)+"/vectors/batch", map[string]any{"records": records}, &response)
	return response.Records, err
}

func (c *Client) Get(ctx context.Context, collection, namespace, id string) (Record, error) {
	var record Record
	path := collectionPath(collection) + "/vectors/" + url.PathEscape(id)
	if namespace != "" {
		path += "?namespace=" + url.QueryEscape(namespace)
	}
	err := c.do(ctx, http.MethodGet, path, nil, &record)
	return record, err
}

func (c *Client) Scroll(ctx context.Context, collection string, options ScrollOptions) (RecordPage, error) {
	return c.scroll(ctx, collectionPath(collection)+"/vectors", options)
}

// DistributedScroll traverses one placement owner per shard using an
// epoch-fenced cluster cursor.
func (c *Client) DistributedScroll(ctx context.Context, collection string, options ScrollOptions) (RecordPage, error) {
	return c.scroll(ctx, clusterCollectionPath(collection)+"/vectors", options)
}

func (c *Client) scroll(ctx context.Context, basePath string, options ScrollOptions) (RecordPage, error) {
	if options.Limit < 0 || options.Limit > 200 {
		return RecordPage{}, fmt.Errorf("gideondb: scroll limit must be between 1 and 200 when set")
	}
	query := url.Values{}
	if options.Namespace != "" {
		query.Set("namespace", options.Namespace)
	}
	if options.Limit != 0 {
		query.Set("limit", strconv.Itoa(options.Limit))
	}
	if options.Cursor != "" {
		query.Set("cursor", options.Cursor)
	}
	if options.IncludeVector {
		query.Set("include_vector", "true")
	}
	path := basePath
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var page RecordPage
	err := c.do(ctx, http.MethodGet, path, nil, &page)
	return page, err
}

func (c *Client) Delete(ctx context.Context, collection, namespace, id string) error {
	path := collectionPath(collection) + "/vectors/" + url.PathEscape(id)
	if namespace != "" {
		path += "?namespace=" + url.QueryEscape(namespace)
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) Search(ctx context.Context, collection string, options SearchOptions) ([]SearchResult, error) {
	var response struct {
		Results []SearchResult `json:"results"`
	}
	err := c.do(ctx, http.MethodPost, collectionPath(collection)+"/search", searchBody(options, false), &response)
	return response.Results, err
}

func (c *Client) DistributedSearch(ctx context.Context, collection string, options SearchOptions) (DistributedSearchResponse, error) {
	var response DistributedSearchResponse
	err := c.do(ctx, http.MethodPost, clusterCollectionPath(collection)+"/search", searchBody(options, true), &response)
	return response, err
}

func (c *Client) DistributedBatchUpsert(ctx context.Context, collection string, records []Record, acknowledgement string) (DistributedWriteResponse, error) {
	var response DistributedWriteResponse
	body := map[string]any{"records": records}
	if acknowledgement != "" {
		body["acknowledgement"] = acknowledgement
	}
	err := c.do(ctx, http.MethodPost, clusterCollectionPath(collection)+"/vectors/batch", body, &response)
	return response, err
}

func searchBody(options SearchOptions, distributed bool) map[string]any {
	body := map[string]any{"vector": options.Vector, "top_k": options.TopK}
	if options.EFSearch != 0 {
		body["ef_search"] = options.EFSearch
	}
	if options.Namespace != "" {
		body["namespace"] = options.Namespace
	}
	if options.Filter != nil {
		body["filter"] = options.Filter
	}
	if distributed && options.AllowPartial {
		body["allow_partial"] = true
	}
	return body
}

func collectionPath(name string) string { return "/v1/collections/" + url.PathEscape(name) }

func clusterCollectionPath(name string) string {
	return "/v1/cluster/collections/" + url.PathEscape(name)
}

func (c *Client) do(ctx context.Context, method, path string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("gideondb: encode request: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+path, body)
	if err != nil {
		return fmt.Errorf("gideondb: create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.userAgent)
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("gideondb: request: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("gideondb: read response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return fmt.Errorf("gideondb: response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		apiErr := &APIError{StatusCode: response.StatusCode}
		_ = json.Unmarshal(payload, apiErr)
		return apiErr
	}
	if responseBody == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if len(payload) == 0 {
		return errors.New("gideondb: successful response has no body")
	}
	if err := json.Unmarshal(payload, responseBody); err != nil {
		return fmt.Errorf("gideondb: decode response: %w", err)
	}
	return nil
}
