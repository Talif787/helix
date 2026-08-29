# Helix

A distributed key-value store and storage engine, built from first principles in Go. Helix is a leaderless, tunably-consistent store in the lineage of Amazon Dynamo and Apache Cassandra: the eventually-consistent, partition-tolerant counterpart to a Raft-based strongly-consistent store. It is complete end to end, from an on-disk LSM storage engine through replication and gossip membership up to a gRPC data plane, an HTTP gateway, and a separate web operator console.

The operator console lives in its own repository: https://github.com/Talif787/helix-console

## What it is

A cluster of identical nodes, no leader. Any node can coordinate any request. Keys are placed on a consistent-hash ring with virtual nodes and replicated to N nodes; reads and writes are satisfied by tunable R and W quorums, so you dial the consistency and availability trade-off per deployment. Nodes discover each other and detect failures with SWIM gossip, repair divergence with hinted handoff and Merkle-tree anti-entropy, and expose a coordinated client API over gRPC (optionally with mutual TLS). An HTTP gateway aggregates the cluster for browser clients and proxies the KV data plane, and the console renders it all for an operator.

## Architecture at a glance

```
  browser ── HTTPS ──> operator console (Next.js, separate repo)
                          │  server-side proxy, holds the gateway token
                          ▼
                       gateway (cmd/gateway, HTTP :8080)
                          │  aggregates status/members/ring/metrics, proxies KV, streams SSE
        ┌─────────────────┼─────────────────┐
        ▼                 ▼                 ▼
     node helix-0      node helix-1      node helix-2      (cmd/kvnode)
     gRPC :7070        gRPC :7070        gRPC :7070        data + membership planes
     admin :9090       admin :9090       admin :9090       /metrics /healthz /api/v1/*
        └───── consistent-hash ring, N/R/W quorums, SWIM gossip, anti-entropy ─────┘
```

Each node stacks these layers, lowest to highest:

- Storage engine: a write-ahead log with CRC-framed records and crash-safe recovery, an in-memory skip-list memtable, on-disk sorted SSTables with a block cache and bloom filters, and background compaction.
- Partitioning: a consistent-hash ring with virtual nodes and a coordinator that routes each key to its replica set.
- Replication and tunable consistency: N replicas with R and W quorums, last-write-wins conflict resolution with version metadata, and read repair.
- Membership: SWIM gossip with a failure detector, so the ring reflects live nodes without a central registry.
- Anti-entropy: hinted handoff for writes that miss a temporarily-down replica, and Merkle-tree comparison to repair durable divergence.
- Transport and interfaces: a gRPC data and membership plane (optionally mutual TLS), a thin CLI client, and a read-only admin API.
- Observability: Prometheus metrics, health and readiness endpoints, and structured logging.

The dependency direction points inward: transport and daemon wiring depend on the cluster, membership, and storage layers; the storage engine depends only on the standard library. The full design write-up is in the console repository at docs/ARCHITECTURE.md; this repository's docs/ holds a development guide and a per-phase testing runbook for every subsystem.

## Repository layout

```
cmd/
  kvnode         node entrypoint: data + membership planes over gRPC, joins the cluster
  gateway        HTTP gateway: cluster aggregation, KV proxy, SSE stream (browser-facing)
  helixctl       command-line client: coordinated get/put/delete over gRPC
  helixcert      certificate helper for mutual TLS
  clusterdemo, swimdemo, hintdemo, repairdemo, sstdemo   runnable subsystem demos

internal/
  storage        WAL, memtable, SSTables, block cache, bloom filters, compaction, engine
  cluster        consistent-hash ring, coordinator, replication, quorum, read repair
  membership     SWIM gossip and failure detection
  rpc            gRPC client and server for the data and membership planes
  daemon         node lifecycle: builds and runs a clustered node from configuration
  gateway        HTTP gateway server, KV proxy, cluster aggregation, metrics parsing, SSE
  metrics        Prometheus instrumentation
  observability  structured logging and correlation-id helpers
  tlsutil        mutual-TLS configuration helpers
  config         environment-driven configuration and validation
  simulation     deterministic simulation, chaos, and load harness

proto/helix      gRPC service and message definitions
deploy/
  helm/helix     Helm chart (StatefulSet, headless + client services)
  k8s            raw manifests with a kustomization
  terraform      kind (local) and gke (cloud) modules
```

## Build and run

Requires Go 1.22 or newer. The only third-party dependencies are gRPC and Protobuf.

```
make build       # compile to bin/
make test        # unit tests
make race        # unit tests under the race detector
make check       # fmt + vet + race (the pre-commit gate)
make docker-build
```

### Single node, interactive

```
make run
```

Then, in the shell: `SET user:1 alice`, `GET user:1`, `DEL user:1`, `STATS`, `HELP`, `EXIT`. Data is written under `HELIX_DATA_DIR` (default `./data`); restart against the same directory and keys are recovered from the WAL.

### A local three-node cluster (Docker Compose)

```
make docker-build
make docker-up          # helix-a, helix-b, helix-c on one Docker network
make docker-logs        # follow logs
make docker-down        # stop and remove
```

Talk to it with the CLI:

```
go run ./cmd/helixctl -addr 127.0.0.1:7070 put order:5005 shipped
go run ./cmd/helixctl -addr 127.0.0.1:7070 get order:5005
```

### On Kubernetes

The Helm chart brings up a three-replica StatefulSet with stable per-pod DNS:

```
kind create cluster --name helix-test
make docker-build && kind load docker-image helix:local --name helix-test
helm upgrade --install helix deploy/helm/helix -n helix --create-namespace
kubectl -n helix rollout status statefulset/helix
```

Raw manifests (`deploy/k8s`) and Terraform modules for a local kind cluster and for GKE (`deploy/terraform`) are provided as alternatives.

### The gateway and console

The gateway exposes the cluster to browsers:

```
export HELIX_GATEWAY_TOKEN="op-secret-token"
export HELIX_GATEWAY_NODES="helix-0=127.0.0.1:7070;127.0.0.1:9090,helix-1=127.0.0.1:7071;127.0.0.1:9091,helix-2=127.0.0.1:7072;127.0.0.1:9092"
go run ./cmd/gateway            # listens on :8080
```

The operator console (separate repo) proxies to the gateway server-side and renders overview, members, metrics, and a KV browser. See its README, and the end-to-end integration runbook it ships, for the full two-repo bring-up.

## Client API

The gRPC ClientService offers coordinated `Get`, `Put`, and `Delete`; every operation runs through the full quorum, so any node can coordinate. `helixctl` is a thin wrapper over the client library. Each node also serves a read-only admin API on the metrics port (default 9090): `GET /api/v1/status`, `/members`, `/ring`, and `/key/{key}`, plus `/metrics` (Prometheus) and `/healthz`.

The gateway exposes a browser-facing HTTP API on :8080, all under a bearer token except `/healthz`:

| Method and path | Purpose |
| --- | --- |
| `GET /healthz` | liveness (unauthenticated) |
| `GET /api/v1/cluster/status` | per-node status, aggregated |
| `GET /api/v1/cluster/members` | SWIM membership as each node sees it |
| `GET /api/v1/cluster/ring` | ring ownership |
| `GET /api/v1/cluster/metrics` | parsed Prometheus metrics per node |
| `GET /api/v1/stream` | Server-Sent Events snapshot stream |
| `GET/PUT/DELETE /api/v1/kv/{key}` | KV data plane, quorum-coordinated |

KV writes take `{"value":"..."}` or `{"value_base64":"..."}`; reads return both a `value_base64` and, when the bytes are valid UTF-8, a convenience `value`.

## Configuration

All settings come from the environment with safe defaults, validated once at startup.

Storage and logging (all binaries):

| Variable | Default | Meaning |
| --- | --- | --- |
| `HELIX_DATA_DIR` | `./data` | Directory for the WAL and SSTables. |
| `HELIX_SYNC_WRITES` | `true` | Fsync on every write; durability is still guaranteed at Close and Sync. |
| `HELIX_MAX_KEY_BYTES` | `65536` | Maximum accepted key size. |
| `HELIX_MAX_VALUE_BYTES` | `1048576` | Maximum accepted value size. |
| `HELIX_MEMTABLE_MAX_BYTES` | `67108864` | Memtable size that triggers a flush. |
| `HELIX_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `HELIX_LOG_FORMAT` | `json` | `json` or `text`. |

Node clustering (`cmd/kvnode`):

| Variable | Default | Meaning |
| --- | --- | --- |
| `HELIX_NODE_ID` | (required) | This node's id; must appear in `HELIX_PEERS`. |
| `HELIX_PEERS` | (required) | Comma list of `id=addr` for every node. |
| `HELIX_BIND_ADDR` | this node's peer address | Listen address. |
| `HELIX_N`, `HELIX_R`, `HELIX_W` | `3`, `2`, `2` | Replication factor and read/write quorums. |
| `HELIX_VNODES` | `128` | Virtual nodes per node on the ring. |
| `HELIX_MAX_HINTS` | `N` | Max hinted-handoff entries per node. |
| `HELIX_TLS_CERT` / `KEY` / `CA` | (none) | PEM paths for mutual TLS (all three, or none). |
| `HELIX_METRICS_ADDR` | (none) | Serve `/metrics`, `/healthz`, and the admin API here. |
| `HELIX_SWIM_INTERVAL` | `1s` | Membership round period. |
| `HELIX_ANTIENTROPY_INTERVAL` | `30s` | Anti-entropy round period. |
| `HELIX_HINT_INTERVAL` | `10s` | Hint-delivery period. |
| `HELIX_REQUEST_TIMEOUT` | `2s` | Per-replica RPC timeout in the coordinator. |

Gateway (`cmd/gateway`):

| Variable | Default | Meaning |
| --- | --- | --- |
| `HELIX_GATEWAY_ADDR` | `:8080` | Listen address. |
| `HELIX_GATEWAY_TOKEN` | (none) | Operator bearer token; empty disables auth. |
| `HELIX_GATEWAY_CORS_ORIGIN` | (none) | Allowed browser origin; empty disables CORS. |
| `HELIX_GATEWAY_NODES` | (required) | Comma list of `id=grpcHost:port;adminHost:port`. |
| `HELIX_GATEWAY_HTTP_TIMEOUT` | `5s` | Per-node call timeout. |
| `HELIX_GATEWAY_STREAM_INTERVAL` | `2s` | SSE snapshot cadence. |

## Consistency and durability semantics

A write returns success once W replicas acknowledge; a read consults R replicas and returns the newest version, resolving conflicts by last-write-wins with version metadata, and repairing stale replicas in the background (read repair). With the defaults (N=3, R=2, W=2) the quorums overlap (R + W > N), which gives read-your-writes. Writes that miss a temporarily-down replica are stored as hints and delivered when it returns; durable divergence is reconciled by periodic Merkle-tree anti-entropy.

At the storage layer, a write that returns without error is durable to the WAL per the sync policy. A write that returns an error is an unknown outcome: it may or may not have reached disk, so callers should retry idempotently. This is the honest failure model of any real storage system and is exercised by the recovery tests.

## Testing

```
make check                  # fmt, vet, and the race-enabled unit tests
go test ./...               # the full unit suite
```

Beyond unit tests, `internal/simulation` holds a deterministic simulation, chaos, and load harness for the distributed layers, and `docs/` contains a step-by-step testing runbook for every subsystem (storage, partitioning, replication, membership, anti-entropy, transport, observability, simulation, and the gateway). The console repository carries its own unit, route, component, and Playwright end-to-end suites.

## Deployment and CI/CD

A multi-stage `Dockerfile` produces a small non-root image. `docker-compose.yml` runs a three-node cluster locally. `deploy/` provides a Helm chart, raw Kubernetes manifests with a kustomization, and Terraform modules for kind and GKE. GitHub Actions run the build and tests on every push and pull request (`.github/workflows/ci.yml`) and deploy on version tags (`cd.yml`); a `cloudbuild.yaml` mirrors CI on GCP Cloud Build.

## Design phases (all complete)

Helix was built in phases, each independently buildable and testable:

1. Storage engine foundation: WAL, memtable, engine, recovery.
2. On-disk SSTables, flush, bloom filters, block cache, compaction.
3. Partitioning: consistent-hash ring with virtual nodes, coordinator.
4. Replication and tunable consistency: quorum, versioning and last-write-wins, read repair.
5. Membership: SWIM gossip and failure detection.
6. Anti-entropy: hinted handoff and Merkle-tree repair.
7. Transport and interfaces: gRPC plus mutual TLS, client, admin API.
8. Observability: Prometheus metrics, health and readiness, structured logs.
9. Deterministic simulation harness, chaos, and load tests.
10. DevOps: Dockerfile, Compose, Kubernetes StatefulSet, Helm, Terraform, CI/CD.
11. Documentation: runbooks and operational guides.
12. HTTP gateway and web operator console (the latter in a separate repository).

## A note on the module path

The module path is `github.com/talifpathan/helix`. To reuse this under your own path, find-and-replace it across the tree and in `go.mod`.
