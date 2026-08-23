# Testing Phase 2, Part 1 (SSTable format and bloom filters)

A self-contained runbook for a fresh Cloud Shell session. Every command can be copied and run as-is.

## Scope and what does not apply at this phase

Phase 2 Part 1 is a pure Go library layer: the on-disk SSTable format (block-based writer, reader, iterator) and the bloom filter. It is intentionally not wired into the running node yet, so several items from a typical service runbook do not exist here. They are listed so nothing looks missing by accident:

- No external database, cache, or message broker. The SSTable file on disk is the storage artifact; there is nothing to install or start.
- No long-running backend service, no HTTP or gRPC server, and therefore no health endpoint, credentials, tokens, or URLs. Those arrive in Phase 7 (gRPC, mTLS, client and admin APIs).
- No environment variables are required to run these tests. The `HELIX_` variables configure the Phase 1 node only. The demo tool takes flags instead.
- The only "sample data" that is meaningful is key/value records. This runbook supplies concrete dummy records, IDs, and payloads for every scenario.

The runnable artifacts for this phase are the test suite and a self-verifying demo tool, `cmd/sstdemo`.

## 1. Verify the existing environment

Run this whole block. It checks the toolchain, the repository, the Phase 2 Part 1 files, the git remote, and the on-disk data volume, and prints a status for each.

```bash
echo "== Go =="; go version 2>/dev/null || echo "Go NOT installed"
echo "== Repo =="; [ -d ~/helix/.git ] && echo "~/helix present" || echo "~/helix MISSING"
echo "== Phase 2 Part 1 files =="
for f in \
  internal/storage/sstable.go \
  internal/storage/bloom.go \
  internal/storage/sstable_test.go \
  internal/storage/bloom_test.go \
  cmd/sstdemo/main.go ; do
  [ -f ~/helix/"$f" ] && echo "ok       $f" || echo "MISSING  $f"
done
echo "== git remote =="; git -C ~/helix remote -v 2>/dev/null | head -1 || echo "no remote"
echo "== data volume =="; [ -d ~/helix/data ] && du -sh ~/helix/data || echo "no data dir yet (expected on a fresh session)"
```

Interpretation: Go must be 1.22 or newer. If `~/helix` is present and all five files show `ok`, skip to section 3. If anything is `MISSING`, do section 2.

## 2. Install or initialize what is missing

### Go is missing or older than 1.22

```bash
cd ~ && curl -sSLO https://go.dev/dl/go1.22.12.linux-amd64.tar.gz
mkdir -p ~/.local && tar -C ~/.local -xzf go1.22.12.linux-amd64.tar.gz
echo 'export PATH=$HOME/.local/go/bin:$PATH' >> ~/.bashrc && source ~/.bashrc
go version
```

### The repository is missing

```bash
git clone https://github.com/Talif787/helix.git ~/helix
```

### The Phase 2 Part 1 files are missing

These files are delivered in `helix-phase1.zip`. Upload it with the Cloud Shell three-dot menu, then overwrite the source tree in place (this does not touch your `.git` directory, which is excluded from the archive):

```bash
cd ~ && unzip -o helix-phase1.zip     # refreshes ~/helix/... including the new files
```

Re-run the section 1 verification block; every file should now show `ok`.

Recommended: once the tests pass (sections 6 and 7), commit these files to a branch so future fresh sessions get them straight from git rather than a zip:

```bash
cd ~/helix
git switch -c phase-2/sstables
git add internal/storage/sstable.go internal/storage/bloom.go \
        internal/storage/sstable_test.go internal/storage/bloom_test.go \
        cmd/sstdemo/main.go
git commit -m "feat(storage): add SSTable format and bloom filter (phase 2 part 1)"
git push -u origin phase-2/sstables
```

## 3. Configure environment variables and services

Nothing to configure for this phase. There are no services to start and no variables the SSTable tests read. Confirm the module resolves with no third-party dependencies:

```bash
cd ~/helix
go env GOFLAGS          # normally empty; no special flags needed
cat go.mod              # module path and go version only, no require block
```

If you want a clean module sanity check (it should print nothing and change nothing, since there are no dependencies):

```bash
go mod tidy && git diff --stat go.mod go.sum 2>/dev/null || true
```

## 4. Start the backend and supporting services

There is no backend service in this phase. The equivalent of "start it and see it work" is the self-verifying demo tool, which writes a real SSTable and queries it:

```bash
cd ~/helix
go run ./cmd/sstdemo
```

Expected: a short transcript of writes and reads ending in `PASS: all SSTable demo checks succeeded`, and an exit code of 0. Confirm the exit code:

```bash
go run ./cmd/sstdemo ; echo "exit=$?"
```

You can point it at a different directory or change the record count:

```bash
go run ./cmd/sstdemo -dir ./data/demo -items 20000
```

Note: the Phase 1 node (`make run`) still exists but does not use SSTables yet, so it is not part of this test.

## 5. Verify service and backend health

The health of a library layer is: it compiles, it vets clean, its demo self-check passes, and the produced file is well-formed.

```bash
cd ~/helix
go build ./...          # compiles every package, including cmd/sstdemo
go vet ./...            # static analysis, should print nothing
go run ./cmd/sstdemo ; echo "demo exit=$?"
```

Inspect the on-disk artifact directly. The last 8 bytes of every SSTable are the magic number `HELIXSST`:

```bash
ls -l ~/helix/data/demo/000001.sst
tail -c 8 ~/helix/data/demo/000001.sst | xxd
# expect the ASCII bytes: 4854 5453 5353 ... shown as "TSTSSS" style; the string HELIXSST is present
```

To see the human-readable magic clearly:

```bash
tail -c 8 ~/helix/data/demo/000001.sst | tr -d '\0' ; echo
# prints: HELIXSST  (byte order shows the magic that OpenSSTable validates)
```

## 6. Run Phase 2, Part 1

Run the storage package tests, then the full race-enabled gate.

```bash
cd ~/helix
go test ./internal/storage/                 # fast unit run
go test -race -count=1 ./internal/storage/  # race detector, no test cache
make check                                  # fmt + vet + race across the whole module
```

A successful full gate ends with `check passed`.

## 7. Execute each test scenario with dummy values

Each scenario below lists the dummy data it uses and the exact command to run just that test with verbose output. The `-v` flag prints each test name and PASS or FAIL.

### SSTable: present, absent, and tombstone reads
Dummy data: keys `apple`, `banana`, `date` (values `red`, `yellow`, `brown`), a tombstone for `cherry`, and probes for absent keys `fig` and `aardvark`.

```bash
go test -run TestSSTableWriteReadGet -v ./internal/storage/
```

### SSTable: rejects non-ascending keys
Dummy data: add key `b`, then attempt key `a`, which must fail.

```bash
go test -run TestSSTableRejectsNonAscending -v ./internal/storage/
```

### SSTable: ordered full scan (iterator)
Dummy data: 100 records `key000`..`key099` with values `val000`..`val099`.

```bash
go test -run TestSSTableIterator -v ./internal/storage/
```

### SSTable: multi-block indexing and lookups
Dummy data: 5000 records `key00000000`..`key00004999`, each with a 40-byte value, forcing many data blocks. Probes keys in the first, middle, and last blocks.

```bash
go test -run TestSSTableAcrossManyBlocks -v ./internal/storage/
```

### SSTable: empty table edge case
Dummy data: none (zero records). Any lookup must return absent and the iterator must yield nothing.

```bash
go test -run TestSSTableEmpty -v ./internal/storage/
```

### SSTable: binary-safe values
Dummy data: key `k` with value bytes `00 ff 10 00 42`.

```bash
go test -run TestSSTableBinaryValues -v ./internal/storage/
```

### Bloom filter: no false negatives
Dummy data: 1000 keys `present-0`..`present-999`; every one must report present.

```bash
go test -run TestBloomNoFalseNegatives -v ./internal/storage/
```

### Bloom filter: false-positive rate is reasonable
Dummy data: 1000 present keys, then 10000 absent probes `absent-0`..`absent-9999`; observed rate must stay under a loose 0.05 bound.

```bash
go test -run TestBloomFalsePositiveRateIsReasonable -v ./internal/storage/
```

### Bloom filter: serialize round-trip and corrupt input
Dummy data: 500 keys `k-0`..`k-499` serialized and reloaded; plus a 3-byte corrupt buffer that must be rejected.

```bash
go test -run 'TestBloomSerializeRoundTrip|TestBloomLoadRejectsCorrupt' -v ./internal/storage/
```

### End-to-end demo with realistic dummy records
Dummy data: users `user:1001` (Ada Lovelace), `user:1002` (Alan Turing), `user:1003` (Grace Hopper) with JSON values, a tombstone for `user:2001`, an absent probe `user:9999`, and 5000 generated `item:*` records.

```bash
go run ./cmd/sstdemo
```

## 8. Verify the expected results

- Every `go test` command should end with `ok  github.com/talifpathan/helix/internal/storage` and, with `-v`, a `--- PASS` line per test.
- `make check` should end with `check passed`.
- `go run ./cmd/sstdemo` should print `PASS: all SSTable demo checks succeeded` and exit 0. A representative transcript:

```
wrote 5004 records to ./data/demo/000001.sst (NNNNNN bytes)
GET user:1001      -> {"name":"Ada Lovelace","email":"ada@example.com"}
GET user:1003      -> {"name":"Grace Hopper","email":"grace@example.com"}
GET item:00000000  -> payload-0
GET user:9999      -> (absent)
GET user:2001      -> (tombstone)
SCAN 5004 records in order, first=item:00000000 last=user:2001
PASS: all SSTable demo checks succeeded
```

- The SSTable file exists under `data/demo/` and its final 8 bytes spell `HELIXSST`.

## 9. Troubleshooting

- `go: command not found` or a version below 1.22: install Go per section 2, then `source ~/.bashrc`.
- `cannot find module` or `go.mod file not found`: run commands from `~/helix` (the module root), not a subdirectory.
- `use of internal package ... not allowed`: you are running from outside the module. The demo and tests must run from within `~/helix`.
- `go test -race` fails to build with a cgo or gcc error: the race detector needs cgo; Cloud Shell ships gcc, so this should not occur. If it does, run `sudo apt-get update && sudo apt-get install -y build-essential`, then retry (note this install does not persist across Cloud Shell sessions).
- `gofmt` differences fail `make check`: run `make fmt` to auto-format, review `git diff`, then re-run `make check`.
- Demo file already exists or a stale run: the writer truncates on create, so re-runs are safe. To start clean, `rm -rf ~/helix/data/demo`.
- Work disappeared after a break: only `$HOME` persists and the Cloud Shell VM recycles after inactivity. Keep everything under `~/helix` and commit or push often.
- Tests pass but you want to confirm they actually ran (not cached): add `-count=1`, for example `go test -count=1 ./internal/storage/`.
