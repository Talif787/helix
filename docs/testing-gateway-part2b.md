# Testing gateway slice 2b: KV read-write over gRPC

This slice adds the KV data plane to the gateway: `GET`, `PUT`, and `DELETE /api/v1/kv/{key}`,
proxied to a node's gRPC `ClientService` (the same quorum-coordinated path helixctl uses), with
failover across nodes so a single unreachable coordinator does not fail the request.

## What it adds

- `GET /api/v1/kv/{key}` returns `{key, found, value, value_base64}`. Values are arbitrary bytes,
  carried losslessly as `value_base64` and, when valid UTF-8, also as `value` for convenience. A
  missing key is 404.
- `PUT /api/v1/kv/{key}` body `{"value":"..."}` (UTF-8) or `{"value_base64":"..."}` (binary),
  exactly one. Returns `{key, ok:true}`.
- `DELETE /api/v1/kv/{key}` returns `{key, deleted:true}`.
- Error mapping: not-found 404, invalid argument 400, unavailable 503, deadline exceeded 504, other
  node errors (including quorum failures, which arrive as gRPC Internal with a message) 502 with the
  node's message relayed.
- Node failover: each write and delete is tried against each node until one succeeds.

All authenticated with the operator bearer token, like the rest of the API. No dependency change.

Note: `{key}` matches a single path segment, so a key containing a slash must be sent with the
slash percent-encoded (`%2F`); the console does this.

## Build and unit-test

```bash
cd ~/helix
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet ./... 2>&1 | head -20
GOTOOLCHAIN=local go test -race -count=1 ./internal/gateway/
make check
git status --porcelain go.mod go.sum vendor/     # expect no output
```

The tests use a fake client (no gRPC server needed) to cover value encoding and decoding, the gRPC
to HTTP error mapping, the GET/PUT/DELETE handlers (including not-found, invalid body, and no-nodes),
and failover across a downed node.

## Run against the kind cluster (dummy values)

The gateway reaches each node's gRPC port (7070) for KV and admin port (9090) for status. Port-forward
both for each node:

```bash
kubectl -n helix port-forward pod/helix-0 7070:7070 9090:9090 >/tmp/pf0.log 2>&1 &
kubectl -n helix port-forward pod/helix-1 7071:7070 9091:9090 >/tmp/pf1.log 2>&1 &
kubectl -n helix port-forward pod/helix-2 7072:7070 9092:9090 >/tmp/pf2.log 2>&1 &
sleep 2

cd ~/helix
export HELIX_GATEWAY_TOKEN="op-secret-token"
export HELIX_GATEWAY_CORS_ORIGIN="http://localhost:3000"
export HELIX_GATEWAY_NODES="helix-0=127.0.0.1:7070;127.0.0.1:9090,helix-1=127.0.0.1:7071;127.0.0.1:9091,helix-2=127.0.0.1:7072;127.0.0.1:9092"
GOTOOLCHAIN=local go run ./cmd/gateway &
sleep 2
export T="Authorization: Bearer op-secret-token"
```

## Test scenarios

```bash
# Scenario 1: write a key
curl -s -X PUT -H "$T" -H 'Content-Type: application/json' \
  -d '{"value":"shipped"}' localhost:8080/api/v1/kv/order:5005
# expect: {"key":"order:5005","ok":true}

# Scenario 2: read it back (quorum-coordinated)
curl -s -H "$T" localhost:8080/api/v1/kv/order:5005
# expect: {"key":"order:5005","found":true,"value":"shipped","value_base64":"c2hpcHBlZA=="}

# Scenario 3: a missing key is 404
curl -s -o /dev/null -w "%{http_code}\n" -H "$T" localhost:8080/api/v1/kv/never-written
# expect: 404

# Scenario 4: binary value round-trips via base64 (0xff 0x00 0xfe)
curl -s -X PUT -H "$T" -d '{"value_base64":"/wD+"}' localhost:8080/api/v1/kv/bin:1
curl -s -H "$T" localhost:8080/api/v1/kv/bin:1
# expect: found true, value omitted (not valid UTF-8), value_base64 "/wD+"

# Scenario 5: delete
curl -s -X DELETE -H "$T" localhost:8080/api/v1/kv/order:5005
curl -s -o /dev/null -w "%{http_code}\n" -H "$T" localhost:8080/api/v1/kv/order:5005
# expect: {"key":"order:5005","deleted":true} then 404

# Scenario 6: no token is rejected
curl -s -o /dev/null -w "%{http_code}\n" localhost:8080/api/v1/kv/order:5005     # expect: 401

# Scenario 7: failover, kill the first node's gRPC forward and write again
kill %1 2>/dev/null    # drop helix-0
curl -s -X PUT -H "$T" -d '{"value":"via-failover"}' localhost:8080/api/v1/kv/order:6006
# expect: still {"...","ok":true}, coordinated by helix-1 or helix-2
```

Cleanup:

```bash
kill %1 %2 %3 2>/dev/null    # port-forwards
# stop the gateway (fg then Ctrl-C, or kill the go run process)
```

## Expected results

- Unit tests pass under `-race`.
- PUT then GET returns the written value; a missing key is 404; binary round-trips via base64; DELETE
  then GET is 404; no token is 401; and a write still succeeds after one node's gRPC endpoint is
  dropped (failover).

## Troubleshooting

- GET/PUT return 503 "no cluster nodes configured": the gRPC addresses in `HELIX_GATEWAY_NODES` are
  empty or the forwards are down. Confirm the 7070 forwards and the node entries.
- PUT returns 502 with "write quorum not met": a genuine cluster condition (not enough live
  replicas), relayed from the node; bring the cluster back to a healthy quorum.
- A key with a slash returns 404 or misroutes: percent-encode the slash (`%2F`) in the path.
- 400 on PUT: the body must be JSON with exactly one of `value` or `value_base64`.
