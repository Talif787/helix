# Testing Phase 11 Part 3: recovery and convergence through anti-entropy

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 11
Part 3. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 3 is the recovery half of the simulation phase. Part 2 proved safety under fault (no
fabricated reads, read-after-write freshness). Part 3 proves liveness after fault: a node can be
taken down and brought back, and the Merkle anti-entropy repair path actually closes the gaps a
partition or a crash leaves behind, so once the cluster is healed and quiesced every replica of a
key agrees on its value. What applies:

- A Go test suite: the whole deliverable is code plus tests. Running Part 3 means running them.
- The real coordinator, storage engine, and repair path: the simulation wires the actual cluster
  components and drives the real `cluster.Repair` (Merkle-tree anti-entropy), so it exercises real
  reconciliation, not a mock.
- The storage engine's durability: a restarted node reopens its engine from the same data
  directory, so its write-ahead log replays and the state it held before the crash returns.

What is different, and the same as Part 2:

- No Docker, no containers, no compose. This is in-process Go.
- No protoc or code generation. No .proto changed.
- No long-running service, no ports, no HTTP or gRPC on the wire. Everything is in memory.
- No external database, broker, credentials, or environment variables.
- No new dependencies, so `go.mod`, `go.sum`, and `vendor/` are unchanged and there is no
  `go mod vendor` step this part.

Boundaries worth stating so results are not misread:

- Quiescence: crash, restart, anti-entropy, and the convergence check are meaningful only when no
  workload is in flight. They inspect or mutate the shared node registry, which the coordinators'
  request goroutines read concurrently, so the scenarios do these things between workload bursts,
  when the cluster is idle. `RunWorkload` is synchronous, so a call after it returns is quiescent.
- Convergence is an eventual property: a divergence observed after a heal but before anti-entropy
  is expected, not a bug. The anti-vacuous test relies on exactly that.
- This is still a seeded, fault-injecting simulation, not a bit-for-bit deterministic scheduler.
  The partition and crash schedule are set explicitly by the test and are replayable; goroutine
  interleaving is not controlled.

What Part 3 adds and this runbook verifies: a node can crash (its engine closes) and restart (its
durable state replays); a write missed during a partition or a crash is closed by one anti-entropy
round; every replica then agrees; the convergence check catches a genuine divergence when no
repair has run; and a second repair over a converged cluster reconciles nothing.

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
protoc --version 2>/dev/null || echo "note: protoc absent (fine; Part 3 needs no codegen)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 11 Part 3 source present?
for f in internal/simulation/recovery.go internal/simulation/recovery_test.go \
         docs/testing-phase11-part3.md; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done
# Parts 1 and 2 must also be present (Part 3 builds on them).
for f in internal/simulation/cluster.go internal/simulation/network.go \
         internal/simulation/workload.go internal/simulation/invariants.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING (Part 1/2): $f"; fi
done

# 1f. Dependencies present and pinned; this part adds none.
grep -q 'google.golang.org/grpc ' "$HELIX_HOME/go.mod" && echo "present: grpc require" || echo "MISSING: grpc require"
head -3 "$HELIX_HOME/go.mod" | grep -q 'go 1.22' && echo "present: go 1.22 directive" || echo "WARNING: go directive drifted"
```

Interpretation: 1a MISSING -> 2a; 1d MISSING -> 2b; 1e MISSING while 1d FOUND -> 2c; 1f MISSING -> 2e.

---

## 2. Install or initialize anything missing

Do only the sub-steps flagged by section 1.

### 2a. Install Go 1.22+ (only if 1a is missing or too old)

```bash
cd /tmp
curl -fsSLO https://go.dev/dl/go1.22.6.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.22.6.linux-amd64.tar.gz
export PATH="/usr/local/go/bin:$PATH"
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
git switch main && git pull --ff-only          # if Phase 11 Part 3 is merged
#   or: git switch phase-11/recovery && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2d. protoc (not needed for Part 3)

Skip. No proto changed, so no code generation is required.

### 2e. Restore dependencies if go.mod lost them (only if 1f showed grpc missing)

```bash
cd "$HELIX_HOME"
go mod edit -go=1.22
GOTOOLCHAIN=local go get google.golang.org/grpc@v1.64.1 google.golang.org/protobuf@v1.34.2
GOTOOLCHAIN=local go mod tidy
GOTOOLCHAIN=local go mod vendor    # only if go.mod/go.sum actually changed
```

---

## 3. Configure the required environment variables and services

There are none. Part 3 is pure in-process Go: no ports, no services, no external stores, and no
environment variables beyond the shell paths in section 0. The test values are baked into the
tests. For reference, the dummy inputs the scenarios use:

| Input | Value |
| --- | --- |
| node ids | `n0`, `n1`, `n2` |
| replication | N=3, R=2, W=2, hinting disabled (`MaxHints: -1`) so anti-entropy is the only repair path |
| sample keys | `a`..`e`, `k0`..`k3`, `x`, `p`/`q`/`r` |
| sample values | the workload's `<key>#<n>` sequence, plus `<key>-base` and `only-majority`/`v1` |
| the crashed node | `n2` (isolated or fully crashed, then restarted) |

Hinting is disabled on purpose: with three nodes at N=3 there is no fallback node to hold a hint
anyway, so a write missed during an outage is a genuine gap that only anti-entropy can close,
which is what this part is meant to prove.

---

## 4. Start the backend and supporting services

Nothing to start. The tests build the simulated cluster in-process. Just confirm the tree builds.

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go build ./...
```

---

## 5. Verify service and backend health

"Health" for this part is a clean build and a clean vet, since there is no running service.

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go vet ./... 2>&1 | head -20     # must be silent
GOTOOLCHAIN=local go build ./internal/simulation/
```

---

## 6. Run Phase 11 Part 3

```bash
cd "$HELIX_HOME"

# 6a. The simulation package, verbosely, with the race detector.
GOTOOLCHAIN=local go test -race -count=1 -v ./internal/simulation/

# 6b. The whole suite with the race detector, to confirm nothing else regressed.
GOTOOLCHAIN=local go test -race -count=1 ./...

# 6c. fmt, vet, and the race suite together (the CI gate).
make check
```

There is no new dependency, so `make check` should leave the tree clean. Confirm it does:

```bash
cd "$HELIX_HOME"
git status --porcelain go.mod go.sum vendor/    # expect no output
```

---

## 7. Execute each test scenario with dummy values

The scenarios below are the Part 3 tests. Run them individually to see each property in isolation.

### Scenario A: convergence after a partition heals (the headline)

Isolate `n2`, drive writes through the majority so `n2` misses them, heal, and show anti-entropy
brings `n2` current.

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go test -race -count=1 -run TestConvergenceAfterPartitionHeal -v ./internal/simulation/
```

### Scenario B: crash, restart, and converge

Crash `n2` (its engine closes), keep writing through the majority, restart `n2` (its durable state
replays from the write-ahead log), then repair and confirm every replica agrees and a coordinated
read through the restarted node returns the latest value.

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go test -race -count=1 -run TestCrashRestartRecoversAndConverges -v ./internal/simulation/
```

### Scenario C: durability across a crash and restart

A value written before a crash survives the close and reopen of the node's engine, with no repair
involved.

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go test -race -count=1 -run TestCrashRestartPreservesDurableData -v ./internal/simulation/
```

### Scenario D: the convergence check actually fires (anti-vacuous)

With a genuine divergence in place and no repair run, the check must report a violation.

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go test -race -count=1 -run TestConvergenceCatchesDivergentReplicas -v ./internal/simulation/
```

### Scenario E: repair is idempotent

A second anti-entropy round over an already-converged cluster reconciles nothing.

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go test -race -count=1 -run TestAntiEntropyIdempotent -v ./internal/simulation/
```

---

## 8. Verify the expected results

### Scenario A (partition heal)

- `TestConvergenceAfterPartitionHeal` passes. Internally it asserts a divergence exists after the
  heal but before repair (so the check is not vacuous), then that one anti-entropy round leaves no
  convergence violations.

### Scenario B (crash, restart, converge)

- `TestCrashRestartRecoversAndConverges` passes: every majority op succeeded while `n2` was down,
  the restarted node reopened cleanly, anti-entropy left no violations, and the coordinated read
  through `n2` returned `k0#8` (the last write).

### Scenario C (durability)

- `TestCrashRestartPreservesDurableData` passes: the pre-crash value is present on the restarted
  node with no repair, proving the write-ahead log replayed.

### Scenario D (anti-vacuous)

- `TestConvergenceCatchesDivergentReplicas` passes, meaning the check reported the expected
  violation. A failure here means the convergence check is not detecting real divergence.

### Scenario E (idempotent repair)

- `TestAntiEntropyIdempotent` passes: the second round reconciled zero keys and the cluster stayed
  converged.

### Automated suite

- `go test -race -count=1 ./...` is green and `make check` prints `check passed` with a clean
  working tree.

---

## 9. Troubleshooting

- `go vet` fails: fix before committing; CI runs the same step.
- `git status` shows `go.mod`/`go.sum`/`vendor/` modified: this part adds no dependency. Revert
  with `git checkout -- go.mod go.sum` and, if vendor changed, `git checkout -- vendor/`, then
  confirm the `make fmt` target excludes vendor so it does not re-dirty on the next run.
- `make check` leaves the tree dirty: the fmt vendor-exclusion fix from Phase 10 is not on this
  checkout. Confirm `grep -A1 '^fmt:' Makefile` shows the non-vendor form before committing.
- A convergence test fails with replicas disagreeing after anti-entropy: the repair path did not
  reconcile. Re-run the committed `internal/cluster/repair.go` tests
  (`go test -race -run Repair ./internal/cluster/`) to confirm the baseline repair still works,
  then check that the restarted node had its preference function set (Restart wires it).
- `TestConvergenceCatchesDivergentReplicas` fails (no violation): the convergence check regressed
  and is not detecting a known divergence. Confirm you are on the committed `recovery.go`.
- A data race under `-race`: crash, restart, anti-entropy, or the convergence check was called
  while a workload was still running. These are quiescent-only operations; run them after
  `RunWorkload` returns, not concurrently.
- `TestCrashRestartPreservesDurableData` fails (data lost): the engine did not replay its WAL on
  reopen. Confirm the data directory was not removed between crash and restart (the same
  `BaseDir/<id>` must persist), and that the engine was opened with the default sync settings.
- Tests show `(cached)`: add `-count=1` to force a real run.
- `.gitignore` shows a stray change: discard it before committing.
