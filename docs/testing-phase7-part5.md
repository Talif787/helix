# Testing Phase 7 Part 5: mutual TLS for gRPC

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 7
Part 5. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 5 secures the gRPC traffic from the earlier parts with mutual TLS. A self-signed CA
issues one certificate per node, valid for both roles a node plays (serving and dialing), so
every connection is encrypted and both ends present and verify a CA-signed certificate. This
is the first part with real credentials, and it is entirely additive: the server, client,
transport, and messenger already accept gRPC options, so mTLS is a new cert library plus two
glue functions plus a cert tool, with no change to the existing network code. What applies:

- Credentials: yes, now, for the first time. There is a CA (ca.crt, ca.key) and one cert and
  key per node. The `helixcert` tool generates them.
- A cert-generation service/tool: `cmd/helixcert`, a CLI that writes the PEM files.
- A network service and addresses: yes. Nodes serve NodeService over mutually authenticated
  TLS at host:port. In this part the servers start inside the tests on 127.0.0.1.

Different from recent parts, and simpler because of it:

- Code generation: NOT required. No .proto changed in this part, so there is nothing to
  regenerate. `make proto` is unnecessary here.
- New dependencies: none. The cert library is standard-library crypto only; the gRPC TLS
  credentials come from the grpc module already present.

Still not applicable in this part, and it is more useful to say so than to invent it:

- External databases, brokers, caches: none. Nodes embed their own engines.
- A standalone daemon that loads certs from disk and serves continuously: not yet. The cert
  library exposes ServerConfigFromFiles and ClientConfigFromFiles for it, but the node binary
  that uses them is Part 6. Here, servers run inside tests and certs are generated in memory
  (or on disk via helixcert for inspection).
- Cert rotation, hot-reload, or revocation (CRL): out of scope by design.

What Part 5 adds and this runbook verifies: the tree builds with no codegen; the CA issues
node certs that chain to it for both server and client auth; a three-node coordinator runs
entirely over mutually authenticated TLS; a certificate-less client is refused; and the
`helixcert` tool produces a usable CA and per-node cert files.

## 0. One-time shell setup used by every section

```bash
export HELIX_HOME="$HOME/helix"
export HELIX_REPO="https://github.com/Talif787/helix.git"
export HELIX_CERTS="/tmp/helix-certs"
```

---

## 1. Verify the existing environment

```bash
# 1a. Go toolchain. Helix's module is pinned to Go 1.22.
go version || echo "MISSING: Go toolchain"

# 1b. Supporting tools.
git --version || echo "MISSING: git"
gh --version 2>/dev/null || echo "note: GitHub CLI not found (only needed for PRs)"
openssl version 2>/dev/null || echo "note: openssl not found (only used for optional cert inspection)"

# 1c. protoc is NOT needed for this part (no proto changed), but note whether it is present.
protoc --version 2>/dev/null || echo "note: protoc absent (fine; Part 5 needs no codegen)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 7 Part 5 source present?
for f in \
  internal/tlsutil/tlsutil.go \
  internal/tlsutil/tlsutil_test.go \
  internal/rpc/tls.go \
  internal/rpc/tls_network_test.go \
  cmd/helixcert/main.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done
grep -q 'helixcert' "$HELIX_HOME/Makefile" 2>/dev/null \
  && echo "present: helixcert in Makefile build target" || echo "MISSING: helixcert build target"

# 1f. Dependencies present and pinned (from earlier parts; Part 5 adds none).
grep -q 'google.golang.org/grpc' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "present: grpc in go.mod" || echo "MISSING: grpc dep (restore per 2e)"
head -3 "$HELIX_HOME/go.mod" 2>/dev/null | grep -q 'go 1.22' \
  && echo "go.mod pinned to 1.22" || echo "note: check the go directive"
```

Interpretation: below go1.22 -> 2a; 1d MISSING -> 2b; 1e MISSING while 1d FOUND -> 2d; 1f
showing grpc MISSING -> 2e. protoc is not needed this part.

---

## 2. Install or initialize anything missing

Do only the sub-steps flagged by section 1.

### 2a. Install Go 1.22+ (only if 1a is missing or too old)

```bash
GO_VERSION=1.22.6
cd "$HOME"
curl -fsSLO "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
rm -rf "$HOME/go-sdk" && mkdir -p "$HOME/go-sdk"
tar -C "$HOME/go-sdk" -xzf "go${GO_VERSION}.linux-amd64.tar.gz"
export PATH="$HOME/go-sdk/go/bin:$PATH"
echo 'export PATH="$HOME/go-sdk/go/bin:$PATH"' >> "$HOME/.bashrc"
go version
```

### 2b. Clone the repository (only if 1d is missing)

```bash
cd "$HOME"
git clone "$HELIX_REPO" helix
cd "$HELIX_HOME"
git status
```

### 2c. protoc (not needed for Part 5)

Skip. No contract changed in this part, so there is nothing to generate. protoc is only
needed for parts that touch a .proto.

### 2d. Update an existing checkout (only if 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only           # if Phase 7 Part 5 is merged
#   or: git switch phase-7/mtls && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2e. Restore dependencies if go.mod lost them (only if 1f showed grpc missing)

```bash
cd "$HELIX_HOME"
git checkout -- go.mod go.sum
grep 'google.golang.org/grpc ' go.mod   # should now show grpc
head -3 go.mod                           # should show: go 1.22
```

---

## 3. Configure the required environment variables and services

There are no HELIX_* environment variables on this path; TLS is configured in code through
tls.Config, and the tests generate certs in memory. The one real new concept is the
credential set, which the `helixcert` tool materializes on disk.

| Input | Meaning | Value |
| --- | --- | --- |
| node address (URL) | the gRPC dial target for a node | `127.0.0.1:<port>` (tests use OS-assigned ports) |
| CA files | trust root for the cluster | `$HELIX_CERTS/ca.crt`, `$HELIX_CERTS/ca.key` |
| node cert / key | a node's identity for serving and dialing | `$HELIX_CERTS/<node>.crt`, `$HELIX_CERTS/<node>.key` |

Dummy values the tests and tool use:

| Input | Meaning | Dummy value |
| --- | --- | --- |
| CA common name | the CA's subject | `helix-test-ca` (tests), `helix-ca` (tool default) |
| node ids | cluster members | `node-a`, `node-b`, `node-c` |
| client id | the coordinator's outbound cert | `client` |
| SAN hosts | names/IPs in each cert | `127.0.0.1`, `localhost` (plus the node id) |
| sample key / value | the write over mTLS | `account:42` = `balance-100` |

Certs are credentials: the tool writes keys with 0600 and certs with 0644. There is no
database or broker to configure.

---

## 4. Start the backend and supporting services

There is no codegen and nothing to regenerate this part. Confirm the tree builds under the CI
toolchain, then optionally generate a real cert set with the tool.

```bash
cd "$HELIX_HOME"

# 1) Build everything, including the new cert tool, under the Go 1.22 constraint CI uses.
GOTOOLCHAIN=local go build ./...

# 2) Build the binaries so the cert tool is available.
make build
ls -l bin/helixcert   # expect the compiled tool

# 3) Generate a real cert set to inspect (optional; the tests generate their own in memory).
rm -rf "$HELIX_CERTS"
./bin/helixcert -dir "$HELIX_CERTS" -nodes node-a,node-b,node-c -hosts 127.0.0.1,localhost
ls -l "$HELIX_CERTS"
#    expect: ca.crt ca.key node-a.crt node-a.key node-b.crt node-b.key node-c.crt node-c.key
```

There is no long-running daemon in this part; the mTLS servers start and stop inside the
tests. The node binary that loads these cert files and serves continuously is Part 6.

---

## 5. Verify service and backend health

Health means the module builds under Go 1.22, static analysis is clean, the cert library and
mTLS tests pass, and the tool produces verifiable certs.

```bash
cd "$HELIX_HOME"
make fmt
make vet                                    # expect no output, zero exit
GOTOOLCHAIN=local go build ./...            # expect no output
go test ./internal/tlsutil/ ./internal/rpc/ # expect ok for both
echo "exit code: $?"                        # expect 0
```

If you generated certs in section 4 and have openssl, confirm a node cert really chains to
the CA and carries both usages (optional, illustrative):

```bash
openssl verify -CAfile "$HELIX_CERTS/ca.crt" "$HELIX_CERTS/node-a.crt"   # expect: node-a.crt: OK
openssl x509 -in "$HELIX_CERTS/node-a.crt" -noout -ext extendedKeyUsage,subjectAltName
#    expect TLS Web Server Authentication and TLS Web Client Authentication, plus the SANs
```

---

## 6. Run Phase 7 Part 5 (automated tests)

The mTLS coordinator test dials and serves concurrently, so run under the race detector.

```bash
cd "$HELIX_HOME"

# 6a. The two affected packages, verbosely, with the race detector.
go test -race -v ./internal/tlsutil/ ./internal/rpc/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends with
`check passed`. Section 6a should include the Part 5 additions alongside the earlier gRPC and
SWIM tests:

```
--- PASS: TestCAIssuesVerifiableNodeCert
--- PASS: TestLoadCARoundTrip
--- PASS: TestServerAndClientConfigsBuild
--- PASS: TestMTLSCoordinatorRoundTrip
--- PASS: TestMTLSRejectsPlaintextClient
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: build and cert tool (the prerequisite)

Covered in section 4. Success is `GOTOOLCHAIN=local go build ./...` clean, `bin/helixcert`
built, and the tool writing ca.* plus per-node .crt/.key files. This proves the cert library
and tool compile and produce output.

### Scenario B: the CA issues verifiable node certs (no network)

Confirms a node cert chains to the CA for both server and client auth, and the CN is set.

```bash
cd "$HELIX_HOME"
go test -race -v -run 'TestCAIssuesVerifiableNodeCert|TestLoadCARoundTrip|TestServerAndClientConfigsBuild' ./internal/tlsutil/
```

### Scenario C: coordinator over mutually authenticated TLS (the headline)

Dummy data (built in): CA `helix-test-ca`; nodes `node-a`/`node-b`/`node-c` on 127.0.0.1;
a `client` cert for the coordinator's outbound connections; N=3, R=2, W=2; key
`account:42`=`balance-100`. Every operation crosses an encrypted, client-verified connection.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestMTLSCoordinatorRoundTrip ./internal/rpc/
```

### Scenario D: the server refuses a certificate-less client

Confirms mutual auth is enforced: a plaintext (insecure) client dialing the mTLS server is
rejected on use.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestMTLSRejectsPlaintextClient ./internal/rpc/
```

### Scenario E: parameterized mTLS round-trip with your own dummy values

This lets you set your own CA name, node ids, and key-value data, and watch a coordinator
drive them over mutual TLS on real localhost sockets. It drops a temporary test into the rpc
package, runs it, and is removed afterward.

Create the test (edit the marked block to your own dummy data):

```bash
cd "$HELIX_HOME"
cat > internal/rpc/manual_scenario_test.go <<'HELIX_EOF'
package rpc

import (
	"context"
	"net"
	"testing"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/storage"
	"github.com/talifpathan/helix/internal/tlsutil"
)

func TestMTLSManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	caName := "my-helix-ca"
	ids := []string{"node-1", "node-2", "node-3"}
	n, r, w := 3, 2, 2
	key := []byte("user:1001")
	val := []byte("alice")
	// ---------------------------------------------------------

	ca, err := tlsutil.GenerateCA(caName)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	peers := NewPeerRegistry()
	ring := cluster.NewRing(128)
	var cleanups []func()
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()

	for _, id := range ids {
		certPEM, keyPEM, err := ca.IssueNodeCert(id, []string{"127.0.0.1", "localhost", id})
		if err != nil {
			t.Fatalf("issue %s: %v", id, err)
		}
		srvCfg, err := tlsutil.ServerConfig(certPEM, keyPEM, ca.CertPEM)
		if err != nil {
			t.Fatalf("server config %s: %v", id, err)
		}
		eng, err := storage.Open(storage.Options{DataDir: t.TempDir()})
		if err != nil {
			t.Fatalf("open %s: %v", id, err)
		}
		node := cluster.NewLocalNode(id, eng)
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen %s: %v", id, err)
		}
		srv := NewGRPCServer(node, ServerTLSOption(srvCfg))
		go func() { _ = srv.Serve(lis) }()
		cleanups = append(cleanups, func() { srv.GracefulStop(); _ = node.Close() })
		peers.Set(id, lis.Addr().String())
		ring.Add(id)
		t.Logf("%s serving over mTLS at %s", id, lis.Addr().String())
	}

	clientCertPEM, clientKeyPEM, err := ca.IssueNodeCert("client", []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("client cert: %v", err)
	}
	cliCfg, err := tlsutil.ClientConfig(clientCertPEM, clientKeyPEM, ca.CertPEM)
	if err != nil {
		t.Fatalf("client config: %v", err)
	}

	tr := NewGRPCTransport(peers, ClientTLSOption(cliCfg))
	defer tr.Close()
	coord := cluster.NewCoordinator(ring, tr, n, r, w, 0, nil, nil)
	ctx := context.Background()

	if err := coord.Put(ctx, key, val); err != nil {
		t.Fatalf("put over mTLS: %v", err)
	}
	got, err := coord.Get(ctx, key)
	if err != nil || string(got) != string(val) {
		t.Fatalf("get over mTLS: got %q err %v", got, err)
	}
	t.Logf("read back %q over mutually authenticated TLS", got)
}
HELIX_EOF
echo "created internal/rpc/manual_scenario_test.go"
```

Run it (the `-v` flag shows the serving addresses and the read-back value):

```bash
cd "$HELIX_HOME"
go test -race -v -run TestMTLSManualScenario ./internal/rpc/
```

Clean up when finished (the temp test, and the on-disk certs if you generated them):

```bash
cd "$HELIX_HOME"
rm -f internal/rpc/manual_scenario_test.go
rm -rf "$HELIX_CERTS"
echo "removed manual scenario test and generated certs"
```

---

## 8. Verify the expected results

### Scenario A (build and cert tool)

- `GOTOOLCHAIN=local go build ./...` succeeds; `bin/helixcert` exists; the tool writes
  ca.crt, ca.key, and a .crt/.key pair per node into `$HELIX_CERTS`.

### Scenario B (CA and configs)

- PASS: a node cert verifies against the CA for both ExtKeyUsageServerAuth and
  ExtKeyUsageClientAuth, its CN matches the node id, a reloaded CA can still issue certs, and
  ServerConfig/ClientConfig build (and reject a bad CA PEM).

### Scenario C (coordinator over mTLS)

- PASS: `account:42` reads back `balance-100` with every operation over an encrypted,
  mutually authenticated connection, and `-race` is clean.

### Scenario D (plaintext rejected)

- PASS: a plaintext client's GetVersioned fails against the mTLS server, proving the server
  requires and verifies a client certificate rather than accepting anonymous connections.

### Scenario E (parameterized)

- The test PASS line, plus `-v` log lines: each node's serving address and the value read
  back over mutual TLS.

### Cert tool output (optional openssl)

- `openssl verify -CAfile ca.crt node-a.crt` prints `node-a.crt: OK`; the cert's extended key
  usage lists both server and client authentication, and its SANs include 127.0.0.1,
  localhost, and node-a.

### Automated suite

- Section 6 shows the three tlsutil tests and both mTLS tests PASS alongside the earlier gRPC
  and SWIM tests, every package `ok`, and `make check` exiting 0 with no race warnings.

---

## 9. Troubleshooting

Build (no codegen this part)
- `go build` fails with an x509 or tls symbol undefined: your Go is older than 1.22. Confirm
  `go version`; the crypto APIs used here are long stable, so this points at a toolchain
  problem, not the code.
- `undefined: credentials.NewTLS` or `grpc.Creds`: the grpc module is missing from go.mod.
  Restore with `git checkout -- go.mod go.sum` (do not run `go mod tidy` to chase it), then
  `grep 'google.golang.org/grpc ' go.mod`.
- `go: go.mod requires go >= 1.25`: unrelated to Part 5 (no deps changed here), but if a
  previous part's tidy bumped it, hold at 1.22: `go mod edit -go=1.22` then
  `GOTOOLCHAIN=local go build ./...`.

Certificates and mTLS
- `TestMTLSCoordinatorRoundTrip` fails with a certificate/hostname verification error: the
  server cert lacks a SAN for the dialed address. The tests dial 127.0.0.1, and startTLSNode
  issues certs with 127.0.0.1 and localhost SANs, so this should not happen; if you changed
  the dial address, add it to the cert's hosts in IssueNodeCert.
- `x509: certificate signed by unknown authority`: the client or server config was built with
  a different CA than issued the peer cert. In a real deployment, every node and client must
  trust the same ca.crt; regenerate the whole set with one `helixcert` run.
- `TestMTLSRejectsPlaintextClient` unexpectedly passes the RPC (no error): the server is not
  actually requiring a client cert. Confirm ServerConfig sets ClientAuth to
  RequireAndVerifyClientCert; anything weaker allows anonymous clients.
- Handshake fails with a TLS version error: both ends pin TLS 1.3 (MinVersion VersionTLS13);
  this only surfaces if you edited the configs to allow older versions against a 1.3-only peer.
- `openssl verify` reports an error but the Go tests pass: openssl and Go apply slightly
  different defaults; trust the Go tests, which use the exact verification path the cluster
  uses. The openssl check is illustrative only.

Cert tool
- `helixcert: no nodes given`: pass `-nodes node-a,node-b,...`; it is required.
- Permission denied writing certs: choose a writable `-dir` (the runbook uses `/tmp`); keys
  are written 0600, so re-running over an existing dir is fine.
- The generated key files look readable to others: they are mode 0600 by design; if a later
  tool complains a key is too open, confirm the perms survived a copy.

Deferred features (not bugs)
- Looking for a running secured server to connect to from outside a test: none yet. The mTLS
  servers run inside tests here; the daemon that loads ca.crt and node certs from disk and
  serves continuously is Part 6 (the library already exposes ServerConfigFromFiles and
  ClientConfigFromFiles for it).
- Looking for cert rotation or revocation: out of scope for this part.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above
  rather than typing. If the prompt looks corrupted after a large paste, run `reset` or open
  a new Cloud Shell tab.
