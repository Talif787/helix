# Testing frontend slice 1: node status API

The operator console needs truthful data about membership, the ring, and config, none of which the
node exposed over the network (only /metrics and /healthz). This slice adds a read-only JSON status
API on the existing admin server, reusing in-process state, so the console's gateway (built next)
can aggregate it across nodes. It is a prerequisite for the console, not the console itself.

## What it adds

Four read-only routes on the admin server (the metrics port, default 9090):

- `GET /api/v1/status` node id, config (N/R/W, vnodes, timeouts), uptime, storage stats, peers
- `GET /api/v1/members` SWIM members with state (alive/suspect/dead) and incarnation
- `GET /api/v1/ring` ring vnode count, size, and the nodes on the ring
- `GET /api/v1/key/{key}` the preference list (which nodes hold a given key)

All read-only, no new dependency, no proto change. The JSON is built by pure functions so it is unit
tested without a running daemon.

## Scope note on access control

These routes sit next to /metrics and /healthz, which are already unauthenticated and meant for
in-cluster scraping. They are consumed server-to-server by the gateway, not by the browser, so they
are unauthenticated here for consistency. Browser-facing auth and CORS live in the gateway (the next
slice), which is where a request from an operator's browser actually arrives.

## Build and test

```bash
cd ~/helix
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet ./... 2>&1 | head -20        # must be silent
GOTOOLCHAIN=local go test -race -count=1 ./internal/daemon/
make check
git status --porcelain go.mod go.sum vendor/          # expect no output
```

The tests cover the status, members, ring, and key payloads, that an empty member list serializes as
`[]` (not null, so the console can iterate it), and that the key route returns a real preference list
routed through a Go 1.22 pattern mux.

## Verify live against the kind cluster (optional but recommended)

If your kind cluster is up (from the deployment docs), port-forward a node's metrics port and curl
the new routes:

```bash
kubectl -n helix port-forward pod/helix-0 9090:9090 >/dev/null 2>&1 &
sleep 2

curl -s localhost:9090/api/v1/status  | head -40
curl -s localhost:9090/api/v1/members
curl -s localhost:9090/api/v1/ring
curl -s localhost:9090/api/v1/key/order:5005

kill %1 2>/dev/null
```

Expected: `/status` shows `n=3`, the vnode count, uptime, and storage figures; `/members` lists the
three pods with `"state":"alive"` once SWIM has converged; `/ring` lists the three nodes; and
`/key/order:5005` returns a three-node preference list. Note the image must be rebuilt and reloaded
for the running pods to serve these routes (`make docker-build && kind load docker-image helix:local
--name helix-test`, then restart the pods or `helm upgrade`).

## Expected results

- `go test ./internal/daemon/` passes under `-race`.
- The four routes return valid JSON with the fields above.
- `make check` is green with a clean tree; no dependency change.

## Troubleshooting

- A route returns 404: the running node predates this slice. Rebuild the image and reload it into
  kind, then restart the pods.
- `/api/v1/key/...` returns 404 for a key containing a slash: `{key}` matches a single path segment,
  so URL-encode slashes in the key (the console will do this automatically).
- `/members` is empty or all one node early on: SWIM has not converged yet; wait a few seconds.
- `go vet` complains about the mux pattern syntax: the method-pattern routes need Go 1.22, which the
  module targets; confirm `go version` is 1.22 or newer with `GOTOOLCHAIN=local`.
