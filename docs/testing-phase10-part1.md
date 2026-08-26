# Testing Phase 10 Part 1: containerization

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 10
Part 1. It is self-contained: every command can be copied and run as written.

## What applies to Helix and what does not

Part 1 containerizes Helix. It adds a multi-stage Dockerfile that builds static kvnode and
helixctl binaries onto a small Alpine runtime, a .dockerignore, a docker-compose.yml that
stands up a three-node cluster, and Make targets. For the first time the substance is Docker,
not Go tooling. What applies:

- Docker and Docker Compose: required. This is the dependency and the runtime for this part.
- Image build and containers: the deliverable is an image and a running multi-container
  cluster.
- Named volumes: each node persists its engine to a Docker volume, so data survives restarts.
- Health checks: each container has a Docker HEALTHCHECK that hits the node's /healthz.
- URLs and addresses: host ports map to each node (gRPC 7070-7072, metrics 9090-9092).

What is different this part:

- Go is used only inside the build image. You do not need a local Go toolchain to build or run
  the cluster, though it is handy for building a host-side helixctl.
- Code generation and dependencies: no proto changed and no dependency added; the image build
  runs the same pinned Go 1.22 build as CI, inside the builder stage.

Boundaries worth stating so results are not misread:

- helixctl and the client plane are gRPC, not HTTP. curl applies only to the metrics/health
  endpoint (9090-9092), not to the data path (7070-7072).
- helix_cluster_members reads zero for a few seconds after startup until SWIM converges over
  the Compose network. That is expected, not a failure.
- The host-side helixctl you run against the mapped ports is built locally (make build); it does
  not have to be the in-container binary.

What Part 1 adds and this runbook verifies: the image builds; docker compose brings up three
healthy nodes; a write through one node is readable through another; the metrics endpoint is
scrapeable per node; data survives a restart via the volumes; and the stack tears down cleanly.

## 0. One-time shell setup used by every section

```bash
export HELIX_HOME="$HOME/helix"
export HELIX_REPO="https://github.com/Talif787/helix.git"
```

---

## 1. Verify the existing environment

```bash
# 1a. Docker engine and Compose v2 (the core dependency this part). Cloud Shell ships both.
docker version --format '{{.Server.Version}}' 2>/dev/null && echo "present: docker daemon" \
  || echo "MISSING or not running: docker daemon"
docker compose version 2>/dev/null || echo "MISSING: docker compose v2"

# 1b. Supporting tools.
git --version || echo "MISSING: git"
curl --version >/dev/null 2>&1 && echo "present: curl" || echo "MISSING: curl"

# 1c. Go is only needed to build a host-side helixctl (optional; the image builds Go itself).
go version 2>/dev/null || echo "note: no local Go (fine; only needed to build host-side helixctl)"

# 1d. Repository present?
if [ -d "$HELIX_HOME/.git" ]; then
  echo "FOUND repo at $HELIX_HOME"; git -C "$HELIX_HOME" log --oneline -3
else
  echo "MISSING: repo not present at $HELIX_HOME"
fi

# 1e. Is the Phase 10 Part 1 source present?
for f in Dockerfile docker-compose.yml .dockerignore; do
  if [ -f "$HELIX_HOME/$f" ]; then echo "present: $f"; else echo "MISSING: $f"; fi
done
grep -q 'docker-up' "$HELIX_HOME/Makefile" 2>/dev/null \
  && echo "present: Makefile docker targets" || echo "MISSING: Makefile docker targets"

# 1f. Existing Helix containers/images/volumes from a previous run?
docker compose -f "$HELIX_HOME/docker-compose.yml" ps 2>/dev/null | tail -n +1
docker images helix:local --format 'image present: {{.Repository}}:{{.Tag}} ({{.Size}})' 2>/dev/null
docker volume ls --filter name=helix --format 'volume present: {{.Name}}' 2>/dev/null
```

Interpretation: 1a MISSING -> 2a; 1d MISSING -> 2b; 1e MISSING while 1d FOUND -> 2c; 1f showing
old containers/volumes -> clean them in 2d before a fresh run.

---

## 2. Install or initialize anything missing

Do only the sub-steps flagged by section 1.

### 2a. Docker (only if 1a is missing; Cloud Shell normally has it)

```bash
# Cloud Shell includes Docker. If you are on a plain VM instead:
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"   # then log out/in so the group applies
docker version
docker compose version
```

### 2b. Clone the repository (only if 1d is missing)

```bash
cd "$HOME"
git clone "$HELIX_REPO" helix
cd "$HELIX_HOME"
git status
```

### 2c. Update an existing checkout (only if 1e found missing files)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only          # if Phase 10 Part 1 is merged
#   or: git switch phase-10/docker && git pull --ff-only   # if still on its branch
git log --oneline -3
```

### 2d. Clean any prior Helix cluster state (only if 1f showed leftovers)

```bash
cd "$HELIX_HOME"
docker compose down -v 2>/dev/null || true   # remove old containers and volumes
docker image rm helix:local 2>/dev/null || true
docker volume ls --filter name=helix -q | xargs -r docker volume rm 2>/dev/null || true
echo "cleaned prior Helix docker state"
```

---

## 3. Configure the required environment variables and services

All node configuration lives in docker-compose.yml, so there are no shell environment variables
to set for the cluster. The values baked into the compose file are the dummy configuration for
this part:

| Setting (per service) | Value | Meaning |
| --- | --- | --- |
| HELIX_NODE_ID | helix-a / helix-b / helix-c | node identity |
| HELIX_PEERS | helix-a=helix-a:7070,helix-b=helix-b:7070,helix-c=helix-c:7070 | membership by Compose DNS name |
| HELIX_BIND_ADDR | 0.0.0.0:7070 | gRPC listener inside the container |
| HELIX_METRICS_ADDR | 0.0.0.0:9090 | metrics/health HTTP server |
| HELIX_DATA_DIR | /data | engine storage (a named volume) |
| HELIX_LOG_FORMAT | json | structured logs |

Host port mappings (how you reach each node from Cloud Shell):

| Node | gRPC (host -> container) | metrics (host -> container) |
| --- | --- | --- |
| helix-a | 127.0.0.1:7070 -> 7070 | 127.0.0.1:9090 -> 9090 |
| helix-b | 127.0.0.1:7071 -> 7070 | 127.0.0.1:9091 -> 9090 |
| helix-c | 127.0.0.1:7072 -> 7070 | 127.0.0.1:9092 -> 9090 |

Dummy data used in the scenarios:

| Input | Value |
| --- | --- |
| sample key/value | `order:5005`=`shipped`, `user:1`=`alice` |
| missing key | `never-written` |

There is no external database, broker, or credential to configure. Replication uses the
defaults (N=3, R=2, W=2), which suit three nodes, so no quorum tuning is needed.

---

## 4. Start the backend and supporting services

Build the image and bring up the cluster. Also build a host-side helixctl to drive it.

```bash
cd "$HELIX_HOME"

# 1) Build the runtime image.
docker build -t helix:local .
docker images helix:local

# 2) Bring up the three-node cluster (builds if needed, runs detached).
docker compose up --build -d
docker compose ps

# 3) Build a host-side helixctl to talk to the mapped ports (optional but convenient).
#    Requires a local Go; if you have none, use `docker compose exec` in Scenario B instead.
go build -o bin/helixctl ./cmd/helixctl 2>/dev/null && echo "built host helixctl" \
  || echo "no local Go; use docker compose exec helixctl ... instead"
```

Wait for health (give SWIM a few seconds to converge):

```bash
cd "$HELIX_HOME"
for i in $(seq 1 12); do
  status=$(docker compose ps --format '{{.Name}} {{.Health}}')
  echo "$status"
  echo "$status" | grep -q 'unhealthy\|starting' || { echo "all healthy"; break; }
  sleep 3
done
```

Stop the cluster when done (keeps volumes):

```bash
docker compose stop
```

Full teardown including volumes:

```bash
docker compose down -v
```

---

## 5. Verify service and backend health

Health here is container health plus a live request and a live scrape.

```bash
cd "$HELIX_HOME"

# Container health as Docker sees it.
docker compose ps --format '{{.Name}}\t{{.State}}\t{{.Health}}'

# Node a's /healthz over the mapped metrics port.
curl -s -o /dev/null -w "helix-a /healthz: %{http_code}\n" http://127.0.0.1:9090/healthz
curl -s http://127.0.0.1:9090/healthz

# A live coordinated write/read via helixctl (host binary).
./bin/helixctl -addr 127.0.0.1:7070 put health-check ok   # expect: OK
./bin/helixctl -addr 127.0.0.1:7070 get health-check      # expect: ok
```

If you have no host-side helixctl, use the in-container one:

```bash
docker compose exec helix-a helixctl -addr 127.0.0.1:7070 put health-check ok
docker compose exec helix-a helixctl -addr 127.0.0.1:7070 get health-check
```

---

## 6. Run Phase 10 Part 1

There is no Go test suite for this part; the deliverable is the image and the running cluster.
"Running Part 1" is building the image and bringing the cluster to a healthy state, which
section 4 does. Confirm the end state:

```bash
cd "$HELIX_HOME"
docker compose ps                       # three services, State running, Health healthy
docker image inspect helix:local --format 'image OK: {{.Id}}' 2>/dev/null
docker volume ls --filter name=helix    # three data volumes exist
```

If you want the same build the way CI would run it, the Makefile targets wrap these:

```bash
make docker-build     # docker build -t helix:local .
make docker-up        # docker compose up --build -d
make docker-logs      # follow logs
make docker-down      # docker compose down -v
```

---

## 7. Execute each test scenario with dummy values

Bring the cluster up (section 4) before these. Scenarios use the host-side helixctl; swap in
`docker compose exec helix-a helixctl ...` if you have no local Go.

### Scenario A: image builds and the cluster is healthy

```bash
cd "$HELIX_HOME"
docker build -t helix:local .
docker compose up --build -d
sleep 8
docker compose ps --format '{{.Name}}\t{{.Health}}'   # expect all three: healthy
```

### Scenario B: coordinated write and read across nodes (the headline)

Write through node a, read the same key back through node c and node b. This proves the
containers form one cluster, not three isolated stores.

```bash
cd "$HELIX_HOME"
./bin/helixctl -addr 127.0.0.1:7070 put order:5005 shipped   # via helix-a; expect: OK
./bin/helixctl -addr 127.0.0.1:7072 get order:5005           # via helix-c; expect: shipped
./bin/helixctl -addr 127.0.0.1:7071 get order:5005           # via helix-b; expect: shipped
```

### Scenario C: per-node metrics are scrapeable

```bash
# Node a metrics.
curl -s http://127.0.0.1:9090/metrics | grep -E 'helix_up|helix_cluster_members'
# Node b and c health.
curl -s -o /dev/null -w "helix-b /healthz: %{http_code}\n" http://127.0.0.1:9091/healthz
curl -s -o /dev/null -w "helix-c /healthz: %{http_code}\n" http://127.0.0.1:9092/healthz
```

Give the cluster about 10 seconds after startup, then re-scrape; `helix_cluster_members{state="alive"}`
should be nonzero as SWIM converges.

### Scenario D: data survives a restart (volumes persist)

Write a value, restart the cluster without removing volumes, and confirm the value is still
there.

```bash
cd "$HELIX_HOME"
./bin/helixctl -addr 127.0.0.1:7070 put persist:1 keep-me     # expect: OK
docker compose restart                                        # containers restart, volumes kept
sleep 8
./bin/helixctl -addr 127.0.0.1:7070 get persist:1            # expect: keep-me
```

### Scenario E: a missing key is a clean not-found

```bash
cd "$HELIX_HOME"
./bin/helixctl -addr 127.0.0.1:7070 get never-written   # expect: (not found)
echo "exit: $?"                                          # expect: 0
```

### Scenario F: clean teardown

```bash
cd "$HELIX_HOME"
docker compose down -v
docker compose ps                          # expect: no services
docker volume ls --filter name=helix       # expect: no helix volumes
```

---

## 8. Verify the expected results

### Scenario A (build and health)

- `docker build` completes and `helix:local` exists.
- `docker compose ps` shows three services, all with Health `healthy` within a few seconds.

### Scenario B (cross-node read)

- `OK` for the write via helix-a; `shipped` when reading the same key via helix-c and helix-b.
  A different value or a not-found would mean the nodes are not clustered.

### Scenario C (metrics)

- Node a `/metrics` includes `helix_up 1` and the `helix_cluster_members` family; b and c
  `/healthz` return 200. After ~10s, `helix_cluster_members{state="alive"}` is nonzero.

### Scenario D (persistence)

- `keep-me` is returned after the restart, proving the named volumes persisted the engine data.

### Scenario E (not-found)

- `(not found)` printed, exit 0.

### Scenario F (teardown)

- `docker compose ps` lists no services and no helix volumes remain.

---

## 9. Troubleshooting

Docker basics
- `Cannot connect to the Docker daemon`: the daemon is not running or your user lacks access. In
  Cloud Shell it should just work; on a VM, `sudo systemctl start docker` and ensure your user
  is in the docker group (2a), then re-login.
- `docker compose: command not found` but `docker-compose` exists: you have Compose v1. This
  file targets v2 (`docker compose`). Install the Compose plugin or adapt the commands.

Build
- The build fails at `go build` with `go.mod requires go >= 1.25`: the module drifted off 1.22.
  On the host, `go mod edit -go=1.22` and commit, then rebuild the image. The builder stage
  pins GOTOOLCHAIN=local, so it will not silently fetch a newer toolchain.
- `failed to solve: ... go.sum` mismatch during `go mod download`: go.sum is stale or missing.
  On the host run `GOTOOLCHAIN=local go mod tidy`, commit, and rebuild.
- Very slow first build: expected. The builder downloads modules and compiles; subsequent builds
  reuse cached layers unless go.mod/go.sum or sources change.

Cluster health
- A container is `unhealthy`: check its logs (`docker compose logs helix-a`). A common cause is
  a node unable to resolve a peer; confirm all three services are on the same Compose network
  (they are by default) and HELIX_PEERS uses the service names.
- `helix_cluster_members` stays zero well past 10s: SWIM is not converging. Confirm all three
  containers are running (`docker compose ps`) and none crashed on startup (logs).
- helixctl `connection refused` from the host: the cluster is not up, or you used the wrong host
  port. gRPC ports are 7070/7071/7072; metrics are 9090/9091/9092. Do not point helixctl at a
  metrics port.

Behavior
- A read returns the wrong value or not-found right after a write: with N=3, R=2, W=2 a write
  needs two acks and a read two responses; if a node is unhealthy you can drop below quorum.
  Confirm all three are healthy, then retry.
- helixctl `context deadline exceeded`: a node is unreachable within the timeout, or a write
  cannot reach quorum because a node is down. Check `docker compose ps` and logs.

Ports and networking
- `bind: address already in use` on `up`: a host port (7070-7072 or 9090-9092) is taken, perhaps
  by a bare-metal kvnode from an earlier runbook. Stop it (`pkill -f kvnode`) or change the host
  port mappings in docker-compose.yml.
- Trying to `curl` a gRPC port (7070): expected to fail or hang; that plane is gRPC, not HTTP.
  Use helixctl for data and curl only for the metrics ports.

State and cleanup
- Stale data from a prior run appears after `up`: you kept volumes. `docker compose down -v`
  removes them for a clean slate (2d).
- Disk fills with old images/volumes over many runs: `docker system prune` and
  `docker volume prune` reclaim space (review what they remove first).

Terminal display
- Long pasted blocks wrap and look garbled: prefer the command blocks above rather than typing.
  If the prompt looks corrupted after a large paste, run `reset` or open a new Cloud Shell tab.
