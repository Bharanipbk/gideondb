// Package config implements layered process configuration.
package config

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Bharanipbk/gideondb/internal/wal"
)

type Config struct {
	HTTPAddress          string   `json:"http_address"`
	AdvertiseAddress     string   `json:"advertise_address,omitempty"`
	ClusterID            string   `json:"cluster_id,omitempty"`
	Peers                []string `json:"peers,omitempty"`
	DataPath             string   `json:"data_path"`
	WALSync              string   `json:"wal_sync"`
	CheckpointEvery      uint64   `json:"checkpoint_every"`
	ReplicationFactor    int      `json:"replication_factor"`
	PlacementCapacity    uint32   `json:"placement_capacity"`
	APIKeyFile           string   `json:"api_key_file,omitempty"`
	PrincipalsFile       string   `json:"principals_file,omitempty"`
	AllowUnauthenticated bool     `json:"allow_unauthenticated"`
	AllowInsecureHTTP    bool     `json:"allow_insecure_http"`
	EnableStaticRouting  bool     `json:"enable_static_routing"`
	TLSCertFile          string   `json:"tls_cert_file,omitempty"`
	TLSKeyFile           string   `json:"tls_key_file,omitempty"`
	TLSCAFile            string   `json:"tls_ca_file,omitempty"`
	NodeTLSCertFile      string   `json:"node_tls_cert_file,omitempty"`
	NodeTLSKeyFile       string   `json:"node_tls_key_file,omitempty"`
	RateLimitPerSecond   int      `json:"rate_limit_per_second"`
	RateLimitBurst       int      `json:"rate_limit_burst"`
	AuditRetention       int      `json:"audit_retention"`
}

func Default() Config {
	return Config{HTTPAddress: "127.0.0.1:6333", DataPath: "./data", WALSync: string(wal.SyncAlways), CheckpointEvery: 1000, ReplicationFactor: 1, PlacementCapacity: 1, RateLimitPerSecond: 100, RateLimitBurst: 200, AuditRetention: 4096}
}

// Load overlays a strict JSON object onto base.
func Load(path string, base Config) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	result := base
	if err := decoder.Decode(&result); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Config{}, fmt.Errorf("config must contain one JSON object")
	}
	return result, nil
}

// ApplyEnv overlays supported environment variables. getenv is injectable for tests.
func ApplyEnv(value Config, getenv func(string) (string, bool)) (Config, error) {
	strings := []struct {
		name   string
		target *string
	}{
		{"GIDEONDB_HTTP_ADDRESS", &value.HTTPAddress}, {"GIDEONDB_DATA_PATH", &value.DataPath},
		{"GIDEONDB_ADVERTISE_ADDRESS", &value.AdvertiseAddress},
		{"GIDEONDB_CLUSTER_ID", &value.ClusterID},
		{"GIDEONDB_WAL_SYNC", &value.WALSync}, {"GIDEONDB_API_KEY_FILE", &value.APIKeyFile},
		{"GIDEONDB_PRINCIPALS_FILE", &value.PrincipalsFile},
		{"GIDEONDB_TLS_CERT_FILE", &value.TLSCertFile}, {"GIDEONDB_TLS_KEY_FILE", &value.TLSKeyFile}, {"GIDEONDB_TLS_CA_FILE", &value.TLSCAFile},
		{"GIDEONDB_NODE_TLS_CERT_FILE", &value.NodeTLSCertFile}, {"GIDEONDB_NODE_TLS_KEY_FILE", &value.NodeTLSKeyFile},
	}
	for _, item := range strings {
		if raw, ok := getenv(item.name); ok {
			*item.target = raw
		}
	}
	if raw, ok := getenv("GIDEONDB_CHECKPOINT_EVERY"); ok {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("GIDEONDB_CHECKPOINT_EVERY: %w", err)
		}
		value.CheckpointEvery = parsed
	}
	if raw, ok := getenv("GIDEONDB_REPLICATION_FACTOR"); ok {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("GIDEONDB_REPLICATION_FACTOR: %w", err)
		}
		value.ReplicationFactor = parsed
	}
	if raw, ok := getenv("GIDEONDB_PLACEMENT_CAPACITY"); ok {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return Config{}, fmt.Errorf("GIDEONDB_PLACEMENT_CAPACITY: %w", err)
		}
		value.PlacementCapacity = uint32(parsed)
	}
	for _, item := range []struct {
		name   string
		target *int
	}{{"GIDEONDB_RATE_LIMIT_PER_SECOND", &value.RateLimitPerSecond}, {"GIDEONDB_RATE_LIMIT_BURST", &value.RateLimitBurst}, {"GIDEONDB_AUDIT_RETENTION", &value.AuditRetention}} {
		if raw, ok := getenv(item.name); ok {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", item.name, err)
			}
			*item.target = parsed
		}
	}
	if raw, ok := getenv("GIDEONDB_PEERS"); ok {
		value.Peers = splitPeers(raw)
	}
	booleans := []struct {
		name   string
		target *bool
	}{
		{"GIDEONDB_ALLOW_UNAUTHENTICATED", &value.AllowUnauthenticated}, {"GIDEONDB_ALLOW_INSECURE_HTTP", &value.AllowInsecureHTTP},
		{"GIDEONDB_ENABLE_STATIC_ROUTING", &value.EnableStaticRouting},
	}
	for _, item := range booleans {
		if raw, ok := getenv(item.name); ok {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", item.name, err)
			}
			*item.target = parsed
		}
	}
	return value, nil
}

func ApplyFlags(value Config, values map[string]string) (Config, error) {
	for name, raw := range values {
		switch name {
		case "http-address":
			value.HTTPAddress = raw
		case "advertise-address":
			value.AdvertiseAddress = raw
		case "peers":
			value.Peers = splitPeers(raw)
		case "cluster-id":
			value.ClusterID = raw
		case "data-path":
			value.DataPath = raw
		case "wal-sync":
			value.WALSync = raw
		case "api-key-file":
			value.APIKeyFile = raw
		case "principals-file":
			value.PrincipalsFile = raw
		case "tls-cert-file":
			value.TLSCertFile = raw
		case "tls-key-file":
			value.TLSKeyFile = raw
		case "tls-ca-file":
			value.TLSCAFile = raw
		case "node-tls-cert-file":
			value.NodeTLSCertFile = raw
		case "node-tls-key-file":
			value.NodeTLSKeyFile = raw
		case "checkpoint-every":
			parsed, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return Config{}, err
			}
			value.CheckpointEvery = parsed
		case "replication-factor":
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return Config{}, err
			}
			value.ReplicationFactor = parsed
		case "placement-capacity":
			parsed, err := strconv.ParseUint(raw, 10, 32)
			if err != nil {
				return Config{}, err
			}
			value.PlacementCapacity = uint32(parsed)
		case "rate-limit-per-second":
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return Config{}, err
			}
			value.RateLimitPerSecond = parsed
		case "rate-limit-burst":
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return Config{}, err
			}
			value.RateLimitBurst = parsed
		case "audit-retention":
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				return Config{}, err
			}
			value.AuditRetention = parsed
		case "allow-unauthenticated":
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return Config{}, err
			}
			value.AllowUnauthenticated = parsed
		case "allow-insecure-http":
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return Config{}, err
			}
			value.AllowInsecureHTTP = parsed
		case "enable-static-routing":
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return Config{}, err
			}
			value.EnableStaticRouting = parsed
		}
	}
	return value, nil
}

func (c Config) Validate() error {
	if c.HTTPAddress == "" || c.DataPath == "" {
		return fmt.Errorf("http_address and data_path are required")
	}
	if c.WALSync != string(wal.SyncAlways) && c.WALSync != string(wal.SyncAsync) {
		return fmt.Errorf("wal_sync must be always or async")
	}
	if c.ReplicationFactor < 1 {
		return fmt.Errorf("replication_factor must be positive")
	}
	if c.PlacementCapacity < 1 || c.PlacementCapacity > 256 {
		return fmt.Errorf("placement_capacity must be between 1 and 256")
	}
	if c.RateLimitPerSecond < 1 || c.RateLimitBurst < 1 || c.RateLimitBurst < c.RateLimitPerSecond {
		return fmt.Errorf("rate_limit_per_second must be positive and rate_limit_burst must be at least that value")
	}
	if c.AuditRetention < 256 || c.AuditRetention > 1000000 {
		return fmt.Errorf("audit_retention must be between 256 and 1000000")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("tls_cert_file and tls_key_file are required together")
	}
	if c.TLSCAFile != "" && c.TLSCertFile == "" {
		return fmt.Errorf("tls_ca_file requires tls_cert_file and tls_key_file")
	}
	if (c.NodeTLSCertFile == "") != (c.NodeTLSKeyFile == "") {
		return fmt.Errorf("node_tls_cert_file and node_tls_key_file are required together")
	}
	if c.NodeTLSCertFile != "" && c.TLSCAFile == "" {
		return fmt.Errorf("node TLS identity requires tls_ca_file")
	}
	if len(c.Peers) > 256 {
		return fmt.Errorf("peers must contain at most 256 entries")
	}
	if c.ClusterID != "" {
		decoded, err := hex.DecodeString(c.ClusterID)
		if err != nil || len(decoded) != 16 || c.ClusterID != strings.ToLower(c.ClusterID) || allZero(decoded) {
			return fmt.Errorf("cluster_id must be 32 lowercase hexadecimal characters and non-zero")
		}
	}
	return nil
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

func splitPeers(raw string) []string {
	var result []string
	for _, peer := range strings.Split(raw, ",") {
		if peer = strings.TrimSpace(peer); peer != "" {
			result = append(result, peer)
		}
	}
	return result
}
