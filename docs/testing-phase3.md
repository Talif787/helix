# Testing Phase 3: partitioning with a consistent-hash ring and coordinator

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 3.
It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Phase 3 partitions the keyspace across several nodes using a consistent-hash ring, with a
coordinator that routes each key to its owning node. Crucially, in this phase the nodes
run in-process behind a transport interface. That means several items from a typical
service runbook still do not apply, and saying so is more useful than inventing them:

- Separate processes, ports, or network listeners: none. The cluster is several storage
  engines and a ring inside one process. The transport is an in-process function call.
  Networked nodes with gRPC arrive in Phase 7.
- External databases, caches, or brokers: none. Each node is its own embedded storage
  engine writing to its own subdirectory. The "database volume" is that directory tree.
- Credentials, API keys, tokens, connection URLs, request payloads: none. The equivalent
  inputs are node IDs and key-value pairs, all provided as dummy values below.
- Environment variables: the cluster is configured in code through cluster.Options
  (node IDs, virtual-node count, base directory), not through HELIX_* variables. Those
  variables still configure the single-node kvnode, but they are not on the Phase 3 path.

What Phase 3 adds, and therefore what we verify: keys spread across nodes, every read
routes back to the key's owner, a key's data lives only on its owner, routing is
deterministic and stable, membership changes move only the expected keys, and the error
paths (empty ring, unregistered owner, missing key) behave.

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
gh --version 2>/dev/null || echo "note: GitHub CLI not found (only needed for PRs, not tests)"

# 1c. Does the repository already exist here?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"
  git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1d. Is the Phase 3 code present? These files are new this phase.
for f in \
  internal/cluster/ring.go \
  internal/cluster/coordinator.go \
  internal/cluster/cluster.go \
  internal/cluster/transport.go \
  internal/cluster/node.go \
  cmd/clusterdemo/main.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done

# 1e. Does the Makefile have the demos target that builds clusterdemo?
grep -q '^demos:' "$HELIX_HOME/Makefile" 2>/dev/null \
  && echo "present: Makefile demos target" \
  || echo "MISSING: Makefile demos target"
```

Interpretation:
- 1a below go1.22: follow 2a.
- 1c MISSING: follow 2b.
- 1d or 1e MISSING while 1c is FOUND: your checkout predates Phase 3; follow 2c.

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

### 2c. Update an existing checkout to Phase 3 (only if 1d or 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only        # if Phase 3 is merged
#   or: git switch phase-3/partitioning && git pull --ff-only   # if still on its branch
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

The cluster creates its per-node directories on demand under whatever base directory the
code passes. The demo uses a private temp directory it deletes on exit, so there is
nothing to provision. The optional on-disk scenario in section 7 uses
`./data-cluster-manual`; it is cleared at the start of that scenario.

---

## 3. Configure the required environment variables and services

There are no services to configure and no environment variables on the Phase 3 path. The
cluster is configured in code through `cluster.Options`. This table documents each field
and the dummy values this runbook uses, so you can change them in the parameterized
scenario in section 7.

| Option / input | Meaning | Dummy value used here |
| --- | --- | --- |
| node IDs | The set of nodes in the cluster (also used as ring keys and data subdir names) | `node-a`, `node-b`, `node-c` (demo); `node-1`, `node-2`, `node-3` (manual) |
| VNodes | Virtual nodes per physical node on the ring | 128 (DefaultVNodes) |
| BaseDir | Parent directory; node `x` stores data in `BaseDir/x` | temp dir (demo); `./data-cluster-manual` (manual) |
| Storage.SyncWrites | fsync each write on every node's engine | true |
| sample keys | The data to route and store | `user:1001`, `user:1002`, `order:5001`, and `key:00000`..`key:02999` |
| sample values | Values paired with the keys | `alice`, `bob`, `widget`, and `val:00000`.. |
| credentials, URLs, payloads | not applicable in this phase | none |

---

## 4. Start the backend and supporting services

There is no long-lived server and no supporting service. The Phase 3 vehicle is the
`clusterdemo` binary, a self-verifying tool that builds a three-node cluster, exercises
it, prints a report, and exits (0 on success, non-zero on any failed check).

```bash
cd "$HELIX_HOME"

# Build the demo tools (sstdemo and clusterdemo).
make demos
ls -l ./bin/

# If make is unavailable, build directly:
#   go build -o bin/clusterdemo ./cmd/clusterdemo
```

---

## 5. Verify service and backend health

Health for this phase means: it builds, static analysis is clean, and the self-verifying
demo runs to PASS.

```bash
cd "$HELIX_HOME"

# 5a. Static health.
make fmt
make vet    # expect no output, zero exit code

# 5b. Liveness and correctness in one shot: the demo brings up a cluster and checks it.
./bin/clusterdemo
echo "exit code: $?"   # expect 0
```

Expected: a per-node distribution report, a read-back confirmation, three sample keys each
shown owned by one node and absent on the others, and a final `clusterdemo: PASS` line
with exit code 0. Exact per-node counts vary by hash but always sum to 3000 with every
node non-zero.

---

## 6. Run Phase 3 (automated tests)

The authoritative verification is the Go test suite under the race detector. The ring's
read-write lock and concurrent routing are the concurrency surface this phase adds.

```bash
cd "$HELIX_HOME"

# 6a. Just the cluster package, verbosely, with the race detector.
go test -race -v ./internal/cluster/

# 6b. The whole suite with the race detector (also re-runs storage tests, confirming the
#     new package did not disturb anything).
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends
cleanly. Section 6a should include:

```
--- PASS: TestRingLookupEmpty
--- PASS: TestRingDeterministicRegardlessOfAddOrder
--- PASS: TestRingDistributionSpread
--- PASS: TestRingRemoveOnlyMovesRemovedNodesKeys
--- PASS: TestRingAddOnlyStealsToNewNode
--- PASS: TestRingLookupNDistinctAndOrdered
--- PASS: TestInProcessTransportRegistration
--- PASS: TestCoordinatorErrorsWithoutNodes
--- PASS: TestCoordinatorErrorsWhenOwnerUnregistered
--- PASS: TestCoordinatorRoutesToOwner
--- PASS: TestClusterPutGetDelete
--- PASS: TestClusterDataLocality
--- PASS: TestClusterRoutingIsStable
--- PASS: TestClusterMissingKey
--- PASS: TestClusterRejectsDuplicateNodeID
--- PASS: TestClusterRejectsEmptyMembership
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: partitioning end to end (the demo)

Dummy data (built into the demo): node IDs `node-a`, `node-b`, `node-c`; keys
`key:00000`..`key:02999`; values `val:00000`.. .

```bash
cd "$HELIX_HOME"
./bin/clusterdemo
```

This single run covers distribution, coordinator read-back, data locality, deterministic
ownership, and delete. Read the report per section 8.

### Scenario B: ring properties in isolation

These target the consistent-hashing guarantees with deterministic inputs.

```bash
cd "$HELIX_HOME"

# Determinism and even spread.
go test -race -v -run 'TestRingDeterministicRegardlessOfAddOrder|TestRingDistributionSpread' ./internal/cluster/

# Membership change moves only the expected keys.
go test -race -v -run 'TestRingRemoveOnlyMovesRemovedNodesKeys|TestRingAddOnlyStealsToNewNode' ./internal/cluster/

# Preference list is distinct and primary-first (used by replication later).
go test -race -v -run 'TestRingLookupNDistinctAndOrdered' ./internal/cluster/
```

### Scenario C: coordinator routing and error paths

```bash
cd "$HELIX_HOME"

# Coordinator sends each key to the same node the ring names.
go test -race -v -run 'TestCoordinatorRoutesToOwner' ./internal/cluster/

# Empty ring and unregistered owner return typed errors.
go test -race -v -run 'TestCoordinatorErrorsWithoutNodes|TestCoordinatorErrorsWhenOwnerUnregistered' ./internal/cluster/
```

### Scenario D: parameterized cluster with your own dummy values (on disk)

This scenario lets you supply your own node IDs and key-value pairs and then inspect the
per-node data directories to see the partitioning on disk. It works by dropping a
temporary test into the package, running it, and removing it.

Create the test (edit the values in the marked block to your own dummy data):

```bash
cd "$HELIX_HOME"
cat > internal/cluster/manual_scenario_test.go <<'HELIX_EOF'
package cluster

import (
	"context"
	"os"
	"testing"

	"github.com/talifpathan/helix/internal/storage"
)

func TestClusterManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	nodeIDs := []string{"node-1", "node-2", "node-3"}
	baseDir := "./data-cluster-manual"
	vnodes := 128
	pairs := map[string]string{
		"user:1001":  "alice",
		"user:1002":  "bob",
		"user:1003":  "carol",
		"order:5001": "widget",
		"order:5002": "gadget",
		"order:5003": "sprocket",
	}
	deleteKey := "user:1002"
	// ---------------------------------------------------------

	_ = os.RemoveAll(baseDir)
	c, err := NewCluster(nodeIDs, Options{
		BaseDir: baseDir,
		VNodes:  vnodes,
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
	for k, want := range pairs {
		got, err := c.Get(ctx, []byte(k))
		if err != nil {
			t.Fatalf("get %s: %v", k, err)
		}
		if string(got) != want {
			t.Fatalf("get %s: want %s got %s", k, want, got)
		}
		owner, _ := c.OwnerOf([]byte(k))
		t.Logf("key %-11s value %-9s owner %s", k, want, owner)
	}

	if err := c.Delete(ctx, []byte(deleteKey)); err != nil {
		t.Fatalf("delete %s: %v", deleteKey, err)
	}
	if _, err := c.Get(ctx, []byte(deleteKey)); err == nil {
		t.Fatalf("expected %s to be deleted", deleteKey)
	}
	t.Logf("deleted %s; per-node data is under %s", deleteKey, baseDir)
}
HELIX_EOF
echo "created internal/cluster/manual_scenario_test.go"
```

Run it and inspect the on-disk partitioning:

```bash
cd "$HELIX_HOME"
go test -race -v -run TestClusterManualScenario ./internal/cluster/

echo "--- per-node data directories (each node has its own WAL and MANIFEST) ---"
ls -R ./data-cluster-manual
```

Clean up when finished (both the temporary test and its data):

```bash
cd "$HELIX_HOME"
rm -f internal/cluster/manual_scenario_test.go
rm -rf ./data-cluster-manual
echo "removed manual scenario test and its data"
```

---

## 8. Verify the expected results

### Scenario A (demo)

Expected output shape (counts vary, structure does not):

```
seeded 3000 keys across 3 nodes
  node-a    NNNN keys (XX.X%)
  node-b    NNNN keys (XX.X%)
  node-c    NNNN keys (XX.X%)
verified read-back of all 3000 keys through the coordinator
  key:00000 owned by node-? and absent on the other nodes
  key:01500 owned by node-? and absent on the other nodes
  key:02999 owned by node-? and absent on the other nodes
deleted 3 keys and confirmed a neighbor survived
clusterdemo: PASS
```

Checks to confirm: three nodes each with a non-zero share summing to 3000; the read-back
line; each sample key owned by exactly one node and absent elsewhere (this is the proof
that data is partitioned, not replicated); and the final `PASS` with exit code 0.

### Scenario B (ring)

- Determinism and distribution tests PASS: ownership is independent of add order and no
  node is starved or dominant.
- Removal test PASS: after removing a node, every key that was not on it keeps its owner,
  and no key still points at the removed node.
- Addition test PASS: after adding a node, keys either stay put or move onto the new node,
  never between existing nodes.
- Preference-list test PASS: LookupN returns distinct nodes, primary first, and returns
  all nodes when asked for more than exist.

### Scenario C (coordinator)

- Routing test PASS: for every sample key the coordinator reaches the same node the ring
  names.
- Error tests PASS: an empty ring returns ErrNoNodes; a ring naming an owner absent from
  the transport returns ErrNodeUnavailable.

### Scenario D (parameterized, on disk)

- The test PASS line, plus `-v` log lines mapping each key to its value and owner, for
  example `key user:1001  value alice     owner node-3`.
- `ls -R ./data-cluster-manual` shows three subdirectories `node-1`, `node-2`, `node-3`,
  each containing its own `000001.wal` and `MANIFEST`. Separate directories per node is
  the on-disk evidence that the keyspace is split across independent engines.

### Automated suite

- Section 6 shows every listed test PASS, every package `ok`, and `make check` exiting 0
  with no race detector warnings.

---

## 9. Troubleshooting

Setup and toolchain
- `go: command not found` or a version below go1.22: run section 2a, then `source ~/.bashrc`.
- `go: cannot find main module`: you are not inside the repository. `cd "$HELIX_HOME"`.
- `make: command not found`: build directly with `go build -o bin/clusterdemo ./cmd/clusterdemo`.
- `use of internal package ... not allowed`: you are trying to import `internal/cluster`
  from outside the module. The parameterized scenario in section 7 avoids this by placing
  its test inside the package; keep that file under `internal/cluster/`.

Demo and tests
- `clusterdemo: FAIL: ...`: the message names the failed check. "received no keys" points
  at a ring or membership problem; "want X got Y" points at routing or storage. Rerun the
  matching focused test from section 6a with `-v` for detail.
- A node shows 0 keys, or all keys on one node: a degenerate ring. Confirm VNodes is at
  least a few dozen (the default is 128) and that more than one node id was passed.
- `go test -race` reports a DATA RACE: capture it and treat it as a real defect in the
  ring lock or the transport, not a flake. Save it with
  `go test -race ./internal/cluster/ 2>&1 | tee /tmp/helix-race.txt` and share that file.
- Leftover `manual_scenario_test.go` causing `go test ./...` to create `data-cluster-manual`
  on later runs: remove it and the directory per the cleanup step in Scenario D.

Data and disk
- `no space left on device` while running the demo: it writes to a temp directory under
  `$TMPDIR`. Free space or set `TMPDIR` to a larger location before running.
- Wanting to see the data but the demo cleaned it up: the demo intentionally uses a
  self-deleting temp directory. Use Scenario D, which writes to `./data-cluster-manual`
  and leaves it for inspection.

Services and integration
- Looking for a port, URL, or health endpoint to curl: there is none in this phase. The
  cluster is in-process and has no network listener until Phase 7. Health is section 5.
- Looking for connection strings or credentials: there are none. Each node is an embedded
  engine; its only state is its own subdirectory under BaseDir.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above
  rather than typing. If the prompt looks corrupted after a large paste, run `reset` or
  open a new Cloud Shell tab.
