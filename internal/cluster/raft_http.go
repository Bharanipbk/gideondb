package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type HTTPRaftTransport struct {
	ClusterID string
	APIKey    string
	Client    *http.Client
}

func (t *HTTPRaftTransport) RequestVote(ctx context.Context, peer RaftPeer, request RequestVoteRequest) (RequestVoteResponse, error) {
	var response RequestVoteResponse
	err := t.call(ctx, peer, "/v1/internal/raft/request-vote", request, &response)
	return response, err
}

func (t *HTTPRaftTransport) AppendEntries(ctx context.Context, peer RaftPeer, request AppendEntriesRequest) (AppendEntriesResponse, error) {
	var response AppendEntriesResponse
	err := t.call(ctx, peer, "/v1/internal/raft/append-entries", request, &response)
	return response, err
}

func (t *HTTPRaftTransport) InstallSnapshot(ctx context.Context, peer RaftPeer, request InstallSnapshotRequest) (InstallSnapshotResponse, error) {
	var response InstallSnapshotResponse
	err := t.call(ctx, peer, "/v1/internal/raft/install-snapshot", request, &response)
	return response, err
}

func (t *HTTPRaftTransport) TimeoutNow(ctx context.Context, peer RaftPeer, request TimeoutNowRequest) (TimeoutNowResponse, error) {
	var response TimeoutNowResponse
	err := t.call(ctx, peer, "/v1/internal/raft/timeout-now", request, &response)
	return response, err
}

func (t *HTTPRaftTransport) call(ctx context.Context, peer RaftPeer, path string, requestBody, responseBody any) error {
	if !ValidNodeID(t.ClusterID) || !ValidNodeID(peer.NodeID) || peer.BaseURL == "" {
		return fmt.Errorf("invalid Raft transport configuration")
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(peer.BaseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GideonDB-Cluster-ID", t.ClusterID)
	if t.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+t.APIKey)
	}
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("Raft peer %s returned HTTP %d", peer.NodeID, response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(responseBody); err != nil {
		return fmt.Errorf("decode Raft response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("Raft response must contain one JSON value")
	}
	return nil
}
