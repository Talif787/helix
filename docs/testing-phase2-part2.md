# Testing Phase 2, Part 2 (engine integration: flush, merged reads, manifest recovery)

A self-contained runbook for a fresh Cloud Shell session. Every command can be copied and run as-is.

## Scope and what does not apply at this phase

Phase 2 Part 2 wires the SSTable and memtable layers into a working log-structured merge engine. It is still a Go library plus the `kvnode` node process; it is not a networked service, so some typical runbook items do not exist yet and are mapped to their real analog:

- No external database, cache, or message broker, and no HTTP or gRPC server. The `kvnode` node is the backend you start, and its data directory is the storage volume.
- No credentials, tokens, or URLs. Those arrive in Phase 7 (gRPC, mTLS, client and admin APIs). The meaningful inputs here are key/value records and the `HELIX_` environment variables.
- The storage volume is a plain directory. After a flush it contains numbered WAL segments (`NNNNNN.wal`), SSTables (`NNNNNN.sst`), and a `MANIFEST` file. This runbook shows how to seed it, inspect it, and prove recovery from it.

What is new versus Part 1: the engine now flushes full memtables to SSTables in the background, merges reads across the memtable and all SSTables, and recovers on restart from the manifest plus any unflushed WAL segments. The node gained `FLUSH` and richer `STATS`.

## 1. Verify the existing environment

```bash
echo "== Go =="; go version 2>/dev/null || echo "Go NOT installed"
echo "== Repo =="; [ -d ~/helix/.git ] && echo "~/helix present" || echo "~/helix MISSING"
echo "== Phase 2 Part 2 files =="
for f in \
  internal/storage/engine.go \
  internal/storage/manifest.go \
  internal/storage/engine_flush_test.go ; do
  [ -f ~/helix/"$f" ] && echo "ok       $f" || echo "MISSING  $f"
done
echo "== Part 2 markers in engine.go =="
grep -q "drainImmutables" ~/helix/internal/storage/engine.go 2>/dev/null && echo "ok       engine has flush integration" || echo "MISSING  engine flush integration"
grep -q "last_seq" ~/helix/internal/storage/manifest.go 2>/dev/null && echo "ok       manifest has last_seq" || echo "MISSING  manifest last_seq"
echo "== git branch =="; git -C ~/helix branch --show-current 2>/dev/null || echo "no repo"
echo "== data volume =="; [ -d ~/helix/data ] && du -sh ~/helix/data 2>/dev/null || echo "no data dir yet (expected on a fresh session)"
```

Interpretation: Go must be 1.22 or newer. If `~/helix` is present, all three files show `ok`, and both markers show `ok`, skip to section 3. Otherwise do section 2.

## 2. Install or initialize what is missing

### Go missing or older than 1.22
```bash
cd ~ && curl -sSLO https://go.dev/dl/go1.22.12.linux-amd64.tar.gz
mkdir -p ~/.local && tar -C ~/.local -xzf go1.22.12.linux-amd64.tar.gz
echo 'export PATH=$HOME/.local/go/bin:$PATH' >> ~/.bashrc && source ~/.bashrc
go version
```

### Repository missing
```bash
git clone https://github.com/Talif787/helix.git ~/helix
```

### Phase 2 Part 2 files missing
They are delivered in `helix-phase1.zip`. Upload it with the Cloud Shell three-dot menu, then overwrite the source tree (this does not touch `.git`):
```bash
cd ~ && unzip -o helix-phase1.zip
```
Re-run the section 1 checks; every line should read `ok`.

Recommended once the tests pass: commit Part 2 to a branch so future sessions get it from git.
```bash
cd ~/helix
git switch -c phase-2/engine-integration
git add internal/storage/engine.go internal/storage/manifest.go \
        internal/storage/engine_flush_test.go internal/config/config.go cmd/kvnode/main.go
git commit -m "feat(storage): engine integration with flush, merged reads, and manifest recovery"
git push -u origin phase-2/engine-integration
```

## 3. Configure environment variables and services

There are no services to start. The `kvnode` node reads these variables (all optional, with defaults). For testing flush behavior, the important one is `HELIX_MEMTABLE_MAX_BYTES`: set it small to force frequent background flushes.

| Variable | Suggested test value | Effect |
| --- | --- | --- |
| `HELIX_DATA_DIR` | `./data/node` | The storage volume directory. Use a fresh path to start clean. |
| `HELIX_MEMTABLE_MAX_BYTES` | `512` | Flush threshold. Small means writes rotate and flush often. |
| `HELIX_SYNC_WRITES` | `true` | Fsync each write. |
| `HELIX_LOG_FORMAT` | `text` | Human-readable logs for the demo. |
| `HELIX_LOG_LEVEL` | `info` | Show the flush and open log lines. |

Build the node once so repeated runs are fast:
```bash
cd ~/helix
go build -o bin/kvnode ./cmd/kvnode
```

## 4. Start the backend and supporting services

The backend is the `kvnode` node. Start it interactively:
```bash
cd ~/helix
HELIX_DATA_DIR=./data/node HELIX_LOG_FORMAT=text ./bin/kvnode
# then type, one per line:
#   SET user:1001 alice
#   GET user:1001
#   FLUSH
#   STATS
#   EXIT
```

Or drive it non-interactively by piping a script (this is how the scenarios below seed data):
```bash
printf 'SET user:1001 alice\nSET user:1002 bob\nFLUSH\nSTATS\nGET user:1001\nEXIT\n' \
  | HELIX_DATA_DIR=./data/node HELIX_LOG_FORMAT=text ./bin/kvnode
```

Expected: a `storage engine opened` log line, a `flushed memtable to sstable` line after `FLUSH`, a `STATS` line showing `sstables=1`, `GET user:1001` printing `alice`, then a clean `engine closed cleanly` on exit.

## 5. Verify service and backend health

Health here is: it compiles and vets, the node opens and closes cleanly, and the on-disk volume is well formed.

```bash
cd ~/helix
go build ./... && go vet ./...
```

Inspect the storage volume the node just produced:
```bash
ls -l data/node
cat data/node/MANIFEST
```

Expected: a `MANIFEST`, one or more `.sst` files, and an active `.wal` file. The manifest is JSON of roughly this shape (exact numbers vary):
```json
{
  "tables": [3],
  "next_file_num": 4,
  "last_flushed_wal": 1,
  "last_seq": 2
}
```
`tables` lists the live SSTable file numbers, `last_flushed_wal` is how far flushing has progressed, and `last_seq` is the sequence high-water mark that keeps IDs monotonic across restarts.

## 6. Run Phase 2, Part 2

(The eighth requirement in the request labels this "Part 1"; it means Part 2, the current phase.)

Run the package tests, then the full race-enabled gate:
```bash
cd ~/helix
go test ./internal/storage/                 # fast unit run
go test -race -count=1 ./internal/storage/  # race detector, no cache
make check                                  # fmt + vet + race across the module; ends with "check passed"
```

## 7. Execute each test scenario with the required dummy values

### Automated tests

Each command runs one test verbosely. Dummy data for each is listed.

Flush then reopen, data served from SSTables. Dummy data: 200 records `key0000`..`key0199` with values `val0000`..`val0199`, threshold 256 bytes.
```bash
go test -run TestEngineFlushPersistsAcrossReopen -v ./internal/storage/
```

Merged read precedence. Dummy data: key `k` set to `v1` (flushed), then `v2` (memtable), then deleted; proves memtable shadows SSTable and a newer tombstone shadows an older value.
```bash
go test -run TestEngineMergedReadPrecedence -v ./internal/storage/
```

Recovery from SSTables and the active WAL together. Dummy data: 50 `flushed00`..`flushed49` (flushed to an SSTable) and 20 `active00`..`active19` (left in the active WAL).
```bash
go test -run TestEngineRecoversSSTablesAndActiveWAL -v ./internal/storage/
```

Sequence numbers stay monotonic across a flush. Dummy data: keys `a`,`b` flushed, then key `c`; next-seq must go 3 then 4, not reset.
```bash
go test -run TestEngineSeqMonotonicAcrossFlush -v ./internal/storage/
```

Delete survives a flush and a reopen. Dummy data: key `gone` set then deleted then flushed.
```bash
go test -run TestEngineDeleteAcrossFlushStaysDeleted -v ./internal/storage/
```

Concurrent writes with background flushing (run under the race detector). Dummy data: 2000 keys `k00000`..`k01999` written from many goroutines, threshold 512 bytes.
```bash
go test -race -run TestEngineConcurrentWritesWithFlush -v ./internal/storage/
```

Phase 1 engine behavior still holds (regression check).
```bash
go test -run 'TestEnginePutGetDelete|TestEngineDurabilityAcrossReopen|TestEngineValidation' -v ./internal/storage/
```

### Backend scenarios through the node

These exercise the same features through the running node with concrete dummy data. Each uses its own fresh data directory so results are deterministic.

Background auto-flush under load. Dummy data: 500 records `key1`..`key500`, threshold 512 bytes. Watch for `flushed memtable to sstable` log lines and `sstables` greater than 1 in STATS.
```bash
{ for i in $(seq 1 500); do echo "SET key$i val$i"; done; echo STATS; echo EXIT; } \
  | HELIX_DATA_DIR=./data/auto HELIX_MEMTABLE_MAX_BYTES=512 HELIX_LOG_FORMAT=text ./bin/kvnode
ls -l data/auto
cat data/auto/MANIFEST
```

Recovery via SSTable (manifest path). Dummy data: key `persist` = `yes`, flushed in session 1, read back in session 2.
```bash
printf 'SET persist yes\nFLUSH\nEXIT\n' | HELIX_DATA_DIR=./data/rec HELIX_LOG_FORMAT=text ./bin/kvnode
printf 'GET persist\nSTATS\nEXIT\n'     | HELIX_DATA_DIR=./data/rec HELIX_LOG_FORMAT=text ./bin/kvnode
```

Recovery via WAL replay (no flush). Dummy data: key `walonly` = `here`, written but never flushed, recovered on restart.
```bash
printf 'SET walonly here\nEXIT\n' | HELIX_DATA_DIR=./data/rec2 HELIX_LOG_FORMAT=text ./bin/kvnode
printf 'GET walonly\nEXIT\n'      | HELIX_DATA_DIR=./data/rec2 HELIX_LOG_FORMAT=text ./bin/kvnode
```

Delete shadows a flushed value. Dummy data: key `temp` set and flushed, then deleted and flushed; must read as absent.
```bash
printf 'SET temp v\nFLUSH\nDEL temp\nFLUSH\nGET temp\nEXIT\n' \
  | HELIX_DATA_DIR=./data/del HELIX_LOG_FORMAT=text ./bin/kvnode
```

## 8. Verify the expected results

- `go test` runs end with `ok  github.com/talifpathan/helix/internal/storage`, and with `-v` a `--- PASS` line per test.
- `make check` ends with `check passed`.
- Auto-flush scenario: STATS shows `sstables` of 2 or more, and `data/auto` contains several `.sst` files plus a `MANIFEST` whose `tables` array lists them.
- Recovery via SSTable: session 2 prints `yes` for `GET persist` and STATS shows `sstables=1`; the value came from disk, not memory.
- Recovery via WAL: session 2 prints `here` for `GET walonly`; this proves WAL replay, since no flush occurred.
- Delete scenario: `GET temp` prints `(nil)`.
- The `MANIFEST` after any flush is valid JSON with non-empty `tables`, and `last_seq` is greater than zero.

## 9. Troubleshooting

- No flush happening / `sstables=0`: the threshold is higher than the data written. Lower `HELIX_MEMTABLE_MAX_BYTES` (for example 256 or 512), or use the explicit `FLUSH` command.
- Unexpected data on start: a reused data directory recovers its prior state. To start clean, remove it first, for example `rm -rf data/node`.
- `open sstable N: ...` error on start: the `MANIFEST` references an SSTable file that is missing or truncated. This happens only if files were hand-edited or a disk error occurred. For a scratch directory, delete it and re-seed.
- Writes appear to hang: the immutable queue is full because flushing stalled (a disk error sets a sticky flush error and applies backpressure). Check the logs for `flush failed`; for a scratch directory, start clean.
- `go test -race` fails to build with a cgo or gcc error: the race detector needs cgo; Cloud Shell ships gcc. If needed, `sudo apt-get update && sudo apt-get install -y build-essential` (does not persist across sessions).
- `gofmt` differences fail `make check`: run `make fmt`, review `git diff`, re-run `make check`.
- Tests seem cached: add `-count=1`.
- Work disappeared after a break: only `$HOME` persists and the Cloud Shell VM recycles after inactivity. Keep everything under `~/helix` and commit or push often.
