package rest

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
	"github.com/vectordb/vectordb/internal/core"
	"github.com/vectordb/vectordb/internal/metadata"
)

type distributedSearchRequest struct {
	searchRequest
	AllowPartial bool `json:"allow_partial,omitempty"`
}

type shardSearchFailure struct {
	ShardID uint32 `json:"shard_id"`
	NodeID  string `json:"node_id"`
	Error   string `json:"error"`
}

type shardSearchOutcome struct {
	results []core.SearchResult
	failure *shardSearchFailure
}

func (s *Server) distributedSearch(w http.ResponseWriter, r *http.Request) {
	if !s.requireStaticPlacement(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var request distributedSearchRequest
	if !decode(w, r, &request) {
		return
	}
	filter, err := metadata.Parse(request.Filter)
	if err != nil {
		writeError(w, err)
		return
	}
	config, _, err := s.engine.DescribeCollection(r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := core.ValidateVector(request.Vector, config.Dimension); err != nil {
		writeError(w, err)
		return
	}
	if request.TopK <= 0 {
		writeError(w, fmt.Errorf("%w: top_k must be positive", core.ErrInvalidArgument))
		return
	}
	peers, peerErr := s.membershipPeers()
	if peerErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: peerErr.Error()})
		return
	}
	epoch := s.currentMetadataEpoch()
	table, err := cluster.PlanPlacementWithCapacity(epoch, s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, []core.CollectionConfig{config})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "placement_unavailable", Message: err.Error()})
		return
	}
	peerByID := make(map[string]cluster.Peer, len(peers))
	for _, peer := range peers {
		if peer.NodeID != "" {
			peerByID[peer.NodeID] = peer
		}
	}
	outcomes := make([]shardSearchOutcome, len(table.Shards))
	workers := min(len(outcomes), min(32, max(1, runtime.GOMAXPROCS(0)*2)))
	jobs := make(chan int)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for position := range jobs {
				assignment := table.Shards[position]
				var results []core.SearchResult
				var searchErr error
				if assignment.NodeID == s.nodeID {
					results, searchErr = s.engine.SearchShardFiltered(config.Name, assignment.ShardID, request.Namespace, request.Vector, request.TopK, filter)
				} else {
					peer, exists := peerByID[assignment.NodeID]
					if !exists {
						searchErr = fmt.Errorf("placement node is not discoverable")
					} else {
						results, searchErr = s.remoteShardSearch(ctx, peer, config.Name, assignment.ShardID, request.searchRequest, r.Header.Get("traceparent"))
					}
				}
				if searchErr != nil {
					outcomes[position].failure = &shardSearchFailure{ShardID: assignment.ShardID, NodeID: assignment.NodeID, Error: searchErr.Error()}
				} else {
					outcomes[position].results = results
				}
			}
		}()
	}
	for position := range outcomes {
		jobs <- position
	}
	close(jobs)
	wait.Wait()
	allResults := make([][]core.SearchResult, 0, len(outcomes))
	failures := make([]shardSearchFailure, 0)
	for _, outcome := range outcomes {
		if outcome.failure != nil {
			failures = append(failures, *outcome.failure)
		} else {
			allResults = append(allResults, outcome.results)
		}
	}
	if len(failures) > 0 && !request.AllowPartial {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "distributed_search_failed", "message": "one or more shard searches failed", "failures": failures, "metadata_epoch": epoch})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": mergeDistributedTopK(allResults, request.TopK), "partial": len(failures) > 0, "failures": failures, "metadata_epoch": epoch, "authoritative_placement": s.staticPlacementReady()})
}

func (s *Server) remoteShardSearch(ctx context.Context, peer cluster.Peer, collection string, shardID uint32, request searchRequest, traceparent string) ([]core.SearchResult, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	target := peer.SeedURL + "/v1/internal/shards/" + url.PathEscape(collection) + "/" + strconv.FormatUint(uint64(shardID), 10) + "/search"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-VectorDB-Cluster-ID", s.clusterID)
	httpRequest.Header.Set("X-VectorDB-Target-Node-ID", peer.NodeID)
	httpRequest.Header.Set("X-VectorDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	if s.peerAPIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+s.peerAPIKey)
	}
	if traceparent != "" {
		httpRequest.Header.Set("traceparent", traceparent)
	}
	response, err := s.internalClient.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peer returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	var body struct {
		Results       []core.SearchResult `json:"results"`
		ShardID       uint32              `json:"shard_id"`
		MetadataEpoch uint64              `json:"metadata_epoch"`
	}
	if err := decoder.Decode(&body); err != nil {
		return nil, fmt.Errorf("decode peer response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("peer response must contain one JSON value")
	}
	if body.ShardID != shardID || body.MetadataEpoch != s.currentMetadataEpoch() {
		return nil, fmt.Errorf("peer response fence mismatch")
	}
	return body.Results, nil
}

func mergeDistributedTopK(groups [][]core.SearchResult, k int) []core.SearchResult {
	values := make(distributedResultHeap, 0, k)
	for _, group := range groups {
		for _, result := range group {
			if len(values) < k {
				heap.Push(&values, result)
				continue
			}
			worst := values[0]
			if result.Score > worst.Score || (result.Score == worst.Score && result.ID < worst.ID) {
				values[0] = result
				heap.Fix(&values, 0)
			}
		}
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].Score > values[j].Score || (values[i].Score == values[j].Score && values[i].ID < values[j].ID)
	})
	return values
}

type distributedResultHeap []core.SearchResult

func (values distributedResultHeap) Len() int { return len(values) }
func (values distributedResultHeap) Less(i, j int) bool {
	return values[i].Score < values[j].Score || (values[i].Score == values[j].Score && values[i].ID > values[j].ID)
}
func (values distributedResultHeap) Swap(i, j int) { values[i], values[j] = values[j], values[i] }
func (values *distributedResultHeap) Push(value any) {
	*values = append(*values, value.(core.SearchResult))
}
func (values *distributedResultHeap) Pop() any {
	old := *values
	value := old[len(old)-1]
	*values = old[:len(old)-1]
	return value
}
