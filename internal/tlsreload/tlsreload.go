// Package tlsreload loads TLS identities and trust roots for every new
// handshake so atomically replaced secret files take effect without restart.
package tlsreload

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

type Files struct{ Cert, Key, CA string }

func (f Files) load() (tls.Certificate, *x509.CertPool, error) {
	certificate, err := tls.LoadX509KeyPair(f.Cert, f.Key)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load TLS identity: %w", err)
	}
	data, err := os.ReadFile(f.CA)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load TLS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return tls.Certificate{}, nil, fmt.Errorf("TLS CA bundle contains no certificates")
	}
	return certificate, roots, nil
}

func ServerConfig(files Files) (*tls.Config, error) {
	if _, _, err := files.load(); err != nil {
		return nil, err
	}
	base := &tls.Config{MinVersion: tls.VersionTLS13}
	base.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		certificate, roots, err := files.load()
		if err != nil {
			return nil, err
		}
		return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: roots}, nil
	}
	return base, nil
}

func ClientConfig(files Files) (*tls.Config, error) {
	if _, _, err := files.load(); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // Verification is performed below against reloadable roots.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			certificate, _, err := files.load()
			return &certificate, err
		},
		VerifyConnection: func(state tls.ConnectionState) error {
			_, roots, err := files.load()
			if err != nil {
				return err
			}
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("peer supplied no certificate")
			}
			intermediates := x509.NewCertPool()
			for _, certificate := range state.PeerCertificates[1:] {
				intermediates.AddCert(certificate)
			}
			_, err = state.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: state.ServerName, Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
			return err
		},
	}, nil
}
