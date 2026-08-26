# Testing Phase 9 Part 2: the helixctl command-line client

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 9
Part 2. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 2 adds helixctl, a command-line client that wraps the Part 1 client library. It dials any
node's ClientService and issues coordinated Get, Put, and Delete, so for the first time you can
operate the cluster from the shell. What applies:

- A real command-line surface: yes. This is the manual client the earlier runbooks routed
  around; here the hands-on scenarios use helixctl directly with real keys and values.
- A network service and addresses: the CLI dials a node's gRPC listener at its bind address
  (the -addr flag).
- Credentials: optional mutual TLS via -tls-cert, -tls-key, and -tls-ca (certs from helixcert).

Different from recent parts, and simpler because of it:

- Code generation: NOT required. No .proto changed.
- New dependencies: none. helixctl uses the client library and grpc already present.

Boundaries worth stating so results are not misread:

- helixctl speaks gRPC through the client library, not HTTP. curl still does not apply to the
  data path. The only HTTP surface remains the Phase 8 metrics endpoint (/healthz, /metrics),
  which is unrelated to helixctl.
- A get of a missing key is a normal result: helixctl prints "(not found)" and exits 0, not an
  error. Scripts that need to distinguish absence should match that output.

Still not applicable:

- External databases, brokers: none. Nodes embed their own engines.

What Part 2 adds and this runbook verifies: helixctl builds; its put, get, and delete work
against a running node; a get of a missing key prints "(not found)"; usage errors are reported
cleanly; a write on one node of a multi-node cluster is readable through another node via the
CLI; and mutual TLS works with helixcert-issued certs.

## 0. One-time shell setup used by every section

```bash
export HELIX_HOME="$HOME/helix"
export HELIX_REPO="https://github.com/Talif787/helix.git"
export HELIX_RUN="/tmp/helix-run"
export HELIX_CERTS="/tmp/helix-certs"
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
protoc --version 2>/dev/null || echo "note: protoc absent (fine; Part 2 needs no codegen)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 9 Part 2 source present?
for f in cmd/helixctl/main.go cmd/helixctl/main_test.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done
grep -q 'cmd/helixctl' "$HELIX_HOME/Makefile" 2>/dev/null \
  && echo "present: Makefile builds helixctl" || echo "MISSING: Makefile helixctl build target"

# 1f. Dependencies present and pinned.
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

### 2c. protoc (not needed for Part 2)

Skip. No contract changed in this part.

### 2d. Update an existing checkout (only if 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only          # if Phase 9 Part 2 is merged
#   or: git switch phase-9/client-cli && git pull --ff-only   # if still on its branch
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

The automated test configures everything in code and needs no environment variables. To run
helixctl against a node, use the cluster variables from Phase 7 Part 6 to launch nodes, and the
helixctl flags to reach them.

Node launch variables (per node):

| Variable | Meaning | Example |
| --- | --- | --- |
| HELIX_NODE_ID | this node's id | `node-a` |
| HELIX_PEERS | id=addr for every node | `node-a=127.0.0.1:7070,node-b=127.0.0.1:7071,node-c=127.0.0.1:7072` |
| HELIX_BIND_ADDR | gRPC listen address; helixctl dials this | `127.0.0.1:7070` |
| HELIX_DATA_DIR | this node's storage directory | `/tmp/helix-run/node-a` |
| HELIX_N / HELIX_R / HELIX_W | replication and quorums; 1/1/1 for a single node | `3` / `2` / `2` for a cluster |

helixctl flags and inputs:

| Flag / input | Meaning | Example |
| --- | --- | --- |
| -addr | node ClientService address | `127.0.0.1:7070` |
| -timeout | request timeout | `5s` |
| -tls-cert / -tls-key / -tls-ca | client certs for mutual TLS (all three, or none) | `$HELIX_CERTS/client.crt` etc. |
| subcommand | operation | `put`, `get`, `delete` |
| key / value | data | `account:42`, `balance-100` |

Dummy values the automated test uses (all built in):

| Input | Meaning | Dummy value |
| --- | --- | --- |
| node id | in-process replica | `n1` |
| N / R / W | quorum for the test server | 1 / 1 / 1 |
| sample key/value | put then get | `k`=`v` |
| missing key | not-found read | `missing` |
| bad invocations | usage-error table | `put k`, `get`, `bogus`, partial `-tls-cert` |

There is no database or broker to configure.

---

## 4. Start the backend and supporting services

No codegen this part. Build all binaries, then run a node so helixctl has something to talk to.

```bash
cd "$HELIX_HOME"
GOTOOLCHAIN=local go build ./...
make build
ls -l bin/kvnode bin/helixctl bin/helixcert
```

### 4a. Single node with quorum one (simplest target for helixctl)

```bash
cd "$HELIX_HOME"
rm -rf "$HELIX_RUN" && mkdir -p "$HELIX_RUN"
HELIX_NODE_ID=solo \
HELIX_PEERS="solo=127.0.0.1:7070" \
HELIX_BIND_ADDR="127.0.0.1:7070" \
HELIX_DATA_DIR="$HELIX_RUN/solo" \
HELIX_N=1 HELIX_R=1 HELIX_W=1 \
HELIX_LOG_FORMAT=text \
./bin/kvnode > "$HELIX_RUN/solo.log" 2>&1 &
echo "started solo node (pid $!) on 127.0.0.1:7070"
sleep 2
grep -i 'daemon serving' "$HELIX_RUN/solo.log"
```

### 4b. Three-node cluster (to prove reads cross nodes)

```bash
cd "$HELIX_HOME"
rm -rf "$HELIX_RUN" && mkdir -p "$HELIX_RUN"
PEERS="node-a=127.0.0.1:7070,node-b=127.0.0.1:7071,node-c=127.0.0.1:7072"
for spec in "node-a:7070" "node-b:7071" "node-c:7072"; do
  id="${spec%%:*}"; port="${spec##*:}"
  HELIX_NODE_ID="$id" HELIX_PEERS="$PEERS" HELIX_BIND_ADDR="127.0.0.1:$port" \
  HELIX_DATA_DIR="$HELIX_RUN/$id" HELIX_LOG_FORMAT=text \
  ./bin/kvnode > "$HELIX_RUN/$id.log" 2>&1 &
  echo "started $id (pid $!) on 127.0.0.1:$port"
done
sleep 2
grep -i 'daemon serving' "$HELIX_RUN/node-a.log"
```

Stop any running nodes when done:

```bash
pkill -f './bin/kvnode' && echo "stopped all kvnode processes"
```

---

## 5. Verify service and backend health

Health means the tree builds, the helixctl test passes, and a running node answers a CLI call.

```bash
cd "$HELIX_HOME"
make fmt
make vet                                 # expect no output, zero exit
GOTOOLCHAIN=local go build ./...         # expect no output
go test ./cmd/helixctl/                  # expect: ok  github.com/talifpathan/helix/cmd/helixctl
echo "exit code: $?"                     # expect 0
```

With the solo node running from 4a, a live CLI check:

```bash
cd "$HELIX_HOME"
./bin/helixctl -addr 127.0.0.1:7070 put health-check ok   # expect: OK
./bin/helixctl -addr 127.0.0.1:7070 get health-check      # expect: ok
```

---

## 6. Run Phase 9 Part 2 (automated tests)

helixctl and its test server run over gRPC concurrently, so run under the race detector.

```bash
cd "$HELIX_HOME"

# 6a. The helixctl package, verbosely, with the race detector.
go test -race -v ./cmd/helixctl/

# 6b. The whole suite with the race detector.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: every test prints `--- PASS`, each package prints `ok`, and `make check` ends with
`check passed`. Section 6a should include:

```
--- PASS: TestHelixctlPutGetDelete
--- PASS: TestHelixctlUsageErrors
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: build (the prerequisite)

Covered in section 4. Success is `bin/helixctl` present and `make build` producing all three
binaries.

### Scenario B: automated put/get/delete round-trip

Dummy data (built in): a single-node server (N=R=W=1); key `k`=`v`; missing key `missing`. The
test drives put, get, get-missing, delete, and get-after-delete through helixctl's run function.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestHelixctlPutGetDelete ./cmd/helixctl/
```

### Scenario C: automated usage errors

Dummy data (built in): a table of bad invocations (no command, `put k`, `get`, `bogus`, partial
`-tls-cert`). Each must return an error.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestHelixctlUsageErrors ./cmd/helixctl/
```

### Scenario D: live CLI round-trip against the solo node

Start the solo node from 4a, then drive real commands with dummy values.

```bash
cd "$HELIX_HOME"
A="127.0.0.1:7070"
./bin/helixctl -addr $A put user:1001 alice        # expect: OK
./bin/helixctl -addr $A get user:1001              # expect: alice
./bin/helixctl -addr $A put user:1001 alice-v2     # overwrite; expect: OK
./bin/helixctl -addr $A get user:1001              # expect: alice-v2
./bin/helixctl -addr $A delete user:1001           # expect: OK
./bin/helixctl -addr $A get user:1001              # expect: (not found)
./bin/helixctl -addr $A get never-written          # expect: (not found)
echo "exit code of last (not found): $?"            # expect 0
```

### Scenario E: a write on one node, read through another (cluster)

Start the three-node cluster from 4b. With default 3/2/2, a coordinated write commits to a
quorum, so any node can serve the read. Write via node-a, read via node-c.

```bash
cd "$HELIX_HOME"
./bin/helixctl -addr 127.0.0.1:7070 put order:5005 shipped   # via node-a; expect: OK
./bin/helixctl -addr 127.0.0.1:7072 get order:5005           # via node-c; expect: shipped
./bin/helixctl -addr 127.0.0.1:7071 get order:5005           # via node-b; expect: shipped
```

### Scenario F: mutual TLS with helixcert

Issue certs (including a client cert), start a TLS cluster, and point helixctl at it with the
client cert. This reuses helixcert from Phase 7 Part 5.

```bash
cd "$HELIX_HOME"
pkill -f './bin/kvnode' 2>/dev/null || true
rm -rf "$HELIX_CERTS"
# Issue node certs plus a "client" identity for helixctl.
./bin/helixcert -dir "$HELIX_CERTS" -nodes node-a,client -hosts 127.0.0.1,localhost

rm -rf "$HELIX_RUN" && mkdir -p "$HELIX_RUN"
HELIX_NODE_ID=node-a \
HELIX_PEERS="node-a=127.0.0.1:7070" \
HELIX_BIND_ADDR="127.0.0.1:7070" \
HELIX_DATA_DIR="$HELIX_RUN/node-a" \
HELIX_N=1 HELIX_R=1 HELIX_W=1 \
HELIX_TLS_CERT="$HELIX_CERTS/node-a.crt" HELIX_TLS_KEY="$HELIX_CERTS/node-a.key" HELIX_TLS_CA="$HELIX_CERTS/ca.crt" \
HELIX_LOG_FORMAT=text \
./bin/kvnode > "$HELIX_RUN/node-a.log" 2>&1 &
sleep 2
grep -i 'daemon serving' "$HELIX_RUN/node-a.log"

# helixctl with the client cert over mutual TLS.
./bin/helixctl -addr 127.0.0.1:7070 \
  -tls-cert "$HELIX_CERTS/client.crt" -tls-key "$HELIX_CERTS/client.key" -tls-ca "$HELIX_CERTS/ca.crt" \
  put secure:1 encrypted-value        # expect: OK
./bin/helixctl -addr 127.0.0.1:7070 \
  -tls-cert "$HELIX_CERTS/client.crt" -tls-key "$HELIX_CERTS/client.key" -tls-ca "$HELIX_CERTS/ca.crt" \
  get secure:1                        # expect: encrypted-value

# A plaintext client against the TLS node should fail.
./bin/helixctl -addr 127.0.0.1:7070 get secure:1 ; echo "plaintext exit: $? (nonzero expected)"
```

Clean up after the scenarios:

```bash
pkill -f './bin/kvnode' 2>/dev/null || true
rm -rf "$HELIX_RUN" "$HELIX_CERTS"
echo "stopped nodes and removed run/cert dirs"
```

---

## 8. Verify the expected results

### Scenario A (build)

- `bin/helixctl` exists; `make build` also produced `bin/kvnode` and `bin/helixcert`.

### Scenario B (round-trip test)

- PASS: put prints OK, get returns `v`, a missing key prints `(not found)`, delete prints OK,
  and a get after delete prints `(not found)`.

### Scenario C (usage errors)

- PASS: every bad invocation returns an error (no command, missing args, unknown command,
  partial TLS flags).

### Scenario D (live solo)

- `OK` for puts and delete; `alice` then `alice-v2` for gets; `(not found)` after delete and for
  a never-written key; the last command exits 0 despite `(not found)`.

### Scenario E (cluster cross-read)

- `OK` for the write via node-a; `shipped` when reading the same key via node-c and node-b,
  proving the write is coordinated across the cluster, not local to one node.

### Scenario F (mutual TLS)

- `OK` and `encrypted-value` for the TLS client with the client cert; the plaintext client
  exits nonzero, confirming the node requires and verifies client certificates.

### Automated suite

- Section 6 shows both helixctl tests PASS alongside every earlier test, every package `ok`, and
  `make check` exiting 0 with no race warnings.

---

## 9. Troubleshooting

Build (no codegen this part)
- `undefined: rpc.DialClient` or `rpc.ClientTLSOption`: the checkout predates Phase 9. Update
  per 2d and confirm `internal/rpc/client_api.go` and `internal/rpc/tls.go` exist.
- `go: go.mod requires go >= 1.25`: hold at 1.22 (`go mod edit -go=1.22`), keep deps pinned, and
  rebuild with `GOTOOLCHAIN=local`. Do not `go mod tidy` to chase it.

Connecting
- `helixctl: ... connection refused`: no node is listening at -addr, or you used the wrong port.
  Confirm the node is up (`pgrep -af kvnode`) and -addr matches HELIX_BIND_ADDR. The error
  appears on the first request because the client dials lazily.
- `helixctl: context deadline exceeded`: the node is unreachable or overloaded within -timeout.
  Raise -timeout, or check the node log in `$HELIX_RUN`.
- helixctl hangs then times out on a single node: a write cannot reach quorum. On one node use
  HELIX_N=1 HELIX_R=1 HELIX_W=1; the default 3/2/2 needs a write quorum of two.

Behavior
- get prints `(not found)` for a key you just wrote: on a single node the write may not have
  committed (quorum), or you wrote via a different key or node. On a cluster, confirm the write
  returned OK before reading.
- `unknown command`: the first non-flag argument must be put, get, or delete. Flags come before
  the subcommand: `helixctl -addr X get key`, not `helixctl get -addr X key`.
- `-tls-cert, -tls-key, and -tls-ca must be given together`: pass all three TLS flags or none.

TLS (Scenario F)
- `helixctl: ... transport: authentication handshake failed` from a plaintext client against a
  TLS node: expected; the node requires client certs. Pass the three -tls-* flags.
- `x509: certificate signed by unknown authority`: the client cert and the node cert must come
  from the same helixcert run (same CA). Regenerate the whole set and retry.
- `bad certificate` or handshake failure with certs present: the client cert must have client
  authentication usage; helixcert issues certs valid for both server and client, so reissue if
  you hand-made one.

Test isolation
- tests show `(cached)`: add `-count=1` to force a real run.
- a stray node holds a port: `pkill -f './bin/kvnode'` before restarting.

Protocol
- trying to `curl` a node's client port: expected to fail; the client plane is gRPC, not HTTP.
  Use helixctl. The only HTTP surface is the Phase 8 metrics endpoint on HELIX_METRICS_ADDR.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the command blocks above rather than typing.
  If the prompt looks corrupted after a large paste, run `reset` or open a new Cloud Shell tab.
- Background jobs clutter the shell: list them with `jobs`, stop everything with
  `pkill -f './bin/kvnode'`.
