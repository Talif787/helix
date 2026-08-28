# Testing gateway slice 2d: SSE live stream

This is the final gateway slice. It adds `GET /api/v1/stream`, a Server-Sent Events endpoint that
pushes a periodic snapshot of membership and metrics, so the console can show live topology and
dashboards without polling. After this, the gateway is feature-complete.

## What it adds

- `GET /api/v1/stream` emits `text/event-stream`. It sends an initial `snapshot` event immediately,
  then one every interval (default 2s), each carrying `{ts, members, metrics}` (the same aggregates
  as the REST endpoints). It stops cleanly when the client disconnects.
- Config: `HELIX_GATEWAY_STREAM_INTERVAL` (default `2s`).

Auth note: the stream stays behind the operator bearer token like every other route. Browsers
cannot set the `Authorization` header on the old `EventSource` API, so the console consumes this
with a `fetch` streaming reader (which can send headers). Do not move the token to a query string.

No new dependency.

## Build and unit-test

```bash
cd ~/helix
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet ./... 2>&1 | head -20
GOTOOLCHAIN=local go test -race -count=1 ./internal/gateway/
make check
git status --porcelain go.mod go.sum vendor/     # expect no output
```

The tests cover snapshot aggregation (members and metrics), the interval default, that the handler
writes an SSE `snapshot` frame and flushes then exits when the client is gone, and that a
non-flushing writer yields 500.

## Run against the kind cluster (dummy values)

```bash
kubectl -n helix port-forward pod/helix-0 7070:7070 9090:9090 >/tmp/pf0.log 2>&1 &
kubectl -n helix port-forward pod/helix-1 7071:7070 9091:9090 >/tmp/pf1.log 2>&1 &
kubectl -n helix port-forward pod/helix-2 7072:7070 9092:9090 >/tmp/pf2.log 2>&1 &
sleep 2

cd ~/helix
export HELIX_GATEWAY_TOKEN="op-secret-token"
export HELIX_GATEWAY_CORS_ORIGIN="http://localhost:3000"
export HELIX_GATEWAY_NODES="helix-0=127.0.0.1:7070;127.0.0.1:9090,helix-1=127.0.0.1:7071;127.0.0.1:9091,helix-2=127.0.0.1:7072;127.0.0.1:9092"
export HELIX_GATEWAY_STREAM_INTERVAL="2s"
go run ./cmd/gateway &        # use go run, not go build, so no binary lands in the repo
sleep 2
export T="Authorization: Bearer op-secret-token"
```

## Test scenarios

```bash
# Scenario 1: stream a few frames, then stop after 7 seconds (expect 3 to 4 snapshot events)
curl -sN --max-time 7 -H "$T" localhost:8080/api/v1/stream
# expect repeating blocks:
#   event: snapshot
#   data: {"ts":...,"members":{"nodes":[...]},"metrics":{"nodes":[...]}}

# Scenario 2: content type is text/event-stream
curl -s -D - -o /dev/null --max-time 3 -H "$T" localhost:8080/api/v1/stream | grep -i content-type
# expect: Content-Type: text/event-stream

# Scenario 3: auth is enforced (no token)
curl -s -o /dev/null -w "%{http_code}\n" --max-time 3 localhost:8080/api/v1/stream    # expect: 401

# Scenario 4: the stream reflects a change. In another terminal, watch the stream, then delete a
# pod so SWIM eventually marks it dead; the members frames should show it leave/return.
kubectl -n helix delete pod helix-2
# in the streaming terminal you will see members frames change over the next several seconds
```

Cleanup:

```bash
kill %1 %2 %3 2>/dev/null    # or the correct job numbers from `jobs -l`
# stop the gateway (fg then Ctrl-C, or kill the go run process)
```

## Consuming it from the console (reference)

The console reads it with fetch streaming, not EventSource, so it can send the token:

```js
const res = await fetch(`${GATEWAY}/api/v1/stream`, {
  headers: { Authorization: `Bearer ${token}` },
});
const reader = res.body.getReader();
// decode chunks, split on "\n\n", parse the "data:" line of each "event: snapshot" block
```

## Expected results

- Unit tests pass under `-race`.
- The stream returns `text/event-stream`, emits an immediate frame then one per interval, requires
  the token (401 without), and its member frames change as the cluster changes.

## Troubleshooting

- `curl` hangs with no output: that is expected for a stream; use `-N` (no buffering) and `--max-time`
  to bound it, as above.
- No frames, immediate close: confirm the token header and that at least the admin port-forwards are
  up (the snapshot aggregates members and metrics from the admin API).
- 500 "streaming unsupported": the response writer does not support flushing; this does not happen
  with the standard server, only if the gateway is fronted by something that buffers. Disable
  proxy buffering on any intermediary (for a tunnel, ensure it streams).
- Frames are large or frequent: raise `HELIX_GATEWAY_STREAM_INTERVAL`.
