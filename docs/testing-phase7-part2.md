# Testing Phase 7 Part 2: gRPC server, client, and networked transport

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 7
Part 2. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 2 puts Helix nodes on real sockets. A coordinator on one machine can now reach replicas
on others over gRPC, using the same Replica and Transport seams the in-process transport
used. This is the first part with genuine external dependencies, a code-generation step, and
network serving, so several items that did not apply in earlier phases now do:

- External dependencies: yes, now. The generated protobuf code imports google.golang.org/grpc
  and google.golang.org/protobuf. These are added to go.mod by `go mod tidy` after codegen.
- Code generation: required. The gRPC server, client, and transport import a generated
  package (internal/rpc/helixv1) that only exists after you run `make proto`.
- A network service and a URL: yes, but scoped. The gRPC server binds a real TCP listener and
  is reached at a host:port address. In this part it is started inside the round-trip test on
  127.0.0.1 with an OS-assigned port, not as a standalone daemon. The daemon that serves
  continuously is Part 5.

Still not applicable in this part, and it is more useful to say so than to invent it:

- Credentials and TLS: none yet. Connections are plaintext (insecure). mTLS is Part 4.
- External databases, brokers, caches: none. Each node is its own embedded engine.
- Anti-entropy and SWIM over the wire: not yet. The client's MerkleTree and BucketEntries
  return Unimplemented on purpose; those RPCs arrive in Part 3. The data path (get, put,
  hint) is fully networked here.

What Part 2 adds and this runbook verifies: the proto contract generates and compiles; the
type conversions round-trip losslessly; and a real three-node coordinator drives put, get,
overwrite, not-found, and delete entirely over gRPC on localhost.

## 0. One-time shell setup used by every section

```bash
export HELIX_HOME="$HOME/helix"
export HELIX_REPO="https://github.com/Talif787/helix.git"
```

---

## 1. Verify the existing environment

```bash
# 1a. Go toolchain. Helix needs Go 1.22 or newer.
go version || echo "MISSING: Go toolchain"

# 1b. Supporting tools.
git --version || echo "MISSING: git"
gh --version 2>/dev/null || echo "note: GitHub CLI not found (only needed for PRs)"

# 1c. protoc, the protocol buffer compiler (required for this part's codegen).
protoc --version 2>/dev/null || echo "MISSING: protoc (install in 2c)"

# 1d. The protoc Go plugins (required for codegen).
(which protoc-gen-go && which protoc-gen-go-grpc) >/dev/null 2>&1 \
  && echo "present: protoc Go plugins" || echo "MISSING: protoc-gen-go / protoc-gen-go-grpc (install in 2c)"

# 1e. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1f. Is the Phase 7 Part 2 source present?
for f in \
  internal/rpc/server.go \
  internal/rpc/client.go \
  internal/rpc/transport.go \
  internal/rpc/convert.go \
  internal/rpc/network_test.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done

# 1g. Is generated code already present, and are the deps wired into go.mod?
if [ -f "$HELIX_HOME/internal/rpc/helixv1/node.pb.go" ]; then
  echo "present: generated code (make proto has been run and committed)"
else
  echo "MISSING: generated code (run make proto in 4)"
fi
grep -q 'google.golang.org/grpc' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "present: grpc in go.mod" || echo "MISSING: grpc dep (run go mod tidy in 4)"
```

Interpretation: below go1.22 -> 2a; 1c or 1d MISSING -> 2c; 1e MISSING -> 2b; 1f MISSING while
1e FOUND -> 2d; 1g MISSING is expected on a fresh checkout and is resolved in section 4.

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

### 2b. Clone the repository (only if 1e is missing)

```bash
cd "$HOME"
git clone "$HELIX_REPO" helix
cd "$HELIX_HOME"
git status
```

### 2c. Install protoc and the Go plugins (required for this part)

```bash
# The compiler. Cloud Shell is Debian-based.
sudo apt-get update && sudo apt-get install -y protobuf-compiler
protoc --version   # expect libprotoc 3.x or newer

# The Go plugins install into $(go env GOPATH)/bin, which must be on PATH so protoc finds them.
cd "$HELIX_HOME"
make proto-tools
export PATH="$PATH:$(go env GOPATH)/bin"
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> "$HOME/.bashrc"
which protoc-gen-go protoc-gen-go-grpc   # both must resolve
```

### 2d. Update an existing checkout (only if 1f found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only          # if Phase 7 Part 2 is merged
#   or: git switch phase-7/grpc-transport && git pull --ff-only   # if still on its branch
git log --oneline -3
```

---

## 3. Configure the required environment variables and services

There are no HELIX_* environment variables on this path; the coordinator, transport, and
peer registry are configured in code. Two shell PATH entries matter for codegen, and one
address concept is now real.

| Variable / input | Why | Value |
| --- | --- | --- |
| PATH includes Go bin | run `go` and installed plugins | `$HOME/go-sdk/go/bin` (if you installed Go in 2a) |
| PATH includes GOPATH bin | protoc must find the plugins | `$(go env GOPATH)/bin` |
| node address (URL) | the gRPC dial target for a node | `127.0.0.1:<port>` (the test uses an OS-assigned port) |

Dummy values the round-trip uses (all built into the test):

| Input | Meaning | Dummy value |
| --- | --- | --- |
| node ids | the three replicas | `node-a`, `node-b`, `node-c` |
| addresses | dial targets | `127.0.0.1:0` at listen time, resolved to real ports |
| N / R / W | replication and quorums | 3 / 2 / 2 |
| sample key / value | the write exercised over gRPC | `account:42` = `balance-100`, then `balance-250` |
| missing key | not-found probe | `nope` |

There is no supporting service (database, broker) to configure. Each node embeds its own
engine in a temp directory the test creates.

---

## 4. Start the backend and supporting services

The runnable artifact in Part 2 is the gRPC round-trip test, which starts real gRPC servers
on localhost, drives a coordinator across them, and shuts them down. Before it can build or
run, you must generate the protobuf code and pull in the dependencies. This is the required
one-time setup for the part.

```bash
cd "$HELIX_HOME"
export PATH="$PATH:$(go env GOPATH)/bin"   # ensure the plugins are findable

# 1) Generate Go from the contract (creates internal/rpc/helixv1/*.pb.go).
make proto
ls -l internal/rpc/helixv1/   # expect node.pb.go and node_grpc.pb.go

# 2) Add the runtime dependencies the generated and hand-written gRPC code import.
go mod tidy
grep -E 'google.golang.org/(grpc|protobuf)' go.mod   # both should now appear

# 3) Build everything, including the new internal/rpc package.
go build ./...
```

There is no long-running daemon to launch in this part; the gRPC server is started and
stopped inside the test. The continuously serving node binary is Part 5.

---

## 5. Verify service and backend health

Health means codegen produced the package, the module builds, static analysis is clean, and
the round-trip test passes (which is the closest thing to a live health check in this part,
since it actually serves and dials gRPC).

```bash
cd "$HELIX_HOME"
make fmt
make vet                              # expect no output, zero exit
go build ./...                        # expect no output
go test ./internal/rpc/               # expect: ok  github.com/talifpathan/helix/internal/rpc
echo "exit code: $?"                  # expect 0
```

If you want to confirm a socket is really opened and served, run just the round-trip test
verbosely; it stands up three listeners and tears them down:

```bash
go test -v -run TestGRPCCoordinatorRoundTrip ./internal/rpc/
```

---

## 6. Run Phase 7 Part 2 (automated tests)

The transport dials and caches clients concurrently and the coordinator fans out, so run
under the race detector.

```bash
cd "$HELIX_HOME"

# 6a. Just the rpc package, verbosely, with the race detector.
go test -race -v ./internal/rpc/

# 6b. The whole suite with the race detector, to confirm nothing regressed.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: the rpc tests print `--- PASS`, every package prints `ok`, and `make check` ends
with `check passed`. Section 6a should include:

```
--- PASS: TestPeerRegistrySetAddressRemove
--- PASS: TestPeerRegistryNodesSorted
--- PASS: TestPeerRegistrySnapshotIsCopy
--- PASS: TestPeerRegistryConcurrentAccess
--- PASS: TestVersionedValueProtoRoundTrip
--- PASS: TestGRPCCoordinatorRoundTrip
--- PASS: TestGRPCTransportUnknownNode
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: codegen and build (the prerequisite)

Covered in section 4. Success is `internal/rpc/helixv1/node.pb.go` and `node_grpc.pb.go`
existing, `go.mod` listing grpc and protobuf, and `go build ./...` succeeding. This proves
the contract compiles against the runtime libraries.

### Scenario B: type conversion round-trip (no network)

Dummy data (built into the test): a normal value with a two-entry vector clock, a tombstone,
and an empty-clock value. Confirms value, clock, timestamp, and tombstone survive the trip.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestVersionedValueProtoRoundTrip ./internal/rpc/
```

### Scenario C: three-node coordinator over gRPC (the headline)

Dummy data (built into the test): nodes `node-a`/`node-b`/`node-c` on 127.0.0.1 with
OS-assigned ports; N=3, R=2, W=2; key `account:42` written as `balance-100` then overwritten
to `balance-250`; missing key `nope`. Every replica put and get crosses a socket.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestGRPCCoordinatorRoundTrip ./internal/rpc/
```

### Scenario D: transport miss for an unknown node

Confirms the transport reports a miss for an unregistered id rather than dialing a bogus
address.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestGRPCTransportUnknownNode ./internal/rpc/
```

### Scenario E: parameterized gRPC round-trip with your own dummy values

This lets you supply your own node ids, quorum settings, and key-value data, and watch a
coordinator drive them over real localhost sockets. It drops a temporary test into the
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
)

func TestGRPCManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	ids := []string{"node-1", "node-2", "node-3"}
	n, r, w := 3, 2, 2
	key := []byte("user:1001")
	val := []byte("alice")
	overwrite := []byte("alice-v2")
	// ---------------------------------------------------------

	peers := NewPeerRegistry()
	ring := cluster.NewRing(128)
	var cleanups []func()
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()

	for _, id := range ids {
		eng, err := storage.Open(storage.Options{DataDir: t.TempDir()})
		if err != nil {
			t.Fatalf("open %s: %v", id, err)
		}
		node := cluster.NewLocalNode(id, eng)
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen %s: %v", id, err)
		}
		srv := NewGRPCServer(node)
		go func() { _ = srv.Serve(lis) }()
		cleanups = append(cleanups, func() { srv.GracefulStop(); _ = node.Close() })
		peers.Set(id, lis.Addr().String())
		ring.Add(id)
		t.Logf("%s serving at %s", id, lis.Addr().String())
	}

	tr := NewGRPCTransport(peers)
	defer tr.Close()
	coord := cluster.NewCoordinator(ring, tr, n, r, w, 0, nil, nil)
	ctx := context.Background()

	if err := coord.Put(ctx, key, val); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := coord.Get(ctx, key)
	if err != nil || string(got) != string(val) {
		t.Fatalf("get: got %q err %v", got, err)
	}
	t.Logf("read back %q over gRPC", got)

	if err := coord.Put(ctx, key, overwrite); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, err = coord.Get(ctx, key)
	if err != nil || string(got) != string(overwrite) {
		t.Fatalf("get after overwrite: got %q err %v", got, err)
	}
	t.Logf("after overwrite, read %q; preference list is %v", got, ring.LookupN(key, n))
}
HELIX_EOF
echo "created internal/rpc/manual_scenario_test.go"
```

Run it (the `-v` flag shows the serving addresses and read-back values):

```bash
cd "$HELIX_HOME"
go test -race -v -run TestGRPCManualScenario ./internal/rpc/
```

Clean up when finished (only the temp test; the engines use test temp dirs that Go removes):

```bash
cd "$HELIX_HOME"
rm -f internal/rpc/manual_scenario_test.go
echo "removed manual scenario test"
```

---

## 8. Verify the expected results

### Scenario A (codegen and build)

- `internal/rpc/helixv1/` contains `node.pb.go` and `node_grpc.pb.go`.
- `go.mod` lists `google.golang.org/grpc` and `google.golang.org/protobuf`.
- `go build ./...` succeeds with no output.

### Scenario B (conversion round-trip)

- PASS: for each case the value bytes, timestamp, deleted flag, and every clock entry match
  after cluster to proto to cluster, and the clocks compare Equal.

### Scenario C (coordinator over gRPC)

- PASS: `account:42` reads back `balance-100`, then `balance-250` after overwrite (proving
  reconciliation carried the newer value across the wire); `nope` returns not found; after a
  delete, `account:42` returns not found. Every operation crossed a socket, and `-race` is
  clean.

### Scenario D (unknown node)

- PASS: the transport returns ok=false for an unregistered id and does not attempt a dial.

### Scenario E (parameterized)

- The test PASS line, plus `-v` log lines: each node's serving address, the value read back,
  and the value after overwrite with the key's preference list.

### Automated suite

- Section 6 shows all seven rpc tests PASS, every package `ok`, and `make check` exiting 0
  with no race detector warnings.

---

## 9. Troubleshooting

Codegen and dependencies
- `protoc: command not found`: run 2c.
- `protoc-gen-go: program not found` or `--go_out: protoc-gen-go: plugin failed`: the plugins
  are not on PATH. Run `export PATH="$PATH:$(go env GOPATH)/bin"` and retry `make proto`.
- `go build` fails with `cannot find package .../internal/rpc/helixv1`: you have not run
  `make proto` yet, or it wrote elsewhere. Regenerate and confirm the files exist under
  `internal/rpc/helixv1/`.
- `missing go.sum entry for google.golang.org/grpc`: run `go mod tidy` (section 4 step 2). It
  needs outbound network, which Cloud Shell has by default.
- `undefined: grpc.NewClient`: your grpc version predates the current client constructor. Run
  `go get google.golang.org/grpc@latest && go mod tidy` to move to a version that has it.

Build and interface errors
- `*NodeClient does not implement cluster.Replica`: a method signature drifted from the
  interface. Confirm the five methods (GetVersioned, PutVersioned, PutHint, MerkleTree,
  BucketEntries) match `internal/cluster/transport.go` exactly, including the KeyFilter and
  MerkleTree types.
- `undefined: helixv1.NodeServiceServer` or a getter like `GetIntended`: the generated code
  does not match the contract you built against. Re-run `make proto` after confirming
  `proto/helix/v1/node.proto` is the current version, then rebuild.

Networking and the round-trip test
- `TestGRPCCoordinatorRoundTrip` times out or fails to connect: another process may hold the
  port, or a firewall blocks loopback (unusual in Cloud Shell). The test uses `127.0.0.1:0`
  so the OS picks a free port; a persistent failure points to the servers not starting.
  Re-run with `-v` to see how far it gets.
- Connection errors mentioning DNS or the target scheme: if your grpc version is unhappy with
  a raw `host:port`, prefix the dial target with `passthrough:///`. In this part the address
  comes from `lis.Addr().String()`, which is a plain `127.0.0.1:port`; modern grpc resolves
  it fine, so this is only relevant if you see a resolver error.
- `connection refused` on the first RPC: `grpc.NewClient` dials lazily, so the failure appears
  on the first Get/Put rather than at dial time. Confirm the server goroutine started and the
  address in the peer registry matches `lis.Addr().String()`.
- `go test -race` reports a DATA RACE: treat it as a real defect in the transport's client
  cache or the coordinator fan-out, not a flake. Capture it with
  `go test -race ./internal/rpc/ 2>&1 | tee /tmp/helix-race.txt`.

Deferred features (not bugs)
- Calling anti-entropy over a gRPC client returns `Unimplemented`: expected. MerkleTree and
  BucketEntries over the wire are Part 3. The data path (get, put, hint) is complete here.
- Looking for TLS, certificates, or a `--tls` flag: none in this part. Connections are
  plaintext; mTLS is Part 4.
- Looking for a standalone server to leave running: none yet. The gRPC server runs inside the
  test in this part; the continuously serving daemon is Part 5.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above
  rather than typing. If the prompt looks corrupted after a large paste, run `reset` or open a
  new Cloud Shell tab.
