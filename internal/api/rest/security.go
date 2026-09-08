package rest

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/cluster"
)

type PeerProvider interface{ Peers() []cluster.Peer }

type RaftProtocol interface {
	RequestVote(cluster.RequestVoteRequest) (cluster.RequestVoteResponse, error)
	AppendEntries(cluster.AppendEntriesRequest) (cluster.AppendEntriesResponse, error)
	InstallSnapshot(cluster.InstallSnapshotRequest) (cluster.InstallSnapshotResponse, error)
}

type Options struct {
	APIKey, NodeID, ClusterID, AdvertiseAddress string
	PrincipalsFile                              string
	EventLogPath                                string
	EventLogRetention                           int
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
	RateLimitPerSecond                          int
	RateLimitBurst                              int
	DashboardUsername                           string
	DashboardPassword                           string
}

type Principal struct {
	Name               string   `json:"name"`
	Key                string   `json:"key"`
	Role               string   `json:"role"`
	CollectionPrefixes []string `json:"collection_prefixes,omitempty"`
}

type principalSet struct {
	Principals []Principal `json:"principals"`
}
type principalStore struct {
	path string
	mu   sync.Mutex
}
type principalContextKey struct{}

func LoadPrincipalsFile(path string) ([]Principal, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !secureAPIKeyFile(info) {
		return nil, fmt.Errorf("principals file permissions must restrict access to the process owner or group")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var set principalSet
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&set); err != nil {
		return nil, fmt.Errorf("decode principals: %w", err)
	}
	if len(set.Principals) == 0 {
		return nil, fmt.Errorf("principals file must contain at least one principal")
	}
	seen := map[string]bool{}
	for i := range set.Principals {
		p := &set.Principals[i]
		if p.Name == "" || len(p.Key) < 16 || (p.Role != "reader" && p.Role != "writer" && p.Role != "admin") || seen[p.Name] {
			return nil, fmt.Errorf("invalid principal at position %d", i)
		}
		seen[p.Name] = true
	}
	return set.Principals, nil
}

func (s *principalStore) authenticate(key string) (*Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	principals, err := LoadPrincipalsFile(s.path)
	if err != nil {
		return nil, err
	}
	provided := sha256.Sum256([]byte(key))
	for _, principal := range principals {
		digest := sha256.Sum256([]byte(principal.Key))
		if subtle.ConstantTimeCompare(provided[:], digest[:]) == 1 {
			copy := principal
			copy.Key = ""
			return &copy, nil
		}
	}
	return nil, nil
}

// AuthenticatePrincipalFile resolves one bearer key against the reloadable
// principals file. The returned principal never contains credential material.
func AuthenticatePrincipalFile(path, key string) (*Principal, error) {
	return (&principalStore{path: path}).authenticate(key)
}

func principalFromRequest(r *http.Request) *Principal {
	p, _ := r.Context().Value(principalContextKey{}).(*Principal)
	return p
}

func allowedCollection(p *Principal, name string) bool {
	if p == nil || p.Role == "admin" || name == "" {
		return true
	}
	for _, prefix := range p.CollectionPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func allowedRole(p *Principal, r *http.Request) bool {
	if p == nil || p.Role == "admin" {
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/v1/internal/") || strings.HasPrefix(r.URL.Path, "/v1/cluster/rebalance") || strings.HasPrefix(r.URL.Path, "/v1/cluster/backup") || strings.HasPrefix(r.URL.Path, "/v1/cluster/metadata") || r.URL.Path == "/v1/collections" && r.Method == http.MethodPost {
		return false
	}
	if r.Method == http.MethodDelete && r.PathValue("name") != "" && r.PathValue("id") == "" {
		return false
	}
	if p.Role == "reader" && r.Method != http.MethodGet && !strings.HasSuffix(r.URL.Path, "/search") {
		return false
	}
	return true
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
	if s.apiKeyHash == nil && s.principals == nil && s.dashboardAuth == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if s.dashboardAuth != nil {
			if session, ok := s.dashboardAuth.session(r); ok {
				if r.Method != http.MethodGet && r.Method != http.MethodHead && subtle.ConstantTimeCompare([]byte(session.csrf), []byte(r.Header.Get("X-GideonDB-CSRF"))) != 1 {
					writeJSON(w, http.StatusForbidden, apiError{Code: "csrf_rejected", Message: "valid CSRF token required"})
					return
				}
				next(w, r)
				return
			}
		}
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="gideondb"`)
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "authentication required"})
			return
		}
		key := strings.TrimPrefix(header, prefix)
		var principal *Principal
		if s.principals != nil {
			principal, _ = s.principals.authenticate(key)
		}
		provided := sha256.Sum256([]byte(key))
		legacy := s.apiKeyHash != nil && subtle.ConstantTimeCompare(provided[:], s.apiKeyHash) == 1
		if principal == nil && !legacy {
			w.Header().Set("WWW-Authenticate", `Bearer realm="gideondb"`)
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "authentication required"})
			return
		}
		if principal != nil && (!allowedRole(principal, r) || !allowedCollection(principal, firstNonEmpty(r.PathValue("name"), r.PathValue("collection")))) {
			writeJSON(w, http.StatusForbidden, apiError{Code: "forbidden", Message: "principal is not authorized for this operation"})
			return
		}
		if principal != nil {
			r = r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal))
		}
		next(w, r)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
