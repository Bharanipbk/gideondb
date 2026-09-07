package tlsreload

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCertificateAndCAReload(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, caPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "ca.crt")
	ca1, ca1Key, ca1PEM := makeCA(t, 1)
	_, certPEM, keyPEM := makeLeaf(t, 2, ca1, ca1Key)
	writePair(t, certPath, keyPath, certPEM, keyPEM)
	write(t, caPath, ca1PEM)
	server, err := ServerConfig(Files{Cert: certPath, Key: keyPath, CA: caPath})
	if err != nil {
		t.Fatal(err)
	}
	first, err := server.GetConfigForClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	firstLeaf, _ := x509.ParseCertificate(first.Certificates[0].Certificate[0])
	_, certPEM, keyPEM = makeLeaf(t, 3, ca1, ca1Key)
	writePair(t, certPath, keyPath, certPEM, keyPEM)
	second, err := server.GetConfigForClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	secondLeaf, _ := x509.ParseCertificate(second.Certificates[0].Certificate[0])
	if firstLeaf.SerialNumber.Cmp(secondLeaf.SerialNumber) == 0 {
		t.Fatal("server certificate was not reloaded")
	}

	client, err := ClientConfig(Files{Cert: certPath, Key: keyPath, CA: caPath})
	if err != nil {
		t.Fatal(err)
	}
	ca2, ca2Key, ca2PEM := makeCA(t, 4)
	leafDER, _, _ := makeLeaf(t, 5, ca2, ca2Key)
	peer, _ := x509.ParseCertificate(leafDER)
	state := tls.ConnectionState{ServerName: "localhost", PeerCertificates: []*x509.Certificate{peer}}
	if client.VerifyConnection(state) == nil {
		t.Fatal("untrusted replacement CA was accepted early")
	}
	write(t, caPath, ca2PEM)
	if err := client.VerifyConnection(state); err != nil {
		t.Fatalf("reloaded CA rejected peer: %v", err)
	}
}

func makeCA(t *testing.T, serial int64) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, _ := x509.ParseCertificate(der)
	return certificate, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func makeLeaf(t *testing.T, serial int64, ca *x509.Certificate, caKey *ecdsa.PrivateKey) ([]byte, []byte, []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	return der, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func writePair(t *testing.T, certPath, keyPath string, cert, key []byte) {
	t.Helper()
	write(t, certPath, cert)
	write(t, keyPath, key)
}
func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
