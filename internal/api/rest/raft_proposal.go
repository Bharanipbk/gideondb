package rest

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
)

type raftProposer interface {
	Propose(context.Context, cluster.MetadataCommand) (uint64, error)
}

type metadataEpochProposal struct {
	ExpectedEpoch uint64 `json:"expected_epoch"`
}

func (s *Server) advanceMetadataEpoch(w http.ResponseWriter, r *http.Request) {
	proposer, ok := s.raftProtocol.(raftProposer)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "raft_unavailable", Message: "metadata proposal service is not configured"})
		return
	}
	var request metadataEpochProposal
	if !decode(w, r, &request) {
		return
	}
	current := s.currentMetadataEpoch()
	if request.ExpectedEpoch != current {
		writeJSON(w, http.StatusConflict, apiError{Code: "epoch_precondition_failed", Message: "expected_epoch does not match the committed metadata epoch"})
		return
	}
	peers, err := s.membershipPeers()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: err.Error()})
		return
	}
	digests, err := cluster.ComputeViewDigestsWithCapacity(current+1, s.nodeID, s.advertiseAddress, s.currentPlacementCapacity(), peers, s.engine.ListCollections())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "cluster_view_invalid", Message: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	epoch, err := proposer.Propose(ctx, cluster.MetadataCommand{Type: "advance_epoch", Epoch: current + 1, MembershipDigest: digests.Membership, CatalogDigest: digests.Catalog, PlacementDigest: digests.Placement, CapacityManifest: digests.CapacityManifest})
	if errors.Is(err, cluster.ErrNotRaftLeader) {
		role, leaderID, term := cluster.RaftFollower, "", uint64(0)
		if status, available := s.raftProtocol.(interface {
			Status() (cluster.RaftRole, string, uint64)
		}); available {
			role, leaderID, term = status.Status()
		}
		writeJSON(w, http.StatusConflict, map[string]any{"code": "not_raft_leader", "message": err.Error(), "raft_role": role, "leader_id": leaderID, "term": term})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "metadata_quorum_unavailable", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"metadata_epoch": epoch, "membership_digest": digests.Membership, "catalog_digest": digests.Catalog, "placement_digest": digests.Placement})
}
