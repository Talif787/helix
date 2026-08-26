// Command helixctl is a command-line client for a Helix cluster. It dials any node's
// ClientService and issues coordinated reads and writes, so operations go through the full
// quorum. It is a thin wrapper over the internal/rpc client library.
//
// Usage:
//
//	helixctl [flags] put <key> <value>
//	helixctl [flags] get <key>
//	helixctl [flags] delete <key>
//
// Flags:
//
//	-addr        node ClientService address (default 127.0.0.1:7070)
//	-timeout     request timeout (default 5s)
//	-tls-cert    client certificate PEM (for mutual TLS)
//	-tls-key     client key PEM
//	-tls-ca      CA PEM
//
// For a plaintext cluster, omit the TLS flags. For a mutually authenticated cluster, pass all
// three TLS flags with certs from helixcert. A get of a missing key prints "(not found)" and
// exits 0.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"google.golang.org/grpc"

	"github.com/talifpathan/helix/internal/rpc"
	"github.com/talifpathan/helix/internal/tlsutil"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "helixctl:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("helixctl", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:7070", "node ClientService address")
	timeout := fs.Duration("timeout", 5*time.Second, "request timeout")
	certPath := fs.String("tls-cert", "", "client certificate PEM (for mutual TLS)")
	keyPath := fs.String("tls-key", "", "client key PEM (for mutual TLS)")
	caPath := fs.String("tls-ca", "", "CA PEM (for mutual TLS)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: helixctl [flags] <put|get|delete> ...")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("no command: expected put, get, or delete")
	}
	cmd := rest[0]
	switch cmd {
	case "put":
		if len(rest) != 3 {
			return errors.New("usage: helixctl put <key> <value>")
		}
	case "get", "delete":
		if len(rest) != 2 {
			return fmt.Errorf("usage: helixctl %s <key>", cmd)
		}
	default:
		return fmt.Errorf("unknown command %q (want put, get, or delete)", cmd)
	}

	dialOpts, err := dialOptions(*certPath, *keyPath, *caPath)
	if err != nil {
		return err
	}

	client, err := rpc.DialClient(*addr, dialOpts...)
	if err != nil {
		return err
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	switch cmd {
	case "put":
		if err := client.Put(ctx, []byte(rest[1]), []byte(rest[2])); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "OK")
	case "get":
		value, found, err := client.Get(ctx, []byte(rest[1]))
		if err != nil {
			return err
		}
		if !found {
			fmt.Fprintln(stdout, "(not found)")
			return nil
		}
		fmt.Fprintln(stdout, string(value))
	case "delete":
		if err := client.Delete(ctx, []byte(rest[1])); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "OK")
	}
	return nil
}

// dialOptions returns the gRPC dial options for the connection: mutual TLS when all three cert
// paths are given, plaintext when none are, and an error for a partial set.
func dialOptions(certPath, keyPath, caPath string) ([]grpc.DialOption, error) {
	set := 0
	for _, p := range []string{certPath, keyPath, caPath} {
		if p != "" {
			set++
		}
	}
	switch set {
	case 0:
		return nil, nil // plaintext
	case 3:
		cfg, err := tlsutil.ClientConfigFromFiles(certPath, keyPath, caPath)
		if err != nil {
			return nil, err
		}
		return []grpc.DialOption{rpc.ClientTLSOption(cfg)}, nil
	default:
		return nil, errors.New("-tls-cert, -tls-key, and -tls-ca must be given together")
	}
}
