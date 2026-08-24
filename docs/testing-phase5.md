# Testing Phase 5: SWIM membership and failure detection

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 5.
It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Phase 5 adds a SWIM membership subsystem: nodes probe each other, mark unresponsive peers
suspect then dead, gossip those changes so everyone converges on the same liveness view,
and let a suspected or restarted node refute by raising its incarnation number. The nodes
run in-process behind a Messenger interface, so several items from a typical service
runbook still do not apply, and saying so is more useful than inventing them:

- Separate processes, ports, network listeners: none. The membership engines run in one
  process and exchange messages through an in-process Messenger. A network transport is a
  later phase.
- External databases, caches, brokers, data volumes: none. Phase 5 stores no key-value
  data at all; its only state is each node's in-memory member list. There are no per-node
  data directories in this phase.
- Credentials, API keys, tokens, connection URLs, request payloads: none. The equivalent
  inputs are node IDs, which node to kill or restart, and the SWIM timing parameters, all
  given as dummy values below.
- Environment variables: the protocol is configured in code through membership.SwimConfig,
  not through HELIX_* variables.
- Integration with routing: membership is a standalone subsystem this phase. It is not yet
  wired into the coordinator or the ring; consuming its liveness view (routing around dead
  nodes, hinted handoff) is a later phase.

What Phase 5 adds, and therefore what we verify: a healthy cluster converges to all-alive;
a killed node is detected (direct probe fails, indirect probes fail, suspect, then dead)
and that death gossips to every survivor; a false suspicion is refuted and cleared
everywhere; and a restarted node is revived everywhere via incarnation refutation.

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

# 1d. Is the Phase 5 code present?
for f in \
  internal/membership/state.go \
  internal/membership/list.go \
  internal/membership/messenger.go \
  internal/membership/swim.go \
  cmd/swimdemo/main.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done

# 1e. Confirm the Makefile demos target builds swimdemo.
grep -q 'swimdemo' "$HELIX_HOME/Makefile" 2>/dev/null \
  && echo "present: Makefile builds swimdemo" \
  || echo "MISSING: swimdemo not in Makefile demos target"
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

### 2c. Update an existing checkout to Phase 5 (only if 1d or 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only        # if Phase 5 is merged
#   or: git switch phase-5/membership && git pull --ff-only   # if still on its branch
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

None. Phase 5 keeps no on-disk state. There is nothing to provision or seed on disk.

---

## 3. Configure the required environment variables and services

There are no services to configure and no environment variables on the Phase 5 path. The
protocol is configured in code through membership.SwimConfig. This table documents each
field and the dummy values this runbook uses.

| Option / input | Meaning | Dummy value used here |
| --- | --- | --- |
| node IDs | Members of the cluster | `node-a`..`node-f` (demo); `n0`..`n4` (tests/manual) |
| Period | Wall-clock time between protocol rounds (only when Start is used) | 15ms (demo) |
| PingTimeout | How long to wait for a direct or indirect ack | 10ms (demo) |
| SuspicionTicks | Rounds a member stays suspect before being declared dead | 3 to 4 |
| IndirectProbes | Number of relays asked to probe when a direct ping fails | 2 |
| GossipFanout | Times a change is retransmitted | 3 (default) |
| MaxPiggyback | Max updates carried on one message | 6 (default) |
| which node fails | The membership event under test | `node-e` (demo); `n4` (tests/manual) |
| credentials, URLs, payloads, keys | not applicable in this phase | none |

Note on timing: tests do not use wall-clock timers at all. They call RunOnce and Tick
directly, so failure detection is driven by logical rounds and is fully deterministic. Only
the demo (and the real Start loop) use Period and PingTimeout.

---

## 4. Start the backend and supporting services

There is no long-lived server and no supporting service. The Phase 5 vehicle is the
`swimdemo` binary, a self-verifying tool that runs a six-node membership cluster with real
timers, kills a node, restarts it, and checks convergence, then exits (0 on success).

```bash
cd "$HELIX_HOME"
make demos           # builds ./bin/swimdemo (and sstdemo, clusterdemo)
ls -l ./bin/
# If make is unavailable: go build -o bin/swimdemo ./cmd/swimdemo
```

---

## 5. Verify service and backend health

Health means it builds, static analysis is clean, and the self-verifying demo runs to PASS.

```bash
cd "$HELIX_HOME"
make fmt
make vet          # expect no output, zero exit code
./bin/swimdemo
echo "exit code: $?"   # expect 0
```

Expected: lines reporting the cluster converging to all-alive, a killed node detected as
dead across every survivor, and a restarted node revived everywhere, ending in
`swimdemo: PASS`.

---

## 6. Run Phase 5 (automated tests)

The authoritative verification is the Go test suite under the race detector. Start runs
every node's protocol loop concurrently and they call into each other's handlers, so
`-race` is the gate that matters most.

```bash
cd "$HELIX_HOME"

# 6a. Just the membership package, verbosely, with the race detector.
go test -race -v ./internal/membership/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends
cleanly. Section 6a should include:

```
--- PASS: TestMergePrecedence
--- PASS: TestApplySelfRefutation
--- PASS: TestSuspectThenTickDeclaresDead
--- PASS: TestGossipRetransmitBudget
--- PASS: TestNextProbeTargetCoversAllInOneRound
--- PASS: TestDeadMembersNotProbed
--- PASS: TestSwimHealthyClusterStaysAlive
--- PASS: TestSwimDetectsAndDisseminatesFailure
--- PASS: TestSwimRefutesFalseSuspicion
--- PASS: TestSwimNodeRejoinsAfterDeath
--- PASS: TestSwimStartStopIsClean
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: convergence, failure detection, and rejoin (the demo)

Dummy data (built into the demo): node IDs `node-a`..`node-f`; Period 15ms, PingTimeout
10ms, SuspicionTicks 4, IndirectProbes 2; the killed and restarted node is `node-e`.

```bash
cd "$HELIX_HOME"
./bin/swimdemo
```

This one run covers convergence to all-alive, detection of a killed node as dead across
survivors, and revival of a restarted node. Read the report per section 8.

### Scenario B: the deterministic core in isolation

These target the merge precedence, self-refutation, suspicion timeout, gossip budget, and
probe rotation with fixed inputs (no timers).

```bash
cd "$HELIX_HOME"

# State precedence and self-refutation.
go test -race -v -run 'TestMergePrecedence|TestApplySelfRefutation' ./internal/membership/

# Suspicion timeout, gossip retransmit budget, probe coverage.
go test -race -v -run 'TestSuspectThenTickDeclaresDead|TestGossipRetransmitBudget|TestNextProbeTargetCoversAllInOneRound|TestDeadMembersNotProbed' ./internal/membership/
```

### Scenario C: protocol behavior end to end (deterministic rounds)

These build a set of in-process engines and drive rounds by hand, so detection, refutation,
and rejoin are reproducible.

```bash
cd "$HELIX_HOME"

# Healthy cluster stays alive; a killed node is detected and gossiped dead.
go test -race -v -run 'TestSwimHealthyClusterStaysAlive|TestSwimDetectsAndDisseminatesFailure' ./internal/membership/

# A false suspicion is refuted; a restarted node rejoins.
go test -race -v -run 'TestSwimRefutesFalseSuspicion|TestSwimNodeRejoinsAfterDeath' ./internal/membership/

# The real Start/Stop loop is clean (uses timers).
go test -race -v -run 'TestSwimStartStopIsClean' ./internal/membership/
```

### Scenario D: parameterized membership scenario with your own dummy values

This lets you supply your own node IDs, timing, and which node fails and rejoins, driving
the protocol deterministically and printing the converged view at each stage. It drops a
temporary test into the package (so it can reach the internal package), runs it, and is
removed afterward.

Create the test (edit the marked block to your own dummy data):

```bash
cd "$HELIX_HOME"
cat > internal/membership/manual_scenario_test.go <<'HELIX_EOF'
package membership

import (
	"context"
	"math/rand"
	"testing"
)

func TestMembershipManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	victim := "n4"          // node to kill, then restart
	suspicionTicks := uint64(3)
	roundsToConverge := 25  // deterministic rounds to run at each stage
	// ---------------------------------------------------------

	msg := NewInProcessMessenger()
	engines := map[string]*Swim{}
	for i, id := range ids {
		engines[id] = NewSwim(id, msg, ids, SwimConfig{
			SuspicionTicks: suspicionTicks,
			IndirectProbes: 2,
			Rand:           rand.New(rand.NewSource(int64(i) + 1)),
		})
	}
	ctx := context.Background()
	runRounds := func(r int) {
		order := make([]string, 0, len(engines))
		for id := range engines {
			order = append(order, id)
		}
		for i := 0; i < r; i++ {
			for _, id := range order {
				engines[id].RunOnce(ctx)
			}
		}
	}
	dump := func(label string) {
		t.Logf("--- %s ---", label)
		for _, id := range ids {
			e, ok := engines[id]
			if !ok {
				continue
			}
			line := id + " sees:"
			for _, m := range e.List().Members() {
				line += " " + m.ID + "=" + m.State.String()
			}
			t.Log(line)
		}
	}

	runRounds(5)
	dump("healthy cluster")

	// Kill the victim.
	msg.Deregister(victim)
	delete(engines, victim)
	runRounds(roundsToConverge)
	dump("after killing " + victim)
	for id, e := range engines {
		if st, _ := e.List().StateOf(victim); st != Dead {
			t.Fatalf("%s should see %s dead, sees %v", id, victim, st)
		}
	}

	// Restart the victim fresh; it must refute and be revived.
	engines[victim] = NewSwim(victim, msg, ids, SwimConfig{
		SuspicionTicks: suspicionTicks, IndirectProbes: 2,
		Rand: rand.New(rand.NewSource(99)),
	})
	runRounds(roundsToConverge)
	dump("after restarting " + victim)
	for id, e := range engines {
		if st, _ := e.List().StateOf(victim); st != Alive {
			t.Fatalf("%s should see %s alive again, sees %v", id, victim, st)
		}
	}
}
HELIX_EOF
echo "created internal/membership/manual_scenario_test.go"
```

Run it (the `-v` flag shows the converged view at each stage):

```bash
cd "$HELIX_HOME"
go test -race -v -run TestMembershipManualScenario ./internal/membership/
```

Clean up when finished (there is no data directory for this phase, only the temp test):

```bash
cd "$HELIX_HOME"
rm -f internal/membership/manual_scenario_test.go
echo "removed manual scenario test"
```

---

## 8. Verify the expected results

### Scenario A (demo)

Expected output shape:

```
started 6 membership nodes
cluster converged: every node sees every node alive
killed node-e
failure detected: every survivor gossiped node-e to dead
restarted node-e
recovery detected: node-e refuted and is alive everywhere again
swimdemo: PASS
```

Checks: convergence to all-alive; every survivor independently reaching `node-e = dead`
(not just the one that first probed it, proving gossip spread); and `node-e` returning to
alive everywhere after restart (proving incarnation refutation revives a node others had
written off). Exit code 0.

### Scenario B (core)

- Merge precedence PASS: higher incarnation wins; at equal incarnation, worse state wins;
  dead is terminal at a given incarnation but a higher incarnation revives.
- Self-refutation PASS: a rumor that self is suspect produces an Alive update at a higher
  incarnation.
- Suspicion timeout PASS: a suspect member is not declared dead before SuspicionTicks and
  is declared dead exactly at the timeout.
- Gossip budget PASS: a change is handed out exactly GossipFanout times, then stops.
- Probe coverage PASS: every member is probed once per round, and dead members are skipped.

### Scenario C (protocol)

- Healthy cluster PASS: with no failures, every node sees every node alive.
- Detection PASS: a killed node converges to dead on every survivor.
- Refutation PASS: a false suspicion of a live node is cleared everywhere.
- Rejoin PASS: a restarted node (fresh at incarnation 0) is revived everywhere.
- Start/Stop PASS: the timer-driven loop starts and stops cleanly, Stop is idempotent, and
  the cluster is still all-alive afterward.

### Scenario D (parameterized)

- The test PASS line, plus `-v` log blocks at three stages: the healthy cluster (all
  members alive), after killing the victim (every survivor shows the victim dead), and
  after restarting (every node shows the victim alive again).

### Automated suite

- Section 6 shows every listed test PASS, every package `ok`, and `make check` exiting 0
  with no race detector warnings.

---

## 9. Troubleshooting

Setup and toolchain
- `go: command not found` or a version below go1.22: run section 2a, then `source ~/.bashrc`.
- `go: cannot find main module`: you are not inside the repository. `cd "$HELIX_HOME"`.
- `make: command not found`: build directly with `go build -o bin/swimdemo ./cmd/swimdemo`.
- `use of internal package ... not allowed`: you are importing `internal/membership` from
  outside the module. The parameterized scenario avoids this by placing its test inside the
  package; keep that file under `internal/membership/`.

Protocol behavior
- `swimdemo: FAIL: cluster did not converge to all-alive`: unexpected in-process. If you
  lowered Period or PingTimeout drastically, raise them; probes need time to complete
  within a round.
- `swimdemo: FAIL: survivors did not converge on ... dead`: the suspicion timeout or the
  gossip budget is too small for the cluster size. Raise SuspicionTicks or GossipFanout.
  The demo's own values are known-good.
- `swimdemo: FAIL: restarted ... was not revived`: this is the refutation path. It relies
  on a node attaching negative news about a peer it contacts; if you changed the engine so
  that news is not attached, a restarted node at incarnation 0 cannot override the dead
  record and will not rejoin.
- A parameterized scenario with only 2 nodes behaves oddly: SWIM needs at least 3 members
  for indirect probing to mean anything. Use 3 or more node IDs.

Determinism and flakiness
- A protocol test fails intermittently: it should not, because the tests use fixed random
  seeds and hand-driven rounds rather than timers. If you see intermittent failure, suspect
  a real concurrency bug and capture it. Save a race report with
  `go test -race ./internal/membership/ 2>&1 | tee /tmp/helix-race.txt` and share it.
- `go test -race` reports a DATA RACE: treat it as a real defect in the member list locking
  or the engine, not a flake, and capture it as above.

Services and integration
- Looking for a port, URL, or health endpoint to curl: there is none in this phase. The
  membership engines are in-process with no network listener until a later phase. Health is
  section 5.
- Looking for data directories or connection strings: there are none. Phase 5 keeps no
  on-disk or external state; each node's only state is its in-memory member list.
- "How does this connect to the store?": it does not yet. Membership is a standalone
  subsystem this phase; wiring its liveness view into routing and handoff is a later phase.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above
  rather than typing. If the prompt looks corrupted after a large paste, run `reset` or
  open a new Cloud Shell tab.
