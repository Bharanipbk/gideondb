package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayeredConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"http_address":"localhost:7000","checkpoint_every":50,"allow_insecure_http":true,"peers":["http://one:6333"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, Default())
	if err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{"VECTORDB_HTTP_ADDRESS": "127.0.0.1:8000", "VECTORDB_CHECKPOINT_EVERY": "75", "VECTORDB_REPLICATION_FACTOR": "2", "VECTORDB_PLACEMENT_CAPACITY": "3", "VECTORDB_ALLOW_INSECURE_HTTP": "false", "VECTORDB_ENABLE_STATIC_ROUTING": "true", "VECTORDB_PEERS": "http://two:6333, http://three:6333"}
	loaded, err = ApplyEnv(loaded, func(name string) (string, bool) { value, ok := environment[name]; return value, ok })
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = ApplyFlags(loaded, map[string]string{"http-address": "127.0.0.1:9000", "checkpoint-every": "100", "replication-factor": "3", "placement-capacity": "4", "peers": "http://four:6333", "enable-static-routing": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.HTTPAddress != "127.0.0.1:9000" || loaded.CheckpointEvery != 100 || loaded.ReplicationFactor != 3 || loaded.PlacementCapacity != 4 || loaded.AllowInsecureHTTP || loaded.EnableStaticRouting {
		t.Fatalf("unexpected layered config: %#v", loaded)
	}
	if loaded.DataPath != "./data" || loaded.WALSync != "always" {
		t.Fatalf("defaults not retained: %#v", loaded)
	}
	if len(loaded.Peers) != 1 || loaded.Peers[0] != "http://four:6333" {
		t.Fatalf("unexpected peers: %#v", loaded.Peers)
	}
}

func TestReplicationFactorValidation(t *testing.T) {
	if Default().ReplicationFactor != 1 {
		t.Fatal("default replication factor must preserve single-primary behavior")
	}
	config := Default()
	config.ReplicationFactor = 0
	if err := config.Validate(); err == nil {
		t.Fatal("expected zero replication factor rejection")
	}
	if _, err := ApplyEnv(Default(), func(name string) (string, bool) {
		if name == "VECTORDB_REPLICATION_FACTOR" {
			return "invalid", true
		}
		return "", false
	}); err == nil {
		t.Fatal("expected invalid replication factor environment rejection")
	}
}

func TestPlacementCapacityValidation(t *testing.T) {
	if Default().PlacementCapacity != 1 {
		t.Fatal("default placement capacity must preserve unweighted placement")
	}
	config := Default()
	config.PlacementCapacity = 257
	if err := config.Validate(); err == nil {
		t.Fatal("expected excessive placement capacity rejection")
	}
	if _, err := ApplyEnv(Default(), func(name string) (string, bool) {
		if name == "VECTORDB_PLACEMENT_CAPACITY" {
			return "invalid", true
		}
		return "", false
	}); err == nil {
		t.Fatal("expected invalid placement capacity environment rejection")
	}
}

func TestStrictConfigAndEnvironmentValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, Default()); err == nil {
		t.Fatal("expected unknown field rejection")
	}
	if _, err := ApplyEnv(Default(), func(name string) (string, bool) {
		if name == "VECTORDB_CHECKPOINT_EVERY" {
			return "invalid", true
		}
		return "", false
	}); err == nil {
		t.Fatal("expected invalid environment rejection")
	}
}

func TestClusterIDValidation(t *testing.T) {
	for _, value := range []string{"bad", "00000000000000000000000000000000", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		config := Default()
		config.ClusterID = value
		if err := config.Validate(); err == nil {
			t.Fatalf("expected invalid cluster ID %q", value)
		}
	}
	config := Default()
	config.ClusterID = "1234567890abcdef1234567890abcdef"
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMutualTLSCARequiresServerIdentity(t *testing.T) {
	config := Default()
	config.TLSCAFile = "/run/tls/ca.crt"
	if err := config.Validate(); err == nil {
		t.Fatal("accepted mutual TLS CA without certificate and key")
	}
	config.TLSCertFile = "/run/tls/tls.crt"
	config.TLSKeyFile = "/run/tls/tls.key"
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}
