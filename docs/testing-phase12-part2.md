# Testing Phase 12 Part 2: SWIM-aware fast-fail of dead nodes

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 12 Part 2.
It is self-contained: every command can be copied and run as written.

## What this part does

Part 1 bounded each replica RPC with a per-attempt timeout, so a hung replica no longer consumes
the caller's deadline. It still dialed a known-dead node and waited out that timeout. This part
closes the remaining gap: the daemon already tracks membership through SWIM, but the coordinator
never consulted it. Now the coordinator skips a replica the local membership view considers Dead,
fast-failing it instead of dialing, so a confirmed-dead node costs nothing rather than a
per-attempt timeout.

Design and its boundaries:

- The coordinator gains an optional liveness predicate (set once at wiring time). With none set,
  every node is attempted, so the change is invisible to code and tests that do not wire it.
- The daemon supplies the predicate from its SWIM list via an O(1) StateOf lookup. Only Dead is
  skipped. Suspect and unknown nodes are still attempted, so a false suspicion never routes traffic
  away from a healthy node; a truly failed node is skipped only once SWIM has confirmed it Dead.
- Skipping is applied to reads, writes, the causal pre-read, and hint placement (a Dead fallback is
  not used for a hint). A skipped node flows through the existing logic exactly like an unreachable
  one: the write hints it to a live fallback and the quorum count is unchanged.
- No background goroutines, no detached contexts, no change to hinting semantics. This is a
  fast-fail check, deliberately not the return-on-W-acks optimization, which was dropped.

This part also closes two small test gaps found in the production-readiness audit: the
observability logging package and the kvnode environment parsing now have unit tests.

## What applies to Helix and what does not

- A Go test suite: no proto and no dependency change, so go.mod, go.sum, and vendor are untouched
  and there is no go mod vendor step.
- No Docker or Kubernetes needed: everything is verified in-process. The simulation gains a
  MarkDead capability so a test can prove a Dead node is skipped without paying the timeout.

## 0. Shell setup

```bash
export HELIX_HOME="$HOME/helix"
```

## 1. Verify the environment

```bash
go version || echo "MISSING: Go toolchain"
grep -q 'SetLiveness' "$HELIX_HOME/internal/cluster/coordinator.go" && echo "present: coordinator liveness" || echo "MISSING"
grep -q 'st != membership.Dead' "$HELIX_HOME/internal/daemon/daemon.go" && echo "present: daemon skip-only-Dead" || echo "MISSING"
grep -q 'func (n \*Network) MarkDead' "$HELIX_HOME/internal/simulation/network.go" && echo "present: sim MarkDead" || echo "MISSING"
[ -f "$HELIX_HOME/internal/observability/logging_test.go" ] && echo "present: observability tests" || echo "MISSING"
[ -f "$HELIX_HOME/cmd/kvnode/main_test.go" ] && echo "present: kvnode tests" || echo "MISSING"
```

## 2. Build and vet

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet ./... 2>&1 | head -20    # must be silent
```

## 3. Run this part

```bash
cd "$HELIX_HOME"

# 3a. The dead-node skip test, verbosely, under the race detector.
GOTOOLCHAIN=local go test -race -count=1 -v -run 'TestSkipsDeadReplicaWithoutTimeout' ./internal/simulation/

# 3b. The whole resilience suite (Part 1 plus Part 2), under -race.
GOTOOLCHAIN=local go test -race -count=1 -v -run 'Available|FailsFast|SkipsDead' ./internal/simulation/

# 3c. The two newly covered packages.
GOTOOLCHAIN=local go test -race -count=1 -v ./internal/observability/ ./cmd/kvnode/

# 3d. Nothing regressed: the whole simulation and cluster packages under -race.
GOTOOLCHAIN=local go test -race -count=1 ./internal/simulation/ ./internal/cluster/

# 3e. The full gate.
make check
git status --porcelain go.mod go.sum vendor/    # expect no output
```

## 4. Scenario walk-through

### Scenario A: a Dead replica is skipped, not merely bounded (the fix)

`TestSkipsDeadReplicaWithoutTimeout` sets a deliberately large per-attempt timeout (1s), hangs
`n2`, and marks it Dead. If the coordinator dialed `n2`, the write would take about a second. The
test asserts the write returns in well under that (300ms), which is only possible if `n2` was
skipped entirely.

```bash
GOTOOLCHAIN=local go test -race -count=1 -run TestSkipsDeadReplicaWithoutTimeout -v ./internal/simulation/
```

### Scenario B: Part 1 behavior still holds for not-yet-dead nodes

A hung replica that is not marked Dead is still handled by the per-attempt timeout (it is dialed,
fails fast at the timeout, and the healthy quorum completes the request). This is the window before
SWIM confirms death.

```bash
GOTOOLCHAIN=local go test -race -count=1 -run 'Available|FailsFast' -v ./internal/simulation/
```

### Scenario C: the audit's test gaps are closed

```bash
GOTOOLCHAIN=local go test -race -count=1 -v ./internal/observability/ ./cmd/kvnode/
```

Expected: parseLevel mapping, logger level wiring, correlation-id round-trip, and the kvnode env
parsing (including HELIX_REQUEST_TIMEOUT default and override) all pass.

### Scenario D: nothing regressed

```bash
GOTOOLCHAIN=local go test -race -count=1 ./internal/simulation/ ./internal/cluster/
```

The read-repair test and the recovery/convergence tests must still pass; the liveness predicate is
unset in those paths, so behavior is unchanged.

## 5. Optional: end to end on Kubernetes

With this change, a node that SWIM has marked Dead is skipped by the coordinator, so a write during
a confirmed node outage no longer pays the per-attempt timeout at all. To see it, rebuild and
upgrade, delete a pod so SWIM eventually marks it Dead (give it more than the suspicion timeout),
then write:

```bash
cd "$HELIX_HOME"
make docker-build && kind load docker-image helix:local --name helix-test
helm upgrade helix deploy/helm/helix -n helix
kubectl -n helix rollout status statefulset/helix --timeout=120s
```

Note this is still bounded by how quickly SWIM converges to Dead; until then Part 1's per-attempt
timeout is what protects the write.

## 6. Expected results

- `TestSkipsDeadReplicaWithoutTimeout` passes: the write returns well under the per-attempt
  timeout, proving the Dead node was skipped.
- The Part 1 resilience tests, the observability tests, and the kvnode tests all pass under -race.
- The simulation and cluster packages pass under -race, including read repair and recovery.
- `make check` prints its success line with a clean tree; go.mod, go.sum, and vendor are unchanged.

## 7. Troubleshooting

- `TestSkipsDeadReplicaWithoutTimeout` fails the elapsed-time assertion: the coordinator is still
  dialing the Dead node. Confirm `co.SetLiveness` is called in NewCluster and that the write, read,
  and pre-read goroutines check `isAttemptable` before dialing.
- A previously passing test now behaves differently: the liveness predicate should be nil (attempt
  all) unless explicitly set. Confirm `NewCoordinator` does not set a default predicate.
- The daemon routes traffic away from a healthy node: the predicate must skip only Dead, never
  Suspect. Confirm the daemon returns `st != membership.Dead` and treats unknown as attemptable.
- A data race under -race in the simulation: any access to the network's dead set must be under the
  network mutex, as MarkDead/MarkAllAlive/isDead do.
- `git status` shows go.mod/go.sum/vendor modified: this part adds no dependency; revert them.
