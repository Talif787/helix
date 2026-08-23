# Helix

A distributed key-value store and storage engine, built from first principles in Go. Helix is a leaderless, tunably-consistent store in the lineage of Amazon Dynamo and Apache Cassandra. It is the eventually-consistent, partition-tolerant counterpart to a Raft-based strongly-consistent store.

This repository is built in phases. Each phase is independently buildable and testable.

## Current phase: Phase 1, Storage Engine Foundation

Phase 1 delivers a durable, single-node, ordered key-value engine:

- A write-ahead log (WAL) with CRC-framed records and crash-safe recovery. A torn or corrupt tail (the expected result of a crash mid-append) is detected on open and truncated, so no partial record is ever replayed and future appends stay clean.
- An in-memory memtable implemented as a concurrent skip list, ordered by key so it can be flushed to a sorted on-disk file in Phase 2.
- An engine facade that appends every write to the WAL before applying it to the memtable, and replays the WAL on open so no acknowledged write is lost across a restart.
- Twelve-factor configuration from the environment, structured logging via `log/slog`, typed errors, and input validation.
- A small interactive shell in `cmd/kvnode` so the full write, read, and recovery path is runnable end to end.

On-disk SSTables, compaction, and bloom filters arrive in Phase 2. Until then a Phase 1 node keeps its live working set in memory, with durability guaranteed by the WAL.

## Layout

```
cmd/kvnode              node entrypoint and interactive shell
internal/storage        WAL, memtable, engine, record codec, errors
internal/config         environment-driven configuration and validation
internal/observability  structured logging and correlation-id helpers
```

Dependency direction points inward: `cmd` depends on `config` and `observability`; `config` depends on `storage`; `storage` depends only on the standard library. I/O sits behind the engine boundary so the distribution and simulation layers in later phases can build on it without change.

## Build and run

Requires Go 1.22 or newer. The project has no third-party dependencies in Phase 1.

```
make build       # compile to bin/kvnode
make run         # build and start the interactive shell
make test        # run the unit tests
make race        # run the unit tests under the race detector
make vet         # run go vet
make fmt         # gofmt the tree
```

In the shell:

```
SET user:1 alice
GET user:1
DEL user:1
STATS
HELP
EXIT
```

Data is written under the configured data directory (default `./data`). Stop the node, start it again against the same directory, and previously written keys are recovered from the WAL.

## Configuration

All settings come from the environment with safe defaults, validated once at startup.

| Variable | Default | Meaning |
| --- | --- | --- |
| `HELIX_DATA_DIR` | `./data` | Directory for the write-ahead log (and later, SSTables). |
| `HELIX_SYNC_WRITES` | `true` | Fsync on every write. Set to `false` to trade durability for throughput; durability is still guaranteed at Close and on `Sync`. |
| `HELIX_MAX_KEY_BYTES` | `65536` | Maximum accepted key size. |
| `HELIX_MAX_VALUE_BYTES` | `1048576` | Maximum accepted value size. |
| `HELIX_MEMTABLE_MAX_BYTES` | `67108864` | Memtable size that will trigger a flush in Phase 2. |
| `HELIX_LOG_LEVEL` | `info` | One of `debug`, `info`, `warn`, `error`. |
| `HELIX_LOG_FORMAT` | `json` | One of `json`, `text`. |

## Durability semantics

A write that returns without error is durable to the WAL according to the sync policy. A write that returns an error must be treated as an unknown outcome: it may or may not have reached disk, so callers should retry idempotently. This matches the honest failure model of any real storage system and is exercised by the recovery tests.

## Roadmap

1. Storage engine foundation (this phase).
2. On-disk SSTables, flush, bloom filters, block cache, and compaction.
3. Partitioning: consistent-hash ring with virtual nodes, coordinator, in-process transport.
4. Replication and tunable consistency: quorum, vector clocks and last-write-wins, read repair.
5. Membership: SWIM gossip and failure detection.
6. Anti-entropy: hinted handoff and Merkle-tree repair.
7. Transport and interfaces: gRPC plus mTLS, client SDK, admin API.
8. Observability: Prometheus metrics, OpenTelemetry tracing, health and readiness.
9. Deterministic simulation harness, chaos, and load tests.
10. DevOps: Dockerfile, Compose, Kubernetes StatefulSet, Helm, Terraform, CI/CD.
11. Documentation: runbooks and operational guides.

The module path is `github.com/talifpathan/helix`; rename it to your own path with a find-and-replace across the tree and in `go.mod`.
