# Testing Phase 7 Part 1: wire contract, codegen toolchain, and peer registry

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 7
Part 1. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Phase 7 is the network phase, and it is being built in parts. Part 1 (this runbook) is the
foundation only: the protobuf wire contract for node-to-node RPC, the code-generation
toolchain that turns it into Go, and a peer registry that maps node ids to network
addresses. The gRPC server, client, networked transport, mTLS, and the node daemon are
Parts 2 through 5 and do not exist yet. So several items a full network runbook would cover
genuinely do not apply here, and saying so is more useful than inventing them:

- A running server, listening port, or network endpoint: none yet. Part 1 adds no process
  that serves traffic. The gRPC server and the node daemon arrive in Parts 2 and 5.
- Credentials, TLS certificates, tokens: none yet. mTLS is Part 4.
- Connection URLs and API payloads: none yet. There is nothing to dial or POST to. The RPC
  request and response shapes are defined in the contract but not yet served.
- External databases, brokers, caches: none. Helix nodes are their own embedded engines.

What Part 1 adds, and therefore what we verify: the peer registry behaves correctly
(stdlib-only Go, fully testable now), and the protobuf contract generates valid Go and
compiles with the gRPC and protobuf runtime libraries. That codegen step is the one new
piece of tooling, so it is the headline scenario.

Important CI note: the committed Part 1 tree introduces no new dependencies and needs no
code generation to build. The .proto file is inert to `go build`, and the peer registry is
standard-library Go. Generated code and the gRPC/protobuf modules are committed in Part 2,
where code first imports them. This runbook shows how to run codegen to validate the
toolchain now, and how to discard that output so your Part 1 tree stays clean.

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

# 1b. Supporting tools (git required; gh only needed for commits).
git --version || echo "MISSING: git"
gh --version 2>/dev/null || echo "note: GitHub CLI not found (only needed for PRs)"

# 1c. protoc, the protocol buffer compiler (only needed to validate codegen in this part).
protoc --version 2>/dev/null || echo "note: protoc not found (install in 2c to validate codegen)"

# 1d. Does the repository already exist here?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 7 Part 1 content present?
for f in \
  proto/helix/v1/node.proto \
  internal/rpc/peers.go \
  internal/rpc/peers_test.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done

# 1f. Confirm the Makefile has the codegen targets.
grep -qE '^proto:' "$HELIX_HOME/Makefile" 2>/dev/null \
  && echo "present: make proto target" || echo "MISSING: proto target"
grep -qE '^proto-tools:' "$HELIX_HOME/Makefile" 2>/dev/null \
  && echo "present: make proto-tools target" || echo "MISSING: proto-tools target"
```

Interpretation: below go1.22 -> 2a; 1d MISSING -> 2b; 1e or 1f MISSING while 1d FOUND -> 2d;
protoc missing and you want to validate codegen (Scenario B) -> 2c.

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

### 2c. Install protoc and the Go plugins (only needed to validate codegen, Scenario B)

```bash
# The compiler itself. Cloud Shell is Debian-based, so apt works.
sudo apt-get update && sudo apt-get install -y protobuf-compiler
protoc --version   # expect libprotoc 3.x or newer

# The Go plugins. They install into $(go env GOPATH)/bin, which must be on PATH so protoc
# can find them.
cd "$HELIX_HOME"
make proto-tools
export PATH="$PATH:$(go env GOPATH)/bin"
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> "$HOME/.bashrc"
which protoc-gen-go protoc-gen-go-grpc   # both should resolve
```

### 2d. Update an existing checkout (only if 1e or 1f found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only         # if Phase 7 Part 1 is merged
#   or: git switch phase-7/wire-contract && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2e. Resolve dependencies (safe any time)

The committed Part 1 tree is still standard-library only, so this confirms a clean module.

```bash
cd "$HELIX_HOME"
go mod download
go mod verify   # expect: all modules verified
```

### 2f. Data directories

None. Part 1 adds no storage and no runtime state. There is nothing to provision or seed.

---

## 3. Configure the required environment variables and services

There are no services to configure and no HELIX_* environment variables on this path. Two
shell PATH entries matter only if you validate codegen (Scenario B):

| Variable | Why | Value |
| --- | --- | --- |
| PATH includes Go bin | run the `go` and installed plugin commands | `$HOME/go-sdk/go/bin` (if you installed Go in 2a) |
| PATH includes GOPATH bin | protoc must find protoc-gen-go and protoc-gen-go-grpc | `$(go env GOPATH)/bin` |

The peer registry, the one runtime component in this part, is configured in code, not
through environment variables. The dummy values it takes are node ids and addresses:

| Input | Meaning | Dummy values used here |
| --- | --- | --- |
| node id | logical node name | `node-a`, `node-b`, `node-c` |
| address | dial target "host:port" | `10.0.0.1:7000`, `10.0.0.2:7000`, `10.0.0.3:7000` |

These addresses are placeholders: nothing dials them in Part 1. They exist to exercise the
registry's set, resolve, overwrite, remove, list, and snapshot behavior.

---

## 4. Start the backend and supporting services

There is no backend to start in Part 1. No server, no listener, no daemon. The peer registry
is a library type exercised by its unit tests, and the contract is a file compiled by the
codegen toolchain. The first runnable network service is the gRPC server in Part 2, and the
node daemon in Part 5.

---

## 5. Verify service and backend health

Health for Part 1 means the module builds, static analysis is clean, and the peer registry
tests pass. There is no service process to probe.

```bash
cd "$HELIX_HOME"
make fmt
make vet                     # expect no output, zero exit code
go build ./...               # expect no output (the .proto is inert to the Go build)
go test ./internal/rpc/      # expect: ok  github.com/talifpathan/helix/internal/rpc
echo "exit code: $?"         # expect 0
```

---

## 6. Run Phase 7 Part 1 (automated tests)

The peer registry is concurrent, so run it under the race detector.

```bash
cd "$HELIX_HOME"

# 6a. Just the new package, verbosely, with the race detector.
go test -race -v ./internal/rpc/

# 6b. The whole suite with the race detector, to confirm nothing regressed.
go test -race ./...

# 6c. fmt, vet, and the race suite together.
make check
```

Expected: the peer registry tests print `--- PASS`, every package prints `ok`, and
`make check` ends with `check passed`. Section 6a should include:

```
--- PASS: TestPeerRegistrySetAddressRemove
--- PASS: TestPeerRegistryNodesSorted
--- PASS: TestPeerRegistrySnapshotIsCopy
--- PASS: TestPeerRegistryConcurrentAccess
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: the peer registry (automated)

Dummy data (built into the tests): node ids `node-a`/`node-b`/`node-c` with addresses like
`10.0.0.1:7000`. Covers set and overwrite, resolve, remove, sorted listing, snapshot
independence, and concurrent access.

```bash
cd "$HELIX_HOME"
go test -race -v -run TestPeerRegistry ./internal/rpc/
```

### Scenario B: the contract generates valid Go and compiles (the headline for this part)

This validates the code-generation toolchain end to end: protoc plus the plugins turn the
.proto into Go, and that Go compiles against the gRPC and protobuf runtime libraries. It
requires protoc and the plugins from 2c. Because Part 1 does not commit generated code, this
scenario generates into a scratch checkout so your real tree stays clean.

```bash
# Work in a throwaway copy so codegen output and go.mod changes are discarded afterward.
cd "$HOME"
rm -rf helix-protogen && cp -r "$HELIX_HOME" helix-protogen
cd "$HOME/helix-protogen"
export PATH="$PATH:$(go env GOPATH)/bin"

# 1) Generate Go from the contract.
make proto
echo "--- generated files ---"
ls -l internal/rpc/helixv1/         # expect node.pb.go and node_grpc.pb.go

# 2) Pull in the runtime dependencies the generated code imports, then build.
go mod tidy                         # adds google.golang.org/grpc and protobuf to go.mod
go build ./...                      # the generated package must compile

echo "PHASE7-PART1 CODEGEN: OK"
```

Clean up the scratch copy when done:

```bash
cd "$HOME" && rm -rf helix-protogen
echo "removed scratch codegen copy"
```

### Scenario C: parameterized peer registry with your own dummy values

This lets you supply your own node ids and addresses and inspect the registry's behavior. It
drops a temporary test into the package, runs it, and is removed afterward.

Create the test (edit the marked block to your own dummy data):

```bash
cd "$HELIX_HOME"
cat > internal/rpc/manual_scenario_test.go <<'HELIX_EOF'
package rpc

import "testing"

func TestPeerRegistryManualScenario(t *testing.T) {
	// ---------------- EDIT THESE DUMMY VALUES ----------------
	peers := map[string]string{
		"node-1": "10.0.0.11:7000",
		"node-2": "10.0.0.12:7000",
		"node-3": "10.0.0.13:7000",
	}
	lookup := "node-2"
	// ---------------------------------------------------------

	r := NewPeerRegistryFromMap(peers)
	t.Logf("registered %d peers: %v", r.Len(), r.Nodes())

	addr, ok := r.Address(lookup)
	if !ok {
		t.Fatalf("expected to resolve %s", lookup)
	}
	t.Logf("%s resolves to %s", lookup, addr)

	r.Set(lookup, "10.9.9.9:7000")
	if a, _ := r.Address(lookup); a != "10.9.9.9:7000" {
		t.Fatalf("overwrite failed, got %s", a)
	}
	r.Remove("node-3")
	if _, ok := r.Address("node-3"); ok {
		t.Fatal("node-3 should be gone after Remove")
	}
	t.Logf("after overwrite and remove, peers: %v", r.Nodes())
}
HELIX_EOF
echo "created internal/rpc/manual_scenario_test.go"
```

Run it (the `-v` flag shows the log lines):

```bash
cd "$HELIX_HOME"
go test -race -v -run TestPeerRegistryManualScenario ./internal/rpc/
```

Clean up when finished (there is no data directory for this part, only the temp test):

```bash
cd "$HELIX_HOME"
rm -f internal/rpc/manual_scenario_test.go
echo "removed manual scenario test"
```

---

## 8. Verify the expected results

### Scenario A (peer registry)

- PASS: set and overwrite resolve to the latest address; remove deletes; `Nodes` returns a
  sorted slice; a `Snapshot` is an independent copy (mutating it does not change the
  registry); concurrent set/resolve/list under `-race` reports no data race.

### Scenario B (codegen)

- `ls internal/rpc/helixv1/` shows `node.pb.go` and `node_grpc.pb.go`.
- `go mod tidy` adds `google.golang.org/grpc` and `google.golang.org/protobuf` to go.mod.
- `go build ./...` succeeds, ending with the `PHASE7-PART1 CODEGEN: OK` line. This proves the
  contract is valid and the generated Go compiles against the runtime libraries, which is the
  toolchain Part 2 depends on.

### Scenario C (parameterized)

- The test PASS line, plus `-v` log lines: the registered peer list, the resolved address,
  and the peer list after an overwrite and a remove.

### Automated suite

- Section 6 shows the four peer registry tests PASS, every package `ok`, and `make check`
  exiting 0 with no race detector warnings.

---

## 9. Troubleshooting

Setup and toolchain
- `go: command not found` or a version below go1.22: run section 2a, then `source ~/.bashrc`.
- `go: cannot find main module`: you are not inside the repository. `cd "$HELIX_HOME"`.
- `make: command not found`: run the underlying commands directly (`go test ./internal/rpc/`,
  and for codegen the `protoc ...` line the `proto` target runs).

Codegen (Scenario B)
- `protoc: command not found`: run 2c to install `protobuf-compiler`.
- `protoc-gen-go: program not found` or `plugin not found`: the plugins are installed but not
  on PATH. Run `export PATH="$PATH:$(go env GOPATH)/bin"` and retry. Confirm with
  `which protoc-gen-go protoc-gen-go-grpc`.
- `go mod tidy` cannot reach the network: Cloud Shell has outbound access by default; if you
  are behind a restricted network, tidy cannot fetch grpc/protobuf. This is the one step that
  needs the network in Part 1's optional codegen validation.
- Generated files appear under a path you did not expect: the `module=` option in the `proto`
  target routes output to each file's `go_package` path, which is `internal/rpc/helixv1`. If
  you edited `go_package`, the output path moves with it.
- After running codegen in your real tree by mistake, your working tree shows untracked
  `internal/rpc/helixv1/*.pb.go` and a modified go.mod/go.sum: discard them with
  `git clean -fd internal/rpc/helixv1 && git checkout -- go.mod go.sum` (Part 1 does not
  commit generated code; that happens in Part 2).

Build and tests
- `go build ./...` fails complaining about `internal/rpc/helixv1`: you have partial generated
  output present. Either finish Scenario B (generate all files and `go mod tidy`) or remove
  the directory: `rm -rf internal/rpc/helixv1`.
- `go test -race` reports a DATA RACE in the peer registry: treat it as a real defect in the
  locking, not a flake, and capture it with
  `go test -race ./internal/rpc/ 2>&1 | tee /tmp/helix-race.txt`.
- Leftover `manual_scenario_test.go` from Scenario C causing failures on later `go test ./...`
  runs: remove it per the cleanup step.

Services and integration
- Looking for a port to curl, a URL to hit, or a certificate to configure: none exist in
  Part 1. The gRPC server is Part 2, control-plane and SWIM RPCs are Part 3, mTLS is Part 4,
  and the runnable node daemon is Part 5. This runbook will be extended per part.
- Looking for connection strings or credentials: there are none in this part.

Terminal display
- Long pasted blocks wrap and look garbled: prefer the heredoc and command blocks above
  rather than typing. If the prompt looks corrupted after a large paste, run `reset` or open
  a new Cloud Shell tab.
