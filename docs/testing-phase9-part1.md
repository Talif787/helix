# Testing Phase 9 Part 1: the client-facing API

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 9
Part 1. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 1 gives Helix a front door. It adds a ClientService (distinct from the node-to-node
NodeService) that runs the full coordinator quorum, and a Go client library that dials any
node and issues coordinated Get, Put, and Delete. The daemon now serves this plane on the same
gRPC listener as the node and membership planes. What applies:

- Code generation: required. A new client.proto means `make proto` regenerates (node,
  membership, and client all emit into internal/rpc/helixv1). Expect node.* and membership.*
  to be rewritten too (usually identical).
- A network service and addresses: the client plane rides the node's existing gRPC listener at
  HELIX_BIND_ADDR, so a client dials the same host:port the cluster uses.
- Programmatic client access: yes, now. Any Go program can import internal/rpc, DialClient, and
  call Put/Get/Delete.

Different from recent parts:

- New dependencies: none. The client plane uses the grpc module already present.

The boundary to understand before verifying, stated plainly so results are not misread:

- There is no command-line client in this part. Part 1 delivers the ClientService and the Go
  client library; the helixctl binary that wraps the library for shell use is Part 2. So the
  authoritative way to exercise the client path here is the Go integration test, and for a
  hands-on check against a running node, a tiny embedded Go driver (Scenario E), not a curl or
  a CLI. curl cannot speak gRPC, so it does not apply to this endpoint.

Still not applicable in this part:

- External databases, brokers: none. Nodes embed their own engines.
- A REST/HTTP client surface: the client plane is gRPC, not HTTP. The only HTTP surface is the
  metrics /healthz and /metrics from Phase 8, which is unrelated.

What Part 1 adds and this runbook verifies: the client contract generates and the tree builds;
a program using the client library can Put, Get, overwrite, Delete, and read a missing key
through the coordinated service over gRPC; and a running daemon answers coordinated client
calls on its gRPC listener.

## 0. One-time shell setup used by every section

```bash
export HELIX_HOME="$HOME/helix"
export HELIX_REPO="https://github.com/Talif787/helix.git"
export HELIX_RUN="/tmp/helix-run"
```

---

## 1. Verify the existing environment

```bash
# 1a. Go toolchain. Helix's module is pinned to Go 1.22.
go version || echo "MISSING: Go toolchain"

# 1b. Supporting tools.
git --version || echo "MISSING: git"
gh --version 2>/dev/null || echo "note: GitHub CLI not found (only needed for PRs)"

# 1c. protoc and the Go plugins (required: client.proto is new this part).
protoc --version 2>/dev/null || echo "MISSING: protoc (install in 2c)"
(which protoc-gen-go && which protoc-gen-go-grpc) >/dev/null 2>&1 \
  && echo "present: protoc Go plugins" || echo "MISSING: protoc plugins (install in 2c)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 9 Part 1 source present?
for f in \
  proto/helix/v1/client.proto \
  internal/rpc/client_server.go \
  internal/rpc/client_api.go \
  internal/rpc/client_network_test.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done
grep -q 'RegisterClientService' "$HELIX_HOME/internal/daemon/daemon.go" 2>/dev/null \
  && echo "present: daemon registers the client plane" || echo "MISSING: daemon client-plane wiring"

# 1f. Dependencies present, pinned, and no unexpected additions.
grep -q 'google.golang.org/grpc' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "present: grpc in go.mod" || echo "MISSING: grpc dep (restore per 2e)"
head -3 "$HELIX_HOME/go.mod" 2>/dev/null | grep -q 'go 1.22' \
  && echo "go.mod pinned to 1.22" || echo "note: check the go directive"
```

Interpretation: below go1.22 -> 2a; 1c MISSING -> 2c; 1d MISSING -> 2b; 1e MISSING while 1d
FOUND -> 2d; 1f showing grpc MISSING -> 2e.

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

### 2c. Install protoc and the Go plugins (required this part)

```bash
sudo apt-get update && sudo apt-get install -y protobuf-compiler
protoc --version
cd "$HELIX_HOME"
make proto-tools
export PATH="$PATH:$(go env GOPATH)/bin"
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> "$HOME/.bashrc"
which protoc-gen-go protoc-gen-go-grpc
```

### 2d. Update an existing checkout (only if 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only          # if Phase 9 Part 1 is merged
#   or: git switch phase-9/client-api && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2e. Restore dependencies if go.mod lost them (only if 1f showed grpc missing)

```bash
cd "$HELIX_HOME"
git checkout -- go.mod go.sum
grep 'google.golang.org/grpc ' go.mod
head -3 go.mod
```

---

## 3. Configure the required environment variables and services

The automated test configures everything in code and needs no environment variables. To run a
node and talk to it with an embedded driver (Scenario E), use the cluster variables from Phase
7 Part 6; there is no new variable this part.

| Variable / input | Meaning | Example |
| --- | --- | --- |
| PATH includes GOPATH bin | protoc must find the plugins | `$(go env GOPATH)/bin` |
| HELIX_NODE_ID | this node's id | `solo` |
| HELIX_PEERS | id=addr for every node | `solo=127.0.0.1:7070` |
| HELIX_BIND_ADDR | gRPC listen address; the client dials this | `127.0.0.1:7070` |
| HELIX_DATA_DIR | this node's storage directory | `/tmp/helix-run/solo` |
| HELIX_N / HELIX_R / HELIX_W | replication and quorums; use 1/1/1 on a single node | `1` / `1` / `1` |

The client address (the URL the client library dials):

| Input | Meaning | Example |
| --- | --- | --- |
| client target | a node's gRPC address serving ClientService | `127.0.0.1:7070` |

Dummy values the test and driver use:

| Input | Meaning | Dummy value |
| --- | --- | --- |
| node ids | in-process replicas behind the coordinator | `node-a`, `node-b`, `node-c` |
| N / R / W | replication and quorums | 3 / 2 / 2 in the test |
| sample key/value | coordinated write | `user:1001`=`alice`, then `alice-v2` |
| missing key | not-found read | `absent` |

There is no database or broker to configure. The client plane is gRPC; there is no HTTP or
REST surface for it, so no curl.

---

## 4. Start the backend and supporting services

This part changes the proto, so regenerate first, then build. Optionally run a single node so
Scenario E can talk to it.

```bash
cd "$HELIX_HOME"
export PATH="$PATH:$(go env GOPATH)/bin"

# 1) Regenerate. client.proto is new, so make proto now emits client.* too.
make proto
ls -l internal/rpc/helixv1/
#    expect node.*.go, membership.*.go, and client.pb.go + client_grpc.pb.go

# 2) Hold the module at Go 1.22 and confirm deps (no new modules this part).
go mod edit -go=1.22
GOTOOLCHAIN=local go mod tidy
head -3 go.mod
grep 'google.golang.org/grpc ' go.mod

# 3) Build under the CI toolchain constraint.
GOTOOLCHAIN=local go build ./...
make build
ls -l bin/kvnode
```

### 4a. Run a single node whose ClientService is reachable (for Scenario E)

```bash
cd "$HELIX_HOME"
rm -rf "$HELIX_RUN" && mkdir -p "$HELIX_RUN"
HELIX_NODE_ID=solo \
HELIX_PEERS="solo=127.0.0.1:7070" \
HELIX_BIND_ADDR="127.0.0.1:7070" \
HELIX_DATA_DIR="$HELIX_RUN/solo" \
HELIX_N=1 HELIX_R=1 HELIX_W=1 \
HELIX_LOG_FORMAT=text \
./bin/kvnode > "$HELIX_RUN/solo.log" 2>&1 &
echo "started kvnode (pid $!) serving ClientService on 127.0.0.1:7070"
sleep 2
grep -i 'daemon serving' "$HELIX_RUN/solo.log"
```

Stop it when done:

```bash
pkill -f './bin/kvnode' && echo "stopped kvnode"
```

Note: there is no CLI to hit this node from the shell in Part 1. Scenario E uses a small Go
program (via `go run`) to talk to it; helixctl arrives in Part 2.

---

## 5. Verify service and backend health

Health means the client contract generated, the tree builds under Go 1.22, static analysis is
clean, and the client integration test passes.

```bash
cd "$HELIX_HOME"
make fmt
make vet                                  # expect no output, zero exit
GOTOOLCHAIN=local go build ./...          # expect no output
go test ./internal/rpc/                   # expect: ok  github.com/talifpathan/helix/internal/rpc
echo "exit code: $?"                      # expect 0
```

To confirm a running node is actually serving the client plane, run the integration test
verbosely (it stands up a coordinator and serves ClientService over a socket):

```bash
go test -v -run TestClientServiceRoundTrip ./internal/rpc/
```

---

## 6. Run Phase 9 Part 1 (automated tests)

The client and server run concurrently over gRPC, so run under the race detector.

```bash
cd "$HELIX_HOME"

# 6a. The rpc package, verbosely, with the race detector.
go test -race -v ./internal/rpc/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends with
`check passed`. Section 6a should include, alongside the earlier gRPC, SWIM, anti-entropy, and
TLS tests:

```
--- PASS: TestClientServiceRoundTrip
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: codegen and build (the prerequisite)

Covered in section 4. Success is `internal/rpc/helixv1/client.pb.go` and `client_grpc.pb.go`
present, the go directive still `go 1.22`, grpc still in go.mod, and `go build ./...` clean.

### Scenario B: client round-trip over gRPC (the headline)

Dummy data (built in): a coordinator over three in-process replicas (`node-a`/`node-b`/
`node-c`, N=3, R=2, W=2) fronted by ClientService on localhost; key `user:1001` written as
`alice` then overwritten to `alice-v2`; missing key `absent`. The client library drives every
operation.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestClientServiceRoundTrip ./internal/rpc/
```

### Scenario C: not-found is a clean result, not an error

Part of the round-trip test: a Get of `absent` returns found=false with a nil error, and a Get
after Delete does the same. To see just that behavior, the same test covers it; the assertions
fail loudly if a missing key surfaces as an error instead of found=false.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestClientServiceRoundTrip ./internal/rpc/ 2>&1 | grep -A1 -i 'found\|delete\|absent' || echo "(assertions internal; a PASS means not-found behaves correctly)"
```

### Scenario D: the daemon serves the client plane

Confirm the running daemon (section 4a) actually registered ClientService. The test in
Scenario E dials the daemon; a successful Put/Get proves the plane is served on the node's gRPC
listener alongside NodeService and MembershipService.

### Scenario E: talk to a running node with an embedded Go driver

Because there is no CLI yet, this scenario uses a throwaway Go program to Put and Get against
the node started in 4a. It lives at cmd/helixdriver inside the Helix module, because Go forbids
a separate module from importing internal/ packages; delete it when done so it is not committed.

Start the node from 4a first, then create the driver inside the Helix module (so it is
allowed to import the internal client library; a separate module is blocked by Go's
internal-package rule):

```bash
cd "$HELIX_HOME"
mkdir -p cmd/helixdriver
cat > cmd/helixdriver/main.go <<'HELIX_EOF'
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/talifpathan/helix/internal/rpc"
)

func main() {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	addr := "127.0.0.1:7070"
	key := []byte("account:42")
	val := []byte("balance-100")
	// ---------------------------------------------------------

	client, err := rpc.DialClient(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Put(ctx, key, val); err != nil {
		fmt.Fprintln(os.Stderr, "put:", err)
		os.Exit(1)
	}
	fmt.Printf("PUT %s=%s OK\n", key, val)

	got, found, err := client.Get(ctx, key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "get:", err)
		os.Exit(1)
	}
	fmt.Printf("GET %s -> found=%v value=%q\n", key, found, got)

	_, found, _ = client.Get(ctx, []byte("does-not-exist"))
	fmt.Printf("GET does-not-exist -> found=%v (want false)\n", found)
}
HELIX_EOF

GOTOOLCHAIN=local go run ./cmd/helixdriver
```

Expected output: `PUT ... OK`, then `GET ... found=true value="balance-100"`, then
`GET does-not-exist -> found=false`.

Clean up:

```bash
cd "$HELIX_HOME"
rm -rf cmd/helixdriver
pkill -f './bin/kvnode' 2>/dev/null || true
echo "removed temp driver and stopped node"
```

---

## 8. Verify the expected results

### Scenario A (codegen and build)

- `internal/rpc/helixv1/` contains `client.pb.go` and `client_grpc.pb.go`.
- `head -3 go.mod` still shows `go 1.22`, grpc still present.
- `GOTOOLCHAIN=local go build ./...` succeeds.

### Scenario B (round-trip)

- PASS: `user:1001` reads back `alice`, then `alice-v2` after overwrite; after Delete a read
  returns found=false; and the missing key `absent` returns found=false with no error. Every
  call crossed the ClientService over gRPC, with `-race` clean.

### Scenario C (not-found)

- The round-trip test PASSes, which includes the not-found and post-delete assertions; a
  missing key surfacing as an error rather than found=false would fail the test.

### Scenario D (daemon serves the plane)

- Scenario E's Put/Get against the running daemon succeeds, proving the daemon registered
  ClientService on its gRPC listener.

### Scenario E (embedded driver)

- `PUT account:42=balance-100 OK`
- `GET account:42 -> found=true value="balance-100"`
- `GET does-not-exist -> found=false`

### Automated suite

- Section 6 shows `TestClientServiceRoundTrip` PASS alongside every earlier test, every package
  `ok`, and `make check` exiting 0 with no race warnings.

---

## 9. Troubleshooting

Codegen and the Go version gate
- `protoc: command not found` or `protoc-gen-go: program not found`: run 2c and
  `export PATH="$PATH:$(go env GOPATH)/bin"`.
- `make proto` does not emit client.*: confirm `proto/helix/v1/client.proto` exists and
  `grep -c 'service ClientService' proto/helix/v1/client.proto` is 1, then rerun.
- `go: go.mod requires go >= 1.25`: hold at 1.22 (`go mod edit -go=1.22`), keep grpc pinned,
  pin the x/ family if needed, then `GOTOOLCHAIN=local go mod tidy`.

Build and interface errors
- `undefined: helixv1.ClientServiceServer` or `helixv1.NewClientServiceClient`: the client code
  was not regenerated. Re-run `make proto` and confirm `client_grpc.pb.go` exists.
- `*cluster.Coordinator does not implement rpc.CoordinatorAPI`: a coordinator method signature
  drifted from Put/Get/Delete. The compile-time assertion in client_server.go will point at it;
  match the signatures in internal/cluster/coordinator.go.

Client behavior
- `TestClientServiceRoundTrip` fails a write with an error: the in-process coordinator uses
  N=3, R=2, W=2 with three replicas, which should always commit; a failure here points at the
  coordinator or transport, not the client plane.
- Scenario E driver: `connection refused` on the first call: the node is not running or you
  dialed the wrong port. Confirm `pgrep -af kvnode` and that `addr` matches HELIX_BIND_ADDR.
  grpc.NewClient is lazy, so this error appears on the first Put/Get, not at DialClient.
- Scenario E driver: a Put returns an error on a single node: set HELIX_N=1 HELIX_R=1 HELIX_W=1
  when launching the node (section 4a). The default 3/2/2 cannot reach a write quorum of two on
  one node.
- Scenario E driver: `use of internal package ... not allowed`: the driver is outside the Helix
  module. It must live under $HELIX_HOME (cmd/helixdriver), not in a separate module, because
  Go forbids importing internal/ across modules.

Protocol and transport
- The client cannot reach the node with a curl: expected. ClientService is gRPC, not HTTP; curl
  cannot speak it. Use the Go client library (Scenario E) or, in Part 2, helixctl.
- TLS: if the node is running with mutual TLS (HELIX_TLS_*), a plaintext client is refused.
  Pass ClientTLSOption(cfg) to DialClient with a client cert; helixctl in Part 2 exposes this
  via flags.

Test isolation
- tests show `(cached)`: add `-count=1` to force a real run.
- a stray node from a previous run holds the port: `pkill -f './bin/kvnode'`.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above rather
  than typing. If the prompt looks corrupted after a large paste, run `reset` or open a new
  Cloud Shell tab.
