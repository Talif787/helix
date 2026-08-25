# Testing Phase 7 Part 4: Merkle-tree anti-entropy over gRPC

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 7
Part 4. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 4 puts anti-entropy repair on the network. Two replicas on different machines now
exchange Merkle trees and reconcile divergent keys over gRPC. To make that possible, the
repair scope changed from an in-process Go closure to a serializable RepairScope (the two
node ids plus the replication factor), which each node turns back into a key predicate using
its own ring view. This retires the last two Unimplemented stubs in the gRPC client. What
applies:

- External dependencies: the same grpc and protobuf modules from Part 2. Part 4 adds none.
- Code generation: required, and this part changes node.proto (new Merkle and BucketEntries
  RPCs and their messages), so `make proto` regenerates the data-plane files too. Unlike
  Part 3, expect node.pb.go and node_grpc.pb.go to change.
- Storage: yes. Anti-entropy diffs data that lives in each node's engine, so the tests open
  real storage engines in temp directories.
- A network service and addresses: yes. Each node serves NodeService (now including the
  Merkle RPCs) on a gRPC listener at host:port, started inside the tests on 127.0.0.1.

Still not applicable in this part, and it is more useful to say so than to invent it:

- Credentials and TLS: none yet. Connections are plaintext (insecure). mTLS is Part 5.
- A standalone daemon: none yet. Servers run inside tests; the node binary is Part 6.

What Part 4 adds and this runbook verifies: node.proto regenerates and the whole tree still
builds under Go 1.22; the Merkle tree serializes and deserializes losslessly; the in-process
anti-entropy still works after the RepairScope refactor; and a repair between two nodes runs
entirely over gRPC, healing a key one node was missing and reconciling nothing on a second
round.

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

# 1e. Is the Phase 7 Part 4 source present?
grep -q 'type RepairScope struct' "$HELIX_HOME/internal/cluster/repair.go" 2>/dev/null \
  && echo "present: RepairScope (repair.go)" || echo "MISSING: RepairScope refactor"
grep -q 'func (s \*NodeServer) Merkle' "$HELIX_HOME/internal/rpc/server.go" 2>/dev/null \
  && echo "present: Merkle RPC on NodeServer" || echo "MISSING: Merkle RPC server method"
grep -q 'func DeserializeMerkleTree' "$HELIX_HOME/internal/cluster/merkle.go" 2>/dev/null \
  && echo "present: Merkle serialization" || echo "MISSING: Merkle serialization"
for f in internal/rpc/repair_network_test.go internal/cluster/merkle_serialize_test.go; do
  [ -f "$HELIX_HOME/$f" ] && echo "present: $f" || echo "MISSING: $f"
done

# 1f. Dependencies present and pinned (Part 2's deps; Part 4 adds none).
grep -q 'google.golang.org/grpc' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "present: grpc in go.mod" || echo "MISSING: grpc dep (restore per 2e)"
head -3 "$HELIX_HOME/go.mod" 2>/dev/null | grep -q 'go 1.22' \
  && echo "go.mod pinned to 1.22" || echo "note: check the go directive"
```

Interpretation: below go1.22 -> 2a; 1c MISSING -> 2c; 1d MISSING -> 2b; 1e MISSING while 1d
FOUND -> 2d; 1f showing grpc MISSING -> 2e (this exact regression bit us before).

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
git switch main && git pull --ff-only           # if Phase 7 Part 4 is merged
#   or: git switch phase-7/anti-entropy-grpc && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2e. Restore dependencies if go.mod lost them (only if 1f showed grpc missing)

A stray `go mod tidy` at the wrong moment can blank the require block. Restore the committed,
known-good files rather than re-tidying blind:

```bash
cd "$HELIX_HOME"
git checkout -- go.mod go.sum
grep 'google.golang.org/grpc ' go.mod   # should now show grpc
head -3 go.mod                           # should show: go 1.22
```

---

## 3. Configure the required environment variables and services

There are no HELIX_* environment variables on this path; the cluster, repair, and scope are
configured in code. The codegen PATH entries matter, and the node address is a real dial
target.

| Variable / input | Why | Value |
| --- | --- | --- |
| PATH includes Go bin | run `go` and plugins | `$HOME/go-sdk/go/bin` (if Go installed in 2a) |
| PATH includes GOPATH bin | protoc must find the plugins | `$(go env GOPATH)/bin` |
| node address (URL) | the gRPC dial target for a node's NodeService | `127.0.0.1:<port>` (tests use OS-assigned ports) |

Dummy values the tests use (all built in):

| Input | Meaning | Dummy value |
| --- | --- | --- |
| node ids | the replicas | `node-a`, `node-b`, `node-c` |
| addresses | dial targets | `127.0.0.1:0` at listen time, resolved to real ports |
| N | replication factor (all nodes co-replicate every key at N=3 on 3 nodes) | 3 |
| RepairScope | which keys a repair covers | `{NodeA: node-a, NodeB: node-b, N: 3}` |
| seeded key / value | the divergent key one node is missing | `orphan` = `v` |
| clock / timestamp | causal metadata on the seed | `{p:1}`, ts 1 |

Anti-entropy needs data on disk to diff, so each node opens a storage engine in a temp
directory the test creates. There is no external database or broker.

---

## 4. Start the backend and supporting services

The runnable artifacts are the anti-entropy tests, which open engines, serve nodes over gRPC
on localhost, and drive a Merkle repair across them. Before they build, regenerate the
protobuf code (node.proto changed this part) and confirm dependencies. This is the required
setup.

```bash
cd "$HELIX_HOME"
export PATH="$PATH:$(go env GOPATH)/bin"

# 1) Regenerate. node.proto gained Merkle and BucketEntries, so the data-plane files change.
make proto
git status --porcelain internal/rpc/helixv1/
#    expect node.pb.go and node_grpc.pb.go modified, membership.* unchanged

# 2) Confirm dependencies and hold the module at Go 1.22 (Part 4 adds no new modules).
go mod edit -go=1.22
GOTOOLCHAIN=local go mod tidy
head -3 go.mod                              # go directive must still read: go 1.22
grep 'google.golang.org/grpc ' go.mod       # deps must still be present

# 3) Build everything under the CI toolchain constraint.
GOTOOLCHAIN=local go build ./...
```

There is no long-running daemon in this part; servers start and stop inside the tests.

---

## 5. Verify service and backend health

Health means node.proto regenerated, the module builds under Go 1.22, static analysis is
clean, and the anti-entropy tests pass (the live check, since they serve and repair over
gRPC).

```bash
cd "$HELIX_HOME"
make fmt
make vet                                  # expect no output, zero exit
GOTOOLCHAIN=local go build ./...          # expect no output
go test ./internal/cluster/ ./internal/rpc/   # expect ok for both
echo "exit code: $?"                      # expect 0
```

To watch a repair actually cross sockets, run the networked test verbosely:

```bash
go test -v -run TestGRPCAntiEntropyRepairsOverNetwork ./internal/rpc/
```

---

## 6. Run Phase 7 Part 4 (automated tests)

Repair applies writes across replicas concurrently and the gRPC layer is concurrent, so run
under the race detector.

```bash
cd "$HELIX_HOME"

# 6a. The two affected packages, verbosely, with the race detector.
go test -race -v ./internal/cluster/ ./internal/rpc/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends with
`check passed`. The Part 4 additions and the refactored anti-entropy tests should include:

```
--- PASS: TestMerkleSerializeRoundTrip
--- PASS: TestDeserializeMerkleTreeRejectsBadLength
--- PASS: TestClusterAntiEntropyHealsMissingKey
--- PASS: TestClusterAntiEntropyIsIdempotent
--- PASS: TestClusterAntiEntropyHealsOfflineReplicaWithoutHints
--- PASS: TestGRPCAntiEntropyRepairsOverNetwork
--- PASS: (the Part 2 and Part 3 gRPC and SWIM tests)
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: codegen and build (the prerequisite)

Covered in section 4. Success is `git status` showing node.pb.go and node_grpc.pb.go
modified, the go directive still `go 1.22`, and `go build ./...` clean. This proves the new
Merkle RPCs generate and the whole tree compiles.

### Scenario B: Merkle serialization round-trip (no network)

Confirms the tree survives serialize then deserialize, and that a malformed blob is rejected.

```bash
cd "$HELIX_HOME"
go test -race -v -run 'TestMerkleSerialize|TestDeserializeMerkleTreeRejectsBadLength' ./internal/cluster/
```

### Scenario C: in-process anti-entropy still works after the refactor

The RepairScope change must not regress the in-process repair path.

```bash
cd "$HELIX_HOME"
go test -race -v -run 'TestClusterAntiEntropy' ./internal/cluster/
```

### Scenario D: anti-entropy over gRPC (the headline)

Dummy data (built in): nodes `node-a`/`node-b`/`node-c` on 127.0.0.1, N=3; key `orphan`=`v`
seeded only on node-a; repair scope `{node-a, node-b, 3}`. The repair runs entirely over
gRPC.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestGRPCAntiEntropyRepairsOverNetwork ./internal/rpc/
```

### Scenario E: parameterized anti-entropy over gRPC with your own dummy values

This lets you set your own node ids, N, and the divergent key, then watch a repair heal it
over real localhost sockets. It drops a temporary test into the rpc package, runs it, and is
removed afterward.

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

func TestAntiEntropyManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	ids := []string{"node-1", "node-2", "node-3"}
	n := 3
	seedOn := "node-1"          // node that gets the key directly
	healPeer := "node-2"        // node repaired against seedOn
	key := []byte("account:42")
	val := []byte("balance-100")
	// ---------------------------------------------------------

	ring := cluster.NewRing(128)
	for _, id := range ids {
		ring.Add(id)
	}
	peers := NewPeerRegistry()
	locals := map[string]*cluster.LocalNode{}
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
		node.SetPreferenceFunc(ring.LookupN)
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen %s: %v", id, err)
		}
		srv := NewGRPCServer(node)
		go func() { _ = srv.Serve(lis) }()
		cleanups = append(cleanups, func() { srv.GracefulStop(); _ = node.Close() })
		locals[id] = node
		peers.Set(id, lis.Addr().String())
		t.Logf("%s serving at %s", id, lis.Addr().String())
	}

	tr := NewGRPCTransport(peers)
	defer tr.Close()
	ctx := context.Background()

	if err := locals[seedOn].PutVersioned(ctx, key, cluster.VersionedValue{
		Value: val, Clock: cluster.VectorClock{"p": 1}, Timestamp: 1,
	}); err != nil {
		t.Fatalf("seed %s: %v", seedOn, err)
	}
	if _, found, _ := locals[healPeer].GetVersioned(ctx, key); found {
		t.Fatalf("%s should be missing the key before repair", healPeer)
	}

	ra, _ := tr.Replica(seedOn)
	rb, _ := tr.Replica(healPeer)
	scope := cluster.RepairScope{NodeA: seedOn, NodeB: healPeer, N: n}
	reconciled, err := cluster.Repair(ctx, ra, rb, scope)
	if err != nil {
		t.Fatalf("repair over gRPC: %v", err)
	}
	t.Logf("repair reconciled %d key(s) over gRPC", reconciled)

	if vv, found, _ := locals[healPeer].GetVersioned(ctx, key); !found || string(vv.Value) != string(val) {
		t.Fatalf("%s should have been healed to %q, found=%v value=%q", healPeer, val, found, vv.Value)
	}
	again, err := cluster.Repair(ctx, ra, rb, scope)
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if again != 0 {
		t.Fatalf("converged pair should reconcile 0, got %d", again)
	}
	t.Logf("%s healed and pair converged (second round reconciled 0)", healPeer)
}
HELIX_EOF
echo "created internal/rpc/manual_scenario_test.go"
```

Run it (the `-v` flag shows serving addresses and the reconcile count):

```bash
cd "$HELIX_HOME"
go test -race -v -run TestAntiEntropyManualScenario ./internal/rpc/
```

Clean up when finished (only the temp test; engines use test temp dirs Go removes):

```bash
cd "$HELIX_HOME"
rm -f internal/rpc/manual_scenario_test.go
echo "removed manual scenario test"
```

---

## 8. Verify the expected results

### Scenario A (codegen and build)

- `git status` shows `internal/rpc/helixv1/node.pb.go` and `node_grpc.pb.go` modified (and
  membership.* unchanged).
- `head -3 go.mod` still shows `go 1.22`, and grpc is still in the require block.
- `GOTOOLCHAIN=local go build ./...` succeeds with no output.

### Scenario B (serialization)

- PASS: the serialized blob is exactly 2*256*32 bytes, deserializing rebuilds an equal root
  with an empty diff against the original, and a too-short blob is rejected with an error.

### Scenario C (in-process anti-entropy)

- PASS: a key on one replica ends up on all co-replicas; a converged cluster reconciles 0 on
  a second round; and a replica that missed a write during an outage with hinting disabled is
  healed. These are the Phase 6 tests, still green after the RepairScope refactor.

### Scenario D (anti-entropy over gRPC)

- PASS: node-b, which lacked `orphan`, holds it after a repair that ran entirely over gRPC
  (Merkle exchange, bucket diff, entry transfer), and a second repair reconciles 0. This
  proves the Merkle RPCs and the RepairScope crossing the wire.

### Scenario E (parameterized)

- The test PASS line, plus `-v` log lines: each node's serving address, the reconcile count,
  and confirmation the peer was healed and the pair converged.

### Automated suite

- Section 6 shows the serialization, in-process anti-entropy, and over-gRPC anti-entropy
  tests PASS alongside the Part 2 and Part 3 tests, every package `ok`, and `make check`
  exiting 0 with no race warnings.

---

## 9. Troubleshooting

Codegen and the Go version gate
- `protoc: command not found` or `protoc-gen-go: program not found`: run 2c and
  `export PATH="$PATH:$(go env GOPATH)/bin"`.
- node.pb.go does not change after `make proto`: you may be looking at a stale checkout, or
  node.proto was not updated. Confirm `grep -c 'rpc Merkle' proto/helix/v1/node.proto` is 1,
  then regenerate.
- `go: go.mod requires go >= 1.25`: a dependency bumped the module's Go version during tidy.
  Hold it at 1.22: `go mod edit -go=1.22`, keep grpc pinned (`go get google.golang.org/grpc@v1.64.1`),
  pin the x/ family if needed (`go get golang.org/x/net@v0.26.0 golang.org/x/sys@v0.21.0 golang.org/x/text@v0.16.0`),
  then `GOTOOLCHAIN=local go mod tidy`. `GOTOOLCHAIN=local` reproduces CI.
- `go.mod` require block vanished after tidy: restore it with `git checkout -- go.mod go.sum`;
  do not commit a dependency-free go.mod.

Build and interface errors
- `undefined: helixv1.MerkleRequest` or `helixv1.RepairScope`: node.proto was not
  regenerated. Re-run `make proto` and confirm node.pb.go changed.
- `*NodeClient does not implement cluster.Replica`: a MerkleTree or BucketEntries signature
  drifted. Both must take `cluster.RepairScope` now, not the old `KeyFilter`. Confirm against
  `internal/cluster/transport.go`.
- `cannot use ... KeyFilter`: leftover code from before the refactor. The interface uses
  RepairScope; KeyFilter survives only inside `internal/cluster/node.go` as the internal
  predicate type.

Anti-entropy behavior
- `TestGRPCAntiEntropyRepairsOverNetwork` reconciles 0 when it should heal: the seeded key
  was not co-replicated by the pair, so the scope excluded it. With N equal to the node count
  (3 nodes, N=3) every node co-replicates every key; confirm N matches the node count in your
  scenario.
- The healed node still lacks the key: repair is bidirectional, but the direction that
  matters here is seedOn to healPeer. Confirm `SetPreferenceFunc` was called on the served
  nodes (the test does this); without it, the remote node cannot scope and may return an
  empty tree.
- A repair copies a key onto a node outside its preference list: the scope is being ignored.
  Ensure the served LocalNode has its preference function set so it applies the RepairScope;
  an unset preference function means "all keys", which is only safe when N equals the node
  count.

Deferred features (not bugs)
- Looking for TLS or certificates: none in this part; connections are plaintext. mTLS is
  Part 5.
- Looking for a standalone server or a persistent data directory to inspect: none. Servers
  run inside tests; the daemon is Part 6.
- A gRPC client no longer returns Unimplemented for Merkle: correct, that is the point of
  this part. Both stubs are now real RPC calls.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above
  rather than typing. If the prompt looks corrupted after a large paste, run `reset` or open
  a new Cloud Shell tab.
