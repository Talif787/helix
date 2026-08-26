# Testing Phase 11 Part 2: safety invariants under concurrency and partition

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 11
Part 2. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 2 adds the safety-property layer on top of the Part 1 simulation. It records every
operation in a history and checks two invariants a correct quorum store must never violate:

- No fabrication: a successful read never returns a value that was never written.
- Freshness (read-after-write): a successful read never returns a value staler than the most
  recent write that completed before the read began, and never returns not-found once a write
  has completed.

It also adds a concurrent workload driver (one writer per key writing a sequential value series,
plus concurrent readers) and drives it with no faults and under a minority partition.

What applies:

- A Go test suite: the entire deliverable is code plus tests. Running Part 2 means running them.
- The real coordinator, storage engine, and quorum logic, exercised under real concurrency
  through the in-process simulation.

What is different, and simple, this part (same as Part 1):

- No Docker, no protoc, no services, no ports, no HTTP or gRPC on the wire, no database, no
  credentials, no environment variables. Everything is in-process Go.
- No new dependencies, so no re-vendor needed.

The invariant model, stated plainly so results are interpreted correctly:

- The freshness check assumes at most one write per key is in flight at a time. The workload
  enforces this with exactly one writer goroutine per key; readers are unrestricted. Do not use
  CheckFreshness on a history where a key is written concurrently from multiple goroutines.
- A read that is concurrent with a write may legitimately return either the old or the new
  value; the checker treats both as valid. It only flags a read that returns something older
  than a write which had already fully completed, or not-found after a completed write.
- Happens-before comes from a monotonic sequence counter ticked around each operation, so
  "read began after write returned" is a real ordering, not wall-clock guesswork.

What Part 2 adds and this runbook verifies: under concurrency with no faults, both invariants
hold; under a minority partition driven through the majority, every operation succeeds and both
invariants hold; and the checkers themselves fire on known-bad histories (so they are not
passing vacuously).

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
protoc --version 2>/dev/null || echo "note: protoc absent (fine; Part 2 needs no codegen)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 11 Part 2 source present?
for f in \
  internal/simulation/history.go \
  internal/simulation/invariants.go \
  internal/simulation/workload.go \
  internal/simulation/workload_test.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done
# Part 1 must also be present (Part 2 builds on it).
[ -f "$HELIX_HOME/internal/simulation/cluster.go" ] && echo "present: Part 1 foundation" || echo "MISSING: Part 1 foundation"

# 1f. Dependencies present and pinned; this part adds none.
grep -q 'google.golang.org/grpc' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "present: grpc in go.mod" || echo "MISSING: grpc dep (restore per 2e)"
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
git switch main && git pull --ff-only          # if Phase 11 Part 2 is merged
#   or: git switch phase-11/invariants && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2d. protoc (not needed for Part 2)

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
credentials. The simulated cluster and the workload are built entirely in code, and every input
is a literal in the test source. There is nothing to configure.

The dummy values the tests use (all built in) are:

| Input | Meaning | Dummy value |
| --- | --- | --- |
| node ids | simulated cluster members | `n0`, `n1`, `n2` |
| N / R / W | replication factor and quorums | 3 / 2 / 2 |
| keys (no-fault) | workload keys, one writer each | `k0`..`k4` |
| keys (partition) | majority-side workload keys | `a`, `b`, `c` |
| writes per key | sequential value series length | 20 (no-fault), 15 (partition) |
| value pattern | what each write stores | `<key>#<i>`, e.g. `k0#1`, `k0#2`, ... |
| readers | concurrent reader goroutines | 4 (no-fault), 3 (partition) |
| seeds | reader key/node selection RNG | 1 and 2 |
| partition target | isolated minority node | `n2` |
| synthetic values | to make the checkers fire | `k#1`, `k#2`, `real`, `phantom` |

There is no seeding step: the tests create their own clusters and data in temp directories that
Go removes automatically.

---

## 4. Start the backend and supporting services

There is no backend to start; the simulation and workload run inside the test process. Build to
confirm the tree compiles:

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go build ./...
```

No binary, no daemon, no container. Proceed to the tests.

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

## 6. Run Phase 11 Part 2

The workload runs concurrent writers and readers, so run under the race detector.

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
`check passed`. Section 6a should include the Part 1 tests plus, for Part 2:

```
--- PASS: TestWorkloadNoFaults
--- PASS: TestWorkloadMajorityUnderPartition
--- PASS: TestFreshnessCatchesStaleRead
--- PASS: TestFreshnessCatchesLostWrite
--- PASS: TestNoFabricationCatchesPhantom
--- PASS: TestValidHistoryHasNoViolations
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: concurrent workload, no faults (the headline)

Dummy data (built in): 3 nodes, N=3 R=2 W=2; keys `k0`..`k4` each written `k*#1`..`k*#20` by one
writer; 4 concurrent readers; seed 1. Asserts no fabrication and freshness across the whole
recorded history.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestWorkloadNoFaults ./internal/simulation/
```

### Scenario B: majority stays consistent under a minority partition

Dummy data (built in): 3 nodes; `n2` isolated; workload keys `a`,`b`,`c` written 15 times each,
3 readers, driven only through `n0` and `n1`; seed 2. Asserts every operation succeeded and both
invariants hold on the majority side.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestWorkloadMajorityUnderPartition ./internal/simulation/
```

### Scenario C: the checkers actually fire (anti-vacuous)

Dummy data (built in): hand-built histories. A stale read (`k#1` after `k#2` completed) and a
lost write (not-found after a completed write) each produce a freshness violation; a phantom read
(`phantom` never written) produces a fabrication violation. These prove the checkers detect
breaches rather than always passing.

```bash
cd "$HELIX_HOME"
go test -race -v -run 'TestFreshnessCatches|TestNoFabricationCatches' ./internal/simulation/
```

### Scenario D: a valid history passes both checks

Dummy data (built in): a correct sequential history, including a read concurrent with a write
that returns the old value, which is legitimate. Asserts zero violations.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestValidHistoryHasNoViolations ./internal/simulation/
```

### Scenario E: parameterized stress workload

This lets you crank up the concurrency, key count, and write depth to stress the invariants
harder, and optionally isolate a node. It drops a temporary test into the package, runs it, and
is removed afterward.

Create the test (edit the marked block):

```bash
cd "$HELIX_HOME"
cat > internal/simulation/manual_scenario_test.go <<'HELIX_EOF'
package simulation

import (
	"context"
	"fmt"
	"testing"
)

func TestManualStress(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	nodeCount := 5
	n, r, w := 5, 3, 3
	keyCount := 12
	writesPerKey := 40
	readers := 8
	isolate := []string{} // e.g. []string{"n4"} for a minority partition; leave empty for none
	// ---------------------------------------------------------

	ids := make([]string, nodeCount)
	for i := range ids {
		ids[i] = fmt.Sprintf("n%d", i)
	}
	c, err := NewCluster(Config{IDs: ids, N: n, R: r, W: w, BaseDir: t.TempDir(), Seed: 99})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()

	// Nodes to drive through: if isolating, use only the majority side.
	drive := ids
	if len(isolate) > 0 {
		c.Net.Partition(isolate...)
		iso := map[string]bool{}
		for _, id := range isolate {
			iso[id] = true
		}
		drive = drive[:0]
		for _, id := range ids {
			if !iso[id] {
				drive = append(drive, id)
			}
		}
	}

	keys := make([]string, keyCount)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}

	h := RunWorkload(context.Background(), c, WorkloadConfig{
		Keys: keys, WritesPerKey: writesPerKey, Readers: readers, Seed: 99,
		WriterNodes: drive, ReaderNodes: drive,
	})
	ev := h.Events()
	t.Logf("recorded %d operations", len(ev))

	if vs := CheckNoFabrication(ev); len(vs) > 0 {
		t.Fatalf("fabrication violations: %v", vs)
	}
	if vs := CheckFreshness(ev); len(vs) > 0 {
		t.Fatalf("freshness violations: %v", vs)
	}
	t.Log("no fabrication and freshness held across the run")
}
HELIX_EOF
echo "created internal/simulation/manual_scenario_test.go"
```

Run it (the `-v` flag prints the operation count and the pass log):

```bash
cd "$HELIX_HOME"
go test -race -v -run TestManualStress ./internal/simulation/
```

Clean up:

```bash
cd "$HELIX_HOME"
rm -f internal/simulation/manual_scenario_test.go
echo "removed manual scenario test"
```

---

## 8. Verify the expected results

### Scenario A (no-fault workload)

- PASS: at least the writes are recorded, and both `CheckNoFabrication` and `CheckFreshness`
  return no violations. Under real concurrency with R+W>N, every completed write is visible to a
  subsequent quorum read, so freshness holds and nothing is fabricated.

### Scenario B (majority under partition)

- PASS: every recorded operation has OK=true (the majority side never lost quorum), and both
  invariants hold. A minority partition does not compromise the majority's consistency.

### Scenario C (checkers fire)

- PASS: each of the three bad histories yields at least one violation. If any of these passed
  with zero violations, the checker would be broken.

### Scenario D (valid history)

- PASS: zero violations, including for the read concurrent with a write. Confirms the checkers do
  not false-positive on legitimate concurrency.

### Scenario E (stress)

- The PASS line plus a logged operation count (in the thousands for the default parameters), and
  "no fabrication and freshness held across the run". With `isolate` set to a minority, it also
  demonstrates majority-side consistency at higher concurrency.

### Automated suite

- Section 6 shows all Part 2 tests PASS alongside Part 1 and every earlier test, every package
  `ok`, and `make check` exiting 0 with no race warnings.

---

## 9. Troubleshooting

Build
- `undefined: RunWorkload` / `CheckFreshness` / `NewHistory`: the checkout predates Part 2.
  Update per 2c and confirm the four Part 2 files exist.
- `go: go.mod requires go >= 1.25`: hold at 1.22 (`go mod edit -go=1.22`) and rebuild with
  `GOTOOLCHAIN=local`. This part adds no dependency, so do not `go mod tidy` to chase it.
- `inconsistent vendoring`: the vendor tree and go.mod disagree. This part adds no dependency, so
  if it appears, run `GOTOOLCHAIN=local go mod vendor` (should be a no-op) or build a local test
  run with `-mod=mod`.

Invariant failures (real or apparent)
- `CheckFreshness` reports violations in the no-fault workload: this would indicate a genuine
  read-after-write breach, which with R+W>N and no faults should not happen. Before suspecting
  the store, confirm the workload used one writer per key (the default does); a multi-writer key
  breaks the freshness check's assumption and produces false violations.
- `CheckFreshness` reports violations in your parameterized Scenario E: check your N/R/W. If
  R+W is not greater than N (for example N=5, R=2, W=2), a completed write is not guaranteed
  visible to a later read, and freshness can legitimately fail. Use quorums with R+W>N (the
  defaults do).
- `TestWorkloadMajorityUnderPartition` reports a failed op: you drove the workload through the
  isolated node. Confirm WriterNodes and ReaderNodes exclude the partitioned node.
- The `Catches` tests fail (report zero violations): the checker is not detecting a known breach.
  Confirm you are on the committed invariants.go; do not weaken the synthetic histories.

Behavior and performance
- A test runs slowly or the operation count is huge: readers spin in a tight loop until writers
  finish, so total ops scale with how long the writers take. Reduce WritesPerKey or Readers in
  Scenario E if a run is too long.
- `go test -race` reports a data race: the history is mutex/atomic guarded and each reader has its
  own RNG, so a race would be in code you added. Re-run the committed tests alone to confirm the
  baseline is clean.

Test isolation
- Leftover `manual_scenario_test.go` breaks later `go test ./...` runs: remove it per the
  Scenario E cleanup.
- Tests show `(cached)`: add `-count=1` to force a real run.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above rather
  than typing. If the prompt looks corrupted after a large paste, run `reset` or open a new Cloud
  Shell tab.
