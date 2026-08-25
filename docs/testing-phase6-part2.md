# Testing Phase 6 Part 2: Merkle-tree anti-entropy repair

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 6
Part 2. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Phase 6 Part 2 adds anti-entropy repair: two replicas build Merkle trees over the keys they
both replicate, compare tree hashes to find which buckets differ, and exchange only those
buckets' entries to reconcile. This closes the gap hinted handoff (Part 1) leaves: a replica
that missed a write, was never read again, and had no hint. It also adds Engine.Scan, a full
key enumeration the storage engine previously lacked. The nodes still run in-process, so
several items from a typical service runbook do not apply, and saying so is more useful than
inventing them:

- Separate processes, ports, network listeners: none. The cluster runs in one process. A
  network transport is a later phase.
- External databases, caches, brokers: none. Each node is its own embedded engine writing
  to its own subdirectory. The "database volume" is that directory tree.
- Credentials, API keys, tokens, connection URLs, request payloads: none. The equivalent
  inputs are node IDs, key-value pairs, which node goes offline, and the N/R/W/MaxHints
  settings, all given as dummy values below.
- Environment variables: the cluster is configured in code through cluster.Options, not
  through HELIX_* variables.

What Part 2 adds, and therefore what we verify: Engine.Scan enumerates live keys correctly
(merging memtable and SSTables, skipping tombstones, sorted); two identical datasets produce
equal Merkle roots and an empty diff; a diff finds exactly the changed buckets; anti-entropy
heals a replica missing a co-replicated key; a repaired cluster reconciles nothing on a
second round (idempotent); and, the headline, a replica that missed a write during an outage
with hinting disabled and was never read is healed by anti-entropy.

Note on scope: this is Part 2, which completes Phase 6. It builds on the versioned values
from Phase 4 and the hinted handoff from Part 1. Anti-entropy here repairs all node pairs
scoped to co-replicated keys; per-token-range trees and scheduled background repair are
noted as later refinements.

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

# 1d. Is the Phase 6 Part 2 code present?
for f in \
  internal/cluster/merkle.go \
  internal/cluster/repair.go \
  internal/cluster/repair_test.go \
  internal/storage/scan_test.go \
  cmd/repairdemo/main.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done

# 1e. Confirm the anti-entropy wiring exists (Engine.Scan, MerkleTree/BucketEntries, AntiEntropy).
grep -q 'func (e \*Engine) Scan' "$HELIX_HOME/internal/storage/engine.go" 2>/dev/null \
  && echo "present: Engine.Scan" || echo "MISSING: Engine.Scan (storage predates Part 2)"
grep -q 'func (c \*Cluster) AntiEntropy' "$HELIX_HOME/internal/cluster/repair.go" 2>/dev/null \
  && echo "present: Cluster.AntiEntropy" || echo "MISSING: Cluster.AntiEntropy"
grep -q 'MerkleTree' "$HELIX_HOME/internal/cluster/transport.go" 2>/dev/null \
  && echo "present: MerkleTree on Replica" || echo "MISSING: MerkleTree on Replica interface"
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

### 2c. Update an existing checkout (only if 1d or 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only        # if Phase 6 Part 2 is merged
#   or: git switch phase-6/anti-entropy && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2d. Resolve dependencies (safe any time)

Helix is standard-library only (crypto/sha256 and encoding/binary for the Merkle tree are
both stdlib), so this downloads nothing but confirms a clean module.

```bash
cd "$HELIX_HOME"
go mod download
go mod verify   # expect: all modules verified
```

### 2e. Data directories

Each node creates its own subdirectory on demand under the base directory the code passes.
The demo uses a private temp directory it deletes on exit, so nothing needs provisioning.
Merkle trees and hints are in-memory only; repair leaves no separate on-disk artifact beyond
the normal per-node data.

---

## 3. Configure the required environment variables and services

There are no services to configure and no environment variables on this path. The cluster is
configured in code through cluster.Options. This table documents each field and the dummy
values this runbook uses, plus the Merkle constant.

| Option / input | Meaning | Dummy value used here |
| --- | --- | --- |
| node IDs | Members of the cluster | `node-a`, `node-b`, `node-c` (three) |
| N | Replication factor | 3 (equal to node count, so every node co-replicates every key) |
| R | Read quorum | 1 |
| W | Write quorum | 2 |
| MaxHints | Fallback nodes for hints; negative disables hinting | -1 (disabled, to create a gap only anti-entropy can close) |
| VNodes | Virtual nodes per physical node | 128 |
| BaseDir | Parent directory; node `x` stores data in `BaseDir/x` | temp dir |
| Storage.SyncWrites | fsync each write on every node | false in tests (speed) |
| merkleLeaves | Fixed leaf-bucket count in every Merkle tree (compile-time constant) | 256 (not configurable) |
| divergent key / value | The write missed during the outage | `account:42` = `balance-100` |
| which node fails | The divergence event under test | the third preferred replica for the key |
| credentials, URLs, payloads | not applicable in this phase | none |

Why N equals the node count: with N=3 on a 3-node cluster, every node is in every key's
preference list, so the co-replication filter accepts all keys and anti-entropy between any
two nodes reconciles everything. With N smaller than the cluster, repair is correctly scoped
to only the keys a given pair both replicate; the tests cover the N-equals-node-count case
for clarity, and the scoping logic is exercised by the co-replication filter regardless.

---

## 4. Start the backend and supporting services

There is no long-lived server and no supporting service. The Phase 6 Part 2 vehicle is the
`repairdemo` binary, a self-verifying tool that creates divergence during an outage with
hinting disabled, runs an anti-entropy round, and confirms the missing replica is healed and
a second round reconciles nothing.

```bash
cd "$HELIX_HOME"
make demos           # builds ./bin/repairdemo (and the other demos)
ls -l ./bin/
# If make is unavailable: go build -o bin/repairdemo ./cmd/repairdemo
```

---

## 5. Verify service and backend health

Health means it builds, static analysis is clean, and the self-verifying demo runs to PASS.

```bash
cd "$HELIX_HOME"
make fmt
make vet          # expect no output, zero exit code
./bin/repairdemo
echo "exit code: $?"   # expect 0
```

Expected: lines showing seeded shared data, a write made while a replica is offline (with no
hints buffered), the replica rejoining still missing the write, anti-entropy reconciling it,
and a second round reconciling zero, ending in `repairdemo: PASS`.

---

## 6. Run Phase 6 Part 2 (automated tests)

The authoritative verification is the Go test suite under the race detector. Anti-entropy
calls PutVersioned across replicas and the coordinator fans out writes across goroutines, so
`-race` is the gate that matters most.

```bash
cd "$HELIX_HOME"

# 6a. The cluster and storage packages, verbosely, with the race detector, run repeatedly
#     with no cache since parts of the path are concurrent.
go test -race -count=5 -v ./internal/cluster/ ./internal/storage/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends
cleanly. Section 6a should include the new tests alongside the existing ones:

```
--- PASS: TestEngineScanMergesAndSkipsTombstones
--- PASS: TestEngineScanEarlyStop
--- PASS: TestMerkleIdenticalDataMatches
--- PASS: TestMerkleDiffFindsChangedBuckets
--- PASS: TestClusterAntiEntropyHealsMissingKey
--- PASS: TestClusterAntiEntropyIsIdempotent
--- PASS: TestClusterAntiEntropyHealsOfflineReplicaWithoutHints
--- PASS: (all earlier storage, cluster, and membership tests)
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: anti-entropy heals an outage gap (the demo)

Dummy data (built into the demo): node IDs `node-a`, `node-b`, `node-c`; N=3, R=1, W=2,
MaxHints=-1 (hinting disabled); 20 shared seed keys `seed:00`..`seed:19` = `base`; the
divergent key `account:42` = `balance-100`; the failed node is the third preferred replica.

```bash
cd "$HELIX_HOME"
./bin/repairdemo
```

This one run covers divergence during an outage with no hint, healing by anti-entropy, and
idempotence on a second round. Read the report per section 8.

### Scenario B: the Merkle tree in isolation

```bash
cd "$HELIX_HOME"
go test -race -v -run 'TestMerkleIdenticalDataMatches|TestMerkleDiffFindsChangedBuckets' ./internal/cluster/
```

### Scenario C: Engine.Scan foundation

```bash
cd "$HELIX_HOME"
go test -race -v -run 'TestEngineScan' ./internal/storage/
```

### Scenario D: cluster anti-entropy behavior

```bash
cd "$HELIX_HOME"

# Heals a missing co-replicated key, and is idempotent once converged.
go test -race -v -run 'TestClusterAntiEntropyHealsMissingKey|TestClusterAntiEntropyIsIdempotent' ./internal/cluster/

# The headline case: heals a replica that missed a write during an outage with no hint.
go test -race -v -run 'TestClusterAntiEntropyHealsOfflineReplicaWithoutHints' ./internal/cluster/
```

### Scenario E: parameterized anti-entropy with your own dummy values (on disk)

This lets you supply your own node IDs, quorum and hint settings, seed data, and the
divergent key, then inspect the per-node data directories before and after repair. It drops a
temporary test into the package (so it can reach the internal package), runs it, and is
removed afterward.

Create the test (edit the marked block to your own dummy data):

```bash
cd "$HELIX_HOME"
cat > internal/cluster/manual_scenario_test.go <<'HELIX_EOF'
package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/talifpathan/helix/internal/storage"
)

func TestAntiEntropyManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	n, r, w := 3, 1, 2
	seedCount := 15
	divergentKey := []byte("account:42")
	divergentVal := []byte("balance-100")
	baseDir := os.Getenv("HELIX_MANUAL_DIR")
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "helix-repair-manual")
	}
	// ---------------------------------------------------------

	_ = os.RemoveAll(baseDir)
	c, err := NewCluster(nodeIDs, Options{
		BaseDir: baseDir, VNodes: 128, N: n, R: r, W: w, MaxHints: -1, // hinting disabled
		Storage: storage.Options{SyncWrites: true},
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	for i := 0; i < seedCount; i++ {
		if err := c.Put(ctx, []byte(fmt.Sprintf("seed:%02d", i)), []byte("base")); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	down := c.PreferenceList(divergentKey, n)[n-1]
	t.Logf("%s is replicated to %v; taking %s offline", divergentKey, c.PreferenceList(divergentKey, n), down)
	c.Transport().Deregister(down)

	if err := c.Put(ctx, divergentKey, divergentVal); err != nil {
		t.Fatalf("write during outage (W=%d): %v", w, err)
	}
	t.Logf("wrote %s while %s offline; buffered hints: %d (expect 0, hinting disabled)", divergentKey, down, c.PendingHints())

	downNode := mustManualNode(t, c, down)
	c.Transport().Register(down, downNode)
	if _, found, _ := downNode.GetVersioned(ctx, divergentKey); found {
		t.Fatalf("%s should be missing the write before repair", down)
	}

	reconciled, err := c.AntiEntropy(ctx)
	if err != nil {
		t.Fatalf("anti-entropy: %v", err)
	}
	t.Logf("anti-entropy reconciled %d key(s)", reconciled)
	if vv, found, _ := downNode.GetVersioned(ctx, divergentKey); !found || string(vv.Value) != string(divergentVal) {
		t.Fatalf("%s should have been healed to %q, found=%v value=%q", down, divergentVal, found, vv.Value)
	}

	again, err := c.AntiEntropy(ctx)
	if err != nil {
		t.Fatalf("second round: %v", err)
	}
	if again != 0 {
		t.Fatalf("converged cluster should reconcile 0 on a second round, got %d", again)
	}
	abs, _ := filepath.Abs(baseDir)
	t.Logf("healed and converged; per-node data is under %s", abs)
}

func mustManualNode(t *testing.T, c *Cluster, id string) *LocalNode {
	t.Helper()
	nd, ok := c.Node(id)
	if !ok {
		t.Fatalf("node %s not found", id)
	}
	return nd
}
HELIX_EOF
echo "created internal/cluster/manual_scenario_test.go"
```

Run it (set an absolute directory so the data is inspectable; go test's working directory is
the package directory, not your shell's):

```bash
cd "$HELIX_HOME"
export HELIX_MANUAL_DIR="$HELIX_HOME/data-repair-manual"
go test -race -v -run TestAntiEntropyManualScenario ./internal/cluster/

echo "--- per-node data directories ---"
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

Expected output shape:

```
cluster of 3 nodes, N=3 W=2, hinting disabled; seeded 20 shared keys
took replica node-? offline (no hints will be stored)
wrote "account:42" while node-? was offline; buffered hints: 0
node-? rejoined but is missing the write, and no hint exists to heal it
ran anti-entropy: reconciled 1 key(s)
anti-entropy healed node-?: it now holds the write it missed
second round reconciled 0 keys: replicas are in sync
repairdemo: PASS
```

Checks: buffered hints is 0 (proving this gap is not covered by hinted handoff); the rejoined
replica is missing the write until repair; anti-entropy reconciles it; and a second round
reconciles 0 (the trees match, so no data is exchanged). Exit code 0.

### Scenario B (Merkle)

- Identical PASS: the same entries in a different order produce equal roots and an empty
  diff (order independence, and equality detection with no false differences).
- Diff PASS: a missing key and a changed value are localized to exactly their buckets, and an
  unchanged key's bucket does not appear (barring a hash collision into a changed bucket).

### Scenario C (Engine.Scan)

- Merge and tombstone PASS: Scan returns the newest version of each key across the memtable
  and SSTables, skips a deleted key, and returns keys in ascending order.
- Early stop PASS: returning false from the callback stops iteration.

### Scenario D (cluster anti-entropy)

- Heal missing key PASS: a key written to only one replica ends up on all co-replicas after
  a round.
- Idempotent PASS: a converged cluster reconciles 0 on a second round.
- Offline-without-hints PASS: a replica that missed a write during an outage with hinting
  disabled, never read, is healed by anti-entropy. This is the gap Part 1 cannot close.

### Scenario E (parameterized)

- The test PASS line, plus `-v` log lines: the preference list, the write during the outage,
  buffered hints of 0, the reconciled count, and convergence (0) on the second round.
- `ls -R "$HELIX_MANUAL_DIR"` shows the per-node subdirectories. Before repair the once-down
  node lacks the divergent key; after the test runs (which repairs before it ends) all
  co-replicas hold it. If you did not set HELIX_MANUAL_DIR, the data is under
  `$TMPDIR/helix-repair-manual` (the absolute path is printed in the final `-v` log line).

### Automated suite

- Section 6 shows every listed test PASS, every package `ok`, and `make check` exiting 0 with
  no race detector warnings.

---

## 9. Troubleshooting

Setup and toolchain
- `go: command not found` or a version below go1.22: run section 2a, then `source ~/.bashrc`.
- `go: cannot find main module`: you are not inside the repository. `cd "$HELIX_HOME"`.
- `make: command not found`: build directly with `go build -o bin/repairdemo ./cmd/repairdemo`.
- `use of internal package ... not allowed`: you are importing `internal/cluster` from
  outside the module. The parameterized scenario avoids this by placing its test inside the
  package; keep that file under `internal/cluster/`.

Anti-entropy behavior
- `repairdemo: FAIL: ... should have been healed by anti-entropy`: the divergent key was not
  co-replicated by the pair being repaired, or the down node's tree was not compared. With
  N equal to the node count every node co-replicates every key, so confirm N matches the
  number of nodes in your scenario, or that the key's preference list includes the node you
  expect to heal.
- `second round reconciled N keys` with N greater than 0: the cluster did not converge in one
  round, which points to a repair that pushed data only one way. Repair is bidirectional
  (each side's entries are applied to the other); if you modified it, check both loops run.
- A repair copies a key onto a node that should not hold it: the co-replication filter is not
  being applied. RepairPair must scope with coReplicationFilter; a raw Repair with a nil
  filter compares all keys and will spread data across the partition. Use RepairPair or
  AntiEntropy, which scope automatically.
- Anti-entropy heals nothing when you expected divergence: the two replicas may already be in
  sync because a healthy write reached all N replicas (the coordinator writes to every
  preferred replica). To create a genuine gap, take a replica offline before the write and
  disable hinting (MaxHints -1), as the demo and the offline-without-hints test do.

Merkle and Scan
- Two datasets you believe are identical show a nonempty diff: the encoded versions differ.
  The leaf hash covers value, clock, timestamp, and tombstone flag, so a differing timestamp
  alone changes the bucket. That is correct behavior, not a bug.
- Engine.Scan returns a deleted key: it should skip engine-level tombstones. Cluster-level
  deletes are stored as a versioned value with a tombstone flag (a live engine record), so
  they are returned by Scan by design and carried through anti-entropy so deletes propagate.
- `go test -race` reports a DATA RACE: treat it as a real defect, not a flake. Capture it
  with `go test -race ./internal/cluster/ ./internal/storage/ 2>&1 | tee /tmp/helix-race.txt`
  and share it.

Data and disk
- `ls` of the manual scenario dir reports "No such file or directory": go test runs with its
  working directory set to the package directory, so a relative BaseDir lands under
  `internal/cluster`. Set `HELIX_MANUAL_DIR` to an absolute path (as section 7 does), or read
  the absolute path from the test's final `-v` log line.
- Leftover `manual_scenario_test.go` recreating the manual data dir on later `go test ./...`
  runs: remove it and the directory per the cleanup step in Scenario E.

Services and integration
- Looking for a port, URL, or health endpoint to curl: there is none in this phase. The
  cluster is in-process with no network listener until a later phase. Health is section 5.
- Looking for connection strings or credentials: there are none. Each node is an embedded
  engine; its only durable state is its own subdirectory under BaseDir.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above
  rather than typing. If the prompt looks corrupted after a large paste, run `reset` or open
  a new Cloud Shell tab.
