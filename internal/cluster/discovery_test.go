package cluster

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

const (
	localTestNode  = "11111111111111111111111111111111"
	remoteTestNode = "22222222222222222222222222222222"
	clusterTestID  = "33333333333333333333333333333333"
)

func TestDiscoveryPollAndRetainLastSeenOnFailure(t *testing.T) {
	var fail atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/node" || r.Header.Get("Authorization") != "Bearer shared-secret" {
			return testResponse(http.StatusBadRequest, "bad request"), nil
		}
		if fail.Load() {
			return testResponse(http.StatusServiceUnavailable, "unavailable"), nil
		}
		return testResponse(http.StatusOK, fmt.Sprintf(`{"node_id":%q,"cluster_id":"33333333333333333333333333333333","advertise_address":"peer:6333","started_at":"2026-01-01T00:00:00Z","mode":"static-discovery","metadata_epoch":7,"replication_factor":2,"placement_capacity":3,"membership_digest":%q,"catalog_digest":%q,"placement_digest":%q}`, remoteTestNode, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64))), nil
	})}

	discovery, err := NewDiscovery(localTestNode, clusterTestID, []string{"http://peer-a:6333/"}, "shared-secret", client)
	if err != nil {
		t.Fatal(err)
	}
	discovery.Poll(context.Background())
	peer := discovery.Peers()[0]
	if !peer.Healthy || peer.NodeID != remoteTestNode || peer.AdvertiseAddress != "peer:6333" || peer.MetadataEpoch != 7 || peer.ReplicationFactor != 2 || peer.PlacementCapacity != 3 || peer.ProtocolVersion != 1 || peer.NegotiatedProtocol != 1 || peer.MembershipDigest != strings.Repeat("a", 64) || peer.LastSeen.IsZero() {
		t.Fatalf("unexpected healthy peer: %#v", peer)
	}
	lastSeen := peer.LastSeen

	fail.Store(true)
	discovery.Poll(context.Background())
	peer = discovery.Peers()[0]
	if peer.Healthy || peer.State != PeerSuspected || peer.ConsecutiveFailures != 1 || peer.NodeID != remoteTestNode || peer.PlacementCapacity != 3 || !peer.LastSeen.Equal(lastSeen) || !strings.Contains(peer.Error, "HTTP 503") {
		t.Fatalf("unexpected failed peer: %#v", peer)
	}
	discovery.Poll(context.Background())
	discovery.Poll(context.Background())
	peer = discovery.Peers()[0]
	if peer.State != PeerUnhealthy || peer.ConsecutiveFailures != 3 {
		t.Fatalf("expected unhealthy peer: %#v", peer)
	}
	fail.Store(false)
	discovery.Poll(context.Background())
	peer = discovery.Peers()[0]
	if peer.State != PeerRecovering || peer.Healthy || peer.ConsecutiveSuccesses != 1 {
		t.Fatalf("expected recovering peer: %#v", peer)
	}
	discovery.Poll(context.Background())
	peer = discovery.Peers()[0]
	if peer.State != PeerHealthy || !peer.Healthy || peer.ConsecutiveSuccesses != 2 {
		t.Fatalf("expected recovered peer: %#v", peer)
	}
}

func TestDiscoveryRejectsIncompatibleProtocolRange(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"node_id":%q,"cluster_id":%q,"advertise_address":"peer:6333","started_at":"2026-01-01T00:00:00Z","mode":"static-discovery","min_protocol_version":3,"protocol_version":4}`, remoteTestNode, clusterTestID)
		return testResponse(http.StatusOK, body), nil
	})}
	discovery, err := NewDiscovery(localTestNode, clusterTestID, []string{"http://peer-a:6333"}, "", client)
	if err != nil {
		t.Fatal(err)
	}
	discovery.Poll(context.Background())
	peer := discovery.Peers()[0]
	if peer.Healthy || peer.State != PeerUnhealthy || !strings.Contains(peer.Error, "incompatible cluster protocol") {
		t.Fatalf("unexpected incompatible peer: %#v", peer)
	}
}

func TestDiscoveryKeepsNeverObservedPeerUnknown(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return testResponse(http.StatusServiceUnavailable, "unavailable"), nil
	})}
	discovery, err := NewDiscovery(localTestNode, clusterTestID, []string{"http://unknown:6333"}, "", client)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		discovery.Poll(context.Background())
	}
	peer := discovery.Peers()[0]
	if peer.State != PeerUnknown || peer.Healthy || peer.NodeID != "" || peer.ConsecutiveFailures != 3 {
		t.Fatalf("unexpected unknown peer: %#v", peer)
	}
}

func TestDiscoveryRejectsSelfAndDuplicateNodeIDs(t *testing.T) {
	clientFor := func(nodeID string) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return testResponse(http.StatusOK, fmt.Sprintf(`{"node_id":%q,"cluster_id":"33333333333333333333333333333333","advertise_address":"peer:6333","started_at":"2026-01-01T00:00:00Z","mode":"standalone"}`, nodeID)), nil
		})}
	}
	discovery, err := NewDiscovery(localTestNode, clusterTestID, []string{"http://self:6333"}, "", clientFor(localTestNode))
	if err != nil {
		t.Fatal(err)
	}
	discovery.Poll(context.Background())
	if peers := discovery.Peers(); len(peers) != 0 {
		t.Fatalf("self seed was not pruned: %#v", peers)
	}

	discovery, err = NewDiscovery(localTestNode, clusterTestID, []string{"http://peer-a:6333", "http://peer-b:6333"}, "", clientFor(remoteTestNode))
	if err != nil {
		t.Fatal(err)
	}
	discovery.Poll(context.Background())
	for _, peer := range discovery.Peers() {
		if peer.Healthy || !strings.Contains(peer.Error, "duplicate node ID") {
			t.Fatalf("unexpected duplicate peer: %#v", peer)
		}
	}
}

func TestDiscoveryValidatesConfigurationAndResponse(t *testing.T) {
	for _, seed := range []string{"", "ftp://host", "http://user@host", "http://host/path"} {
		if _, err := NewDiscovery(localTestNode, clusterTestID, []string{seed}, "", nil); err == nil {
			t.Fatalf("expected invalid seed %q", seed)
		}
	}
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return testResponse(http.StatusOK, `{"node_id":"bad","advertise_address":"peer:6333","started_at":"2026-01-01T00:00:00Z","mode":"standalone"}{}`), nil
	})}
	discovery, err := NewDiscovery(localTestNode, clusterTestID, []string{"http://peer-a:6333"}, "", client)
	if err != nil {
		t.Fatal(err)
	}
	discovery.Poll(context.Background())
	if discovery.Peers()[0].Healthy {
		t.Fatal("expected malformed response rejection")
	}
}

func TestDiscoveryRejectsDifferentCluster(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"node_id":%q,"cluster_id":"44444444444444444444444444444444","advertise_address":"peer:6333","started_at":"2026-01-01T00:00:00Z","mode":"static-discovery"}`, remoteTestNode)
		return testResponse(http.StatusOK, body), nil
	})}
	discovery, err := NewDiscovery(localTestNode, clusterTestID, []string{"http://peer-a:6333"}, "", client)
	if err != nil {
		t.Fatal(err)
	}
	discovery.Poll(context.Background())
	peer := discovery.Peers()[0]
	if peer.Healthy || !strings.Contains(peer.Error, "different cluster") {
		t.Fatalf("unexpected peer: %#v", peer)
	}
}
