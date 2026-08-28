# Testing gateway slice 2a: skeleton, auth, CORS, cluster status

This slice adds the gateway service (backend-for-frontend) in the `helix` repo: `internal/gateway`
(config, auth, CORS, health, and cluster-status aggregation) plus a thin `cmd/gateway`. It is the
foundation the console will call. This slice is HTTP-only: it aggregates each node's status API. KV
read-write over gRPC is slice 2b; metrics aggregation and the SSE stream are slice 2c.

## What it adds

- `GET /healthz` open, the gateway's own liveness (no token), so a tunnel or load balancer can probe it.
- `GET /api/v1/cluster/status` authenticated, fetches every node's `/api/v1/status` concurrently and
  returns them, relaying each node's raw JSON and reporting unreachable nodes as `ok:false` with an
  error rather than failing the whole request.
- Bearer-token auth (constant-time) on the API, and a strict single-origin CORS allowlist with
  preflight handling.

No dependency change, no proto change.

## Configuration (environment variables)

| Variable | Example | Meaning |
| --- | --- | --- |
| HELIX_GATEWAY_ADDR | :8080 | listen address (default :8080) |
| HELIX_GATEWAY_TOKEN | op-secret-token | operator bearer token (empty disables auth, dev only) |
| HELIX_GATEWAY_CORS_ORIGIN | http://localhost:3000 | allowed browser origin (empty disables CORS) |
| HELIX_GATEWAY_NODES | helix-0=127.0.0.1:7070;127.0.0.1:9090,... | per node: id=grpcAddr;adminAddr |
| HELIX_GATEWAY_HTTP_TIMEOUT | 5s | per-node call timeout |

The gRPC address in each node entry is not used until slice 2b (KV), but the format requires both,
so set it to the node's real gRPC address.

## Build and unit-test

```bash
cd ~/helix
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet ./... 2>&1 | head -20
GOTOOLCHAIN=local go test -race -count=1 ./internal/gateway/
make check
git status --porcelain go.mod go.sum vendor/     # expect no output
```

The tests cover node-list parsing, env config (defaults, overrides, bad timeout), the auth
middleware (401 with no/wrong/non-bearer token, pass with the right one, disabled when no token),
CORS preflight and open health, and status aggregation with one healthy and one failing node.

## Run it against the kind cluster (dummy values)

The gateway runs locally and reaches each node's admin port (9090) over port-forwards. Open three
port-forwards to distinct local ports:

```bash
kubectl -n helix port-forward pod/helix-0 9090:9090 >/tmp/pf0.log 2>&1 &
kubectl -n helix port-forward pod/helix-1 9091:9090 >/tmp/pf1.log 2>&1 &
kubectl -n helix port-forward pod/helix-2 9092:9090 >/tmp/pf2.log 2>&1 &
sleep 2
```

Run the gateway with dummy config:

```bash
cd ~/helix
export HELIX_GATEWAY_ADDR=":8080"
export HELIX_GATEWAY_TOKEN="op-secret-token"
export HELIX_GATEWAY_CORS_ORIGIN="http://localhost:3000"
export HELIX_GATEWAY_NODES="helix-0=127.0.0.1:7070;127.0.0.1:9090,helix-1=127.0.0.1:7071;127.0.0.1:9091,helix-2=127.0.0.1:7072;127.0.0.1:9092"
export HELIX_GATEWAY_HTTP_TIMEOUT="5s"
GOTOOLCHAIN=local go run ./cmd/gateway &
sleep 2
```

## Test scenarios

```bash
# Scenario 1: gateway health (open, no token)
curl -s localhost:8080/healthz            # expect: ok

# Scenario 2: cluster status without a token is rejected
curl -s -o /dev/null -w "%{http_code}\n" localhost:8080/api/v1/cluster/status    # expect: 401

# Scenario 3: cluster status with the operator token
curl -s -H "Authorization: Bearer op-secret-token" localhost:8080/api/v1/cluster/status | head -60
# expect JSON: {"nodes":[{"node_id":"helix-0","ok":true,"status":{...node status...}}, ...three]}

# Scenario 4: CORS preflight from the console origin
curl -s -i -X OPTIONS -H "Origin: http://localhost:3000" localhost:8080/api/v1/cluster/status | head -8
# expect: HTTP/1.1 204 and Access-Control-Allow-Origin: http://localhost:3000

# Scenario 5: partial failure is reported, not fatal (kill one forward, re-query)
kill %3 2>/dev/null   # drop helix-2's port-forward
curl -s -H "Authorization: Bearer op-secret-token" localhost:8080/api/v1/cluster/status \
  | grep -o '"ok":[a-z]*'    # expect two true and one false
```

Cleanup:

```bash
kill %1 %2 %3 2>/dev/null    # port-forwards
# stop the gateway: fg then Ctrl-C, or kill the go run process
```

## Expected results

- Unit tests pass under `-race`.
- `/healthz` returns `ok` without a token; `/api/v1/cluster/status` returns 401 without the token
  and the aggregated JSON with it; a preflight returns 204 with the allowlist header; and a downed
  node shows `ok:false` while the others still return.

## Do not expose this without auth

Run with `HELIX_GATEWAY_TOKEN` set. With it empty the API is unauthenticated (the process logs a
warning); that is for local development only. When you later put this behind a Cloudflare Tunnel,
keep the token set and the CORS origin pinned to the console's real URL.

## Troubleshooting

- `HELIX_GATEWAY_NODES must list at least one node`: the variable is empty or malformed; use
  `id=grpcAddr;adminAddr` entries separated by commas.
- Every node shows `ok:false` with a connection error: the port-forwards are not up, or the ports in
  `HELIX_GATEWAY_NODES` admin addresses do not match the forwarded local ports.
- 401 with the right token: confirm the header is exactly `Authorization: Bearer op-secret-token`
  and that `HELIX_GATEWAY_TOKEN` matches.
- No CORS header: `HELIX_GATEWAY_CORS_ORIGIN` is empty; set it to the console origin.
