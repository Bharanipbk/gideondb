package rest

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/vectordb/vectordb/internal/cluster"
)

type PeerProvider interface{ Peers() []cluster.Peer }

type RaftProtocol interface {
	RequestVote(cluster.RequestVoteRequest) (cluster.RequestVoteResponse, error)
	AppendEntries(cluster.AppendEntriesRequest) (cluster.AppendEntriesResponse, error)
	InstallSnapshot(cluster.InstallSnapshotRequest) (cluster.InstallSnapshotResponse, error)
}

type Options struct {
	APIKey, NodeID, ClusterID, AdvertiseAddress string
	MetadataEpoch                               uint64
	ReplicationFactor                           int
	PlacementCapacity                           uint32
	PeerProvider                                PeerProvider
	InternalHTTPClient                          *http.Client
	EnableStaticRouting                         bool
	RaftStore                                   *cluster.RaftStore
	RaftProtocol                                RaftProtocol
	RebalanceBarriers                           *cluster.RebalanceBarrierStore
	RebalanceExecutor                           *cluster.RebalanceExecutor
	RequireInternalMTLS                         bool
}

func requireInternalMTLS(enabled bool, next http.Handler) http.Handler {
	if !enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/internal/") && (r.TLS == nil || len(r.TLS.VerifiedChains) == 0) {
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "mutual_tls_required", Message: "verified client certificate required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func LoadAPIKeyFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !secureAPIKeyFile(info) {
		return "", fmt.Errorf("API key file permissions must restrict access to the process owner or group")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if len(key) < 16 {
		return "", fmt.Errorf("API key must contain at least 16 characters")
	}
	return key, nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func IsLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) protected(next http.HandlerFunc) http.HandlerFunc {
	if s.apiKeyHash == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="vectordb"`)
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "authentication required"})
			return
		}
		provided := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
		if subtle.ConstantTimeCompare(provided[:], s.apiKeyHash) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="vectordb"`)
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "authentication required"})
			return
		}
		next(w, r)
	}
}
