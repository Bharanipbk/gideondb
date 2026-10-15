package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/embedding"
)

// textRecord is a record as clients send it: either a vector, or text that
// the collection's vectorizer embeds on the server. Text is stored in
// payload.text unless the payload already sets that key.
type textRecord struct {
	core.Record
	Text string `json:"text,omitempty"`
}

// maxEmbedTextBytes bounds one text so a single record cannot make the
// server send megabytes to an embedding provider.
const maxEmbedTextBytes = 64 << 10

// vectorizerFor returns the collection's embedding model, writing an error
// response and returning false when text cannot be embedded.
func (s *Server) vectorizerFor(w http.ResponseWriter, config core.CollectionConfig) (*embedding.Model, bool) {
	if config.Vectorizer == "" {
		writeError(w, fmt.Errorf("%w: collection %q has no vectorizer; send vectors or create it with \"vectorizer\"", core.ErrInvalidArgument, config.Name))
		return nil, false
	}
	model, ok := s.embedders.Get(config.Vectorizer)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "vectorizer_unavailable", Message: fmt.Sprintf("vectorizer %q is not configured on this node", config.Vectorizer)})
		return nil, false
	}
	return model, true
}

// resolveRecords turns text records into vector records. It returns false
// after writing an error response.
func (s *Server) resolveRecords(ctx context.Context, w http.ResponseWriter, collectionName string, inputs []textRecord) ([]core.Record, bool) {
	records := make([]core.Record, len(inputs))
	var texts []string
	var positions []int
	for i, input := range inputs {
		records[i] = input.Record
		if input.Text == "" {
			continue
		}
		if len(input.Vector) > 0 {
			writeError(w, fmt.Errorf("%w: record %d sets both vector and text", core.ErrInvalidArgument, i))
			return nil, false
		}
		if len(input.Text) > maxEmbedTextBytes {
			writeError(w, fmt.Errorf("%w: record %d text exceeds %d bytes", core.ErrInvalidArgument, i, maxEmbedTextBytes))
			return nil, false
		}
		texts = append(texts, input.Text)
		positions = append(positions, i)
	}
	if len(texts) == 0 {
		return records, true
	}
	config, _, err := s.engine.DescribeCollection(collectionName)
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	model, ok := s.vectorizerFor(w, config)
	if !ok {
		return nil, false
	}
	vectors, err := model.Embed(ctx, texts, config.Dimension)
	if err != nil {
		writeEmbeddingError(w, err)
		return nil, false
	}
	for i, position := range positions {
		record := &records[position]
		record.Vector = vectors[i]
		if _, exists := record.Payload["text"]; !exists {
			payload := make(map[string]any, len(record.Payload)+1)
			for key, value := range record.Payload {
				payload[key] = value
			}
			payload["text"] = inputs[position].Text
			record.Payload = payload
		}
	}
	return records, true
}

// resolveQuery embeds request.Text into request.Vector when text is given.
func (s *Server) resolveQuery(ctx context.Context, w http.ResponseWriter, collectionName string, request *searchRequest) bool {
	if request.Text == "" {
		return true
	}
	if len(request.Vector) > 0 {
		writeError(w, fmt.Errorf("%w: search sets both vector and text", core.ErrInvalidArgument))
		return false
	}
	if len(request.Text) > maxEmbedTextBytes {
		writeError(w, fmt.Errorf("%w: text exceeds %d bytes", core.ErrInvalidArgument, maxEmbedTextBytes))
		return false
	}
	config, _, err := s.engine.DescribeCollection(collectionName)
	if err != nil {
		writeError(w, err)
		return false
	}
	model, ok := s.vectorizerFor(w, config)
	if !ok {
		return false
	}
	vectors, err := model.Embed(ctx, []string{request.Text}, config.Dimension)
	if err != nil {
		writeEmbeddingError(w, err)
		return false
	}
	request.Vector = vectors[0]
	return true
}

// validateVectorizer rejects a collection whose vectorizer is unknown here
// or declares a different dimension.
func (s *Server) validateVectorizer(config core.CollectionConfig) error {
	if config.Vectorizer == "" {
		return nil
	}
	model, ok := s.embedders.Get(config.Vectorizer)
	if !ok {
		return fmt.Errorf("%w: vectorizer %q is not configured on this node", core.ErrInvalidArgument, config.Vectorizer)
	}
	if model.Dimension() > 0 && model.Dimension() != config.Dimension {
		return fmt.Errorf("%w: vectorizer %q produces %d dimensions, collection has %d", core.ErrInvalidArgument, config.Vectorizer, model.Dimension(), config.Dimension)
	}
	return nil
}

func writeEmbeddingError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeJSON(w, http.StatusGatewayTimeout, apiError{Code: "embedding_timeout", Message: "embedding request timed out"})
		return
	}
	// Provider errors are returned verbatim: they never contain the API key,
	// and they tell the operator what the model server rejected.
	writeJSON(w, http.StatusBadGateway, apiError{Code: "embedding_failed", Message: err.Error()})
}

func (s *Server) embedderStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"embedders": s.embedders.Stats()})
}
