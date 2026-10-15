package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	selfSignedCertName = "self-signed.crt"
	selfSignedKeyName  = "self-signed.key"
	selfSignedLifetime = 825 * 24 * time.Hour
	// selfSignedRenewBefore regenerates a certificate this close to expiry.
	selfSignedRenewBefore = 30 * 24 * time.Hour
)

// ensureSelfSignedCertificate returns a certificate and key in dir, creating
// them when missing, unreadable or near expiry. It exists so a container can
// serve HTTPS on its first start; production deployments supply real
// certificates with -tls-cert-file and -tls-key-file.
func ensureSelfSignedCertificate(dir string, addresses ...string) (string, string, error) {
	certPath, keyPath := filepath.Join(dir, selfSignedCertName), filepath.Join(dir, selfSignedKeyName)
	if pair, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, parseErr := x509.ParseCertificate(pair.Certificate[0]); parseErr == nil && time.Until(leaf.NotAfter) > selfSignedRenewBefore {
			return certPath, keyPath, nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("create TLS directory: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", "", err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "gideondb self-signed", Organization: []string{"GideonDB"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(selfSignedLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		template.DNSNames = append(template.DNSNames, hostname)
	}
	for _, address := range addresses {
		host, _, err := net.SplitHostPort(address)
		if err != nil || host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			if !ip.IsUnspecified() {
				template.IPAddresses = append(template.IPAddresses, ip)
			}
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	if err := writeFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return "", "", err
	}
	if err := writeFileAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
