// Package tlsutil generates and loads the certificates that secure Helix's gRPC traffic.
// A single self-signed CA issues one certificate per node that is valid for both roles a
// node plays (it serves the node and membership services, and it dials its peers), so every
// connection is mutually authenticated: each side presents a CA-signed certificate and
// verifies the other against the shared CA. The package deals only in PEM bytes and
// crypto/tls configs; turning a config into gRPC credentials lives in the rpc package, so
// tlsutil stays free of any gRPC dependency.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const certValidity = 10 * 365 * 24 * time.Hour

// CA is a self-signed certificate authority that issues node certificates.
type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	KeyPEM  []byte
	key     *ecdsa.PrivateKey
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func marshalKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// GenerateCA creates a new self-signed CA with the given common name.
func GenerateCA(commonName string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	keyPEM, err := marshalKey(key)
	if err != nil {
		return nil, err
	}
	return &CA{
		Cert:    cert,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  keyPEM,
		key:     key,
	}, nil
}

// LoadCA reconstructs a CA from its certificate and key PEM, for issuing more node certs.
func LoadCA(certPEM, keyPEM []byte) (*CA, error) {
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, err
	}
	key, ok := tlsCert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("tlsutil: CA key is not an ECDSA key")
	}
	return &CA{Cert: cert, CertPEM: certPEM, KeyPEM: keyPEM, key: key}, nil
}

// IssueNodeCert signs a certificate for nodeID valid for both server and client auth. hosts
// are the DNS names and IP addresses the node is reached at; a client verifies the address
// it dialed against these, so include every hostname or IP a peer may use (for example
// "127.0.0.1", "localhost", and the node's real hostname).
func (ca *CA) IssueNodeCert(nodeID string, hosts []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = marshalKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM, nil
}

func caPool(caPEM []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("tlsutil: no certificates found in CA PEM")
	}
	return pool, nil
}

// ServerConfig builds a TLS config for a node's gRPC server: it presents the node's
// certificate and requires and verifies a CA-signed client certificate on every connection.
func ServerConfig(certPEM, keyPEM, caPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	pool, err := caPool(caPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ClientConfig builds a TLS config for dialing peers: it presents the node's certificate and
// verifies the server against the CA. ServerName is left empty so gRPC verifies the address
// actually dialed against the peer certificate's SANs.
func ClientConfig(certPEM, keyPEM, caPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	pool, err := caPool(caPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func readFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: read %s: %w", path, err)
	}
	return b, nil
}

// ServerConfigFromFiles loads cert, key, and CA PEM from disk and builds a server config.
func ServerConfigFromFiles(certFile, keyFile, caFile string) (*tls.Config, error) {
	c, err := readFile(certFile)
	if err != nil {
		return nil, err
	}
	k, err := readFile(keyFile)
	if err != nil {
		return nil, err
	}
	ca, err := readFile(caFile)
	if err != nil {
		return nil, err
	}
	return ServerConfig(c, k, ca)
}

// ClientConfigFromFiles loads cert, key, and CA PEM from disk and builds a client config.
func ClientConfigFromFiles(certFile, keyFile, caFile string) (*tls.Config, error) {
	c, err := readFile(certFile)
	if err != nil {
		return nil, err
	}
	k, err := readFile(keyFile)
	if err != nil {
		return nil, err
	}
	ca, err := readFile(caFile)
	if err != nil {
		return nil, err
	}
	return ClientConfig(c, k, ca)
}

// WriteFile writes PEM (or any) bytes to path with restrictive permissions, creating parent
// directories as needed. Used by the cert tool.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}
