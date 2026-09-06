package rest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

type backupLeaderStatus struct{ fixedRaftStatus }

func (b backupLeaderStatus) Status() (cluster.RaftRole, string, uint64) {
	return cluster.RaftLeader, "11111111111111111111111111111111", b.term
}

func TestAuthenticatedBackupFreezeCaptureAndRelease(t *testing.T) {
	const nodeID = "11111111111111111111111111111111"
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateCollection(core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 4}); err != nil {
		t.Fatal(err)
	}
	server := NewWithOptions(db, nil, Options{NodeID: nodeID, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 1})
	call := func(method, path, operation string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		request.Header.Set("X-GideonDB-Cluster-ID", clusterID)
		request.Header.Set("X-GideonDB-Target-Node-ID", nodeID)
		request.Header.Set("X-GideonDB-Metadata-Epoch", "1")
		request.Header.Set(backupOperationHeader, operation)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		return response
	}
	if response := call(http.MethodPost, "/v1/internal/backup/freeze", "backup-1"); response.Code != http.StatusOK {
		t.Fatalf("freeze status=%d body=%s", response.Code, response.Body.String())
	}
	write := httptest.NewRequest(http.MethodPost, "/v1/collections/docs/vectors", strings.NewReader(`{"id":"one","vector":[1,2]}`))
	write.Header.Set("Content-Type", "application/json")
	blocked := httptest.NewRecorder()
	server.Handler().ServeHTTP(blocked, write)
	if blocked.Code != http.StatusServiceUnavailable || !strings.Contains(blocked.Body.String(), "backup_in_progress") {
		t.Fatalf("blocked write status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	pointResponse := call(http.MethodGet, "/v1/internal/backup/recovery-point", "backup-1")
	if pointResponse.Code != http.StatusOK {
		t.Fatalf("capture status=%d body=%s", pointResponse.Code, pointResponse.Body.String())
	}
	var point struct {
		MetadataEpoch uint64 `json:"metadata_epoch"`
		Shards        []any  `json:"shards"`
	}
	if err := json.Unmarshal(pointResponse.Body.Bytes(), &point); err != nil || point.MetadataEpoch != 1 || len(point.Shards) != 4 {
		t.Fatalf("point=%#v err=%v", point, err)
	}
	if response := call(http.MethodPost, "/v1/internal/backup/release", "wrong"); response.Code != http.StatusConflict {
		t.Fatalf("wrong release status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(http.MethodPost, "/v1/internal/backup/release", "backup-1"); response.Code != http.StatusOK {
		t.Fatalf("release status=%d body=%s", response.Code, response.Body.String())
	}
	write = httptest.NewRequest(http.MethodPost, "/v1/collections/docs/vectors", strings.NewReader(`{"id":"one","vector":[1,2]}`))
	write.Header.Set("Content-Type", "application/json")
	written := httptest.NewRecorder()
	server.Handler().ServeHTTP(written, write)
	if written.Code != http.StatusOK {
		t.Fatalf("write after release status=%d body=%s", written.Code, written.Body.String())
	}
}

func TestLeaderCoordinatesRecoveryPointAcrossNodesAndReleasesBarriers(t *testing.T) {
	const nodeA = "11111111111111111111111111111111"
	const nodeB = "22222222222222222222222222222222"
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 4}
	dbA, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	for _, db := range []*engine.Engine{dbA, dbB} {
		if err := db.CreateCollection(config); err != nil {
			t.Fatal(err)
		}
	}
	digests, err := cluster.ComputeViewDigests(1, nodeA, "node-a:6333", []cluster.Peer{{NodeID: nodeB, AdvertiseAddress: "node-b:6333"}}, []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	peerA := cluster.Peer{SeedURL: "http://node-a:6333", NodeID: nodeA, AdvertiseAddress: "node-a:6333", Healthy: true, State: cluster.PeerHealthy, MetadataEpoch: 1, ReplicationFactor: 1, MembershipDigest: digests.Membership, CatalogDigest: digests.Catalog, PlacementDigest: digests.Placement}
	peerB := cluster.Peer{SeedURL: "http://node-b:6333", NodeID: nodeB, AdvertiseAddress: "node-b:6333", Healthy: true, State: cluster.PeerHealthy, MetadataEpoch: 1, ReplicationFactor: 1, MembershipDigest: digests.Membership, CatalogDigest: digests.Catalog, PlacementDigest: digests.Placement}
	follower := NewWithOptions(dbB, nil, Options{NodeID: nodeB, ClusterID: clusterID, AdvertiseAddress: "node-b:6333", MetadataEpoch: 1, PeerProvider: staticPeerProvider{peerA}, EnableStaticRouting: true, RaftProtocol: fixedRaftStatus{term: 4}})
	client := &http.Client{Transport: restRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		follower.Handler().ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
	leader := NewWithOptions(dbA, nil, Options{NodeID: nodeA, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 1, PeerProvider: staticPeerProvider{peerB}, InternalHTTPClient: client, EnableStaticRouting: true, RaftProtocol: backupLeaderStatus{fixedRaftStatus{term: 4}}})
	payload := bytes.NewBufferString(`{"operation":"cluster-backup-1"}`)
	response := httptest.NewRecorder()
	leader.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/cluster/backup/recovery-point", payload))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"nodes":["`+nodeA+`","`+nodeB+`"]`) {
		t.Fatalf("coordinator status=%d body=%s", response.Code, response.Body.String())
	}
	if err := follower.freezeBackup("verification"); err != nil {
		t.Fatal(err)
	}
	if err := follower.releaseBackup("verification"); err != nil {
		t.Fatal(err)
	}
}
