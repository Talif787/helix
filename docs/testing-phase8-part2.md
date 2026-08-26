# Testing Phase 8 Part 2: coordinator request metrics

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 8
Part 2. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 2 instruments the coordinator hot path. It adds histogram support to the metrics library
and times and classifies every Put, Get, and Delete through a small interface the cluster
package owns (so the cluster stays decoupled from the metrics backend). The daemon exports two
new families on /metrics: helix_requests_total{op,result} and
helix_request_duration_seconds{op}. What applies:

- The /metrics endpoint from Part 1: the two new families appear there alongside the gauges.
- Configuration: the same variables as Part 1, plus a note that request counters only commit
  on a single node when the quorum is set to one (HELIX_N/R/W=1).

Different from recent parts, and simpler because of it:

- Code generation: NOT required. No .proto changed.
- New dependencies: none. Histograms are part of the same standard-library metrics package.

The boundary to understand before verifying, stated plainly so results are not misread:

- Request metrics only populate when something calls the coordinator's Put, Get, or Delete.
  There is still no external client binary (that is a later increment), and the background
  loops (SWIM, anti-entropy, hint delivery) do not issue coordinator requests. So a node you
  launch by hand serves /metrics with the two families DECLARED (their # HELP and # TYPE
  lines) but with NO data rows until a request happens. The authoritative way to exercise and
  verify the request metrics is therefore the tests (and the embedded driver in Scenario F),
  which call the daemon's Put/Get/Delete directly.

Still not applicable in this part:

- External databases, brokers, a Prometheus server: none required; curl and the tests suffice.
- Tracing: out of scope; that would be a separate part.

What Part 2 adds and this runbook verifies: the histogram renders correct cumulative buckets,
_sum, and _count; the coordinator reports one observation per request with the right op and
result (including not_found for a missing read); and after real writes the daemon's /metrics
shows the request counter and latency histogram populated.

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

# 1c. protoc is NOT needed for this part.
protoc --version 2>/dev/null || echo "note: protoc absent (fine; Part 2 needs no codegen)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 8 Part 2 source present?
grep -q 'func (r \*Registry) NewHistogram' "$HELIX_HOME/internal/metrics/metrics.go" 2>/dev/null \
  && echo "present: histogram support (metrics)" || echo "MISSING: histogram support"
grep -q 'type Metrics interface' "$HELIX_HOME/internal/cluster/metrics.go" 2>/dev/null \
  && echo "present: cluster.Metrics interface" || echo "MISSING: cluster.Metrics interface"
grep -q 'func (c \*Coordinator) SetMetrics' "$HELIX_HOME/internal/cluster/coordinator.go" 2>/dev/null \
  && echo "present: coordinator instrumentation" || echo "MISSING: coordinator instrumentation"
grep -q 'helix_requests_total' "$HELIX_HOME/internal/daemon/daemon.go" 2>/dev/null \
  && echo "present: daemon request metrics" || echo "MISSING: daemon request metrics"

# 1f. Dependencies present, pinned, and still stdlib for metrics.
grep -q 'google.golang.org/grpc' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "present: grpc in go.mod" || echo "MISSING: grpc dep (restore per 2e)"
grep -qi 'prometheus' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "note: prometheus dep present (unexpected)" || echo "no prometheus dep (correct)"
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
git switch main && git pull --ff-only          # if Phase 8 Part 2 is merged
#   or: git switch phase-8/request-metrics && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2e. Restore dependencies if go.mod lost them (only if 1f showed grpc missing)

```bash
cd "$HELIX_HOME"
git checkout -- go.mod go.sum
grep 'google.golang.org/grpc ' go.mod
head -3 go.mod
```

---

## 3. Configure the required environment variables and services

The automated tests configure everything in code. To scrape a running node, use the Part 1
variables. The one Part 2 nuance is the quorum: to commit writes on a single node so the
request counters advance, set the quorum to one.

| Variable | Meaning | Example |
| --- | --- | --- |
| HELIX_METRICS_ADDR | admin HTTP server for /metrics and /healthz | `127.0.0.1:9090` |
| HELIX_NODE_ID | this node's id | `solo` |
| HELIX_PEERS | id=addr for every node | `solo=127.0.0.1:7070` |
| HELIX_BIND_ADDR | gRPC listen address | `127.0.0.1:7070` |
| HELIX_DATA_DIR | this node's storage directory | `/tmp/helix-run/solo` |
| HELIX_N / HELIX_R / HELIX_W | replication and quorums; use 1/1/1 on a single node | `1` / `1` / `1` |
| HELIX_LOG_FORMAT | log format | `text` |

New metric families this part exposes:

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| helix_requests_total | counter | op, result | coordinator requests; op in put/get/delete, result in ok/error/not_found |
| helix_request_duration_seconds | histogram | op | request latency, with _bucket/_sum/_count |

Dummy values the tests use (all built in):

| Input | Meaning | Dummy value |
| --- | --- | --- |
| ops | operations exercised | `put`, `get`, `delete` |
| results | outcome labels | `ok`, `not_found` (and `error` on failure) |
| buckets | histogram bounds (seconds) | `0.01, 0.1, 1` in the library test; DefaultLatencyBuckets in the daemon |
| sample key/value | writes driven in tests | `k`=`v` |
| missing key | not-found read | `missing` |

There is no database or broker to configure.

---

## 4. Start the backend and supporting services

No codegen this part. Build, then optionally run a single node with the quorum set to one and
metrics on.

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go build ./...
make build
ls -l bin/kvnode
```

### 4a. Run one node with metrics on (families declared, awaiting a driver)

```bash
cd "$HELIX_HOME"
rm -rf "$HELIX_RUN" && mkdir -p "$HELIX_RUN"
HELIX_NODE_ID=solo \
HELIX_PEERS="solo=127.0.0.1:7070" \
HELIX_BIND_ADDR="127.0.0.1:7070" \
HELIX_DATA_DIR="$HELIX_RUN/solo" \
HELIX_N=1 HELIX_R=1 HELIX_W=1 \
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

Reminder: this hand-run node has no client issuing Put/Get/Delete, so the request families
will show their TYPE headers but no data rows. To see them populate, drive requests through the
tests (Scenario D) or the embedded driver (Scenario F).

---

## 5. Verify service and backend health

Health means the tests pass and, for a running node, the families are declared on /metrics.

```bash
cd "$HELIX_HOME"
make fmt
make vet                                                     # expect no output, zero exit
GOTOOLCHAIN=local go build ./...                             # expect no output
go test ./internal/metrics/ ./internal/cluster/ ./internal/daemon/   # expect ok for all three
echo "exit code: $?"                                         # expect 0
```

With a node running from section 4a, confirm the families are declared:

```bash
curl -s "http://$HELIX_METRICS_ADDR/metrics" | grep -E '# TYPE helix_requests_total|# TYPE helix_request_duration_seconds'
```

You should see both TYPE lines (counter and histogram). Data rows appear only after requests.

---

## 6. Run Phase 8 Part 2 (automated tests)

The metrics types are concurrent and the daemon test starts a real HTTP server, so run under
the race detector.

```bash
cd "$HELIX_HOME"

# 6a. The three affected packages, verbosely, with the race detector.
go test -race -v ./internal/metrics/ ./internal/cluster/ ./internal/daemon/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends with
`check passed`. The Part 2 additions include:

```
--- PASS: TestHistogramExposition
--- PASS: TestCoordinatorReportsRequestMetrics
--- PASS: TestDaemonRequestMetrics
```

alongside the Part 1 metrics tests and every earlier test.

---

## 7. Execute each test scenario with dummy values

### Scenario A: build (the prerequisite)

Covered in section 4: `GOTOOLCHAIN=local go build ./...` clean and `bin/kvnode` built.

### Scenario B: histogram exposition (no network)

Dummy data (built in): buckets 0.01, 0.1, 1 with a `op` label; observations of 0.005, 0.05,
and 5, which land in cumulative buckets 1, 2, 2, and +Inf 3, with sum 5.055.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestHistogramExposition ./internal/metrics/
```

### Scenario C: coordinator instrumentation (fake sink)

Dummy data (built in): a single in-process replica with N=R=W=1; a Put, a Get, a Get of a
missing key, and a Delete. Asserts the recorded op and result labels, including that the
missing read is classified `not_found`, not `error`.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestCoordinatorReportsRequestMetrics ./internal/cluster/
```

### Scenario D: daemon request metrics on /metrics (headline)

Dummy data (built in): a single node with quorum one; a Put then a Get through the daemon API;
then the test scrapes /metrics and asserts the counter and histogram lines are present.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestDaemonRequestMetrics ./internal/daemon/
```

### Scenario E: manual scrape of a running node

With a node running from section 4a, confirm the families are declared. Because nothing drives
requests on a hand-run node, expect the TYPE headers but no data rows.

```bash
echo "--- families declared ---"
curl -s "http://$HELIX_METRICS_ADDR/metrics" \
  | grep -E 'helix_requests_total|helix_request_duration_seconds'
```

If the only lines are `# HELP` and `# TYPE`, that is correct for a node with no client traffic.

### Scenario F: parameterized driver that populates the request metrics

This drives your own mix of operations through a real daemon, then scrapes /metrics so you can
watch the counter and histogram fill in. It drops a temporary test into the daemon package,
runs it, and is removed afterward.

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

func TestRequestMetricsManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	puts := 5
	gets := 3
	missingGets := 2 // reads of keys that do not exist -> result="not_found"
	deletes := 1
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
		N:           1, R: 1, W: 1, // single-node quorum so writes commit
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

	ctx := context.Background()
	for i := 0; i < puts; i++ {
		if err := d.Put(ctx, key, val); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	for i := 0; i < gets; i++ {
		if _, err := d.Get(ctx, key); err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
	}
	for i := 0; i < missingGets; i++ {
		_, _ = d.Get(ctx, []byte("nope"))
	}
	for i := 0; i < deletes; i++ {
		if err := d.Delete(ctx, key); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}

	resp, err := http.Get("http://" + d.MetricsAddr() + "/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	// Log the request-metric lines so you can see the counts and buckets.
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "helix_requests_total") ||
			strings.HasPrefix(line, "helix_request_duration_seconds_count") {
			t.Log(line)
		}
	}
	if !strings.Contains(text, `helix_requests_total{op="put",result="ok"}`) {
		t.Fatalf("expected put/ok counter after %d puts:\n%s", puts, text)
	}
	if missingGets > 0 && !strings.Contains(text, `result="not_found"`) {
		t.Fatalf("expected a not_found result after %d missing gets", missingGets)
	}
}
HELIX_EOF
echo "created internal/daemon/manual_scenario_test.go"
```

Run it (the `-v` flag prints the request-metric lines):

```bash
cd "$HELIX_HOME"
go test -race -v -run TestRequestMetricsManualScenario ./internal/daemon/
```

Clean up:

```bash
cd "$HELIX_HOME"
rm -f internal/daemon/manual_scenario_test.go
echo "removed manual scenario test"
```

---

## 8. Verify the expected results

### Scenario B (histogram)

- PASS: cumulative `_bucket` values are 1, 2, 2 for le 0.01, 0.1, 1; `+Inf` is 3; `_count` is
  3; `_sum` is 5.055.

### Scenario C (coordinator)

- PASS: the fake sink records put=[ok], delete=[ok], and get=[ok, not_found], confirming op and
  result classification, including the distinct not_found for a missing read.

### Scenario D (daemon endpoint)

- PASS: after a Put and a Get, /metrics contains `helix_requests_total{op="put",result="ok"} 1`,
  the `op="get"` counter, and `helix_request_duration_seconds_count{op="put"} 1`.

### Scenario E (manual scrape)

- The two families appear with `# HELP` and `# TYPE` lines. Data rows are absent on a hand-run
  node with no client traffic, which is expected.

### Scenario F (parameterized driver)

- The test PASS line plus logged request-metric lines: `helix_requests_total{op="put",...}`
  equal to your `puts` count, an `op="get"` counter, a `result="not_found"` series when
  `missingGets` > 0, and `helix_request_duration_seconds_count{op=...}` matching the driven
  counts.

### Automated suite

- Section 6 shows the histogram, coordinator, and daemon request-metrics tests PASS alongside
  every earlier test, every package `ok`, and `make check` exiting 0 with no race warnings.

---

## 9. Troubleshooting

Build and vet (no codegen this part)
- `go vet` fails on a method signature: a new method collides with a standard interface name.
  Rename it rather than changing its signature (as `WriteExposition` was renamed from
  `WriteTo`).
- `go: go.mod requires go >= 1.25`: unrelated to Part 2 (no deps changed), but if an earlier
  tidy bumped it, hold at 1.22 (`go mod edit -go=1.22`) and rebuild with `GOTOOLCHAIN=local`.
- a prometheus dependency appears in go.mod: unexpected; the metrics are stdlib. If a stray
  `go get` added one, `git checkout -- go.mod go.sum`.

Request metrics not populating
- /metrics shows the families but no `helix_requests_total{...}` rows on a hand-run node: this
  is expected. Nothing drives coordinator requests without a client, and the background loops
  do not issue them. Use Scenario D or F to exercise the metrics.
- request counters stay empty even in a driver on a single node: the write did not commit
  because the quorum could not be met. On one node you must set N=R=W=1 (the tests and Scenario
  F do this); the default 3/2/2 cannot reach a write quorum of two on a single node, so Put
  returns an error and the counter records result="error", not "ok".
- a Get of an existing key records `not_found`: the value was never committed (see the quorum
  point above), or you read a different key than you wrote.

Histogram output
- `_sum` renders as `5.055` or in `g` format: that is valid exposition; Prometheus parses it.
- bucket counts look non-monotonic: they should be cumulative and non-decreasing across le. If
  they are not, the observation loop is wrong; re-run TestHistogramExposition to confirm the
  library itself is correct before suspecting your driver.
- `le="+Inf"` count does not equal `_count`: they must match; +Inf is the total. A mismatch
  points at the library, not the driver.

Cluster decoupling
- a build error about `internal/metrics` inside the cluster package: the cluster must depend
  only on its own `Metrics` interface. The adapter that references the metrics registry lives
  in the daemon, not the cluster.

Test isolation
- leftover `manual_scenario_test.go` breaks later `go test ./...` runs: remove it per the
  Scenario F cleanup.
- tests show `(cached)`: add `-count=1` to force a real run.

Terminal display
- long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above rather
  than typing. If the prompt looks corrupted after a large paste, run `reset` or open a new
  Cloud Shell tab.
- background jobs clutter the shell: list them with `jobs`, stop everything with
  `pkill -f './bin/kvnode'`.
