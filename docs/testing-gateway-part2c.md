# Testing gateway slice 2c: members, ring, and metrics aggregation

This slice completes the gateway's read/observability surface. It generalizes the per-node
aggregation (status, members, ring now share one path) and adds metrics aggregation with a small
dependency-free Prometheus text parser. The SSE live stream is the final slice (2d).

## What it adds and changes

- Refactor: a generic `aggregate(path)` fetches an admin JSON path from every node concurrently and
  relays each node's raw JSON as `data`, reporting unreachable nodes as `ok:false`. The 2a status
  endpoint now uses it (its per-node field is `data` rather than `status`).
- `GET /api/v1/cluster/members` aggregates each node's `/api/v1/members` (per-node SWIM views).
- `GET /api/v1/cluster/ring` aggregates each node's `/api/v1/ring`.
- `GET /api/v1/cluster/metrics` fetches each node's Prometheus `/metrics`, parses it to JSON samples
  (`{name, labels, value}`), and returns them per node.

No new dependency: the Prometheus parser is a small hand-written function, since the node emits
simple gauges. All endpoints stay behind the operator bearer token.

## Build and unit-test

```bash
cd ~/helix
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet ./... 2>&1 | head -20
GOTOOLCHAIN=local go test -race -count=1 ./internal/gateway/
make check
git status --porcelain go.mod go.sum vendor/     # expect no output
```

The tests cover the Prometheus parser (comments and blanks skipped, plain and labeled gauges, a
trailing timestamp, garbage lines ignored), quote-aware label splitting, metrics aggregation with a
healthy and a failing node, and the members endpoint relaying node JSON through the generic
aggregate.

## Run against the kind cluster (dummy values)

Port-forward each node's admin port (9090); gRPC (7070) is only needed for the KV endpoints from 2b.

```bash
kubectl -n helix port-forward pod/helix-0 7070:7070 9090:9090 >/tmp/pf0.log 2>&1 &
kubectl -n helix port-forward pod/helix-1 7071:7070 9091:9090 >/tmp/pf1.log 2>&1 &
kubectl -n helix port-forward pod/helix-2 7072:7070 9092:9090 >/tmp/pf2.log 2>&1 &
sleep 2

cd ~/helix
export HELIX_GATEWAY_TOKEN="op-secret-token"
export HELIX_GATEWAY_CORS_ORIGIN="http://localhost:3000"
export HELIX_GATEWAY_NODES="helix-0=127.0.0.1:7070;127.0.0.1:9090,helix-1=127.0.0.1:7071;127.0.0.1:9091,helix-2=127.0.0.1:7072;127.0.0.1:9092"
go run ./cmd/gateway &        # runs without leaving a binary in the repo
sleep 2
export T="Authorization: Bearer op-secret-token"
```

## Test scenarios

```bash
# Scenario 1: aggregated status (now uses the generic shape, per-node "data")
curl -s -H "$T" localhost:8080/api/v1/cluster/status | head -40
# expect: {"nodes":[{"node_id":"helix-0","ok":true,"data":{...node status...}}, ...three]}

# Scenario 2: aggregated membership (each node's SWIM view)
curl -s -H "$T" localhost:8080/api/v1/cluster/members
# expect: three node results, each data.members listing helix-0/1/2 with "state":"alive"

# Scenario 3: aggregated ring
curl -s -H "$T" localhost:8080/api/v1/cluster/ring
# expect: three node results, each data with vnodes 128 and the three nodes

# Scenario 4: aggregated metrics (parsed to JSON samples)
curl -s -H "$T" localhost:8080/api/v1/cluster/metrics | head -60
# expect: per node, samples like {"name":"helix_up","value":1} and
# {"name":"helix_cluster_members","labels":{"state":"alive"},"value":3}

# Scenario 5: auth still enforced
curl -s -o /dev/null -w "%{http_code}\n" localhost:8080/api/v1/cluster/metrics    # expect: 401

# Scenario 6: partial failure is reported, not fatal
kill %3 2>/dev/null    # drop helix-2's forwards
curl -s -H "$T" localhost:8080/api/v1/cluster/metrics | grep -o '"ok":[a-z]*'    # expect two true, one false
```

Cleanup:

```bash
kill %1 %2 %3 2>/dev/null
# stop the gateway (fg then Ctrl-C, or kill the go run process)
```

## Expected results

- Unit tests pass under `-race`.
- status, members, ring, and metrics each return per-node results; metrics are parsed into named
  samples with labels and values; auth is enforced; and a downed node shows `ok:false` while the
  others still return.

## Troubleshooting

- Metrics samples are empty for a node that is up: confirm the admin address points at the metrics
  port (9090) and that `/metrics` returns the Prometheus text (`curl localhost:9090/metrics`).
- A label with a comma in its value looks split: the parser is quote-aware, so this should not
  happen; if it does, the metric text is malformed.
- status body shows `data` where you expected `status`: intended, the field was generalized across
  status, members, and ring in this slice.
- Every node `ok:false`: the admin port-forwards are down or the admin addresses in
  `HELIX_GATEWAY_NODES` do not match the forwarded ports.
