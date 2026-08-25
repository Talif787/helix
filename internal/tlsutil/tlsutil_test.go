package tlsutil

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestCAIssuesVerifiableNodeCert(t *testing.T) {
	ca, err := GenerateCA("helix-test-ca")
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	if !ca.Cert.IsCA {
		t.Fatal("CA cert should have IsCA set")
	}

	certPEM, keyPEM, err := ca.IssueNodeCert("node-a", []string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("issue node cert: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatal("expected non-empty cert and key PEM")
	}

	// The node cert must chain to the CA and be usable for both server and client auth.
	pool, err := caPool(ca.CertPEM)
	if err != nil {
		t.Fatalf("ca pool: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode node cert PEM")
	}
	nodeCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse node cert: %v", err)
	}
	if _, err := nodeCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("node cert should verify for server auth: %v", err)
	}
	if _, err := nodeCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("node cert should verify for client auth: %v", err)
	}
	if nodeCert.Subject.CommonName != "node-a" {
		t.Fatalf("node cert CN should be node-a, got %q", nodeCert.Subject.CommonName)
	}
}

func TestLoadCARoundTrip(t *testing.T) {
	ca, err := GenerateCA("helix-test-ca")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	loaded, err := LoadCA(ca.CertPEM, ca.KeyPEM)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// A loaded CA can still issue a working cert.
	if _, _, err := loaded.IssueNodeCert("node-b", []string{"127.0.0.1"}); err != nil {
		t.Fatalf("loaded CA should issue certs: %v", err)
	}
}

func TestServerAndClientConfigsBuild(t *testing.T) {
	ca, _ := GenerateCA("helix-test-ca")
	certPEM, keyPEM, _ := ca.IssueNodeCert("node-a", []string{"127.0.0.1"})
	if _, err := ServerConfig(certPEM, keyPEM, ca.CertPEM); err != nil {
		t.Fatalf("server config: %v", err)
	}
	if _, err := ClientConfig(certPEM, keyPEM, ca.CertPEM); err != nil {
		t.Fatalf("client config: %v", err)
	}
	if _, err := ServerConfig(certPEM, keyPEM, []byte("not a cert")); err == nil {
		t.Fatal("server config should reject a bad CA PEM")
	}
}
