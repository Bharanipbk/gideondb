package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/embedding"
)

// textRecord is a record as clients send it. When it has text but no vector
// and the collection has a vectorizer, the server embeds the text; the text
// stays on the record for BM25.
type textRecord struct {
	core.Record
}

// maxEmbedTextBytes bounds one text so a single record cannot make the
// server send megabytes to an embedding provider.
const maxEmbedTextBytes = 64 << 10

// vectorizerFor returns the collection's embedding model. It returns nil and
// true when the collection has none, so text passes through unchanged (for
// lexical search), and false after writing an error response when the
// model is not configured on this node.
func (s *Server) vectorizerFor(w http.ResponseWriter, config core.CollectionConfig) (*embedding.Model, bool) {
	if config.Vectorizer == "" {
		return nil, true
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
		if input.Text != "" && len(input.Vector) == 0 {
			texts = append(texts, input.Text)
			positions = append(positions, i)
		}
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
	if model == nil {
		return records, true
	}
	for i, position := range positions {
		if len(texts[i]) > maxEmbedTextBytes {
			writeError(w, fmt.Errorf("%w: record %d text exceeds the %d bytes that can be embedded", core.ErrInvalidArgument, position, maxEmbedTextBytes))
			return nil, false
		}
	}
	vectors, err := model.Embed(ctx, texts, config.Dimension)
	if err != nil {
		writeEmbeddingError(w, err)
		return nil, false
	}
	for i, position := range positions {
		records[position].Vector = vectors[i]
	}
	return records, true
}

// resolveQuery embeds request.Text into request.Vector when the search has
// text but no vector. Text is kept so lexical retrieval can use it too.
func (s *Server) resolveQuery(ctx context.Context, w http.ResponseWriter, collectionName string, request *searchRequest) bool {
	if request.Text == "" || len(request.Vector) > 0 {
		return true
	}
	config, _, err := s.engine.DescribeCollection(collectionName)
	if err != nil {
		writeError(w, err)
		return false
	}
	model, ok := s.vectorizerFor(w, config)
	if !ok || model == nil {
		return ok
	}
	if len(request.Text) > maxEmbedTextBytes {
		writeError(w, fmt.Errorf("%w: text exceeds the %d bytes that can be embedded", core.ErrInvalidArgument, maxEmbedTextBytes))
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
