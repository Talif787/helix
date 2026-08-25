# Testing Phase 6 Part 1: hinted handoff

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 6
Part 1. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Phase 6 Part 1 adds hinted handoff: when a write cannot reach a preferred replica, the
coordinator parks the write as a hint on a reachable fallback node (a sloppy quorum), and
that hint is replayed to the intended replica once it recovers. The nodes still run
in-process behind the transport interface, so several items from a typical service runbook
still do not apply, and saying so is more useful than inventing them:

- Separate processes, ports, network listeners: none. The cluster runs in one process. A
  network transport is a later phase.
- External databases, caches, brokers: none. Each node is its own embedded engine writing
  to its own subdirectory. The "database volume" is that directory tree.
- Credentials, API keys, tokens, connection URLs, request payloads: none. The equivalent
  inputs are node IDs, key-value pairs, which node goes offline, and the N/R/W/MaxHints
  settings, all given as dummy values below.
- Environment variables: the cluster is configured in code through cluster.Options, not
  through HELIX_* variables.

What Phase 6 Part 1 adds, and therefore what we verify: a write succeeds while a preferred
replica is offline by storing a hint on a fallback (sloppy quorum); the offline replica
holds nothing meanwhile; delivering hints after it recovers heals it; and a write that
cannot reach enough nodes and has no fallback still fails cleanly with a quorum error.

Note on scope: this is Part 1 of Phase 6. Anti-entropy repair with Merkle trees (which
heals a replica that missed a write and was never hinted or read) is Part 2.

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

# 1d. Is the Phase 6 Part 1 code present?
for f in \
  internal/cluster/hint.go \
  internal/cluster/hint_test.go \
  cmd/hintdemo/main.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done

# 1e. Confirm the hinted-handoff wiring exists (PutHint on the Replica interface, MaxHints).
grep -q 'PutHint' "$HELIX_HOME/internal/cluster/transport.go" 2>/dev/null \
  && echo "present: PutHint on Replica" || echo "MISSING: PutHint (tree predates Phase 6 Part 1)"
grep -q 'MaxHints' "$HELIX_HOME/internal/cluster/cluster.go" 2>/dev/null \
  && echo "present: MaxHints option" || echo "MISSING: MaxHints option"
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
git switch main && git pull --ff-only        # if Phase 6 Part 1 is merged
#   or: git switch phase-6/hinted-handoff && git pull --ff-only   # if still on its branch
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
Hints themselves are in-memory only; they leave no on-disk artifact.

---

## 3. Configure the required environment variables and services

There are no services to configure and no environment variables on this path. The cluster
is configured in code through cluster.Options. This table documents each field and the
dummy values this runbook uses.

| Option / input | Meaning | Dummy value used here |
| --- | --- | --- |
| node IDs | Members of the cluster | `node-a`..`node-e` (five, so fallbacks exist) |
| N | Replication factor | 3 |
| R | Read quorum | 2 |
| W | Write quorum | 2 |
| MaxHints | Fallback nodes past the preference list that may hold hints | 2 (zero uses the default, equal to N; negative disables hinting) |
| VNodes | Virtual nodes per physical node | 128 |
| BaseDir | Parent directory; node `x` stores data in `BaseDir/x` | temp dir |
| Storage.SyncWrites | fsync each write on every node | false in tests (speed) |
| sample key / value | The data written during a failure | `account:42` = `balance-100` |
| which node fails | The failure event under test | the third preferred replica for the key |
| credentials, URLs, payloads | not applicable in this phase | none |

Why five nodes: hinted handoff only has somewhere to put a hint when the cluster has more
nodes than the preference list (N). On a 3-node cluster with N=3 there are no fallbacks, so
a down replica's write simply cannot be hinted; five nodes give two fallbacks.

---

## 4. Start the backend and supporting services

There is no long-lived server and no supporting service. The Phase 6 Part 1 vehicle is the
`hintdemo` binary, a self-verifying tool that writes with a replica offline, checks a hint
was buffered, then recovers the replica and confirms the hint healed it.

```bash
cd "$HELIX_HOME"
make demos           # builds ./bin/hintdemo (and the other demos)
ls -l ./bin/
# If make is unavailable: go build -o bin/hintdemo ./cmd/hintdemo
```

---

## 5. Verify service and backend health

Health means it builds, static analysis is clean, and the self-verifying demo runs to PASS.

```bash
cd "$HELIX_HOME"
make fmt
make vet          # expect no output, zero exit code
./bin/hintdemo
echo "exit code: $?"   # expect 0
```

Expected: lines showing the write succeeding with a replica offline, a hint buffered, the
replica holding nothing while offline, and the hint healing it after delivery, ending in
`hintdemo: PASS`.

---

## 6. Run Phase 6 Part 1 (automated tests)

The authoritative verification is the Go test suite under the race detector. The coordinator
fans out writes across goroutines, so `-race` is the gate that matters most.

```bash
cd "$HELIX_HOME"

# 6a. The cluster package, verbosely, with the race detector. Run repeatedly with no cache,
#     since the write path is concurrent (this is the kind of test that must be hammered).
go test -race -count=5 -v ./internal/cluster/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends
cleanly. Section 6a should include the new hint tests alongside the existing ones:

```
--- PASS: TestHintStoreReconcilesAndDrains
--- PASS: TestClusterHintedHandoffHealsRecoveredNode
--- PASS: TestClusterSloppyQuorumWithHints
--- PASS: TestClusterWriteFailsWhenNoFallbacks
--- PASS: TestCoordinatorHintsFailedReplicaToFallback
--- PASS: TestClusterHintlessConfigStillWorks
--- PASS: (all Phase 4 and Phase 5 cluster tests)
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: hinted handoff end to end (the demo)

Dummy data (built into the demo): node IDs `node-a`..`node-e`; N=3, R=2, W=2, MaxHints=2;
key `account:42` with value `balance-100`; the failed node is the third preferred replica.

```bash
cd "$HELIX_HOME"
./bin/hintdemo
```

This one run covers a sloppy-quorum write during a failure, hint buffering, and healing on
recovery. Read the report per section 8.

### Scenario B: the hint store in isolation

```bash
cd "$HELIX_HOME"
go test -race -v -run 'TestHintStoreReconcilesAndDrains' ./internal/cluster/
```

### Scenario C: coordinator and cluster hinting behavior

```bash
cd "$HELIX_HOME"

# A failed preferred replica's write is parked on a fallback.
go test -race -v -run 'TestCoordinatorHintsFailedReplicaToFallback' ./internal/cluster/

# End-to-end heal, sloppy quorum with two replicas down, and clean failure with no fallback.
go test -race -v -run 'TestClusterHintedHandoffHealsRecoveredNode|TestClusterSloppyQuorumWithHints|TestClusterWriteFailsWhenNoFallbacks' ./internal/cluster/

# Hinting disabled still serves a healthy cluster.
go test -race -v -run 'TestClusterHintlessConfigStillWorks' ./internal/cluster/
```

### Scenario D: parameterized hinted handoff with your own dummy values (on disk)

This lets you supply your own node IDs, quorum and hint settings, key-value data, and which
replica fails, then inspect the per-node data directories. It drops a temporary test into
the package (so it can reach the internal package), runs it, and is removed afterward.

Create the test (edit the marked block to your own dummy data):

```bash
cd "$HELIX_HOME"
cat > internal/cluster/manual_scenario_test.go <<'HELIX_EOF'
package cluster

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/talifpathan/helix/internal/storage"
)

func TestHintManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	nodeIDs := []string{"node-1", "node-2", "node-3", "node-4", "node-5"}
	n, r, w, maxHints := 3, 2, 2, 2
	baseDir := os.Getenv("HELIX_MANUAL_DIR")
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "helix-hint-manual")
	}
	key := []byte("account:42")
	value := []byte("balance-100")
	// ---------------------------------------------------------

	_ = os.RemoveAll(baseDir)
	c, err := NewCluster(nodeIDs, Options{
		BaseDir: baseDir, VNodes: 128, N: n, R: r, W: w, MaxHints: maxHints,
		Storage: storage.Options{SyncWrites: true},
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	pref := c.PreferenceList(key, n)
	down := pref[len(pref)-1]
	t.Logf("key %s is replicated to %v; taking %s offline", key, pref, down)
	c.Transport().Deregister(down)

	if err := c.Put(ctx, key, value); err != nil {
		t.Fatalf("write with %s down should meet quorum via a hint: %v", down, err)
	}
	t.Logf("write succeeded; %d hint(s) buffered", c.PendingHints())
	if c.PendingHints() == 0 {
		t.Fatalf("expected a hint buffered for %s", down)
	}

	downNode, _ := c.Node(down)
	if _, found, _ := downNode.GetVersioned(ctx, key); found {
		t.Fatalf("%s should hold nothing while offline", down)
	}

	c.Transport().Register(down, downNode)
	delivered := c.DeliverHints(ctx)
	t.Logf("recovered %s; delivered %d hint(s)", down, delivered)
	if vv, found, _ := downNode.GetVersioned(ctx, key); !found || string(vv.Value) != string(value) {
		t.Fatalf("%s should have been healed to %q, found=%v value=%q", down, value, found, vv.Value)
	}
	if c.PendingHints() != 0 {
		t.Fatalf("expected hints cleared, %d remain", c.PendingHints())
	}
	abs, _ := filepath.Abs(baseDir)
	t.Logf("healed; per-node data is under %s", abs)
}
HELIX_EOF
echo "created internal/cluster/manual_scenario_test.go"
```

Run it (set an absolute directory so the data is inspectable; go test's working directory
is the package directory, not your shell's):

```bash
cd "$HELIX_HOME"
export HELIX_MANUAL_DIR="$HELIX_HOME/data-hint-manual"
go test -race -v -run TestHintManualScenario ./internal/cluster/

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
cluster of 5 nodes, N=3 R=2 W=2; "account:42" is replicated to [node-? node-? node-?]
took preferred replica node-? offline
write succeeded via sloppy quorum; 1 hint(s) buffered on fallback nodes
node-? holds nothing yet, as expected while offline
node-? recovered; delivering hints
delivered 1 hint(s)
hinted handoff complete: node-? now holds the write it missed
hintdemo: PASS
```

Checks: the write succeeds despite a preferred replica being offline; exactly one hint is
buffered; the offline replica holds nothing until recovery; after delivery it holds the
value; and no hints remain. Exit code 0.

### Scenario B (hint store)

- PASS: a later hint for the same key reconciles over an earlier one; intended nodes are
  tracked and sorted; draining removes delivered hints and drops an emptied intended node.

### Scenario C (coordinator and cluster)

- Hints-to-fallback PASS: a failed preferred replica's write lands as a hint on the
  fallback node.
- Heal PASS: the down replica ends up with the hinted write after delivery on recovery.
- Sloppy quorum PASS: with two of three preferred replicas down, the write still meets W=2
  because two fallbacks take hints.
- No-fallback PASS: on a 3-node cluster with two replicas down and no fallback available,
  the write fails with ErrWriteQuorum rather than silently losing data.
- Hintless PASS: with MaxHints negative, a healthy cluster still serves put and get.

### Scenario D (parameterized)

- The test PASS line, plus `-v` log lines: the preference list, the write succeeding with a
  hint, the count of buffered hints, and the heal after delivery.
- `ls -R "$HELIX_MANUAL_DIR"` shows the per-node subdirectories. With N=3 the write reaches
  two preferred replicas plus a hint on a fallback, so those directories are populated; the
  once-offline replica's directory is populated only after `DeliverHints` (which the test
  runs before it ends). If you did not set HELIX_MANUAL_DIR, the data is under
  `$TMPDIR/helix-hint-manual` (the absolute path is printed in the final `-v` log line).

### Automated suite

- Section 6 shows every listed test PASS, every package `ok`, and `make check` exiting 0
  with no race detector warnings.

---

## 9. Troubleshooting

Setup and toolchain
- `go: command not found` or a version below go1.22: run section 2a, then `source ~/.bashrc`.
- `go: cannot find main module`: you are not inside the repository. `cd "$HELIX_HOME"`.
- `make: command not found`: build directly with `go build -o bin/hintdemo ./cmd/hintdemo`.
- `use of internal package ... not allowed`: you are importing `internal/cluster` from
  outside the module. The parameterized scenario avoids this by placing its test inside the
  package; keep that file under `internal/cluster/`.

Hinting behavior
- `hintdemo: FAIL: expected a hint to be buffered`: the cluster had no fallback to hint to.
  Ensure there are more nodes than N and MaxHints is at least 1. The demo uses five nodes
  with N=3 and MaxHints=2.
- A write returns ErrWriteQuorum during a failure: not enough reachable nodes to meet W,
  even with hints. Either fewer than W nodes are up, or MaxHints is too small to reach
  enough fallbacks. This is correct behavior when the cluster genuinely cannot satisfy the
  quorum (see the no-fallback test).
- The recovered node still holds nothing after recovery: you must call DeliverHints after
  re-registering the node. Delivery is triggered, not automatic, in this phase.
- A value written during a failure is not readable yet: reads are served from the
  preference list only in this phase. A hint on a fallback becomes readable after the
  intended replica recovers and the hint is delivered. This is the documented trade-off.

Concurrency and determinism
- `go test -race` reports a DATA RACE: treat it as a real defect in the coordinator fan-out
  or the hint store, not a flake. Capture it with
  `go test -race ./internal/cluster/ 2>&1 | tee /tmp/helix-race.txt` and share it.
- A test fails intermittently: the hint tests are deterministic (no timers). If one flakes,
  suspect a real concurrency bug and capture a race report as above.

Data and disk
- `ls` of the manual scenario dir reports "No such file or directory": go test runs with
  its working directory set to the package directory, so a relative BaseDir lands under
  `internal/cluster`. Set `HELIX_MANUAL_DIR` to an absolute path (as section 7 does), or
  read the absolute path from the test's final `-v` log line.
- Leftover `manual_scenario_test.go` recreating the manual data dir on later `go test ./...`
  runs: remove it and the directory per the cleanup step in Scenario D.

Services and integration
- Looking for a port, URL, or health endpoint to curl: there is none in this phase. The
  cluster is in-process with no network listener until a later phase. Health is section 5.
- Looking for connection strings or credentials: there are none. Each node is an embedded
  engine; its only durable state is its own subdirectory under BaseDir, and hints are in
  memory only.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above
  rather than typing. If the prompt looks corrupted after a large paste, run `reset` or
  open a new Cloud Shell tab.
