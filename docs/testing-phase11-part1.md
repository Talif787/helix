# Testing Phase 11 Part 1: the simulation foundation

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 11
Part 1. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 1 is the foundation of the simulation testing phase. It adds a package that drives the
real cluster in a single process through a controllable, fault-injecting network, so tests can
create partitions and message loss and assert how the cluster responds. What applies:

- A Go test suite: the entire deliverable is code plus tests. "Running Part 1" means running
  those tests.
- The real coordinator and storage engine: the simulation wires the actual cluster components
  behind a transport that satisfies the existing interface, so it exercises real quorum logic,
  not a mock.

What is different, and simpler, this part:

- No Docker, no containers, no compose. This is in-process Go.
- No protoc or code generation. No .proto changed.
- No long-running service, no ports, no HTTP, no gRPC on the wire. Everything is in memory.
- No external database, broker, credentials, or environment variables.
- No new dependencies. The package uses the standard library plus the existing internal
  packages.

Boundary worth stating so results are not misread:

- This is a seeded, fault-injecting simulation, not a bit-for-bit deterministic scheduler. The
  coordinator runs concurrent requests on real goroutines, so goroutine interleaving is not
  controlled. What is reproducible is the partition schedule (set explicitly by the test) and,
  in sequential use, the fault draws. That is enough to replay the high-value failures.

What Part 1 adds and this runbook verifies: a seed produces reproducible fault draws; a
simulated cluster replicates a write with no faults; under a partition the majority side stays
available while the isolated minority cannot reach quorum; and after the partition heals,
availability and quorum-correct reads return.

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

# 1c. protoc is NOT needed for this part (no proto changed).
protoc --version 2>/dev/null || echo "note: protoc absent (fine; Part 1 needs no codegen)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 11 Part 1 source present?
for f in \
  internal/simulation/network.go \
  internal/simulation/cluster.go \
  internal/simulation/simulation_test.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done

# 1f. Dependencies present, pinned, and vendored (Phase 10 vendored the module).
grep -q 'google.golang.org/grpc' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "present: grpc in go.mod" || echo "MISSING: grpc dep (restore per 2e)"
[ -f "$HELIX_HOME/vendor/modules.txt" ] && echo "present: vendor/ tree" || echo "note: no vendor/ (fine unless building offline)"
head -3 "$HELIX_HOME/go.mod" 2>/dev/null | grep -q 'go 1.22' \
  && echo "go.mod pinned to 1.22" || echo "note: check the go directive"
```

Interpretation: below go1.22 -> 2a; 1d MISSING -> 2b; 1e MISSING while 1d FOUND -> 2c; 1f
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

### 2c. Update an existing checkout (only if 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only          # if Phase 11 Part 1 is merged
#   or: git switch phase-11/sim-foundation && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2d. protoc (not needed for Part 1)

Skip. No contract changed in this part.

### 2e. Restore dependencies if go.mod lost them (only if 1f showed grpc missing)

```bash
cd "$HELIX_HOME"
git checkout -- go.mod go.sum
grep 'google.golang.org/grpc ' go.mod
head -3 go.mod
```

---

## 3. Configure the required environment variables and services

None. This part has no environment variables, no services, no database, no ports, and no
credentials. The simulated cluster is built entirely in code, and every input the tests need is
a literal in the test source. There is nothing to configure.

The dummy values the tests use (all built in) are:

| Input | Meaning | Dummy value |
| --- | --- | --- |
| node ids | simulated cluster members | `n0`, `n1`, `n2` |
| N / R / W | replication factor and quorums | 3 / 2 / 2 |
| seeds | fault RNG seeds (reproducibility) | 42 and 43 (differ); 1 (cluster tests) |
| drop probability | per-call drop chance in the reproducibility test | 0.5 |
| sample key/value | replicated write | `k`=`v`, `key`=`majority`, `k`=`during-split` |
| partition target | node isolated from the rest | `n2` |
| data dir | each node's engine storage | the test's temp dir (per node subdir) |

There is no seeding step: the tests create their own clusters and data in temp directories that
Go removes automatically.

---

## 4. Start the backend and supporting services

There is no backend to start. The simulation runs inside the test process. "Starting" it is
building the module and running the tests. Build first to confirm the tree compiles:

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go build ./...
```

No binary, no daemon, no container. Proceed to running the tests.

---

## 5. Verify service and backend health

Health here is: the package compiles, static analysis is clean, and the simulation tests pass.

```bash
cd "$HELIX_HOME"
make fmt
make vet                                   # expect no output, zero exit
GOTOOLCHAIN=local go build ./...           # expect no output
go test ./internal/simulation/             # expect: ok  github.com/talifpathan/helix/internal/simulation
echo "exit code: $?"                       # expect 0
```

There is no running service to health-check; a passing test run is the health signal.

---

## 6. Run Phase 11 Part 1

The simulation spawns concurrent coordinator requests, so run under the race detector.

```bash
cd "$HELIX_HOME"

# 6a. The simulation package, verbosely, with the race detector.
go test -race -v ./internal/simulation/

# 6b. The whole suite with the race detector, to confirm nothing else regressed.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends with
`check passed`. Section 6a should include:

```
--- PASS: TestReproducibleFaults
--- PASS: TestClusterReplicates
--- PASS: TestPartitionQuorum
--- PASS: TestHealRestoresAvailability
--- PASS: TestUnknownNodeAndErrType
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: reproducible fault draws

Dummy data (built in): drop probability 0.5; seeds 42 (twice) and 43. Draws 50 decisions per
seed and asserts the two seed-42 sequences are identical while seed-43 differs.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestReproducibleFaults ./internal/simulation/
```

### Scenario B: replication with no faults (sanity)

Dummy data (built in): 3 nodes `n0`/`n1`/`n2`, N=3, R=2, W=2, seed 1; key `k`=`v`. Writes via
n0, reads via n2.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestClusterReplicates ./internal/simulation/
```

### Scenario C: partition quorum (the headline)

Dummy data (built in): the same 3-node cluster; `n2` is partitioned from `n0` and `n1`. The
majority side writes `key`=`majority` and reads it back; the isolated `n2` fails to write
`key2` and fails to read `key`, because neither can reach the quorum of 2.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestPartitionQuorum ./internal/simulation/
```

### Scenario D: heal restores availability

Dummy data (built in): partition `n2`, write `k`=`during-split` on the majority, heal, then
read `k` via `n2` and expect `during-split` (a quorum read now reaches the others).

```bash
cd "$HELIX_HOME"
go test -race -v -run TestHealRestoresAvailability ./internal/simulation/
```

### Scenario E: unknown node and error contract

Dummy data (built in): a single-node cluster; a write through node `ghost` (not in the cluster)
errors; a gated cross-partition call returns `ErrNodeUnavailable`; a self-call passes.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestUnknownNodeAndErrType ./internal/simulation/
```

### Scenario F: parameterized partition of your choosing

This lets you pick the cluster size, quorum, and which node to isolate, and watch which sides
stay available. It drops a temporary test into the package, runs it, and is removed afterward.

Create the test (edit the marked block):

```bash
cd "$HELIX_HOME"
cat > internal/simulation/manual_scenario_test.go <<'HELIX_EOF'
package simulation

import (
	"context"
	"testing"
)

func TestManualPartition(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	n, r, w := 5, 3, 3
	isolate := []string{"n3", "n4"} // minority of 2; majority is n0,n1,n2
	writeVia := "n0"                // a majority-side node
	readVia := "n4"                 // a minority-side node
	key := []byte("account:42")
	val := []byte("balance-100")
	// ---------------------------------------------------------

	c, err := NewCluster(Config{IDs: ids, N: n, R: r, W: w, BaseDir: t.TempDir(), Seed: 7})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	c.Net.Partition(isolate...)

	// A majority-side write should commit (the 3 majority nodes meet W=3).
	if err := c.Put(ctx, writeVia, key, val); err != nil {
		t.Fatalf("majority write via %s should succeed: %v", writeVia, err)
	}
	t.Logf("majority write via %s OK", writeVia)

	// A minority-side read should fail (only 2 nodes reachable, below R=3).
	if _, err := c.Get(ctx, readVia, key); err == nil {
		t.Fatalf("minority read via %s should fail below quorum", readVia)
	}
	t.Logf("minority read via %s correctly failed below quorum", readVia)

	// After heal, the minority node can read at quorum and sees the value.
	c.Net.Heal()
	got, err := c.Get(ctx, readVia, key)
	if err != nil || string(got) != string(val) {
		t.Fatalf("after heal, read via %s: got %q err %v", readVia, got, err)
	}
	t.Logf("after heal, read via %s returned %q", readVia, got)
}
HELIX_EOF
echo "created internal/simulation/manual_scenario_test.go"
```

Run it (the `-v` flag prints the step-by-step log):

```bash
cd "$HELIX_HOME"
go test -race -v -run TestManualPartition ./internal/simulation/
```

Clean up:

```bash
cd "$HELIX_HOME"
rm -f internal/simulation/manual_scenario_test.go
echo "removed manual scenario test"
```

---

## 8. Verify the expected results

### Scenario A (reproducible faults)

- PASS: the two seed-42 draw sequences match exactly; the seed-43 sequence differs. If the same
  seed produced different draws, the RNG is not seeded deterministically.

### Scenario B (replication)

- PASS: a write via n0 reads back as `v` via n2, confirming the sim wiring routes coordinated
  operations across nodes.

### Scenario C (partition quorum)

- PASS: the majority side writes and reads `majority`; both operations on the isolated `n2`
  return an error. This is the core property: a minority partition cannot reach quorum, and the
  majority stays available.

### Scenario D (heal)

- PASS: after heal, a read via the formerly isolated `n2` returns `during-split`, proving
  availability and quorum-correct reads return once the split clears.

### Scenario E (contract)

- PASS: writing through an unknown node errors; a cross-partition gate returns
  `ErrNodeUnavailable`; a self-call passes.

### Scenario F (parameterized)

- The PASS line plus `-v` logs: the majority write succeeds, the minority read fails below
  quorum, and after heal the minority node reads the value. Adjust the isolate set to make the
  written-through node the minority and you will see the write itself fail instead.

### Automated suite

- Section 6 shows all five simulation tests PASS alongside every earlier test, every package
  `ok`, and `make check` exiting 0 with no race warnings.

---

## 9. Troubleshooting

Build
- `undefined: simulation.NewCluster` or the package is missing: the checkout predates Phase 11.
  Update per 2c and confirm `internal/simulation/` exists.
- `go: go.mod requires go >= 1.25`: hold at 1.22 (`go mod edit -go=1.22`) and rebuild with
  `GOTOOLCHAIN=local`. This part adds no dependency, so do not `go mod tidy` to chase it.
- `inconsistent vendoring`: the vendor tree and go.mod disagree. This part adds no dependency,
  so if it appears, run `GOTOOLCHAIN=local go mod vendor` and confirm no unexpected go.mod
  change, or build with `-mod=mod` to bypass vendoring for a local test run.

Test behavior
- `TestPartitionQuorum` fails with the minority write or read succeeding: the partition is not
  isolating the node, or the quorum is set so a single node satisfies it. With N=3, R=W=2 a lone
  node cannot reach quorum; if you changed the values, re-check that the isolated side has fewer
  than R (or W) nodes.
- `TestPartitionQuorum` fails with the majority write failing: the majority side has fewer than
  W reachable nodes. With one node isolated out of three and W=2, the two remaining nodes meet
  W; if you isolated two of three, the majority can no longer write, which is correct behavior,
  not a bug. Adjust the isolate set.
- `TestReproducibleFaults` fails claiming identical draws for different seeds: extremely
  unlikely; it would mean the seed is not reaching the RNG. Confirm you are on the committed
  code.
- A test hangs: a latency-heavy fault model with a context that never times out could block. The
  committed tests use no latency; if you added `MaxLatency` in Scenario F, give operations a
  context with a timeout.

Race detector
- `go test -race` reports a data race in the simulation: the fault RNG is mutex-guarded and the
  partition map is mutex-guarded, so a race here would be in new code you added. Re-run the
  committed tests alone (`-run 'TestPartition|TestCluster|TestReproducible'`) to confirm the
  baseline is clean.

Test isolation
- Leftover `manual_scenario_test.go` breaks later `go test ./...` runs: remove it per the
  Scenario F cleanup.
- Tests show `(cached)`: add `-count=1` to force a real run.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above rather
  than typing. If the prompt looks corrupted after a large paste, run `reset` or open a new
  Cloud Shell tab.
