package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Bharanipbk/gideondb/pkg/client"
)

// Config selects which tools the server exposes and how they reach GideonDB.
type Config struct {
	Client *client.Client
	// Embedder enables text arguments and the memory tools. Optional.
	Embedder Embedder
	// AllowWrites exposes upsert, delete and the memory write tools.
	AllowWrites bool
	// AllowAdmin exposes create_collection and delete_collection.
	AllowAdmin bool
	// MemoryCollection stores memories; it is created on first use.
	MemoryCollection string
	// Now is the clock used for memory timestamps; defaults to time.Now.
	Now func() time.Time
}

// Tools builds the tool list for cfg.
func Tools(cfg Config) []Tool {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MemoryCollection == "" {
		cfg.MemoryCollection = "agent_memory"
	}
	t := &toolset{cfg: cfg}
	readOnly := map[string]any{"readOnlyHint": true}
	tools := []Tool{
		{
			Name: "list_collections", Description: "List GideonDB collections with their dimension, metric and index.",
			InputSchema: object(nil), Annotations: readOnly, handler: t.listCollections,
		},
		{
			Name: "describe_collection", Description: "Show a collection's configuration and vector count.",
			InputSchema: object(map[string]any{"name": str("Collection name.")}, "name"), Annotations: readOnly, handler: t.describeCollection,
		},
		{
			Name: "search",
			Description: "Nearest-neighbour search in a collection. Pass either `vector` or `text` " +
				"(text needs an embedding model configured on the MCP server).",
			InputSchema: object(map[string]any{
				"collection": str("Collection name."),
				"vector":     floats("Query vector."),
				"text":       str("Query text, embedded by the MCP server."),
				"top_k":      integer("Results to return (default 10, max 100)."),
				"namespace":  str("Optional namespace."),
				"filter":     map[string]any{"type": "object", "description": "Metadata filter, e.g. {\"lang\": \"en\", \"year\": {\"$gte\": 2020}}."},
				"ef_search":  integer("Optional HNSW ef_search override."),
			}, "collection"),
			Annotations: readOnly, handler: t.search,
		},
		{
			Name: "get_record", Description: "Read one record by id.",
			InputSchema: object(map[string]any{"collection": str("Collection name."), "id": str("Record id."), "namespace": str("Optional namespace.")}, "collection", "id"),
			Annotations: readOnly, handler: t.getRecord,
		},
	}
	if cfg.AllowWrites {
		tools = append(tools,
			Tool{
				Name:        "upsert_records",
				Description: "Insert or replace up to 1,000 records. Each record needs `id` and either `vector` or `text`; text is stored in payload.text.",
				InputSchema: object(map[string]any{
					"collection": str("Collection name."),
					"records": map[string]any{"type": "array", "maxItems": 1000, "items": object(map[string]any{
						"id": str("Record id."), "vector": floats("Vector."), "text": str("Text to embed and store."),
						"metadata": map[string]any{"type": "object"}, "payload": map[string]any{"type": "object"}, "namespace": str("Optional namespace."),
					}, "id")},
				}, "collection", "records"),
				Annotations: map[string]any{"idempotentHint": true}, handler: t.upsertRecords,
			},
			Tool{
				Name: "delete_record", Description: "Delete one record by id.",
				InputSchema: object(map[string]any{"collection": str("Collection name."), "id": str("Record id."), "namespace": str("Optional namespace.")}, "collection", "id"),
				Annotations: map[string]any{"destructiveHint": true}, handler: t.deleteRecord,
			},
		)
	}
	if cfg.AllowAdmin {
		tools = append(tools,
			Tool{
				Name: "create_collection", Description: "Create a collection.",
				InputSchema: object(map[string]any{
					"name": str("Collection name."), "dimension": integer("Vector dimension."),
					"metric":     map[string]any{"type": "string", "enum": []string{"cosine", "dot", "l2"}},
					"index_type": map[string]any{"type": "string", "enum": []string{"hnsw", "flat"}},
				}, "name", "dimension"),
				handler: t.createCollection,
			},
			Tool{
				Name: "delete_collection", Description: "Permanently delete a collection and all its records.",
				InputSchema: object(map[string]any{"name": str("Collection name."), "confirm": str("Must equal the collection name.")}, "name", "confirm"),
				Annotations: map[string]any{"destructiveHint": true}, handler: t.deleteCollection,
			},
		)
	}
	if cfg.Embedder != nil {
		tools = append(tools, Tool{
			Name: "recall",
			Description: "Recall a user's memories relevant to a query, ranked by similarity blended with recency. " +
				"Expired memories are never returned.",
			InputSchema: object(map[string]any{
				"user": str("User or agent scope; memories never cross users."), "query": str("What to recall."),
				"top_k":           integer("Memories to return (default 5, max 50)."),
				"recency_weight":  map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "0 = similarity only, 1 = recency only (default 0.2)."},
				"half_life_hours": map[string]any{"type": "number", "exclusiveMinimum": 0, "description": "Recency half-life (default 168)."},
				"filter":          map[string]any{"type": "object", "description": "Optional metadata filter."},
			}, "user", "query"),
			Annotations: readOnly, handler: t.recall,
		})
		if cfg.AllowWrites {
			tools = append(tools,
				Tool{
					Name: "remember", Description: "Store a memory for a user, optionally expiring after ttl_seconds.",
					InputSchema: object(map[string]any{
						"user": str("User or agent scope."), "text": str("Memory text."),
						"metadata":    map[string]any{"type": "object", "description": "Optional scalar metadata for filtering."},
						"ttl_seconds": integer("Optional lifetime; omit to keep forever."),
						"id":          str("Optional id; reusing one replaces that memory."),
					}, "user", "text"),
					handler: t.remember,
				},
				Tool{
					Name: "forget", Description: "Delete one of a user's memories by id.",
					InputSchema: object(map[string]any{"user": str("User scope."), "id": str("Memory id.")}, "user", "id"),
					Annotations: map[string]any{"destructiveHint": true}, handler: t.forget,
				},
				Tool{
					Name: "prune_memories", Description: "Delete a user's expired memories and report how many were removed.",
					InputSchema: object(map[string]any{"user": str("User scope.")}, "user"),
					Annotations: map[string]any{"destructiveHint": true, "idempotentHint": true}, handler: t.pruneMemories,
				},
			)
		}
	}
	return tools
}

type toolset struct{ cfg Config }

func (t *toolset) listCollections(ctx context.Context, _ json.RawMessage) (any, error) {
	collections, err := t.cfg.Client.ListCollections(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"collections": collections}, nil
}

func (t *toolset) describeCollection(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.Name == "" {
		return nil, errors.New("name is required")
	}
	return t.cfg.Client.DescribeCollection(ctx, input.Name)
}

func (t *toolset) search(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		Collection string         `json:"collection"`
		Vector     []float32      `json:"vector"`
		Text       string         `json:"text"`
		TopK       int            `json:"top_k"`
		Namespace  string         `json:"namespace"`
		Filter     map[string]any `json:"filter"`
		EFSearch   int            `json:"ef_search"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.Collection == "" {
		return nil, errors.New("collection is required")
	}
	vector, err := t.vectorFor(ctx, input.Vector, input.Text)
	if err != nil {
		return nil, err
	}
	topK, err := bounded("top_k", input.TopK, 10, 100)
	if err != nil {
		return nil, err
	}
	results, err := t.cfg.Client.Search(ctx, input.Collection, client.SearchOptions{
		Vector: vector, TopK: topK, Namespace: input.Namespace, Filter: input.Filter, EFSearch: input.EFSearch,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"results": results}, nil
}

func (t *toolset) getRecord(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		Collection string `json:"collection"`
		ID         string `json:"id"`
		Namespace  string `json:"namespace"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.Collection == "" || input.ID == "" {
		return nil, errors.New("collection and id are required")
	}
	record, err := t.cfg.Client.Get(ctx, input.Collection, input.Namespace, input.ID)
	if err != nil {
		return nil, err
	}
	record.Vector = nil // vectors are large and rarely useful to a model
	return record, nil
}

func (t *toolset) upsertRecords(ctx context.Context, arguments json.RawMessage) (any, error) {
	type inputRecord struct {
		ID        string         `json:"id"`
		Vector    []float32      `json:"vector"`
		Text      string         `json:"text"`
		Metadata  map[string]any `json:"metadata"`
		Payload   map[string]any `json:"payload"`
		Namespace string         `json:"namespace"`
	}
	var input struct {
		Collection string        `json:"collection"`
		Records    []inputRecord `json:"records"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.Collection == "" || len(input.Records) == 0 || len(input.Records) > 1000 {
		return nil, errors.New("collection and 1 to 1000 records are required")
	}
	var texts []string
	var textPositions []int
	for i, record := range input.Records {
		if record.ID == "" {
			return nil, fmt.Errorf("records[%d].id is required", i)
		}
		if (len(record.Vector) == 0) == (record.Text == "") {
			return nil, fmt.Errorf("records[%d] needs exactly one of vector or text", i)
		}
		if record.Text != "" {
			texts = append(texts, record.Text)
			textPositions = append(textPositions, i)
		}
	}
	if len(texts) > 0 {
		if t.cfg.Embedder == nil {
			return nil, errors.New("text records need an embedding model; start gideondb-mcp with -embed-url and -embed-model")
		}
		vectors, err := t.cfg.Embedder.Embed(ctx, texts)
		if err != nil {
			return nil, err
		}
		for i, position := range textPositions {
			input.Records[position].Vector = vectors[i]
		}
	}
	records := make([]client.Record, len(input.Records))
	for i, record := range input.Records {
		payload := record.Payload
		if record.Text != "" {
			payload = cloneMap(payload)
			payload["text"] = record.Text
		}
		records[i] = client.Record{ID: record.ID, Vector: record.Vector, Metadata: record.Metadata, Payload: payload, Namespace: record.Namespace}
	}
	if _, err := t.cfg.Client.BatchUpsert(ctx, input.Collection, records); err != nil {
		return nil, err
	}
	return map[string]any{"upserted": len(records)}, nil
}

func (t *toolset) deleteRecord(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		Collection string `json:"collection"`
		ID         string `json:"id"`
		Namespace  string `json:"namespace"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.Collection == "" || input.ID == "" {
		return nil, errors.New("collection and id are required")
	}
	if err := t.cfg.Client.Delete(ctx, input.Collection, input.Namespace, input.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": input.ID}, nil
}

func (t *toolset) createCollection(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		Name      string `json:"name"`
		Dimension int    `json:"dimension"`
		Metric    string `json:"metric"`
		IndexType string `json:"index_type"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.Name == "" || input.Dimension <= 0 {
		return nil, errors.New("name and a positive dimension are required")
	}
	if input.Metric == "" {
		input.Metric = "cosine"
	}
	if input.IndexType == "" {
		input.IndexType = "hnsw"
	}
	return t.cfg.Client.CreateCollection(ctx, client.CollectionConfig{
		Name: input.Name, Dimension: input.Dimension, Metric: input.Metric, ShardCount: 1,
		Index: client.IndexConfig{Type: input.IndexType},
	})
}

func (t *toolset) deleteCollection(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		Name    string `json:"name"`
		Confirm string `json:"confirm"`
	}
	if err := decode(arguments, &input); err != nil {
		return nil, err
	}
	if input.Name == "" || input.Confirm != input.Name {
		return nil, errors.New("confirm must equal the collection name")
	}
	if err := t.cfg.Client.DeleteCollection(ctx, input.Name); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": input.Name}, nil
}

func (t *toolset) vectorFor(ctx context.Context, vector []float32, text string) ([]float32, error) {
	if (len(vector) == 0) == (text == "") {
		return nil, errors.New("pass exactly one of vector or text")
	}
	if len(vector) > 0 {
		return vector, nil
	}
	if t.cfg.Embedder == nil {
		return nil, errors.New("text queries need an embedding model; start gideondb-mcp with -embed-url and -embed-model")
	}
	vectors, err := t.cfg.Embedder.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func bounded(name string, value, fallback, limit int) (int, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 0 || value > limit {
		return 0, fmt.Errorf("%s must be between 1 and %d", name, limit)
	}
	return value, nil
}

func cloneMap(values map[string]any) map[string]any {
	clone := make(map[string]any, len(values)+1)
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func object(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integer(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func floats(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": description}
}
