# Testing Phase 2 Part 3: compaction, tombstone GC, and block cache

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase
2 Part 3. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Helix is a single-node, embedded storage engine (a database), not a web service. So
several items from a typical service runbook do not apply in this phase, and pretending
otherwise would be misleading:

- External databases, caches, or message brokers: none. Helix is the database. Its only
  persistent state is a local directory of a MANIFEST file, write-ahead-log segments
  (NNNNNN.wal), and SSTables (NNNNNN.sst). The "database volume" to check is that
  directory.
- Credentials, API keys, tokens, connection URLs: none. There is no network listener
  yet. A gRPC API with mTLS arrives in Phase 7.
- HTTP or gRPC health endpoint: none yet. "Health" here means the process starts, logs
  "storage engine opened", and answers STATS in the interactive shell.
- API request payloads and resource IDs: none. The equivalent inputs are shell commands
  (SET, GET, DEL, FLUSH, STATS) and the key-value records you supply. All dummy values
  are provided below.

What Part 3 adds, and therefore what we verify: size-tiered compaction (merging similar
sized SSTables to bound how many a read consults), tombstone garbage collection (a full
compaction physically drops deleted keys), and an LRU block cache for SSTable data
blocks.

## 0. One-time shell setup used by every section

```bash
# Work from a predictable place.
export HELIX_HOME="$HOME/helix"

# Repository coordinates.
export HELIX_REPO="https://github.com/Talif787/helix.git"
```

---

## 1. Verify the existing environment

Run these checks first. Each prints what exists so you can decide what to initialize in
section 2.

```bash
# 1a. Go toolchain. Helix needs Go 1.22 or newer (module declares go 1.22).
go version || echo "MISSING: Go toolchain"

# 1b. Supporting tools (git is required; gh is only needed later for commits).
git --version || echo "MISSING: git"
gh --version 2>/dev/null || echo "note: GitHub CLI not found (only needed for PRs, not tests)"

# 1c. Does the repository already exist in this Cloud Shell home?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"
  git -C "$HELIX_HOME" remote -v
  git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1d. Does the Part 3 code exist in the working tree? These four files are new or changed
#     in this phase; all four present means the tree is at Part 3.
for f in \
  internal/storage/compaction.go \
  internal/storage/blockcache.go \
  internal/storage/compaction_test.go \
  internal/storage/blockcache_test.go; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done

# 1e. Do leftover data directories from earlier runs exist? Not an error, but tests below
#     use fresh directories, so knowing what is here avoids confusion.
ls -ld "$HELIX_HOME"/data* 2>/dev/null || echo "no data directories yet (expected on a clean checkout)"
```

Interpretation:
- If 1a prints a version below go1.22, follow 2a.
- If 1c says MISSING, follow 2b to clone.
- If 1d lists any MISSING while 1c is FOUND, your checkout predates Part 3; follow 2c to
  pull the latest.
- 1e is informational only.

---

## 2. Install or initialize anything missing

Do only the sub-steps flagged by section 1.

### 2a. Install Go 1.22+ (only if 1a is missing or too old)

Cloud Shell usually ships a recent Go. If not, install a private copy into your home
directory (no sudo needed):

```bash
GO_VERSION=1.22.6
cd "$HOME"
curl -fsSLO "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
rm -rf "$HOME/go-sdk" && mkdir -p "$HOME/go-sdk"
tar -C "$HOME/go-sdk" -xzf "go${GO_VERSION}.linux-amd64.tar.gz"
export PATH="$HOME/go-sdk/go/bin:$PATH"
echo 'export PATH="$HOME/go-sdk/go/bin:$PATH"' >> "$HOME/.bashrc"
go version   # should now print go1.22.6
```

### 2b. Clone the repository (only if 1c is missing)

```bash
cd "$HOME"
git clone "$HELIX_REPO" helix
cd "$HELIX_HOME"
git status
```

### 2c. Update an existing checkout to Part 3 (only if 1d found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
# If Part 3 is merged to main:
git switch main && git pull --ff-only
# If Part 3 is on its feature branch and not yet merged:
#   git switch phase-2/compaction && git pull --ff-only
git log --oneline -3
```

### 2d. Resolve dependencies (safe to run any time)

Helix is standard-library only, so this downloads nothing but confirms the module graph
is clean.

```bash
cd "$HELIX_HOME"
go mod download
go mod verify   # expect: all modules verified
```

### 2e. Initialize data directories for the manual scenarios

The engine creates its data directory on demand, so there is nothing to provision ahead
of time. To guarantee the manual scenarios start clean (no records left from a prior
run), remove the specific directories this runbook uses:

```bash
cd "$HELIX_HOME"
rm -rf ./data-p3 ./data-p3-tomb ./data-p3-cache
echo "cleared Part 3 scratch data directories"
```

---

## 3. Configure environment variables and services

There are no external services to configure. The only configuration is the HELIX_*
environment. Each scenario below sets its own values; this section documents what they
mean and provides the base block.

| Variable | Meaning | Value used here |
| --- | --- | --- |
| HELIX_DATA_DIR | Directory for MANIFEST, WAL, and SSTables | per scenario |
| HELIX_SYNC_WRITES | fsync on every write | true |
| HELIX_MAX_KEY_BYTES | Reject keys larger than this | 65536 |
| HELIX_MAX_VALUE_BYTES | Reject values larger than this | 1048576 |
| HELIX_MEMTABLE_MAX_BYTES | Memtable size that triggers a flush | 2048 (small, to make tables quickly) |
| HELIX_BLOCK_CACHE_BYTES | LRU block cache capacity (new in Part 3) | 1048576 |
| HELIX_COMPACTION_MIN_THRESHOLD | Similar-size SSTables that trigger a compaction (new in Part 3) | per scenario |
| HELIX_LOG_LEVEL | debug, info, warn, error | info |
| HELIX_LOG_FORMAT | json or text | text (readable) |

Base block (individual scenarios override DATA_DIR and COMPACTION_MIN_THRESHOLD):

```bash
cd "$HELIX_HOME"
export HELIX_SYNC_WRITES=true
export HELIX_MAX_KEY_BYTES=65536
export HELIX_MAX_VALUE_BYTES=1048576
export HELIX_MEMTABLE_MAX_BYTES=2048
export HELIX_BLOCK_CACHE_BYTES=1048576
export HELIX_LOG_LEVEL=info
export HELIX_LOG_FORMAT=text
```

---

## 4. Start the backend and supporting services

There are no supporting services. The "backend" is the kvnode binary with an interactive
shell that reads commands on standard input.

```bash
cd "$HELIX_HOME"

# Build the node and the sstable demo tool.
make build   # produces ./bin/kvnode (and ./bin/sstdemo)
ls -l ./bin/

# If make is unavailable for any reason, build directly:
#   go build -o bin/kvnode ./cmd/kvnode
```

You do not start a long-lived server. Each scenario pipes a script of commands into
./bin/kvnode, which opens the engine, runs the commands, and exits cleanly (flushing and
closing on EXIT).

---

## 5. Verify service and backend health

Health for an embedded engine means: it builds, static analysis is clean, it opens a
data directory, and it answers a command.

```bash
cd "$HELIX_HOME"

# 5a. Static health: formatting and vet must be clean.
make fmt
make vet    # expect no output and a zero exit code

# 5b. Liveness: open a throwaway directory, ask for STATS, exit.
export HELIX_DATA_DIR="$HOME/helix/data-healthcheck"
rm -rf "$HELIX_DATA_DIR"
printf 'STATS\nEXIT\n' | ./bin/kvnode
```

Expected: a log line similar to `level=INFO msg="storage engine opened" ... sstables=0`
and a STATS line similar to:

```
keys=0 approx_bytes=0 sstables=0 immutable=0 next_seq=1
```

A fresh directory reporting `sstables=0 next_seq=1` confirms the engine is healthy.
Clean up:

```bash
rm -rf "$HOME/helix/data-healthcheck"
```

---

## 6. Run Phase 2 Part 3 (automated tests)

The authoritative verification is the Go test suite under the race detector. Part 3 is
concurrency-sensitive (background compaction swapping the live table set under a
read-write lock), so `-race` is the gate that matters most.

```bash
cd "$HELIX_HOME"

# 6a. Run only the Part 3 tests, verbosely, with the race detector.
go test -race -v \
  -run 'TestCompaction|TestGetSizeTieredBucket|TestBlockCache' \
  ./internal/storage/

# 6b. Run the entire suite with the race detector (this also re-runs Phase 1 and 2 tests
#     to confirm no regression from the read-path and locking changes).
go test -race ./...

# 6c. Convenience target that runs fmt, vet, and the race suite together.
make check
```

Expected: every listed test prints `--- PASS`, each package prints `ok`, and `make check`
ends without error. Concretely, section 6a should include:

```
--- PASS: TestGetSizeTieredBucket
--- PASS: TestCompactionReducesTableCount
--- PASS: TestCompactionDropsTombstones
--- PASS: TestBlockCacheLRUEviction
--- PASS: TestBlockCacheKeyedByFileNum
--- PASS: TestBlockCacheNilIsNoOp
--- PASS: TestBlockCacheUpdateSameKey
```

---

## 7. Execute each test scenario with dummy values

These manual scenarios exercise the same behavior through the node so you can watch it
happen. Each is a self-contained shell block. The `sleep` between feeding writes and
reading STATS gives the background compaction goroutine time to run while the node is
still alive (the node blocks on standard input during the sleep).

### Scenario A: size-tiered compaction reduces the SSTable count

Dummy data: five rounds of twelve records each, keys `rN-kII`, values `value-N-II`. One
FLUSH per round makes one SSTable per round (five total). With the compaction threshold
set to 3, similar-size tables get merged, so the final count is below five.

```bash
cd "$HELIX_HOME"
export HELIX_DATA_DIR="$HOME/helix/data-p3"
export HELIX_COMPACTION_MIN_THRESHOLD=3
rm -rf "$HELIX_DATA_DIR"

{
  for r in 1 2 3 4 5; do
    for i in $(seq -w 0 11); do
      echo "SET r${r}-k${i} value-${r}-${i}"
    done
    echo "FLUSH"
  done
  sleep 3            # let background compaction run
  echo "STATS"
  echo "GET r1-k00"
  echo "GET r3-k06"
  echo "GET r5-k11"
  echo "EXIT"
} | ./bin/kvnode
```

### Scenario B: tombstone garbage collection during a full compaction, surviving reopen

Dummy data: table one has eight live keys `k0..k7`. Table two deletes `k2` and `k4`,
then adds six new keys `k100..k105`. The two tables are similar in size, so with the
threshold set to 2 the compaction covers all tables (a full compaction), which is the
only case where tombstones can be dropped safely.

```bash
cd "$HELIX_HOME"
export HELIX_DATA_DIR="$HOME/helix/data-p3-tomb"
export HELIX_COMPACTION_MIN_THRESHOLD=2
rm -rf "$HELIX_DATA_DIR"

# First session: seed, delete, flush, then observe the full compaction.
{
  for i in 0 1 2 3 4 5 6 7; do echo "SET k${i} original-${i}"; done
  echo "FLUSH"
  echo "DEL k2"
  echo "DEL k4"
  for i in 100 101 102 103 104 105; do echo "SET k${i} later-${i}"; done
  echo "FLUSH"
  sleep 3
  echo "STATS"
  echo "GET k0"
  echo "GET k2"
  echo "GET k100"
  echo "EXIT"
} | ./bin/kvnode

echo "--- sstables on disk after full compaction (expect 1) ---"
ls -1 "$HELIX_DATA_DIR"/*.sst 2>/dev/null | wc -l

# Second session: reopen and confirm the deletes are still deleted.
{
  echo "GET k2"
  echo "GET k4"
  echo "GET k0"
  echo "GET k100"
  echo "STATS"
  echo "EXIT"
} | ./bin/kvnode
```

### Scenario C: block cache configuration and repeated reads

Dummy data: a handful of keys read several times. There is no cache-hit counter exposed
yet, so this scenario confirms the cache is wired and that repeated reads are correct; it
also verifies the two new configuration validations reject bad input.

```bash
cd "$HELIX_HOME"
export HELIX_DATA_DIR="$HOME/helix/data-p3-cache"
export HELIX_COMPACTION_MIN_THRESHOLD=4
export HELIX_BLOCK_CACHE_BYTES=1048576
rm -rf "$HELIX_DATA_DIR"

# Correct config: seed, flush, then read the same keys repeatedly (served from cache).
{
  echo "SET user:1001 alice"
  echo "SET user:1002 bob"
  echo "SET user:1003 carol"
  echo "FLUSH"
  sleep 1
  echo "GET user:1001"
  echo "GET user:1001"
  echo "GET user:1002"
  echo "GET user:1001"
  echo "STATS"
  echo "EXIT"
} | ./bin/kvnode

echo "--- negative test: zero block cache must be rejected ---"
( export HELIX_BLOCK_CACHE_BYTES=0; printf 'EXIT\n' | ./bin/kvnode ) \
  && echo "UNEXPECTED: node started with invalid cache size" \
  || echo "OK: node refused to start with HELIX_BLOCK_CACHE_BYTES=0"

echo "--- negative test: compaction threshold below 2 must be rejected ---"
( export HELIX_COMPACTION_MIN_THRESHOLD=1; printf 'EXIT\n' | ./bin/kvnode ) \
  && echo "UNEXPECTED: node started with invalid threshold" \
  || echo "OK: node refused to start with HELIX_COMPACTION_MIN_THRESHOLD=1"
```

---

## 8. Verify the expected results

### Scenario A (compaction reduces table count)

- In the streamed log output, look for one `msg="flushed memtable to sstable"` per round
  and at least one `msg="compacted sstables"` line. The compacted line reports the number
  of input tables and whether tombstones were dropped, for example:
  `msg="compacted sstables" inputs=3 output=000007.sst dropped_tombstones=...`.
- The STATS line should report fewer than five SSTables, for example:
  `keys=0 approx_bytes=0 sstables=2 immutable=0 next_seq=61`
  (the exact count depends on tier sizes; the invariant is `sstables < 5`).
- The three GET lines return the seeded values: `value-1-00`, `value-3-06`, `value-5-11`.
  No key is lost by compaction.

### Scenario B (tombstone GC and durability)

- First session STATS shows `sstables=1` (both tables merged by the full compaction).
- First session GETs: `GET k0` returns `original-0`, `GET k100` returns `later-100`,
  and `GET k2` reports the key is not found.
- The on-disk count prints `1`.
- Second session (after reopen): `GET k2` and `GET k4` both report not found, proving the
  deletes survived a restart even though the tombstones were physically removed. `GET k0`
  returns `original-0` and `GET k100` returns `later-100`.

The important distinction: the deleted keys are gone from the file itself, not merely
hidden behind a tombstone. The automated test TestCompactionDropsTombstones proves this
by opening the surviving SSTable directly and confirming the deleted keys are absent.

### Scenario C (block cache and config validation)

- All GET lines return the seeded names (`alice`, `bob`), confirming repeated cached
  reads stay correct.
- Both negative tests print their `OK:` line, confirming the node refuses to start when
  HELIX_BLOCK_CACHE_BYTES is not positive or HELIX_COMPACTION_MIN_THRESHOLD is below 2.
  The rejection message names the offending variable.

### Automated suite

- Section 6 shows every Part 3 test PASS, every package `ok`, and `make check` exiting 0
  with no race detector warnings.

---

## 9. Troubleshooting

Setup and toolchain
- `go: command not found` or a version below go1.22: run section 2a, then re-open the
  shell or `source ~/.bashrc` so PATH includes the private Go install.
- `go: cannot find main module`: you are not inside the repository. `cd "$HELIX_HOME"`.
- `make: command not found`: build directly with `go build -o bin/kvnode ./cmd/kvnode`
  and substitute that binary wherever the runbook uses `make build`.
- `permission denied: ./bin/kvnode`: rebuild with `make build`; if it persists,
  `chmod +x ./bin/kvnode`.

Data and state
- Stale results or unexpected key counts: a previous run left data behind. Remove the
  scenario directory (for example `rm -rf ./data-p3`) and rerun. Every scenario already
  starts with an `rm -rf`.
- STATS shows more SSTables than expected in Scenario A: compaction is asynchronous.
  Increase the `sleep` from 3 to 5 seconds, or confirm the merge happened by checking the
  log for `msg="compacted sstables"`. The count also depends on tier sizes; the test that
  asserts a hard reduction is TestCompactionReducesTableCount.
- Leftover `.sst` files not referenced by MANIFEST after an abrupt termination: this is
  the documented rare crash window where a compaction committed the manifest but had not
  yet deleted its input files. It is harmless: recovery opens only the tables listed in
  MANIFEST, so data is correct. You may delete any `.sst` whose number is not in
  `cat "$HELIX_DATA_DIR"/MANIFEST` if you want to reclaim the space.

Services and integration
- Looking for a port, URL, or health endpoint to curl: there is none in this phase. The
  node has no network listener until Phase 7. Health is section 5.
- Looking for database or broker connection settings: there are none. Helix is the
  database; its state is the local data directory.

Concurrency
- `go test -race` reports a DATA RACE: capture the full report (it names the two
  conflicting goroutines and stacks) and treat it as a real defect in the compaction or
  locking path, not a flake. Save it with
  `go test -race ./internal/storage/ 2>&1 | tee /tmp/helix-race.txt` and share that file.

Terminal display
- Long pasted lines wrap and appear garbled: prefer the scripted blocks above (they feed
  standard input rather than requiring you to type). If the prompt itself looks corrupted
  after a large paste, run `reset` or open a new Cloud Shell tab.
