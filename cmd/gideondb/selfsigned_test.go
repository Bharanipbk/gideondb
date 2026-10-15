package main

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSelfSignedCertificate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	certPath, keyPath, err := ensureSelfSignedCertificate(dir, "0.0.0.0:6333", "db.example.test:6333", "10.0.0.7:6333")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	for _, name := range []string{"localhost", "db.example.test"} {
		if leaf.VerifyHostname(name) != nil {
			t.Fatalf("certificate does not cover %s: %v", name, leaf.DNSNames)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.7"} {
		if leaf.VerifyHostname(ip) != nil {
			t.Fatalf("certificate does not cover %s: %v", ip, leaf.IPAddresses)
		}
	}
	if info, _ := os.Stat(keyPath); info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v", info.Mode())
	}
	before, _ := os.ReadFile(certPath)
	if _, _, err := ensureSelfSignedCertificate(dir); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(certPath); string(after) != string(before) {
		t.Fatal("a valid certificate must be reused, not regenerated")
	}
	_ = os.WriteFile(certPath, []byte("garbage"), 0o644)
	if _, _, err := ensureSelfSignedCertificate(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("corrupt certificate was not replaced: %v", err)
	}
}
