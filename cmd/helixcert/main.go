// Command helixcert generates the certificates for a Helix cluster: one self-signed CA and
// one certificate per node, each valid for both serving and dialing. It writes PEM files a
// node daemon (a later part) loads to secure its gRPC traffic with mutual TLS.
//
// Example:
//
//	helixcert -dir certs -nodes node-a,node-b,node-c -hosts 127.0.0.1,localhost
//
// produces certs/ca.crt, certs/ca.key, and certs/<node>.crt and certs/<node>.key for each
// node, with the given hosts as subject alternative names.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/talifpathan/helix/internal/tlsutil"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "helixcert:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", "certs", "output directory for the generated PEM files")
	nodesCSV := flag.String("nodes", "", "comma-separated node ids to issue certificates for")
	hostsCSV := flag.String("hosts", "127.0.0.1,localhost", "comma-separated SAN hosts (IPs or DNS names) added to every node cert")
	caName := flag.String("ca-name", "helix-ca", "common name for the CA")
	flag.Parse()

	nodes := splitCSV(*nodesCSV)
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes given; use -nodes node-a,node-b,...")
	}
	hosts := splitCSV(*hostsCSV)

	ca, err := tlsutil.GenerateCA(*caName)
	if err != nil {
		return fmt.Errorf("generate CA: %w", err)
	}
	if err := tlsutil.WriteFile(filepath.Join(*dir, "ca.crt"), ca.CertPEM, 0o644); err != nil {
		return err
	}
	if err := tlsutil.WriteFile(filepath.Join(*dir, "ca.key"), ca.KeyPEM, 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s/ca.crt and %s/ca.key\n", *dir, *dir)

	for _, id := range nodes {
		// Each node cert includes the shared hosts plus the node id itself as a SAN.
		certPEM, keyPEM, err := ca.IssueNodeCert(id, append(append([]string{}, hosts...), id))
		if err != nil {
			return fmt.Errorf("issue cert for %s: %w", id, err)
		}
		if err := tlsutil.WriteFile(filepath.Join(*dir, id+".crt"), certPEM, 0o644); err != nil {
			return err
		}
		if err := tlsutil.WriteFile(filepath.Join(*dir, id+".key"), keyPEM, 0o600); err != nil {
			return err
		}
		fmt.Printf("wrote %s/%s.crt and %s/%s.key\n", *dir, id, *dir, id)
	}
	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
