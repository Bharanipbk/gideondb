package rest

import (
	"context"
	"net/http"

	"github.com/Bharanipbk/gideondb/internal/cluster"
)

type raftLeadershipTransfer interface {
	TimeoutNow(context.Context, cluster.TimeoutNowRequest) (cluster.TimeoutNowResponse, error)
}

func (s *Server) raftRequestVote(w http.ResponseWriter, r *http.Request) {
	if !s.validateRaftEnvelope(w, r) {
		return
	}
	var request cluster.RequestVoteRequest
	if !decode(w, r, &request) {
		return
	}
	response, err := s.raftProtocol.RequestVote(request)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_raft_request", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) raftAppendEntries(w http.ResponseWriter, r *http.Request) {
	if !s.validateRaftEnvelope(w, r) {
		return
	}
	var request cluster.AppendEntriesRequest
	if !decode(w, r, &request) {
		return
	}
	response, err := s.raftProtocol.AppendEntries(request)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_raft_request", Message: err.Error()})
		return
	}
	w.Header().Set("X-GideonDB-Metadata-Epoch", formatUint(s.currentMetadataEpoch()))
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) raftInstallSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.validateRaftEnvelope(w, r) {
		return
	}
	var request cluster.InstallSnapshotRequest
	if !decode(w, r, &request) {
		return
	}
	response, err := s.raftProtocol.InstallSnapshot(request)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_raft_snapshot", Message: err.Error()})
		return
	}
	w.Header().Set("X-GideonDB-Metadata-Epoch", formatUint(s.currentMetadataEpoch()))
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) raftTimeoutNow(w http.ResponseWriter, r *http.Request) {
	if !s.validateRaftEnvelope(w, r) {
		return
	}
	transfer, ok := s.raftProtocol.(raftLeadershipTransfer)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "raft_transfer_unavailable", Message: "leadership transfer is not supported"})
		return
	}
	var request cluster.TimeoutNowRequest
	if !decode(w, r, &request) {
		return
	}
	response, err := transfer.TimeoutNow(r.Context(), request)
	if err != nil {
		writeJSON(w, http.StatusConflict, apiError{Code: "invalid_leadership_transfer", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) validateRaftEnvelope(w http.ResponseWriter, r *http.Request) bool {
	if s.raftProtocol == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "raft_unavailable", Message: "metadata Raft is not configured"})
		return false
	}
	if r.Header.Get("X-GideonDB-Cluster-ID") != s.clusterID {
		writeJSON(w, http.StatusConflict, apiError{Code: "cluster_mismatch", Message: "Raft request cluster ID does not match this node"})
		return false
	}
	return true
}
