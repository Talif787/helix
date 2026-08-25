# Testing Phase 7 Part 3: SWIM membership over gRPC

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 7
Part 3. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 3 puts SWIM membership on the network. A node's failure detector now probes peers on
other machines over gRPC (direct Ping, and indirect PingReq through a relay), and gossip
spreads liveness across the cluster over the wire. It reuses the same peer registry and the
same one-listener-per-node model as the data plane. What applies:

- External dependencies: the same ones Part 2 added (google.golang.org/grpc and
  google.golang.org/protobuf). Part 3 adds no new modules.
- Code generation: required, and it now covers a second contract. `make proto` regenerates
  from both node.proto and membership.proto into internal/rpc/helixv1.
- A network service and addresses: yes. Each node serves a MembershipService (and, in a full
  node, the NodeService too) on one gRPC listener, reached at a host:port. In this part the
  servers are started inside the tests on 127.0.0.1 with OS-assigned ports.

Still not applicable in this part, and it is more useful to say so than to invent it:

- Persistent storage or data volumes: none. Membership state is in-memory only; the SWIM
  tests here open no storage engine.
- Credentials and TLS: none yet. Connections are plaintext (insecure). mTLS is Part 5.
- Anti-entropy over the wire: not yet. The NodeClient's MerkleTree and BucketEntries still
  return Unimplemented; those RPCs are Part 4.
- A standalone daemon: none yet. Membership servers run inside tests; the continuously
  serving node binary is Part 6.

What Part 3 adds and this runbook verifies: both contracts generate and compile; the
membership Update and State convert losslessly; a healthy four-node cluster converges to
all-alive over gRPC; and killing a node's server leads every survivor to detect it dead
through real networked probing and gossip.

## 0. One-time shell setup used by every section

```bash
export HELIX_HOME="$HOME/helix"
export HELIX_REPO="https://github.com/Talif787/helix.git"
```

---

## 1. Verify the existing environment

```bash
# 1a. Go toolchain. Helix's module is pinned to Go 1.22.
go version || echo "MISSING: Go toolchain"

# 1b. Supporting tools.
git --version || echo "MISSING: git"
gh --version 2>/dev/null || echo "note: GitHub CLI not found (only needed for PRs)"

# 1c. protoc and the Go plugins (required for codegen).
protoc --version 2>/dev/null || echo "MISSING: protoc (install in 2c)"
(which protoc-gen-go && which protoc-gen-go-grpc) >/dev/null 2>&1 \
  && echo "present: protoc Go plugins" || echo "MISSING: protoc plugins (install in 2c)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 7 Part 3 source present?
for f in \
  proto/helix/v1/membership.proto \
  internal/rpc/membership_server.go \
  internal/rpc/membership_client.go \
  internal/rpc/convert_membership.go \
  internal/rpc/membership_network_test.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done

# 1f. Are the Part 2 dependencies already wired into go.mod (Part 3 needs no new ones)?
grep -q 'google.golang.org/grpc' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "present: grpc in go.mod" || echo "MISSING: grpc dep (run go mod tidy in 4)"
head -3 "$HELIX_HOME/go.mod" | grep -q 'go 1.22' \
  && echo "go.mod pinned to 1.22 (matches CI)" || echo "note: check the go directive in go.mod"
```

Interpretation: below go1.22 -> 2a; 1c MISSING -> 2c; 1d MISSING -> 2b; 1e MISSING while 1d
FOUND -> 2d. If go.mod is not pinned to 1.22, see the troubleshooting note about the Go
version gate.

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

### 2c. Install protoc and the Go plugins (only if 1c flagged them missing)

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
git switch main && git pull --ff-only           # if Phase 7 Part 3 is merged
#   or: git switch phase-7/swim-grpc && git pull --ff-only   # if still on its branch
git log --oneline -3
```

---

## 3. Configure the required environment variables and services

There are no HELIX_* environment variables on this path; SWIM is configured in code through
membership.SwimConfig, and the messenger and server take addresses, not env vars. The PATH
entries for codegen matter, and the node address is a real dial target.

| Variable / input | Why | Value |
| --- | --- | --- |
| PATH includes Go bin | run `go` and plugins | `$HOME/go-sdk/go/bin` (if Go installed in 2a) |
| PATH includes GOPATH bin | protoc must find the plugins | `$(go env GOPATH)/bin` |
| node address (URL) | the gRPC dial target for a node's MembershipService | `127.0.0.1:<port>` (tests use OS-assigned ports) |

Dummy values the SWIM tests use (all built into the tests):

| Input | Meaning | Dummy value |
| --- | --- | --- |
| node ids | the cluster members | `node-a`, `node-b`, `node-c`, `node-d` |
| addresses | dial targets | `127.0.0.1:0` at listen time, resolved to real ports |
| SuspicionTicks | rounds a member stays suspect before dead | 3 |
| IndirectProbes | relays asked when a direct ping fails | 2 |
| PingTimeout | per-probe deadline | 500ms |
| failure event | which node is killed | `node-d` (its server is stopped) |

There is no database, broker, or credential to configure. Membership holds no persistent
state.

---

## 4. Start the backend and supporting services

The runnable artifacts in Part 3 are the SWIM network tests, which start real gRPC
MembershipService servers on localhost, drive SWIM rounds across them, and shut them down.
Before they can build or run, regenerate the protobuf code (now two contracts) and confirm
dependencies. This is the required one-time setup.

```bash
cd "$HELIX_HOME"
export PATH="$PATH:$(go env GOPATH)/bin"

# 1) Regenerate Go from BOTH contracts. make proto globs every .proto under proto/, so this
#    now emits node.* and membership.* into internal/rpc/helixv1.
make proto
ls -l internal/rpc/helixv1/
#    expect: node.pb.go, node_grpc.pb.go, membership.pb.go, membership_grpc.pb.go

# 2) Confirm dependencies (Part 3 adds none beyond Part 2). Keep the module on Go 1.22.
GOTOOLCHAIN=local go mod tidy
head -3 go.mod   # the go directive should still read: go 1.22

# 3) Build everything under the same toolchain constraint CI uses.
GOTOOLCHAIN=local go build ./...
```

There is no long-running daemon in this part; the membership servers start and stop inside
the tests. The continuously serving node binary is Part 6.

---

## 5. Verify service and backend health

Health means both contracts generated, the module builds under Go 1.22, static analysis is
clean, and the SWIM network tests pass (which is the live check here, since they actually
serve and probe over gRPC).

```bash
cd "$HELIX_HOME"
make fmt
make vet                                  # expect no output, zero exit
GOTOOLCHAIN=local go build ./...          # expect no output
go test ./internal/rpc/                   # expect: ok  github.com/talifpathan/helix/internal/rpc
echo "exit code: $?"                      # expect 0
```

To watch membership servers actually come up and converge over sockets, run the convergence
test verbosely:

```bash
go test -v -run TestGRPCSwimConverges ./internal/rpc/
```

---

## 6. Run Phase 7 Part 3 (automated tests)

SWIM engines call into each other's gRPC handlers concurrently, so run under the race
detector.

```bash
cd "$HELIX_HOME"

# 6a. Just the rpc package, verbosely, with the race detector.
go test -race -v ./internal/rpc/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: the rpc tests print `--- PASS`, every package prints `ok`, and `make check` ends
with `check passed`. Section 6a should include the new SWIM tests alongside the Part 1 and
Part 2 ones:

```
--- PASS: TestVersionedValueProtoRoundTrip
--- PASS: TestGRPCCoordinatorRoundTrip
--- PASS: TestGRPCTransportUnknownNode
--- PASS: TestGRPCSwimConverges
--- PASS: TestGRPCSwimDetectsFailure
--- PASS: (the peer registry tests)
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: codegen and build (the prerequisite)

Covered in section 4. Success is all four generated files present under
`internal/rpc/helixv1/`, the go directive still `go 1.22`, and `go build ./...` clean. This
proves both contracts compile against the runtime libraries.

### Scenario B: membership conversion round-trip

The Update and State conversions are exercised inside the network tests, but you can confirm
the whole rpc package's conversions (data plane and membership) in one run:

```bash
cd "$HELIX_HOME"
go test -race -v -run 'RoundTrip' ./internal/rpc/
```

### Scenario C: healthy cluster converges over gRPC (headline 1)

Dummy data (built in): four nodes `node-a`..`node-d` on 127.0.0.1 with OS-assigned ports;
SuspicionTicks 3, IndirectProbes 2, PingTimeout 500ms. Every probe crosses a socket.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestGRPCSwimConverges ./internal/rpc/
```

### Scenario D: failure detection over gRPC (headline 2)

Same cluster; after convergence, `node-d`'s server is stopped. The survivors must probe it,
fail directly and through relays, suspect it, and gossip it dead.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestGRPCSwimDetectsFailure ./internal/rpc/
```

### Scenario E: parameterized SWIM over gRPC with your own dummy values

This lets you set your own node ids, SWIM timing, and which node fails, and watch membership
converge over real localhost sockets. It drops a temporary test into the package, runs it,
and is removed afterward.

Create the test (edit the marked block to your own dummy data):

```bash
cd "$HELIX_HOME"
cat > internal/rpc/manual_scenario_test.go <<'HELIX_EOF'
package rpc

import (
	"context"
	"math/rand"
	"net"
	"testing"
	"time"

	"github.com/talifpathan/helix/internal/membership"
)

func TestSwimManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	ids := []string{"node-1", "node-2", "node-3", "node-4", "node-5"}
	victim := "node-5"          // node whose server we stop
	suspicionTicks := uint64(3)
	convergeRounds := 12
	detectRounds := 35
	// ---------------------------------------------------------

	peers := NewPeerRegistry()
	msngr := NewGRPCMessenger(peers)
	type node struct {
		engine *membership.Swim
		stop   func()
	}
	nodes := map[string]*node{}
	defer func() {
		for _, n := range nodes {
			n.stop()
		}
		_ = msngr.Close()
	}()

	for i, id := range ids {
		cfg := membership.SwimConfig{
			SuspicionTicks: suspicionTicks, IndirectProbes: 2,
			PingTimeout: 500 * time.Millisecond,
			Rand:        rand.New(rand.NewSource(int64(i) + 1)),
		}
		eng := membership.NewSwim(id, msngr, ids, cfg)
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen %s: %v", id, err)
		}
		srv := NewServer(nil, eng)
		go func() { _ = srv.Serve(lis) }()
		peers.Set(id, lis.Addr().String())
		nodes[id] = &node{engine: eng, stop: srv.GracefulStop}
		t.Logf("%s serving at %s", id, lis.Addr().String())
	}

	run := func(rounds int) {
		ctx := context.Background()
		for r := 0; r < rounds; r++ {
			for _, n := range nodes {
				n.engine.RunOnce(ctx)
			}
		}
	}

	run(convergeRounds)
	for _, id := range ids {
		for holder, n := range nodes {
			if st, _ := n.engine.List().StateOf(id); st != membership.Alive {
				t.Fatalf("%s should see %s alive, sees %v", holder, id, st)
			}
		}
	}
	t.Logf("converged: all %d nodes see each other alive", len(ids))

	nodes[victim].stop()
	delete(nodes, victim)
	t.Logf("stopped %s; running %d detection rounds", victim, detectRounds)
	run(detectRounds)

	for holder, n := range nodes {
		st, _ := n.engine.List().StateOf(victim)
		if st != membership.Dead {
			t.Fatalf("%s should see %s dead, sees %v", holder, victim, st)
		}
	}
	t.Logf("all survivors converged on %s dead", victim)
}
HELIX_EOF
echo "created internal/rpc/manual_scenario_test.go"
```

Run it (the `-v` flag shows serving addresses and convergence log lines):

```bash
cd "$HELIX_HOME"
go test -race -v -run TestSwimManualScenario ./internal/rpc/
```

Clean up when finished (only the temp test; membership keeps no on-disk state):

```bash
cd "$HELIX_HOME"
rm -f internal/rpc/manual_scenario_test.go
echo "removed manual scenario test"
```

---

## 8. Verify the expected results

### Scenario A (codegen and build)

- `internal/rpc/helixv1/` contains node.pb.go, node_grpc.pb.go, membership.pb.go, and
  membership_grpc.pb.go.
- `head -3 go.mod` still shows `go 1.22`.
- `GOTOOLCHAIN=local go build ./...` succeeds with no output.

### Scenario B (conversions)

- PASS: the data-plane VersionedValue round-trip and the membership Update round-trip both
  preserve their fields (the membership conversion is exercised through the network tests,
  which fail loudly if a state or incarnation is dropped).

### Scenario C (convergence)

- PASS: after the rounds, every one of the four nodes reports every node Alive, with all
  probing done over gRPC and `-race` clean.

### Scenario D (failure detection)

- PASS: after `node-d`'s server is stopped, every survivor reports `node-d` Dead. This proves
  a failed direct probe, failed indirect probes through relays, the suspicion timeout, and
  dead-state gossip all work over the network. On a failure the test prints each survivor's
  view of `node-d` so you can see who has not converged.

### Scenario E (parameterized)

- The test PASS line, plus `-v` log lines: each node's serving address, a convergence
  confirmation, and confirmation that all survivors see the victim dead.

### Automated suite

- Section 6 shows all rpc tests PASS (peer registry, data-plane round-trip, both SWIM tests),
  every package `ok`, and `make check` exiting 0 with no race warnings.

---

## 9. Troubleshooting

Codegen and the Go version gate
- `protoc: command not found` or `protoc-gen-go: program not found`: run 2c and ensure
  `export PATH="$PATH:$(go env GOPATH)/bin"`.
- `make proto` emits only node.* and not membership.*: the `proto` target globs
  `proto/**/*.proto`; confirm `proto/helix/v1/membership.proto` exists, then rerun.
- `go: go.mod requires go >= 1.25` during build or tidy: a dependency bumped the module's Go
  version. Keep the module on 1.22 the way Part 2 did: `go mod edit -go=1.22`, ensure grpc is
  pinned to a 1.22-era line (`go get google.golang.org/grpc@v1.64.1`), pin the x/ family if
  needed (`go get golang.org/x/net@v0.26.0 golang.org/x/sys@v0.21.0 golang.org/x/text@v0.16.0`),
  then `GOTOOLCHAIN=local go mod tidy`. `GOTOOLCHAIN=local` reproduces CI so a bad bump fails
  locally instead of in Cloud Build.

Build and interface errors
- `undefined: helixv1.MembershipServiceServer` or `helixv1.MemberState_MEMBER_STATE_ALIVE`:
  the membership code was not regenerated. Re-run `make proto` and confirm
  `membership_grpc.pb.go` exists under `internal/rpc/helixv1/`.
- `*GRPCMessenger does not implement membership.Messenger`: a Ping or PingReq signature
  drifted from `internal/membership/messenger.go`. Match them exactly, including the
  `[]membership.Update` parameter and return types.
- `cannot use engine (*membership.Swim) as membership.Handler`: the Swim engine must still
  implement HandlePing and HandlePingReq; confirm you are on a tree that includes Phase 5.

Networking and the SWIM tests
- `TestGRPCSwimDetectsFailure` fails with some survivors still seeing the victim suspect, not
  dead: it did not get enough rounds to finish the suspicion timeout plus gossip. This test
  is deterministic in logic but network round-trips add latency; increase the detection round
  count (the test uses 30; the parameterized scenario uses 35). A persistent failure with
  ample rounds points to a real problem in probing or gossip, not timing.
- Probes to the killed node hang instead of failing fast: stopping the server closes its
  listener, so dials should get connection-refused quickly. If you instead removed the node
  from the registry, cached clients may linger; the test stops the server rather than
  unregistering, which is the reliable way to model a crash.
- Connection errors mentioning the target scheme: if your grpc version rejects a raw
  `host:port`, prefix the dial target with `passthrough:///`. The messenger uses the address
  from `lis.Addr().String()`, a plain `127.0.0.1:port` that modern grpc resolves fine.
- `go test -race` reports a DATA RACE: treat it as a real defect in the messenger's client
  cache or the member list locking, not a flake. Capture with
  `go test -race ./internal/rpc/ 2>&1 | tee /tmp/helix-race.txt`.

Deferred features (not bugs)
- A NodeClient anti-entropy call returns `Unimplemented`: expected. MerkleTree and
  BucketEntries over the wire are Part 4. Part 3 is membership only.
- Looking for TLS or certificates: none in this part; connections are plaintext. mTLS is
  Part 5.
- Looking for a server to leave running or a data directory: none. Membership servers run
  inside the tests and hold no persistent state; the daemon is Part 6.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above
  rather than typing. If the prompt looks corrupted after a large paste, run `reset` or open
  a new Cloud Shell tab.
