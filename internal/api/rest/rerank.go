package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
)

// rerankStatus reports whether the second stage ran. A cross-encoder that
// fails or times out leaves the first-stage order and sets Applied=false.
type rerankStatus struct {
	Applied    bool            `json:"applied"`
	Type       core.RerankType `json:"type"`
	Model      string          `json:"model,omitempty"`
	Candidates int             `json:"candidates"`
	Error      string          `json:"error,omitempty"`
}

// named reports whether the request searches a named vector field.
func (r searchRequest) named() bool { return r.Using != "" || r.MultiVector != nil }

func (r searchRequest) namedQuery() core.NamedQuery {
	return core.NamedQuery{Field: r.Using, Vector: r.Vector, MultiVector: r.MultiVector, TopK: r.TopK, EFSearch: r.EFSearch, Candidates: r.Candidates}
}

// prepareRerank validates named-field and rerank options after takeShape
// and raises request.TopK to the rerank candidate count.
func (s *Server) prepareRerank(collectionName string, request *searchRequest) error {
	if request.named() {
		if request.Using == "" {
			return fmt.Errorf("%w: multi_vector requires using", core.ErrInvalidArgument)
		}
		if request.hybrid() {
			return fmt.Errorf("%w: named vector search cannot be combined with sparse_vector or text", core.ErrInvalidArgument)
		}
		if len(request.Vector) == 0 {
			request.Vector = nil
		}
	} else if request.Candidates != 0 {
		return fmt.Errorf("%w: candidates requires using", core.ErrInvalidArgument)
	}
	if request.Rerank == nil {
		return nil
	}
	config, _, err := s.engine.DescribeCollection(collectionName)
	if err != nil {
		return err
	}
	if request.TopK <= 0 {
		return fmt.Errorf("%w: top_k must be positive", core.ErrInvalidArgument)
	}
	if err := request.Rerank.Validate(config, request.TopK); err != nil {
		return err
	}
	candidates := request.Rerank.CandidateCount(request.TopK)
	if request.Rerank.Type == core.RerankCrossEncoder {
		model, ok := s.rerankers.Get(request.Rerank.Model)
		if !ok {
			return fmt.Errorf("%w: reranker %q is not configured on this node", core.ErrInvalidArgument, request.Rerank.Model)
		}
		if request.Rerank.Candidates == 0 {
			// The default pool shrinks to what the reranker accepts.
			candidates = max(min(candidates, model.MaxDocuments()), request.TopK)
		}
		if candidates > model.MaxDocuments() {
			return fmt.Errorf("%w: reranker %q accepts at most %d candidates", core.ErrInvalidArgument, request.Rerank.Model, model.MaxDocuments())
		}
	}
	request.TopK = candidates
	return nil
}

// searchShardStage runs the first stage on one local shard and the parts of
// the second stage that need the records: MaxSim rescoring, or attaching
// record text for a cross-encoder that the coordinator calls.
func (s *Server) searchShardStage(collectionName string, shardID uint32, request searchRequest, filter *metadata.Expr) ([]core.SearchResult, error) {
	var results []core.SearchResult
	var err error
	switch {
	case request.named():
		results, err = s.engine.SearchShardNamed(collectionName, shardID, request.Namespace, request.namedQuery(), filter)
	case request.hybrid():
		results, err = s.engine.SearchShardHybrid(collectionName, shardID, request.Namespace, request.hybridQuery(), filter)
	default:
		results, err = s.engine.SearchShardFilteredWithEF(collectionName, shardID, request.Namespace, request.Vector, request.TopK, filter, request.EFSearch)
	}
	if err != nil || request.Rerank == nil {
		return results, err
	}
	switch request.Rerank.Type {
	case core.RerankMaxSim:
		return s.engine.RerankMaxSim(collectionName, results, request.Rerank.Field, request.Rerank.MultiVector)
	case core.RerankCrossEncoder:
		if source, _, _ := request.Rerank.DocumentSource(); source == "text" {
			err = s.engine.AttachText(collectionName, results)
		}
	}
	return results, err
}

// rerankLocal applies the second stage when every result is stored here.
func (s *Server) rerankLocal(ctx context.Context, collectionName string, spec core.RerankSpec, results []core.SearchResult) ([]core.SearchResult, *rerankStatus, error) {
	if spec.Type == core.RerankMaxSim {
		reranked, err := s.engine.RerankMaxSim(collectionName, results, spec.Field, spec.MultiVector)
		if err != nil {
			return nil, nil, err
		}
		return reranked, &rerankStatus{Applied: true, Type: core.RerankMaxSim, Candidates: len(results)}, nil
	}
	if source, _, _ := spec.DocumentSource(); source == "text" {
		if err := s.engine.AttachText(collectionName, results); err != nil {
			return nil, nil, err
		}
	}
	reranked, status := s.crossEncode(ctx, spec, results)
	return reranked, status, nil
}

// crossEncode reorders results by an external reranker's scores. On any
// failure it returns results unchanged with Applied=false and the error.
func (s *Server) crossEncode(ctx context.Context, spec core.RerankSpec, results []core.SearchResult) ([]core.SearchResult, *rerankStatus) {
	status := &rerankStatus{Type: core.RerankCrossEncoder, Model: spec.Model, Candidates: len(results)}
	model, ok := s.rerankers.Get(spec.Model)
	if !ok {
		status.Error = fmt.Sprintf("reranker %q is not configured on this node", spec.Model)
		return results, status
	}
	if len(results) == 0 {
		status.Applied = true
		return results, status
	}
	source, field, _ := spec.DocumentSource()
	documents := make([]string, len(results))
	for i, result := range results {
		switch source {
		case "text":
			documents[i] = result.Text
		case "payload":
			documents[i] = documentText(result.Payload[field])
		case "metadata":
			documents[i] = documentText(result.Metadata[field])
		}
	}
	scores, err := model.Rerank(ctx, spec.Query, documents)
	if err != nil {
		s.rerankFailures.Add(1)
		status.Error = err.Error()
		return results, status
	}
	reranked := append([]core.SearchResult(nil), results...)
	for i := range reranked {
		reranked[i].Score = scores[i]
	}
	sort.SliceStable(reranked, func(i, j int) bool { return reranked[i].Score > reranked[j].Score })
	status.Applied = true
	return reranked, status
}

func documentText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// clearText removes record text carried for reranking before responding.
func clearText(results []core.SearchResult) []core.SearchResult {
	for i := range results {
		results[i].Text = ""
	}
	return results
}

func (s *Server) rerankerList(w http.ResponseWriter, _ *http.Request) {
	names := s.rerankers.Names()
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rerankers": names, "rerank_failures": s.rerankFailures.Load()})
}
