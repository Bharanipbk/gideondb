package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
	"github.com/vectordb/vectordb/internal/engine"
)

const backupOperationHeader = "X-VectorDB-Backup-Operation"

func (s *Server) backupBarrierMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !backupDataMutation(r) {
			next.ServeHTTP(w, r)
			return
		}
		s.backupStateMu.Lock()
		frozen := s.backupOperation != ""
		s.backupStateMu.Unlock()
		if frozen {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "backup_in_progress", Message: "data mutations are frozen for a coordinated backup"})
			return
		}
		s.backupGate.RLock()
		defer s.backupGate.RUnlock()
		s.backupStateMu.Lock()
		frozen = s.backupOperation != ""
		s.backupStateMu.Unlock()
		if frozen {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "backup_in_progress", Message: "data mutations are frozen for a coordinated backup"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func backupDataMutation(r *http.Request) bool {
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		return false
	}
	path := r.URL.Path
	return strings.Contains(path, "/vectors") || strings.Contains(path, "/replicas/") || strings.Contains(path, "/rebalance/") || (path == "/v1/collections" && r.Method == http.MethodPost) || (strings.HasPrefix(path, "/v1/collections/") && r.Method == http.MethodDelete)
}

func (s *Server) internalBackupFreeze(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	operation := r.Header.Get(backupOperationHeader)
	if operation == "" || len(operation) > 128 {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_backup_operation", Message: "a bounded backup operation ID is required"})
		return
	}
	if err := s.freezeBackup(operation); err != nil {
		writeJSON(w, http.StatusConflict, apiError{Code: "backup_active", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backup_operation": operation, "frozen": true, "metadata_epoch": s.currentMetadataEpoch()})
}

func (s *Server) internalBackupRecoveryPoint(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	operation := r.Header.Get(backupOperationHeader)
	s.backupStateMu.Lock()
	active := operation != "" && s.backupOperation == operation
	s.backupStateMu.Unlock()
	if !active {
		writeJSON(w, http.StatusConflict, apiError{Code: "backup_not_frozen", Message: "matching backup freeze is required"})
		return
	}
	view, err := s.viewDigests()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "cluster_view_invalid", Message: err.Error()})
		return
	}
	point, err := s.engine.CaptureRecoveryPoint(s.currentMetadataEpoch(), s.nodeID, view.Placement, view.CapacityManifest)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "recovery_point_failed", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, point)
}

func (s *Server) internalBackupArchive(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	operation := r.Header.Get(backupOperationHeader)
	s.backupStateMu.Lock()
	active := operation != "" && s.backupOperation == operation
	s.backupStateMu.Unlock()
	if !active {
		writeJSON(w, http.StatusConflict, apiError{Code: "backup_not_frozen", Message: "matching backup freeze is required"})
		return
	}
	var point engine.NodeRecoveryPoint
	if !decode(w, r, &point) {
		return
	}
	temporary, err := os.CreateTemp("", ".vectordb-node-backup-*.tar.gz")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "backup_archive_failed", Message: err.Error()})
		return
	}
	path := temporary.Name()
	_ = temporary.Close()
	_ = os.Remove(path)
	defer os.Remove(path)
	if err := s.engine.BackupAtRecoveryPoint(path, point); err != nil {
		writeJSON(w, http.StatusConflict, apiError{Code: "backup_recovery_point_mismatch", Message: err.Error()})
		return
	}
	archive, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "backup_archive_failed", Message: err.Error()})
		return
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "backup_archive_failed", Message: err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+s.nodeID+`.tar.gz"`)
	http.ServeContent(w, r, s.nodeID+".tar.gz", info.ModTime(), archive)
}

func (s *Server) internalBackupRelease(w http.ResponseWriter, r *http.Request) {
	if !s.validateInternalFence(w, r) {
		return
	}
	operation := r.Header.Get(backupOperationHeader)
	if err := s.releaseBackup(operation); err != nil {
		writeJSON(w, http.StatusConflict, apiError{Code: "backup_operation_mismatch", Message: "only the active backup operation can release the barrier"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backup_operation": operation, "released": true})
}

func (s *Server) freezeBackup(operation string) error {
	s.backupStateMu.Lock()
	if s.backupOperation != "" {
		active := s.backupOperation
		s.backupStateMu.Unlock()
		if active == operation {
			return nil
		}
		return fmt.Errorf("backup operation %s is already active", active)
	}
	s.backupOperation = operation
	s.backupStateMu.Unlock()
	s.backupGate.Lock()
	return nil
}

func (s *Server) releaseBackup(operation string) error {
	s.backupStateMu.Lock()
	if operation == "" || s.backupOperation != operation {
		s.backupStateMu.Unlock()
		return fmt.Errorf("backup operation mismatch")
	}
	s.backupOperation = ""
	s.backupStateMu.Unlock()
	s.backupGate.Unlock()
	return nil
}

type clusterBackupRequest struct {
	Operation string `json:"operation"`
}

func (s *Server) clusterBackupRecoveryPoint(w http.ResponseWriter, r *http.Request) {
	var request clusterBackupRequest
	if !decode(w, r, &request) {
		return
	}
	if request.Operation == "" || len(request.Operation) > 128 {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_backup_operation", Message: "a bounded backup operation ID is required"})
		return
	}
	status, ok := s.raftProtocol.(interface {
		Status() (cluster.RaftRole, string, uint64)
	})
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "raft_unavailable", Message: "metadata Raft status is required"})
		return
	}
	role, leaderID, _ := status.Status()
	if role != cluster.RaftLeader {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "not_raft_leader", "leader_id": leaderID})
		return
	}
	if !s.staticPlacementReady() {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "cluster_not_ready", Message: "authoritative converged placement is required"})
		return
	}
	peers, err := s.membershipPeers()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "membership_unavailable", Message: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	frozen := make([]cluster.Peer, 0, len(peers))
	if err := s.freezeBackup(request.Operation); err != nil {
		writeJSON(w, http.StatusConflict, apiError{Code: "backup_active", Message: err.Error()})
		return
	}
	defer func() {
		for _, peer := range frozen {
			_ = s.remoteBackupCall(context.Background(), peer, http.MethodPost, "/v1/internal/backup/release", request.Operation, nil)
		}
		_ = s.releaseBackup(request.Operation)
	}()
	for _, peer := range peers {
		if err := s.remoteBackupCall(ctx, peer, http.MethodPost, "/v1/internal/backup/freeze", request.Operation, nil); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "backup_freeze_failed", Message: peer.NodeID + ": " + err.Error()})
			return
		}
		frozen = append(frozen, peer)
	}
	view, err := s.viewDigests()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "cluster_view_invalid", Message: err.Error()})
		return
	}
	local, err := s.engine.CaptureRecoveryPoint(s.currentMetadataEpoch(), s.nodeID, view.Placement, view.CapacityManifest)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "recovery_point_failed", Message: err.Error()})
		return
	}
	points := []engine.NodeRecoveryPoint{local}
	for _, peer := range peers {
		var point engine.NodeRecoveryPoint
		if err := s.remoteBackupCall(ctx, peer, http.MethodGet, "/v1/internal/backup/recovery-point", request.Operation, &point); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "recovery_point_failed", Message: peer.NodeID + ": " + err.Error()})
			return
		}
		points = append(points, point)
	}
	manifest, err := engine.MergeRecoveryPoints(points)
	if err != nil {
		writeJSON(w, http.StatusConflict, apiError{Code: "recovery_points_inconsistent", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": request.Operation, "recovery_point": manifest})
}

func (s *Server) remoteBackupCall(ctx context.Context, peer cluster.Peer, method, path, operation string, destination any) error {
	request, err := http.NewRequestWithContext(ctx, method, peer.SeedURL+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-VectorDB-Cluster-ID", s.clusterID)
	request.Header.Set("X-VectorDB-Target-Node-ID", peer.NodeID)
	request.Header.Set("X-VectorDB-Metadata-Epoch", strconv.FormatUint(s.currentMetadataEpoch(), 10))
	request.Header.Set(backupOperationHeader, operation)
	if s.peerAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+s.peerAPIKey)
	}
	response, err := s.internalClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("peer returned HTTP %d: %s", response.StatusCode, body)
	}
	if destination != nil {
		decoder := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(destination); err != nil {
			return err
		}
	}
	return nil
}
