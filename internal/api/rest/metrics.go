package rest

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var latencyBuckets = [...]float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type requestMetric struct {
	count   uint64
	sum     float64
	buckets [len(latencyBuckets) + 1]uint64
}

type replicaMetric struct {
	leaderSequence   uint64
	followerSequence uint64
}

type metricsRegistry struct {
	mu                    sync.Mutex
	requests              map[string]*requestMetric
	replicas              map[string]*replicaMetric
	replicationOperations map[string]uint64
	inFlight              atomic.Int64
}

func newMetricsRegistry() *metricsRegistry {
	return &metricsRegistry{requests: make(map[string]*requestMetric), replicas: make(map[string]*replicaMetric), replicationOperations: make(map[string]uint64)}
}

func metricKey(method, route, status string) string { return method + "\x00" + route + "\x00" + status }
func replicaMetricKey(collection string, shardID uint32, nodeID string) string {
	return collection + "\x00" + strconv.FormatUint(uint64(shardID), 10) + "\x00" + nodeID
}

func (m *metricsRegistry) setReplicaLeaderSequence(collection string, shardID uint32, nodeID string, sequence uint64) {
	m.mu.Lock()
	metric := m.replicas[replicaMetricKey(collection, shardID, nodeID)]
	if metric == nil {
		metric = &replicaMetric{}
		m.replicas[replicaMetricKey(collection, shardID, nodeID)] = metric
	}
	metric.leaderSequence = sequence
	m.mu.Unlock()
}

func (m *metricsRegistry) setReplicaFollowerSequence(collection string, shardID uint32, nodeID string, sequence uint64) {
	m.mu.Lock()
	metric := m.replicas[replicaMetricKey(collection, shardID, nodeID)]
	if metric == nil {
		metric = &replicaMetric{}
		m.replicas[replicaMetricKey(collection, shardID, nodeID)] = metric
	}
	metric.followerSequence = sequence
	m.mu.Unlock()
}

func (m *metricsRegistry) observeReplication(operation, result, collection string, shardID uint32, nodeID string, sequence uint64) {
	m.mu.Lock()
	m.replicationOperations[operation+"\x00"+result]++
	if result == "success" {
		metric := m.replicas[replicaMetricKey(collection, shardID, nodeID)]
		if metric == nil {
			metric = &replicaMetric{}
			m.replicas[replicaMetricKey(collection, shardID, nodeID)] = metric
		}
		metric.followerSequence = sequence
	}
	m.mu.Unlock()
}

func (m *metricsRegistry) observe(method, route string, status int, elapsed time.Duration) {
	statusText := strconv.Itoa(status)
	key := metricKey(method, route, statusText)
	seconds := elapsed.Seconds()
	m.mu.Lock()
	metric := m.requests[key]
	if metric == nil {
		metric = &requestMetric{}
		m.requests[key] = metric
	}
	metric.count++
	metric.sum += seconds
	bucket := len(latencyBuckets)
	for position, upper := range latencyBuckets {
		if seconds <= upper {
			bucket = position
			break
		}
	}
	metric.buckets[bucket]++
	m.mu.Unlock()
}

func (m *metricsRegistry) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.inFlight.Add(1)
		started := time.Now()
		tracked := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(tracked, r)
		m.inFlight.Add(-1)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		m.observe(r.Method, route, tracked.status, time.Since(started))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader, w.status = true, status
	w.ResponseWriter.WriteHeader(status)
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func (m *metricsRegistry) writeTo(w io.Writer, s *Server) {
	_, _ = io.WriteString(w, "# HELP gideondb_http_requests_total Completed HTTP requests.\n# TYPE gideondb_http_requests_total counter\n")
	_, _ = io.WriteString(w, "# HELP gideondb_http_request_duration_seconds HTTP request latency.\n# TYPE gideondb_http_request_duration_seconds histogram\n")
	m.mu.Lock()
	keys := make([]string, 0, len(m.requests))
	for key := range m.requests {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts, metric := strings.Split(key, "\x00"), m.requests[key]
		labels := fmt.Sprintf("method=\"%s\",route=\"%s\",status=\"%s\"", escapeLabel(parts[0]), escapeLabel(parts[1]), parts[2])
		_, _ = fmt.Fprintf(w, "gideondb_http_requests_total{%s} %d\n", labels, metric.count)
		var cumulative uint64
		for position, upper := range latencyBuckets {
			cumulative += metric.buckets[position]
			_, _ = fmt.Fprintf(w, "gideondb_http_request_duration_seconds_bucket{%s,le=\"%g\"} %d\n", labels, upper, cumulative)
		}
		cumulative += metric.buckets[len(latencyBuckets)]
		_, _ = fmt.Fprintf(w, "gideondb_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", labels, cumulative)
		_, _ = fmt.Fprintf(w, "gideondb_http_request_duration_seconds_sum{%s} %g\n", labels, metric.sum)
		_, _ = fmt.Fprintf(w, "gideondb_http_request_duration_seconds_count{%s} %d\n", labels, metric.count)
	}
	m.mu.Unlock()
	m.mu.Lock()
	operationKeys := make([]string, 0, len(m.replicationOperations))
	for key := range m.replicationOperations {
		operationKeys = append(operationKeys, key)
	}
	sort.Strings(operationKeys)
	_, _ = io.WriteString(w, "# HELP gideondb_replication_operations_total Replica transport operations.\n# TYPE gideondb_replication_operations_total counter\n")
	for _, key := range operationKeys {
		parts := strings.Split(key, "\x00")
		_, _ = fmt.Fprintf(w, "gideondb_replication_operations_total{operation=\"%s\",result=\"%s\"} %d\n", parts[0], parts[1], m.replicationOperations[key])
	}
	replicaKeys := make([]string, 0, len(m.replicas))
	for key := range m.replicas {
		replicaKeys = append(replicaKeys, key)
	}
	sort.Strings(replicaKeys)
	_, _ = io.WriteString(w, "# HELP gideondb_replication_lag_sequences Leader WAL sequences not acknowledged by a follower.\n# TYPE gideondb_replication_lag_sequences gauge\n")
	for _, key := range replicaKeys {
		parts, metric := strings.Split(key, "\x00"), m.replicas[key]
		lag := uint64(0)
		if metric.leaderSequence > metric.followerSequence {
			lag = metric.leaderSequence - metric.followerSequence
		}
		_, _ = fmt.Fprintf(w, "gideondb_replication_lag_sequences{collection=\"%s\",shard=\"%s\",node_id=\"%s\"} %d\n", escapeLabel(parts[0]), parts[1], escapeLabel(parts[2]), lag)
	}
	m.mu.Unlock()
	_, _ = fmt.Fprintf(w, "# HELP gideondb_http_requests_in_flight Requests currently executing.\n# TYPE gideondb_http_requests_in_flight gauge\ngideondb_http_requests_in_flight %d\n", m.inFlight.Load())
	configs := s.engine.ListCollections()
	_, _ = fmt.Fprintf(w, "# HELP gideondb_collections Configured collections.\n# TYPE gideondb_collections gauge\ngideondb_collections %d\n# HELP gideondb_vectors Live vectors by collection.\n# TYPE gideondb_vectors gauge\n", len(configs))
	for _, config := range configs {
		_, count, err := s.engine.DescribeCollection(config.Name)
		if err == nil {
			_, _ = fmt.Fprintf(w, "gideondb_vectors{collection=\"%s\"} %d\n", escapeLabel(config.Name), count)
		}
	}
	_, _ = io.WriteString(w, "# HELP gideondb_cluster_peer_healthy Whether a configured peer passed its last identity check.\n# TYPE gideondb_cluster_peer_healthy gauge\n")
	_, _ = io.WriteString(w, "# HELP gideondb_cluster_peer_state Current thresholded peer health state.\n# TYPE gideondb_cluster_peer_state gauge\n")
	if s.peerProvider != nil {
		for _, peer := range s.peerProvider.Peers() {
			value := 0
			if peer.Healthy {
				value = 1
			}
			_, _ = fmt.Fprintf(w, "gideondb_cluster_peer_healthy{seed=\"%s\",node_id=\"%s\"} %d\n", escapeLabel(peer.SeedURL), escapeLabel(peer.NodeID), value)
			_, _ = fmt.Fprintf(w, "gideondb_cluster_peer_state{seed=\"%s\",node_id=\"%s\",state=\"%s\"} 1\n", escapeLabel(peer.SeedURL), escapeLabel(peer.NodeID), escapeLabel(string(peer.State)))
		}
	}
	_, _, readinessReasons := s.clusterReadinessState()
	ready := 0
	if len(readinessReasons) == 0 {
		ready = 1
	}
	_, _ = fmt.Fprintf(w, "# HELP gideondb_cluster_view_ready Whether all configured peers report the local epoch and control-plane digests.\n# TYPE gideondb_cluster_view_ready gauge\ngideondb_cluster_view_ready %d\n", ready)
}
