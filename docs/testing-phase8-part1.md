# Testing Phase 8 Part 1: metrics and health endpoints

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 8
Part 1. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 1 makes the node observable. It adds a small dependency-free metrics library that renders
the Prometheus text exposition format, and wires an optional admin HTTP server into the daemon
that serves /metrics and /healthz. Metrics are refreshed on each scrape, so they always
reflect current state. What applies:

- An HTTP endpoint and URLs: yes, for the first time. A node serves /metrics and /healthz at
  the address in HELIX_METRICS_ADDR. You scrape them with curl.
- Configuration: one new variable, HELIX_METRICS_ADDR, plus the cluster variables from Phase 7
  Part 6.
- A long-running service: the same kvnode daemon, now with an admin HTTP listener alongside
  its gRPC listener.

Different from recent parts, and simpler because of it:

- Code generation: NOT required. No .proto changed.
- New dependencies: none. The metrics library is standard-library only, on purpose. The
  standard Prometheus client pulls a large dependency tree that has repeatedly tripped the Go
  1.22 version gate in this project; the hand-rolled core avoids that while emitting the exact
  format Prometheus scrapes.

Still not applicable in this part, and it is more useful to say so than to invent it:

- External databases, brokers, a Prometheus server: none required. Any Prometheus can scrape
  the endpoint, but none is needed to verify the part; curl is enough.
- Request-rate and latency metrics: not yet. The metrics in Part 1 are process, membership,
  and storage gauges (helix_up, helix_uptime_seconds, helix_cluster_members by state, and the
  helix_storage_* gauges). Counters and latency histograms for the coordinator hot path (Put,
  Get, Delete) are Part 2, which is where histogram support is added to the library.
- Tracing: out of scope for this part.

What Part 1 adds and this runbook verifies: the metrics library renders correct, escaped,
deterministically ordered exposition; a running node serves /healthz and /metrics; the metrics
reflect real membership and storage state; and the endpoint is off unless HELIX_METRICS_ADDR
is set.

## 0. One-time shell setup used by every section

```bash
export HELIX_HOME="$HOME/helix"
export HELIX_REPO="https://github.com/Talif787/helix.git"
export HELIX_RUN="/tmp/helix-run"
export HELIX_METRICS_ADDR="127.0.0.1:9090"
```

---

## 1. Verify the existing environment

```bash
# 1a. Go toolchain. Helix's module is pinned to Go 1.22.
go version || echo "MISSING: Go toolchain"

# 1b. Supporting tools.
git --version || echo "MISSING: git"
curl --version >/dev/null 2>&1 && echo "present: curl" || echo "MISSING: curl (needed to scrape /metrics)"

# 1c. protoc is NOT needed for this part (no proto changed).
protoc --version 2>/dev/null || echo "note: protoc absent (fine; Part 1 needs no codegen)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 8 Part 1 source present?
for f in \
  internal/metrics/metrics.go \
  internal/metrics/metrics_test.go \
  internal/daemon/daemon_metrics_test.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done
grep -q 'MetricsAddr' "$HELIX_HOME/internal/daemon/daemon.go" 2>/dev/null \
  && echo "present: daemon metrics wiring" || echo "MISSING: daemon metrics wiring"
grep -q 'HELIX_METRICS_ADDR' "$HELIX_HOME/cmd/kvnode/main.go" 2>/dev/null \
  && echo "present: HELIX_METRICS_ADDR in kvnode" || echo "MISSING: HELIX_METRICS_ADDR"

# 1f. Dependencies present and pinned (from earlier phases; Part 1 adds none).
grep -q 'google.golang.org/grpc' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "present: grpc in go.mod" || echo "MISSING: grpc dep (restore per 2e)"
grep -qi 'prometheus' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "note: a prometheus dep is present (unexpected for Part 1)" || echo "no prometheus dep (correct; metrics are stdlib)"
head -3 "$HELIX_HOME/go.mod" 2>/dev/null | grep -q 'go 1.22' \
  && echo "go.mod pinned to 1.22" || echo "note: check the go directive"
```

Interpretation: below go1.22 -> 2a; curl missing -> 2c; 1d MISSING -> 2b; 1e MISSING while 1d
FOUND -> 2d; 1f showing grpc MISSING -> 2e. protoc is not needed this part.

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

### 2c. curl (only if 1b flagged it missing)

```bash
sudo apt-get update && sudo apt-get install -y curl
curl --version | head -1
```

### 2d. Update an existing checkout (only if 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only          # if Phase 8 Part 1 is merged
#   or: git switch phase-8/metrics && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2e. Restore dependencies if go.mod lost them (only if 1f showed grpc missing)

```bash
cd "$HELIX_HOME"
git checkout -- go.mod go.sum
grep 'google.golang.org/grpc ' go.mod   # should now show grpc
head -3 go.mod                           # should show: go 1.22
```

---

## 3. Configure the required environment variables and services

The automated tests configure everything in code and need no environment variables. To run a
node and scrape it by hand, the one new variable is HELIX_METRICS_ADDR, on top of the Phase 7
Part 6 cluster variables.

| Variable | Meaning | Example |
| --- | --- | --- |
| HELIX_METRICS_ADDR | address for the admin HTTP server (/metrics, /healthz); empty disables it | `127.0.0.1:9090` |
| HELIX_NODE_ID | this node's id | `solo` |
| HELIX_PEERS | id=addr for every node | `solo=127.0.0.1:7070` |
| HELIX_BIND_ADDR | gRPC listen address (defaults to this node's peer addr) | `127.0.0.1:7070` |
| HELIX_DATA_DIR | this node's storage directory | `/tmp/helix-run/solo` |
| HELIX_LOG_FORMAT | log format | `text` |

URLs the endpoint exposes:

| URL | Meaning |
| --- | --- |
| `http://127.0.0.1:9090/healthz` | liveness; returns 200 and `ok` |
| `http://127.0.0.1:9090/metrics` | Prometheus text exposition |

Dummy values the automated tests use (all built in):

| Input | Meaning | Dummy value |
| --- | --- | --- |
| metric names | families under test | `helix_requests_total`, `m`, `hits`, and the daemon's `helix_*` |
| labels | counter/gauge labels | `op=get,result=ok`; `state=alive`; `path=...` (escaping test) |
| node id | single-node daemon | `solo` |
| metrics address | admin server in tests | `127.0.0.1:0` (OS-assigned) |

There is no database, broker, or external Prometheus to configure. A single node fully
exercises the endpoint.

---

## 4. Start the backend and supporting services

No codegen this part. Build, then run a single node with metrics enabled so you can scrape it.

```bash
cd "$HELIX_HOME"

# 1) Build under the CI toolchain constraint.
GOTOOLCHAIN=local go build ./...
make build
ls -l bin/kvnode
```

### 4a. Run one node with the metrics endpoint on

```bash
cd "$HELIX_HOME"
rm -rf "$HELIX_RUN" && mkdir -p "$HELIX_RUN"
HELIX_NODE_ID=solo \
HELIX_PEERS="solo=127.0.0.1:7070" \
HELIX_BIND_ADDR="127.0.0.1:7070" \
HELIX_DATA_DIR="$HELIX_RUN/solo" \
HELIX_METRICS_ADDR="$HELIX_METRICS_ADDR" \
HELIX_LOG_FORMAT=text \
./bin/kvnode > "$HELIX_RUN/solo.log" 2>&1 &
echo "started kvnode (pid $!)"
sleep 2
grep -i 'metrics serving\|daemon serving' "$HELIX_RUN/solo.log"
```

Stop it when done:

```bash
pkill -f './bin/kvnode' && echo "stopped kvnode"
```

Note on cluster gauges: a single node does not list itself in its own membership, so
`helix_cluster_members` will show zeros for a solo node. That is expected. To see nonzero
member gauges, run the three-node cluster from the Phase 7 Part 6 runbook with
HELIX_METRICS_ADDR set to a distinct port per node, then scrape any one of them.

---

## 5. Verify service and backend health

Health has two forms: the automated tests pass, and a running node answers /healthz and
/metrics.

```bash
cd "$HELIX_HOME"
make fmt
make vet                                    # expect no output, zero exit
GOTOOLCHAIN=local go build ./...            # expect no output
go test ./internal/metrics/ ./internal/daemon/   # expect ok for both
echo "exit code: $?"                        # expect 0
```

With a node running from section 4a:

```bash
# Liveness: expect HTTP 200 and the body "ok".
curl -s -o /dev/null -w "healthz status: %{http_code}\n" "http://$HELIX_METRICS_ADDR/healthz"
curl -s "http://$HELIX_METRICS_ADDR/healthz"

# Metrics: expect the process gauge and the family headers.
curl -s "http://$HELIX_METRICS_ADDR/metrics" | grep -E '^helix_up|^# TYPE helix_' | head
```

---

## 6. Run Phase 8 Part 1 (automated tests)

The metrics library is concurrent (atomic counters and gauges) and the endpoint test starts a
real HTTP server, so run under the race detector.

```bash
cd "$HELIX_HOME"

# 6a. The metrics and daemon packages, verbosely, with the race detector.
go test -race -v ./internal/metrics/ ./internal/daemon/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends with
`check passed`. Section 6a should include:

```
--- PASS: TestCounterExposition
--- PASS: TestGaugeExposition
--- PASS: TestDeterministicOrdering
--- PASS: TestLabelValueEscaping
--- PASS: TestConcurrentInc
--- PASS: TestDuplicateRegistrationPanics
--- PASS: TestDaemonMetricsEndpoint
--- PASS: TestDaemonMetricsDisabledByDefault
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: the metrics library exposition (no network)

Dummy data (built in): a `helix_requests_total` counter with labels `op` and `result`; a
gauge with a `state` label; label values containing quotes, a backslash, and a newline for the
escaping check.

```bash
cd "$HELIX_HOME"
go test -race -v -run 'TestCounter|TestGauge|TestDeterministic|TestLabelValue|TestConcurrent|TestDuplicate' ./internal/metrics/
```

### Scenario B: the daemon metrics endpoint (headline)

Dummy data (built in): a single node `solo` with the admin server on 127.0.0.1:0. The test
GETs /healthz and /metrics and asserts 200 plus the presence of `helix_up 1`,
`helix_cluster_members`, `helix_storage_sstables`, and `helix_uptime_seconds`.

```bash
cd "$HELIX_HOME"
go test -race -v -run 'TestDaemonMetrics' ./internal/daemon/
```

### Scenario C: scrape a running node by hand

With a node running from section 4a, confirm the endpoint end to end.

```bash
echo "--- /healthz ---"
curl -s "http://$HELIX_METRICS_ADDR/healthz"

echo "--- selected metrics ---"
curl -s "http://$HELIX_METRICS_ADDR/metrics" \
  | grep -E 'helix_up|helix_uptime_seconds|helix_cluster_members|helix_storage_'

echo "--- content type (should be the Prometheus text format) ---"
curl -s -D - -o /dev/null "http://$HELIX_METRICS_ADDR/metrics" | grep -i content-type
```

### Scenario D: metrics reflect storage state

Because gauges refresh on each scrape, writes visible to the local engine change the storage
gauges. This scenario drops a temporary test that writes through the coordinator, then scrapes
the endpoint and checks the exposition parses and the storage family is present. It is removed
afterward.

Create the test (edit the marked block for your own values):

```bash
cd "$HELIX_HOME"
cat > internal/daemon/manual_scenario_test.go <<'HELIX_EOF'
package daemon

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/talifpathan/helix/internal/storage"
)

func TestMetricsManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	key := []byte("account:42")
	val := []byte("balance-100")
	// ---------------------------------------------------------

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	d, err := New(Config{
		NodeID: "solo", BindAddr: addr, Listener: lis,
		Peers:       map[string]string{"solo": addr},
		N:           1, R: 1, W: 1, // single node: quorum of one so the write commits
		MetricsAddr: "127.0.0.1:0",
		Storage:     storage.Options{DataDir: t.TempDir()},
		SwimInterval: time.Hour, AntiEntropyInterval: time.Hour, HintInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	if err := d.Put(context.Background(), key, val); err != nil {
		t.Fatalf("put: %v", err)
	}

	resp, err := http.Get("http://" + d.MetricsAddr() + "/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	for _, want := range []string{"helix_up 1", "helix_storage_memtable_keys", "helix_storage_sstables"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in exposition:\n%s", want, text)
		}
	}
	t.Logf("scraped %d bytes of exposition after a write", len(text))
}
HELIX_EOF
echo "created internal/daemon/manual_scenario_test.go"
```

Run it:

```bash
cd "$HELIX_HOME"
go test -race -v -run TestMetricsManualScenario ./internal/daemon/
```

Clean up:

```bash
cd "$HELIX_HOME"
rm -f internal/daemon/manual_scenario_test.go
echo "removed manual scenario test"
```

---

## 8. Verify the expected results

### Scenario A (library)

- PASS: the counter emits `# TYPE ... counter` and correctly labelled samples with the right
  values; the gauge emits label-less and labelled samples; series are sorted by label value;
  a value with a quote, backslash, and newline renders as `path="a\"b\\c\nd"`; 100 goroutines
  incrementing reach exactly 10000; and a duplicate metric name panics.

### Scenario B (daemon endpoint)

- PASS: /healthz returns 200; /metrics returns 200 and contains `helix_up 1`, the
  `helix_cluster_members` family, `helix_storage_sstables`, and `helix_uptime_seconds`; and the
  endpoint is absent when MetricsAddr is unset.

### Scenario C (manual scrape)

- /healthz prints `ok`.
- /metrics lists the `helix_up`, `helix_uptime_seconds`, `helix_cluster_members`, and
  `helix_storage_*` lines.
- the Content-Type header is `text/plain; version=0.0.4; charset=utf-8`, the Prometheus text
  format.

### Scenario D (reflects state)

- The test PASS line and a log of the exposition size; the storage gauges are present after a
  write.

### Automated suite

- Section 6 shows the six metrics-library tests and both daemon metrics tests PASS alongside
  every earlier test, every package `ok`, and `make check` exiting 0 with no race warnings.

---

## 9. Troubleshooting

Build (no codegen this part)
- `undefined: metrics.NewRegistry` or `daemon` metrics fields: the checkout predates Part 1.
  Update per 2d and confirm `internal/metrics/metrics.go` exists.
- `go: go.mod requires go >= 1.25`: unrelated to Part 1 (no deps changed), but if an earlier
  tidy bumped it, hold at 1.22 (`go mod edit -go=1.22`) and rebuild with `GOTOOLCHAIN=local`.
- A prometheus dependency shows up in go.mod: unexpected for this part. Part 1's metrics are
  stdlib; if a stray `go get` added one, `git checkout -- go.mod go.sum`.

Endpoint and scraping
- `curl: (7) Failed to connect to 127.0.0.1 port 9090`: the node is not running or
  HELIX_METRICS_ADDR was not set, so the admin server never started. Confirm the process is up
  (`pgrep -af kvnode`) and that the launch command included HELIX_METRICS_ADDR. The gRPC
  listener and the metrics listener are different ports; scrape the metrics port.
- /metrics returns 404: you hit the wrong path or the gRPC port. The admin server only serves
  /metrics and /healthz, on HELIX_METRICS_ADDR, not on HELIX_BIND_ADDR.
- `address already in use` binding the metrics port: another process holds it, or a previous
  node is still running. `pkill -f './bin/kvnode'` and pick another port.
- `helix_cluster_members` shows all zeros: expected for a single node, which does not track
  itself. Run a multi-node cluster to see nonzero counts.

Behavior and values
- `helix_uptime_seconds` does not increase between scrapes: it is refreshed per scrape from the
  start time; scrape again after a few seconds. If it stays 0, the node likely just started.
- storage gauges stay at zero after writes: with a single node you must use N=R=W=1 so the
  write commits (the default 3/2/2 cannot reach quorum on one node). The manual Scenario D sets
  1/1/1 for exactly this reason. Also, keys live in the memtable until a flush, so
  `helix_storage_sstables` may legitimately be 0 until a flush or compaction occurs.
- The exposition looks unordered: series within a family are sorted by label value, but family
  blocks appear in registration order. Both are deterministic; that is expected.

Test isolation
- Leftover `manual_scenario_test.go` breaks later `go test ./...` runs: remove it per the
  Scenario D cleanup.
- A daemon test fails to bind: a prior test process is lingering. `pkill -f kvnode` and
  `pkill -f daemon.test`, then retry.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above rather
  than typing. If the prompt looks corrupted after a large paste, run `reset` or open a new
  Cloud Shell tab.
- Background jobs clutter the shell: list them with `jobs`, stop everything with
  `pkill -f './bin/kvnode'`.
