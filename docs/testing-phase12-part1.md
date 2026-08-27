# Testing Phase 12 Part 1: coordinator write-path resilience

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 12 Part 1.
It is self-contained: every command can be copied and run as written.

## What this part fixes

Kubernetes testing surfaced a real availability bug: a write failed with `write quorum not met
(0/2 acks): context deadline exceeded` when one preferred replica was present in the ring and
resolvable but not actually serving (a pod whose DNS resolved before the process was listening,
made more likely by the headless Service's publishNotReadyAddresses). The cause was in the
coordinator, not the deployment: both the causal pre-read and the read path waited for every
preferred replica, so one hung replica drained the caller's whole deadline, leaving nothing for
the write.

This part makes the coordinator resilient to a slow or hung replica while a quorum of healthy
nodes is available:

- Every replica RPC (pre-read, read, write, and hint) is bounded by a per-attempt timeout, so a
  hung replica fails fast instead of consuming the caller's deadline. This is the actual fix: the
  pre-read can no longer drain the whole deadline waiting on a hung replica. It is configurable via
  HELIX_REQUEST_TIMEOUT (default 2s), which is comfortably inside helixctl's default 5s deadline.
- Both the pre-read and the read still wait for every replica (each attempt bounded), so no replica
  goroutine outlives the call (which would otherwise race with a concurrent crash), and read repair
  still heals every stale replica. Hinting is unchanged and stays synchronous.

The worst-case added latency when one replica is hung is about two times HELIX_REQUEST_TIMEOUT (the
pre-read and the write phase each wait out the hung replica once), which at the 2s default stays
within helixctl's 5s deadline.

What is deliberately left for a later part: returning to the client on W acks before every replica
responds (a latency optimization that removes the write phase's wait on a hung replica, at the cost
of moving hinting to run after the return), and wiring SWIM membership into the coordinator so a
known-dead node is skipped without even a timeout.

## What applies to Helix and what does not

- A Go test suite: the deliverable is a coordinator change plus tests. Running this part means
  running them.
- No proto and no dependency change, so go.mod, go.sum, and vendor are unchanged and there is no
  go mod vendor step.
- No Docker or Kubernetes needed to verify: the reproduction runs in-process. The simulation gains
  a per-node "hung" capability to model a replica that is reachable but never answers, which is the
  in-process analogue of the Kubernetes symptom.

## 0. One-time shell setup

```bash
export HELIX_HOME="$HOME/helix"
```

## 1. Verify the environment

```bash
go version || echo "MISSING: Go toolchain"   # module is pinned to Go 1.22
[ -f "$HELIX_HOME/internal/simulation/resilience_test.go" ] && echo "present: resilience tests" || echo "MISSING"
grep -q 'SetRequestTimeout' "$HELIX_HOME/internal/cluster/coordinator.go" && echo "present: coordinator fix" || echo "MISSING"
grep -q 'HELIX_REQUEST_TIMEOUT' "$HELIX_HOME/cmd/kvnode/main.go" && echo "present: kvnode wiring" || echo "MISSING"
grep -q 'google.golang.org/grpc ' "$HELIX_HOME/go.mod" && echo "present: deps" || echo "MISSING: deps"
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

# 3a. The new resilience tests, verbosely, under the race detector.
GOTOOLCHAIN=local go test -race -count=1 -v -run 'Hung|FailsFast|Available' ./internal/simulation/

# 3b. The whole simulation package, to confirm the coordinator change did not regress the
#     safety, recovery, or read-repair behavior.
GOTOOLCHAIN=local go test -race -count=1 ./internal/simulation/

# 3c. The cluster package, which holds the read-repair test that the read path must not break.
GOTOOLCHAIN=local go test -race -count=1 ./internal/cluster/

# 3d. The full gate.
make check
git status --porcelain go.mod go.sum vendor/    # expect no output
```

## 4. Scenario walk-through

### Scenario A: a write survives one hung replica (the fix)

`TestWriteAvailableWithOneHungReplica` hangs one preferred replica and, with a per-attempt
timeout, writes through a healthy node. The two healthy nodes meet the write quorum, so the write
succeeds and reads back through another node.

```bash
GOTOOLCHAIN=local go test -race -count=1 -run TestWriteAvailableWithOneHungReplica -v ./internal/simulation/
```

Expected: PASS. The write returns success and the value is readable through a different node.

### Scenario B: a read survives one hung replica

`TestReadAvailableWithOneHungReplica` shows a read returns on the healthy quorum, bounded by the
per-attempt timeout, without hanging on the unresponsive replica.

```bash
GOTOOLCHAIN=local go test -race -count=1 -run TestReadAvailableWithOneHungReplica -v ./internal/simulation/
```

### Scenario C: the timeout actually bounds the wait (non-vacuous)

`TestWriteFailsFastWhenQuorumHung` hangs two of three replicas, so no write quorum is reachable.
The write must fail, and it must fail fast (bounded by the per-attempt timeout) rather than
hanging until the caller's deadline. The test asserts both the error is a write-quorum error and
the call returned well under the deadline. Without the timeout the hung replicas would block until
the 5s deadline, so the generous 2s bound would be violated; that is what makes the test prove the
timeout works.

```bash
GOTOOLCHAIN=local go test -race -count=1 -run TestWriteFailsFastWhenQuorumHung -v ./internal/simulation/
```

### Scenario D: nothing regressed

The safety invariants (Phase 11 Part 2), recovery and convergence (Part 3), and read repair
(cluster package) must all still pass, since the read path still visits every replica and R plus W
greater than N still holds.

```bash
GOTOOLCHAIN=local go test -race -count=1 ./internal/simulation/ ./internal/cluster/
```

## 5. Optional: confirm the fix end to end on Kubernetes

If you still have the Helm release from Phase 10 Part 2, rebuild the image with this change, reload
it, and repeat the churn scenario that failed before. With the per-attempt timeout, a write during
a single node's restart should now succeed instead of returning `0/2 acks`.

```bash
cd "$HELIX_HOME"
make docker-build
kind load docker-image helix:local --name helix-test
helm upgrade helix deploy/helm/helix -n helix
kubectl -n helix rollout status statefulset/helix --timeout=120s
# The node reads HELIX_REQUEST_TIMEOUT (default 2s); confirm it is deployed:
kubectl -n helix get statefulset helix -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="HELIX_REQUEST_TIMEOUT")].value}'; echo
# (empty means the default 2s is used, since the chart does not set it explicitly)
```

Note this stays an honest test of steady-state resilience: with N=3 and one node briefly down, a
write to a key whose preference list needs that node now fails fast and, once SWIM and hinting
settle, succeeds. The deeper "skip a SWIM-dead node without any timeout" behavior is the later
part.

## 6. Expected results

- The three resilience tests pass under `-race`.
- The whole simulation package and the cluster package pass under `-race`, including the read-repair
  test, proving the read path still heals stale replicas.
- `make check` prints `check passed` with a clean tree, and go.mod, go.sum, and vendor are
  unchanged.

## 7. Troubleshooting

- A resilience test hangs: a hung-node goroutine had no deadline to unblock it. The tests always
  set a context deadline or a per-attempt timeout; if you adapt them, keep one of those, or the
  gate's wait on a hung node never returns.
- `TestWriteFailsFastWhenQuorumHung` fails on the elapsed-time assertion: the per-attempt timeout
  is not being applied. Confirm the simulation Config sets RequestTimeout and that
  `co.SetRequestTimeout` is called in NewCluster.
- `TestCrashRestartRecoversAndConverges` (simulation) fails the race detector: a replica goroutine
  outlived its coordinator call and raced with a crash that closed an engine. Both the pre-read and
  the read must wait for every replica (each bounded per attempt) rather than returning early, so no
  goroutine is left in flight. Confirm neither `readClock` nor `get` breaks out of its collection
  loop early.
- `TestCoordinatorReadRepairsStaleReplica` (cluster package) fails: the read path is returning
  before visiting every replica, so a stale one is not repaired. Confirm `get` has no early break on
  the quorum count.
- A data race under `-race`: check that any new code touching the sim network's hung set does so
  under the network mutex, as Hang/Unhang/isHung do.
- `git status` shows go.mod/go.sum/vendor modified: this part adds no dependency; revert them.
