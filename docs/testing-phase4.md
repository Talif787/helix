# Testing Phase 4: replication and tunable consistency

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 4.
It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Phase 4 replicates each key to N nodes and adds tunable read and write quorums (R and W),
vector-clock reconciliation, and read repair. The nodes still run in-process behind a
transport interface, so several items from a typical service runbook still do not apply,
and saying so is more useful than inventing them:

- Separate processes, ports, network listeners: none. The cluster is several storage
  engines, a ring, and a coordinator inside one process. Networked nodes with gRPC arrive
  in Phase 7.
- External databases, caches, brokers: none. Each node is its own embedded engine writing
  to its own subdirectory. The "database volume" is that directory tree.
- Credentials, API keys, tokens, connection URLs, request payloads: none. The equivalent
  inputs are node IDs, key-value pairs, and the N/R/W settings, all given as dummy values
  below.
- Environment variables: the cluster is configured in code through cluster.Options
  (node IDs, N, R, W, virtual nodes, base directory), not through HELIX_* variables.

What Phase 4 adds, and therefore what we verify: each key is replicated onto its
preference list; writes and reads succeed with a node offline as long as the quorum is
reachable; concurrent or stale versions are reconciled (vector clock first, last-write-wins
only for true conflicts); a read repairs replicas that were behind; and deletes replicate
as tombstones.

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

# 1b. Supporting tools (git required; gh only needed later for commits).
git --version || echo "MISSING: git"
gh --version 2>/dev/null || echo "note: GitHub CLI not found (only needed for PRs)"

# 1c. Does the repository already exist here?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1d. Is the Phase 4 code present? version.go is new this phase, and the node/coordinator
#     files gained versioned, quorum behavior.
for f in \
  internal/cluster/version.go \
  internal/cluster/version_test.go \
  internal/cluster/coordinator.go \
  internal/cluster/node.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done

# 1e. Confirm the code is at Phase 4 (the versioned Replica interface), not Phase 3.
grep -q 'PutVersioned' "$HELIX_HOME/internal/cluster/transport.go" 2>/dev/null \
  && echo "present: versioned Replica interface" \
  || echo "MISSING: versioned Replica interface (tree predates Phase 4)"
```

Interpretation: below go1.22 -> 2a; 1c MISSING -> 2b; 1d or 1e MISSING while 1c FOUND -> 2c.

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

### 2b. Clone the repository (only if 1c is missing)

```bash
cd "$HOME"
git clone "$HELIX_REPO" helix
cd "$HELIX_HOME"
git status
```

### 2c. Update an existing checkout to Phase 4 (only if 1d or 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only        # if Phase 4 is merged
#   or: git switch phase-4/replication && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2d. Resolve dependencies (safe any time)

Helix is standard-library only, so this downloads nothing but confirms a clean module.

```bash
cd "$HELIX_HOME"
go mod download
go mod verify   # expect: all modules verified
```

### 2e. Data directories

Each node creates its own subdirectory on demand under the base directory the code passes.
The demo uses a private temp directory it deletes on exit, so nothing needs provisioning.
The optional on-disk scenario in section 7 uses a directory you set with HELIX_MANUAL_DIR
and clears it at the start.

---

## 3. Configure the required environment variables and services

There are no services to configure and no environment variables on the Phase 4 path. The
cluster is configured in code through cluster.Options. This table documents each field and
the dummy values this runbook uses.

| Option / input | Meaning | Dummy value used here |
| --- | --- | --- |
| node IDs | Nodes in the cluster (ring keys and data subdir names) | `node-a`, `node-b`, `node-c` (demo); `node-1`, `node-2`, `node-3` (manual) |
| N | Replication factor (copies per key) | 3 (also tested with 2) |
| R | Read quorum (responses required for a read) | 2 |
| W | Write quorum (acks required for a write) | 2 |
| VNodes | Virtual nodes per physical node | 128 |
| BaseDir | Parent directory; node `x` stores data in `BaseDir/x` | temp dir (demo); `HELIX_MANUAL_DIR` (manual) |
| Storage.SyncWrites | fsync each write on every node | true (manual), false (tests, for speed) |
| sample keys / values | Data to replicate | `user:1001`..`order:5002`, `key:00000`.. |
| credentials, URLs, payloads | not applicable in this phase | none |

Note on quorums: R + W > N gives read-your-writes overlap. The defaults (N=3, R=2, W=2)
satisfy this. R and W must not exceed N, and must not exceed the number of reachable nodes
or the operation cannot reach its quorum.

---

## 4. Start the backend and supporting services

There is no long-lived server and no supporting service. The Phase 4 vehicle is the
`clusterdemo` binary, a self-verifying tool that builds a replicated cluster, exercises it
(including a simulated node failure), prints a report, and exits (0 on success).

```bash
cd "$HELIX_HOME"
make demos           # builds ./bin/clusterdemo (and ./bin/sstdemo)
ls -l ./bin/
# If make is unavailable: go build -o bin/clusterdemo ./cmd/clusterdemo
```

---

## 5. Verify service and backend health

Health means it builds, static analysis is clean, and the self-verifying demo runs to PASS.

```bash
cd "$HELIX_HOME"
make fmt
make vet          # expect no output, zero exit code
./bin/clusterdemo
echo "exit code: $?"   # expect 0
```

Expected: a report showing the replication settings, read-back of all keys, per-node
replica counts, a served read and write with one node offline, a read repair after the node
rejoins, and a final `clusterdemo: PASS`.

---

## 6. Run Phase 4 (automated tests)

The authoritative verification is the Go test suite under the race detector. The
coordinator fans out across goroutines, so `-race` is the gate that matters most.

```bash
cd "$HELIX_HOME"

# 6a. Just the cluster package, verbosely, with the race detector.
go test -race -v ./internal/cluster/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends
cleanly. Section 6a should include:

```
--- PASS: TestVectorClockCompare
--- PASS: TestVectorClockMergeAndIncr
--- PASS: TestReconcileCausalWinsOverTimestamp
--- PASS: TestReconcileConcurrentUsesLWW
--- PASS: TestReconcileTombstone
--- PASS: TestVersionedEncodeDecodeRoundTrip
--- PASS: TestVersionedDecodeTombstoneHasClock
--- PASS: TestCoordinatorWriteMeetsQuorumDespiteOneFailure
--- PASS: TestCoordinatorWriteFailsBelowQuorum
--- PASS: TestCoordinatorReadFailsBelowQuorum
--- PASS: TestCoordinatorReadRepairsStaleReplica
--- PASS: TestCoordinatorNoNodes
--- PASS: TestClusterPutGetDelete
--- PASS: TestClusterReplicationPlacement
--- PASS: TestClusterToleratesNodeDownThenRepairs
--- PASS: TestClusterMissingKey
--- PASS: TestClusterRejectsBadQuorum
--- PASS: TestClusterRejectsDuplicateNodeID
--- PASS: TestClusterRejectsEmptyMembership
--- PASS: (ring tests from Phase 3)
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: replication and fault tolerance end to end (the demo)

Dummy data (built into the demo): node IDs `node-a`, `node-b`, `node-c`; N=3, R=2, W=2;
keys `key:00000`..`key:01999`; plus a `failover-key`.

```bash
cd "$HELIX_HOME"
./bin/clusterdemo
```

This one run covers replica placement, quorum read-back, a served read and write with a
node offline, read repair after rejoin, and a replicated delete. Read the report per
section 8.

### Scenario B: reconciliation logic in isolation

These target vector clocks and last-write-wins with deterministic inputs.

```bash
cd "$HELIX_HOME"

# Vector clock ordering, merge, increment.
go test -race -v -run 'TestVectorClock' ./internal/cluster/

# Causality beats a stale timestamp; concurrency falls back to last-write-wins; tombstones.
go test -race -v -run 'TestReconcile' ./internal/cluster/

# Versioned value survives a JSON round trip, including tombstone clocks.
go test -race -v -run 'TestVersioned' ./internal/cluster/
```

### Scenario C: quorum behavior and read repair

```bash
cd "$HELIX_HOME"

# Write meets W with one replica failing; write fails when too few ack.
go test -race -v -run 'TestCoordinatorWrite' ./internal/cluster/

# Read fails below R; a read repairs a stale replica.
go test -race -v -run 'TestCoordinatorRead' ./internal/cluster/
```

### Scenario D: parameterized replicated cluster with your own dummy values (on disk)

This lets you supply your own node IDs, quorum settings, and key-value pairs, then inspect
the per-node data directories. It drops a temporary test into the package (so it can reach
the internal package), runs it, and is removed afterward.

Create the test (edit the marked block to your own dummy data):

```bash
cd "$HELIX_HOME"
cat > internal/cluster/manual_scenario_test.go <<'HELIX_EOF'
package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/talifpathan/helix/internal/storage"
)

func TestClusterManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	n, r, w := 3, 2, 2 // replication factor and read/write quorums
	baseDir := os.Getenv("HELIX_MANUAL_DIR")
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "helix-cluster-manual")
	}
	pairs := map[string]string{
		"user:1001":  "alice",
		"user:1002":  "bob",
		"user:1003":  "carol",
		"order:5001": "widget",
		"order:5002": "gadget",
	}
	updateKey, updateVal := "user:1002", "bob-updated"
	deleteKey := "order:5002"
	// ---------------------------------------------------------

	_ = os.RemoveAll(baseDir)
	c, err := NewCluster(nodeIDs, Options{
		BaseDir: baseDir, VNodes: 128, N: n, R: r, W: w,
		Storage: storage.Options{SyncWrites: true},
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	for k, v := range pairs {
		if err := c.Put(ctx, []byte(k), []byte(v)); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	for k := range pairs {
		t.Logf("key %-11s replicas %v", k, c.PreferenceList([]byte(k), n))
	}

	if err := c.Put(ctx, []byte(updateKey), []byte(updateVal)); err != nil {
		t.Fatalf("update %s: %v", updateKey, err)
	}
	if got, err := c.Get(ctx, []byte(updateKey)); err != nil || string(got) != updateVal {
		t.Fatalf("expected %q, got %q err %v", updateVal, got, err)
	}
	t.Logf("update to %s reconciled to %q", updateKey, updateVal)

	// Fail one replica for a write, then rejoin and let a read repair it.
	pref := c.PreferenceList([]byte(updateKey), n)
	down := pref[len(pref)-1]
	c.Transport().Deregister(down)
	if err := c.Put(ctx, []byte(updateKey), []byte("after-failover")); err != nil {
		t.Fatalf("write with %s down: %v", down, err)
	}
	node, _ := c.Node(down)
	c.Transport().Register(down, node)
	if _, err := c.Get(ctx, []byte(updateKey)); err != nil {
		t.Fatalf("read after rejoin: %v", err)
	}
	if vv, found, _ := node.GetVersioned(ctx, []byte(updateKey)); !found || string(vv.Value) != "after-failover" {
		t.Fatalf("read repair should have healed %s: found=%v value=%q", down, found, vv.Value)
	}
	t.Logf("node %s was offline for a write, rejoined, and was read-repaired", down)

	if err := c.Delete(ctx, []byte(deleteKey)); err != nil {
		t.Fatalf("delete %s: %v", deleteKey, err)
	}
	if _, err := c.Get(ctx, []byte(deleteKey)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected %s deleted, err=%v", deleteKey, err)
	}
	abs, _ := filepath.Abs(baseDir)
	t.Logf("deleted %s; per-node data is under %s", deleteKey, abs)
}
HELIX_EOF
echo "created internal/cluster/manual_scenario_test.go"
```

Run it and inspect the on-disk replication. Set HELIX_MANUAL_DIR to an absolute path so the
data lands somewhere predictable (go test's working directory is the package directory, not
your shell's):

```bash
cd "$HELIX_HOME"
export HELIX_MANUAL_DIR="$HELIX_HOME/data-cluster-manual"
go test -race -v -run TestClusterManualScenario ./internal/cluster/

echo "--- per-node data directories (each node has its own WAL and MANIFEST) ---"
ls -R "$HELIX_MANUAL_DIR"
```

Clean up when finished (both the temporary test and its data):

```bash
cd "$HELIX_HOME"
rm -f internal/cluster/manual_scenario_test.go
rm -rf "$HELIX_MANUAL_DIR"
echo "removed manual scenario test and its data"
```

---

## 8. Verify the expected results

### Scenario A (demo)

Expected output shape (counts vary, structure does not):

```
cluster of 3 nodes, replication N=3 R=2 W=2
wrote and read back 2000 keys with quorum
replica placement verified (6000 copies total for 2000 keys)
  node-a   holds  NNNN keys
  node-b   holds  NNNN keys
  node-c   holds  NNNN keys
took node-? offline
served read and write with node-? offline (W=2, R=2)
node-? rejoined and was read-repaired to the latest value
delete replicated and observed on read
clusterdemo: PASS
```

Checks: with N=3 there are exactly 3 copies per key, so the three per-node counts sum to
three times the key count (6000 for 2000 keys); the read and write both succeed while a
node is offline; the rejoined node ends up holding the latest value (read repair); and the
final PASS with exit code 0.

### Scenario B (reconciliation)

- Vector clock tests PASS: equal, before, after, and concurrent are all classified
  correctly, and merge and increment do not mutate their inputs.
- Reconcile tests PASS: a causal descendant wins even with an older timestamp; concurrent
  versions fall back to the higher timestamp; a causally later tombstone wins.
- Versioned round-trip tests PASS: encode then decode preserves value, clock, timestamp,
  and the deleted flag.

### Scenario C (quorum and repair)

- Write tests PASS: a write succeeds when W replicas ack despite one failure, and returns
  ErrWriteQuorum when too few ack.
- Read tests PASS: a read returns ErrReadQuorum when fewer than R respond, and a normal
  read pushes the winning version to a replica that was behind (read repair).

### Scenario D (parameterized, on disk)

- The test PASS line, plus `-v` log lines: each key's replica set (its preference list),
  the reconciled update, the failover-and-repair line, and the delete.
- `ls -R "$HELIX_MANUAL_DIR"` shows subdirectories `node-1`, `node-2`, `node-3`, each with
  its own `000001.wal` and `MANIFEST`. With N=3 on a 3-node cluster every node holds every
  key, so all three directories are populated. If you did not set HELIX_MANUAL_DIR, the data
  is under `$TMPDIR/helix-cluster-manual` (the absolute path is printed in the final `-v`
  log line).

### Automated suite

- Section 6 shows every listed test PASS, every package `ok`, and `make check` exiting 0
  with no race detector warnings.

---

## 9. Troubleshooting

Setup and toolchain
- `go: command not found` or a version below go1.22: run section 2a, then `source ~/.bashrc`.
- `go: cannot find main module`: you are not inside the repository. `cd "$HELIX_HOME"`.
- `make: command not found`: build directly with `go build -o bin/clusterdemo ./cmd/clusterdemo`.
- `use of internal package ... not allowed`: you are importing `internal/cluster` from
  outside the module. The parameterized scenario avoids this by placing its test inside the
  package; keep that file under `internal/cluster/`.

Quorum and replication
- `clusterdemo: FAIL: ... write quorum not met`: fewer than W replicas were reachable.
  With the demo's own settings this should not happen; if you changed N/R/W, ensure W and R
  do not exceed the number of reachable nodes.
- `cluster: R (..) and W (..) must not exceed N (..)`: NewCluster rejected the settings.
  Pick R and W less than or equal to N.
- A write or read hangs: not expected in-process. If you added artificial delays, remember
  the coordinator waits for all preference-list responses on a read; an unreachable node
  returns immediately via the transport, it does not block.
- Read repair did not heal a node: repair only touches replicas that responded to the read.
  A node still deregistered at read time is skipped (that is hinted handoff, a later phase).
  Confirm the node was re-registered before the repairing Get.

Concurrency
- `go test -race` reports a DATA RACE: capture it and treat it as a real defect in the
  coordinator fan-out, the ring lock, or a node's read-modify-write, not a flake. Save it
  with `go test -race ./internal/cluster/ 2>&1 | tee /tmp/helix-race.txt` and share it.

Data and disk
- `ls` of the manual scenario dir reports "No such file or directory": go test runs with
  its working directory set to the package directory, so a relative BaseDir lands under
  `internal/cluster`. Set `HELIX_MANUAL_DIR` to an absolute path (as section 7 does), or
  read the absolute path from the test's final `-v` log line.
- Leftover `manual_scenario_test.go` recreating the manual data dir on later `go test ./...`
  runs: remove it and the directory per the cleanup step in Scenario D.
- `no space left on device`: the demo writes to a temp directory under `$TMPDIR`. Free
  space or point `TMPDIR` at a larger location.

Services and integration
- Looking for a port, URL, or health endpoint to curl: there is none in this phase. The
  cluster is in-process with no network listener until Phase 7. Health is section 5.
- Looking for connection strings or credentials: there are none. Each node is an embedded
  engine; its only state is its own subdirectory under BaseDir.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above
  rather than typing. If the prompt looks corrupted after a large paste, run `reset` or
  open a new Cloud Shell tab.
