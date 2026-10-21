package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCheckLite(t *testing.T) {
	cases := []struct {
		name, http, grpc, key, principals string
		peers                             []string
		ok                                bool
	}{
		{name: "defaults", http: "127.0.0.1:6333", ok: true},
		{name: "localhost with grpc", http: "localhost:6333", grpc: "127.0.0.1:6334", ok: true},
		{name: "public http", http: "0.0.0.0:6333"},
		{name: "public grpc", http: "127.0.0.1:6333", grpc: "0.0.0.0:6334"},
		{name: "peers", http: "127.0.0.1:6333", peers: []string{"http://127.0.0.1:7333"}},
		{name: "api key", http: "127.0.0.1:6333", key: "0123456789abcdef"},
		{name: "principals", http: "127.0.0.1:6333", principals: "p.json"},
	}
	for _, c := range cases {
		err := checkLite(c.http, c.grpc, c.peers, c.key, c.principals)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestLiteBannerAndDataPath(t *testing.T) {
	var out bytes.Buffer
	printLiteBanner(&out, "dev", "127.0.0.1:6333", "", "/tmp/x", false)
	for _, want := range []string{"lite mode", "http://127.0.0.1:6333/v1", "Dashboard  off", "/tmp/x"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("banner missing %q:\n%s", want, out.String())
		}
	}
	if path := liteDefaultDataPath(); !strings.Contains(path, "lite") {
		t.Errorf("liteDefaultDataPath = %q", path)
	}
}
