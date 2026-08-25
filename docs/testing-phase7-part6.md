# Testing Phase 7 Part 6: the node daemon

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 7
Part 6, the capstone of Phase 7. It is self-contained: every command can be copied and run as
written.

## What applies to Helix and what does not

Part 6 is the runnable node. The daemon assembles the whole stack: a storage engine, a
coordinator over a gRPC transport, a SWIM membership engine over a gRPC messenger, and a
gRPC server hosting both planes on one listener, optionally secured with mutual TLS. It runs
the membership, anti-entropy, and hint-delivery loops until a shutdown signal. This is the
first part where essentially everything applies:

- External dependencies: the grpc and protobuf modules from Part 2. Part 6 adds none.
- Code generation: NOT required. No .proto changed in this part.
- Environment variables: yes, for the first time a rich set. The daemon is configured through
  HELIX_* variables (node id, peers, quorums, TLS paths, intervals).
- A long-running service: yes. `kvnode` is now a real daemon that serves gRPC and runs
  background loops until signalled. This replaces the old interactive storage shell.
- Storage: yes. Each node embeds its own engine at its data directory.
- Credentials: optional. Set the three TLS paths (from `helixcert`) for mutual TLS, or leave
  them empty for plaintext.
- A multi-node cluster: yes. The integration test starts three daemons on loopback; you can
  also launch several `kvnode` processes by hand.

The one boundary that remains, stated plainly so it is not mistaken for a gap:

- There is no external client-facing coordinated gRPC service yet. Clients drive the cluster
  through the daemon's own API (Put/Get/Delete on the coordinator), which is what the tests
  do and what an embedding program would do. An outside process cannot yet open a socket and
  issue a coordinated Put/Get; that would be a small additional service and a helixctl tool, a
  natural next increment but not required for the node to be a complete daemon. Manual,
  in-cluster verification in this runbook therefore goes through the tests and a small
  embedded program, not a standalone client binary.

What Part 6 adds and this runbook verifies: the tree builds with no codegen; a real
three-node daemon cluster replicates a write across nodes, converges membership over gRPC,
and does both again under mutual TLS; the `kvnode` binary starts, serves, and shuts down
cleanly on a signal; and it validates its configuration.

## 0. One-time shell setup used by every section

```bash
export HELIX_HOME="$HOME/helix"
export HELIX_REPO="https://github.com/Talif787/helix.git"
export HELIX_CERTS="/tmp/helix-certs"
export HELIX_RUN="/tmp/helix-run"
```

---

## 1. Verify the existing environment

```bash
# 1a. Go toolchain. Helix's module is pinned to Go 1.22.
go version || echo "MISSING: Go toolchain"

# 1b. Supporting tools.
git --version || echo "MISSING: git"
gh --version 2>/dev/null || echo "note: GitHub CLI not found (only needed for PRs)"

# 1c. protoc is NOT needed for this part (no proto changed).
protoc --version 2>/dev/null || echo "note: protoc absent (fine; Part 6 needs no codegen)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 7 Part 6 source present?
for f in \
  internal/daemon/daemon.go \
  internal/daemon/daemon_test.go \
  cmd/kvnode/main.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done
grep -q 'internal/daemon' "$HELIX_HOME/cmd/kvnode/main.go" 2>/dev/null \
  && echo "present: kvnode wired to the daemon" || echo "MISSING: kvnode is not the daemon wrapper yet"

# 1f. Dependencies present and pinned (from earlier parts; Part 6 adds none).
grep -q 'google.golang.org/grpc' "$HELIX_HOME/go.mod" 2>/dev/null \
  && echo "present: grpc in go.mod" || echo "MISSING: grpc dep (restore per 2e)"
head -3 "$HELIX_HOME/go.mod" 2>/dev/null | grep -q 'go 1.22' \
  && echo "go.mod pinned to 1.22" || echo "note: check the go directive"
```

Interpretation: below go1.22 -> 2a; 1d MISSING -> 2b; 1e MISSING while 1d FOUND -> 2d; 1f
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

### 2c. protoc (not needed for Part 6)

Skip. No contract changed in this part.

### 2d. Update an existing checkout (only if 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only          # if Phase 7 Part 6 is merged
#   or: git switch phase-7/node-daemon && git pull --ff-only   # if still on its branch
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

This is the first part with a substantive daemon configuration. The automated tests configure
everything in code and need no environment variables. The manual cluster in section 4 uses
these HELIX_* variables per node:

| Variable | Meaning | Example |
| --- | --- | --- |
| HELIX_NODE_ID | this node's id (must appear in HELIX_PEERS) | `node-a` |
| HELIX_PEERS | id=addr for every node, comma-separated | `node-a=127.0.0.1:7070,node-b=127.0.0.1:7071,node-c=127.0.0.1:7072` |
| HELIX_BIND_ADDR | address to listen on (defaults to this node's peer addr) | `127.0.0.1:7070` |
| HELIX_DATA_DIR | this node's storage directory (must be unique per node) | `/tmp/helix-run/node-a` |
| HELIX_N / HELIX_R / HELIX_W | replication factor and quorums | `3` / `2` / `2` |
| HELIX_VNODES | virtual nodes per node on the ring | `128` |
| HELIX_MAX_HINTS | max hinted-handoff entries per node | `3` (defaults to N) |
| HELIX_TLS_CERT / KEY / CA | PEM paths for mutual TLS (all three, or none) | `$HELIX_CERTS/node-a.crt` etc. |
| HELIX_SWIM_INTERVAL | membership round period | `1s` |
| HELIX_ANTIENTROPY_INTERVAL | anti-entropy round period | `30s` |
| HELIX_HINT_INTERVAL | hint-delivery period | `10s` |
| HELIX_LOG_LEVEL / HELIX_LOG_FORMAT | logging (from the base config) | `info` / `text` |

Dummy values the automated tests use (all built in):

| Input | Meaning | Dummy value |
| --- | --- | --- |
| node ids | cluster members | `node-0`, `node-1`, `node-2` |
| addresses | dial targets | `127.0.0.1:0` at listen time, resolved to real ports |
| N / R / W | replication and quorums | 3 / 2 / 2 |
| SwimInterval | fast rounds for the test | 50ms |
| sample key / value | replicated write | `k`=`v`, and `secure`=`v` for the TLS test |
| CA / node certs | mutual TLS material | issued in-test from a `helix-test-ca` |

Supporting services: none external. Each node is its own embedded engine. The only
"credentials" are the optional TLS cert files, generated by `helixcert`.

---

## 4. Start the backend and supporting services

There is no codegen this part. Build, then either rely on the in-process test cluster
(section 6) or launch a real multi-node cluster by hand as shown here.

```bash
cd "$HELIX_HOME"

# 1) Build everything, including kvnode and helixcert, under the CI toolchain constraint.
GOTOOLCHAIN=local go build ./...
make build
ls -l bin/kvnode bin/helixcert
```

### 4a. Launch a real three-node cluster (plaintext) by hand

```bash
cd "$HELIX_HOME"
rm -rf "$HELIX_RUN" && mkdir -p "$HELIX_RUN"
PEERS="node-a=127.0.0.1:7070,node-b=127.0.0.1:7071,node-c=127.0.0.1:7072"

# Start each node in the background with its own id, bind address, and data dir.
for spec in "node-a:7070" "node-b:7071" "node-c:7072"; do
  id="${spec%%:*}"; port="${spec##*:}"
  HELIX_NODE_ID="$id" \
  HELIX_PEERS="$PEERS" \
  HELIX_BIND_ADDR="127.0.0.1:$port" \
  HELIX_DATA_DIR="$HELIX_RUN/$id" \
  HELIX_LOG_FORMAT=text HELIX_LOG_LEVEL=info \
  HELIX_SWIM_INTERVAL=500ms \
  ./bin/kvnode > "$HELIX_RUN/$id.log" 2>&1 &
  echo "started $id (pid $!) on 127.0.0.1:$port"
done
sleep 2
echo "--- node-a log ---"; tail -n 8 "$HELIX_RUN/node-a.log"
```

### 4b. Launch the same cluster with mutual TLS (optional)

```bash
cd "$HELIX_HOME"
rm -rf "$HELIX_CERTS"
./bin/helixcert -dir "$HELIX_CERTS" -nodes node-a,node-b,node-c -hosts 127.0.0.1,localhost
rm -rf "$HELIX_RUN" && mkdir -p "$HELIX_RUN"
PEERS="node-a=127.0.0.1:7070,node-b=127.0.0.1:7071,node-c=127.0.0.1:7072"
for spec in "node-a:7070" "node-b:7071" "node-c:7072"; do
  id="${spec%%:*}"; port="${spec##*:}"
  HELIX_NODE_ID="$id" HELIX_PEERS="$PEERS" HELIX_BIND_ADDR="127.0.0.1:$port" \
  HELIX_DATA_DIR="$HELIX_RUN/$id" HELIX_LOG_FORMAT=text \
  HELIX_TLS_CERT="$HELIX_CERTS/$id.crt" HELIX_TLS_KEY="$HELIX_CERTS/$id.key" HELIX_TLS_CA="$HELIX_CERTS/ca.crt" \
  ./bin/kvnode > "$HELIX_RUN/$id.log" 2>&1 &
  echo "started $id with mTLS (pid $!)"
done
sleep 2
grep -i 'serving\|tls' "$HELIX_RUN/node-a.log" | head
```

Stop a hand-launched cluster when done:

```bash
pkill -f './bin/kvnode' && echo "stopped all kvnode processes"
```

Note: the running processes serve node-to-node gRPC, but there is no external client binary in
this part, so the authoritative functional proof of Put/Get across the cluster is the
integration test in section 6 (and the embedded program in Scenario E), which drive the
coordinator directly.

---

## 5. Verify service and backend health

Health has two forms here: the daemon comes up and logs that it is serving, and the automated
cluster tests pass.

```bash
cd "$HELIX_HOME"
make fmt
make vet                                       # expect no output, zero exit
GOTOOLCHAIN=local go build ./...               # expect no output
go test ./internal/daemon/                     # expect: ok  github.com/talifpathan/helix/internal/daemon
echo "exit code: $?"                           # expect 0
```

For a hand-launched cluster (section 4a), health is the serving log line and a clean data
directory per node:

```bash
grep -i 'daemon serving\|kvnode ready' "$HELIX_RUN/node-a.log"
ls -l "$HELIX_RUN"/node-a   # expect the node's engine files
```

A single-node config-validation smoke check (starts, then is stopped immediately):

```bash
cd "$HELIX_HOME"
HELIX_NODE_ID=solo HELIX_PEERS="solo=127.0.0.1:7099" HELIX_DATA_DIR="$HELIX_RUN/solo" \
HELIX_LOG_FORMAT=text timeout 2s ./bin/kvnode ; echo "exit: $? (124 = timed out while serving, which is healthy)"
```

An exit code of 124 from `timeout` means the daemon was still running (serving) when the 2s
elapsed, which is the healthy outcome. A nonzero-but-not-124 exit means it failed to start;
read the logged error.

---

## 6. Run Phase 7 Part 6 (automated tests)

The daemon serves and dials concurrently and runs background loops, so run under the race
detector.

```bash
cd "$HELIX_HOME"

# 6a. The daemon package, verbosely, with the race detector.
go test -race -v ./internal/daemon/

# 6b. The whole suite with the race detector, to confirm nothing regressed.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: the daemon tests print `--- PASS`, every package prints `ok`, and `make check` ends
with `check passed`. Section 6a should include:

```
--- PASS: TestDaemonClusterReplicates
--- PASS: TestDaemonClusterConverges
--- PASS: TestDaemonClusterWithTLS
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: build and binaries (the prerequisite)

Covered in section 4. Success is `GOTOOLCHAIN=local go build ./...` clean and `make build`
producing `bin/kvnode` and `bin/helixcert`.

### Scenario B: replication across a real daemon cluster (headline 1)

Dummy data (built in): three daemons `node-0`/`node-1`/`node-2` on loopback; N=3, R=2, W=2;
key `k`=`v`. A write on one node is read back from another, entirely over gRPC.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestDaemonClusterReplicates ./internal/daemon/
```

### Scenario C: membership convergence over gRPC (headline 2)

Same three daemons; SWIM loops run every 50ms; the test waits for every node to see every
other node alive.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestDaemonClusterConverges ./internal/daemon/
```

### Scenario D: the whole cluster over mutual TLS

Same replication check with certs loaded from files, proving the daemon's TLS file path wires
through to the server and transport.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestDaemonClusterWithTLS ./internal/daemon/
```

### Scenario E: parameterized cluster with your own dummy values

This lets you set your own node ids, quorum, and key-value data, and watch a real daemon
cluster replicate and converge. It drops a temporary test into the daemon package, runs it,
and is removed afterward.

Create the test (edit the marked block to your own dummy data):

```bash
cd "$HELIX_HOME"
cat > internal/daemon/manual_scenario_test.go <<'HELIX_EOF'
package daemon

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/talifpathan/helix/internal/membership"
	"github.com/talifpathan/helix/internal/storage"
)

func TestDaemonManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	ids := []string{"node-1", "node-2", "node-3"}
	n, r, w := 3, 2, 2
	writeOn := "node-1"
	readOn := "node-3"
	key := []byte("account:42")
	val := []byte("balance-100")
	// ---------------------------------------------------------

	listeners := map[string]net.Listener{}
	peers := map[string]string{}
	for _, id := range ids {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen %s: %v", id, err)
		}
		listeners[id] = l
		peers[id] = l.Addr().String()
	}

	daemons := map[string]*Daemon{}
	defer func() {
		for _, d := range daemons {
			_ = d.Stop()
		}
	}()
	for _, id := range ids {
		d, err := New(Config{
			NodeID: id, BindAddr: peers[id], Listener: listeners[id], Peers: peers,
			N: n, R: r, W: w,
			Storage:             storage.Options{DataDir: t.TempDir()},
			SwimInterval:        50 * time.Millisecond,
			AntiEntropyInterval: time.Hour,
			HintInterval:        time.Hour,
		})
		if err != nil {
			t.Fatalf("new %s: %v", id, err)
		}
		if err := d.Start(); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
		daemons[id] = d
		t.Logf("%s serving at %s", id, d.Addr())
	}

	ctx := context.Background()
	if err := daemons[writeOn].Put(ctx, key, val); err != nil {
		t.Fatalf("put on %s: %v", writeOn, err)
	}
	got, err := daemons[readOn].Get(ctx, key)
	if err != nil || string(got) != string(val) {
		t.Fatalf("read on %s: got %q err %v", readOn, got, err)
	}
	t.Logf("wrote on %s, read %q back on %s", writeOn, got, readOn)

	deadline := time.Now().Add(5 * time.Second)
	for {
		converged := true
		for holder, d := range daemons {
			for _, target := range ids {
				if target == holder {
					continue
				}
				if st, ok := d.Members().StateOf(target); !ok || st != membership.Alive {
					converged = false
				}
			}
		}
		if converged {
			t.Log("all nodes see each other alive")
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("did not converge in time")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
HELIX_EOF
echo "created internal/daemon/manual_scenario_test.go"
```

Run it (the `-v` flag shows serving addresses and convergence):

```bash
cd "$HELIX_HOME"
go test -race -v -run TestDaemonManualScenario ./internal/daemon/
```

Clean up when finished (the temp test; test data dirs are removed by Go automatically):

```bash
cd "$HELIX_HOME"
rm -f internal/daemon/manual_scenario_test.go
echo "removed manual scenario test"
```

### Scenario F: the kvnode binary lifecycle (manual)

Covered in sections 4a and 5: start a cluster, confirm the serving log lines and per-node
data directories, then stop with `pkill -f './bin/kvnode'`. This proves the binary reads its
config, serves, and shuts down.

---

## 8. Verify the expected results

### Scenario A (build)

- `GOTOOLCHAIN=local go build ./...` succeeds; `bin/kvnode` and `bin/helixcert` exist.

### Scenario B (replication)

- PASS: a write on node-0 is read back as `v` on node-2, proving the coordinator replicates
  across real daemons over gRPC, with `-race` clean.

### Scenario C (convergence)

- PASS: within the deadline, every node reports every other node Alive, using the running
  SWIM loops over gRPC.

### Scenario D (TLS)

- PASS: the same replication succeeds with certs loaded from files, proving the daemon's
  mutual-TLS path is wired end to end.

### Scenario E (parameterized)

- The test PASS line, plus `-v` log lines: each node's serving address, the value read back on
  a different node than it was written, and a convergence confirmation.

### Scenario F (binary)

- `node-a.log` contains a "daemon serving" (and "kvnode ready") line; `$HELIX_RUN/node-a`
  contains engine files; `pkill` stops the processes without error.

### Automated suite

- Section 6 shows the three daemon tests PASS alongside every earlier test, every package
  `ok`, and `make check` exiting 0 with no race warnings.

---

## 9. Troubleshooting

Build (no codegen this part)
- `undefined: daemon.New` or `daemon.Config`: the daemon package is missing or the checkout
  predates Part 6. Update per 2d and confirm `internal/daemon/daemon.go` exists.
- `undefined: config.Load` or `observability.NewLogger`: you are on a checkout missing earlier
  scaffolding; confirm `internal/config` and `internal/observability` are present.
- `go: go.mod requires go >= 1.25`: hold at 1.22 (`go mod edit -go=1.22`) and rebuild with
  `GOTOOLCHAIN=local`; do not `go mod tidy` to chase it.

Daemon startup (hand-launched cluster)
- `HELIX_NODE_ID is required` or `HELIX_PEERS must include this node`: the node id is empty or
  not present as a key in HELIX_PEERS. Every node uses the same HELIX_PEERS; only HELIX_NODE_ID
  and HELIX_BIND_ADDR/HELIX_DATA_DIR differ per node.
- `HELIX_PEERS entry ... must be id=addr`: a malformed peer spec. Use
  `id=host:port,id=host:port` with no spaces.
- `listen ...: address already in use`: a previous cluster is still running or the port is
  taken. `pkill -f './bin/kvnode'` and pick fresh ports.
- `open storage: ...` or a lock error: two nodes share a data directory. Each node needs a
  unique HELIX_DATA_DIR.
- The daemon exits immediately with no error: it likely started and you did not keep it in the
  foreground. In the background launch it stays up; check `$HELIX_RUN/<id>.log`.

Cluster behavior
- `TestDaemonClusterConverges` times out: the SWIM loops did not converge within the deadline,
  usually a slow runner. The logic is deterministic; raise the 5s deadline in the test. A
  persistent failure with a generous deadline points at the messenger or member list.
- A hand-launched cluster's nodes never see each other alive in the logs: confirm all three
  share the exact same HELIX_PEERS and that each bind address matches its peer entry.
- Replication returns not-found right after a write: with N=3, R=2, W=2 a write needs 2 acks
  and a read 2 responses; if a node is down you may drop below quorum. Confirm all three
  daemons are up (`pgrep -af kvnode`).

TLS (optional)
- `load server TLS: ...` or `load client TLS: ...` at startup: a cert path is wrong or the
  files were not generated. Re-run `helixcert` and confirm the three HELIX_TLS_* paths point at
  `<id>.crt`, `<id>.key`, and `ca.crt`.
- Nodes fail to talk over TLS with an unknown-authority error: every node must use certs from
  the same `helixcert` run (same CA). Regenerate the whole set at once.
- Mixed plaintext and TLS nodes cannot talk: all nodes must agree. Either all set the TLS
  paths or none do.

Boundary (not a bug)
- Looking for a client binary to Put/Get against the running cluster from another terminal:
  there is none in this part. Coordinated client access goes through the daemon's API; the
  functional proof is the integration test and the Scenario E embedded program. An external
  client service is the next increment.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above rather
  than typing. If the prompt looks corrupted after a large paste, run `reset` or open a new
  Cloud Shell tab.
- Background jobs clutter the shell: list them with `jobs`, and stop everything with
  `pkill -f './bin/kvnode'`.
