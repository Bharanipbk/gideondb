package grpcapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/embedding"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxEmbedTextBytes matches the REST bound on text sent to a provider.
const maxEmbedTextBytes = 64 << 10

// vectorizer returns the collection's embedding model, or nil when the
// collection has none so text passes through for lexical search.
func (s *Server) vectorizer(collection string) (*embedding.Model, core.CollectionConfig, error) {
	config, _, err := s.engine.DescribeCollection(collection)
	if err != nil {
		return nil, config, mapError(err)
	}
	if config.Vectorizer == "" {
		return nil, config, nil
	}
	model, ok := s.options.Embedders.Get(config.Vectorizer)
	if !ok {
		return nil, config, status.Errorf(codes.Unavailable, "vectorizer %q is not configured on this node", config.Vectorizer)
	}
	return model, config, nil
}

// embedRecords fills the vector of every record that has text but no vector
// when the collection has a vectorizer.
func (s *Server) embedRecords(ctx context.Context, collection string, records []core.Record) error {
	var texts []string
	var positions []int
	for i, record := range records {
		if record.Text != "" && len(record.Vector) == 0 {
			texts = append(texts, record.Text)
			positions = append(positions, i)
		}
	}
	if len(texts) == 0 {
		return nil
	}
	model, config, err := s.vectorizer(collection)
	if err != nil || model == nil {
		return err
	}
	for i, position := range positions {
		if len(texts[i]) > maxEmbedTextBytes {
			return status.Errorf(codes.InvalidArgument, "record %d text exceeds the %d bytes that can be embedded", position, maxEmbedTextBytes)
		}
	}
	vectors, err := model.Embed(ctx, texts, config.Dimension)
	if err != nil {
		return embeddingError(err)
	}
	for i, position := range positions {
		records[position].Vector = vectors[i]
	}
	return nil
}

// embedQuery returns the dense query vector, embedding text when the search
// has no vector and the collection has a vectorizer.
func (s *Server) embedQuery(ctx context.Context, collection string, vector []float32, text string) ([]float32, error) {
	if text == "" || len(vector) > 0 {
		return vector, nil
	}
	model, config, err := s.vectorizer(collection)
	if err != nil || model == nil {
		return vector, err
	}
	if len(text) > maxEmbedTextBytes {
		return nil, status.Errorf(codes.InvalidArgument, "text exceeds the %d bytes that can be embedded", maxEmbedTextBytes)
	}
	vectors, err := model.Embed(ctx, []string{text}, config.Dimension)
	if err != nil {
		return nil, embeddingError(err)
	}
	return vectors[0], nil
}

// embeddingError mirrors the REST statuses: timeouts are DEADLINE_EXCEEDED
// and provider failures are UNAVAILABLE with the provider's message.
func embeddingError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "embedding request timed out")
	}
	return status.Error(codes.Unavailable, fmt.Sprintf("embedding failed: %v", err))
}
