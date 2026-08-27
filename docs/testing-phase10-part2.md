# Testing Phase 10 Part 2: Kubernetes manifests

This runbook takes a fresh Google Cloud Shell session to a full verification of Phase 10
Part 2, the raw Kubernetes manifests. It is self-contained: every command can be copied and run
as written. It uses kind (Kubernetes in Docker) for a throwaway local cluster, since Cloud Shell
has Docker.

## What applies to Helix and what does not

Part 2 deploys the containerized node from Part 1 onto Kubernetes. It adds a headless Service for
stable per-pod DNS, a StatefulSet of three nodes wired to each other by that DNS, a client
Service, and a persistent volume per pod. It is the direct analogue of the three-node Compose
cluster, with Kubernetes providing the identity, DNS, and storage. What applies:

- kubectl and a Kubernetes cluster: the dependency and runtime for this part.
- The Part 1 image: the manifests run helix:local, the image Part 1 builds. There is no new build
  here beyond loading that image into the cluster.
- Persistent volumes: each pod gets its own PersistentVolumeClaim mounted at /data, so a restarted
  or rescheduled pod recovers its engine from its own data, exactly as the recovery phase assumes.
- Health checks: readiness and liveness probes hit the node's /healthz on the metrics port.
- DNS and addresses: pods resolve each other at helix-0/1/2.helix-headless.helix.svc.cluster.local.

What is different this part:

- No Go toolchain is needed to run the cluster; only kubectl, kind, and the prebuilt image.
- No proto and no dependency change; this is pure deployment configuration (YAML).

Boundaries worth stating so results are not misread:

- helixctl and the client plane are gRPC, not HTTP. curl or wget apply only to the metrics/health
  endpoint (9090), not the data path (7070).
- helix_cluster_members reads zero for a few seconds after startup until SWIM converges over the
  pod network. That is expected, not a failure.
- Peer discovery uses the headless Service with publishNotReadyAddresses set, so pods can resolve
  each other during bootstrap before they pass readiness.

What Part 2 adds and this runbook verifies: the three pods become ready; a write through one pod is
readable through another (they form one cluster, not three isolated stores); metrics are scrapeable
per pod; data survives a pod deletion via the per-pod volume; and the stack tears down cleanly.

## 0. One-time shell setup used by every section

```bash
export HELIX_HOME="$HOME/helix"
export KIND_CLUSTER="helix-test"
```

---

## 1. Verify the existing environment

```bash
# 1a. Docker (kind runs the cluster inside Docker). Cloud Shell ships it.
docker version --format '{{.Server.Version}}' 2>/dev/null && echo "present: docker" \
  || echo "MISSING or not running: docker daemon"

# 1b. kubectl and kind.
kubectl version --client 2>/dev/null | head -1 || echo "MISSING: kubectl"
kind version 2>/dev/null || echo "MISSING: kind"

# 1c. Repository and the Part 2 manifests.
if [ -d "$HELIX_HOME/.git" ]; then echo "FOUND repo"; else echo "MISSING: repo"; fi
for f in deploy/k8s/namespace.yaml deploy/k8s/headless-service.yaml deploy/k8s/service.yaml \
         deploy/k8s/statefulset.yaml deploy/k8s/kustomization.yaml; do
  [ -f "$HELIX_HOME/$f" ] && echo "present: $f" || echo "MISSING: $f"
done

# 1d. The Part 1 image (built locally). If absent, section 2c builds it.
docker images helix:local --format 'image present: {{.Repository}}:{{.Tag}} ({{.Size}})' 2>/dev/null \
  | grep . || echo "MISSING: helix:local image (build it in 2c)"

# 1e. Any leftover kind cluster from a prior run?
kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER" && echo "FOUND kind cluster $KIND_CLUSTER" \
  || echo "no prior kind cluster named $KIND_CLUSTER"
```

Interpretation: 1a MISSING -> install Docker (see Part 1 runbook 2a); 1b MISSING -> 2a; 1c MISSING
manifests -> 2b; 1d MISSING -> 2c; 1e FOUND -> delete it in 2d for a clean run.

---

## 2. Install or initialize anything missing

### 2a. kubectl and kind (only if 1b is missing)

```bash
# kubectl
curl -fsSLO "https://dl.k8s.io/release/$(curl -fsSL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
sudo install -m 0755 kubectl /usr/local/bin/kubectl && rm kubectl
kubectl version --client | head -1

# kind
curl -fsSLo ./kind https://kind.sigs.k8s.io/dl/v0.23.0/kind-linux-amd64
sudo install -m 0755 kind /usr/local/bin/kind && rm kind
kind version
```

### 2b. Update the checkout (only if 1c found missing manifests)

```bash
cd "$HELIX_HOME"
git fetch origin
git switch main && git pull --ff-only          # if Phase 10 Part 2 is merged
#   or: git switch phase-10/k8s && git pull --ff-only   # if still on its branch
ls deploy/k8s/
```

### 2c. Build the Part 1 image (only if 1d is missing)

```bash
cd "$HELIX_HOME"
make docker-build            # docker build -t helix:local .
docker images helix:local
```

### 2d. Delete a prior kind cluster (only if 1e found one)

```bash
kind delete cluster --name "$KIND_CLUSTER"
```

---

## 3. Configure the required environment variables and services

All node configuration lives in the manifests, so there are no shell variables to set for the
cluster. The values baked into the StatefulSet are the configuration for this part:

| Setting (per pod) | Value | Meaning |
| --- | --- | --- |
| HELIX_NODE_ID | helix-0 / helix-1 / helix-2 (from the pod name) | node identity |
| HELIX_PEERS | helix-0/1/2.helix-headless.helix.svc.cluster.local:7070 | membership by pod DNS |
| HELIX_BIND_ADDR | 0.0.0.0:7070 | gRPC listener inside the pod |
| HELIX_METRICS_ADDR | 0.0.0.0:9090 | metrics/health HTTP server (serves /healthz, /metrics) |
| HELIX_DATA_DIR | /data | engine storage (a per-pod PersistentVolume) |
| HELIX_LOG_FORMAT | json | structured logs |

Replication uses the defaults N=3, R=2, W=2, which suit three nodes, so no quorum tuning is set.
Dummy data for the scenarios: sample key/value `order:5005`=`shipped`; missing key `never-written`.

---

## 4. Start the backend and supporting services

```bash
# 1) Create a throwaway cluster.
kind create cluster --name "$KIND_CLUSTER"
kubectl cluster-info --context "kind-$KIND_CLUSTER"

# 2) Load the local image into the kind node, so imagePullPolicy: IfNotPresent finds it and does
#    not try to pull from a registry.
kind load docker-image helix:local --name "$KIND_CLUSTER"

# 3) Apply all manifests (kustomize creates the namespace first, then the services and StatefulSet).
cd "$HELIX_HOME"
kubectl apply -k deploy/k8s/

# 4) Wait for the StatefulSet to become ready.
kubectl -n helix rollout status statefulset/helix --timeout=120s
```

Tear the whole thing down when finished:

```bash
kind delete cluster --name "$KIND_CLUSTER"
```

---

## 5. Verify service and backend health

```bash
# Pods and their readiness.
kubectl -n helix get pods -o wide
kubectl -n helix get statefulset,svc,pvc

# Node health over the metrics port, from inside a pod (busybox wget ships in the Alpine image).
kubectl -n helix exec helix-0 -- wget -qO- http://127.0.0.1:9090/healthz

# A live coordinated write and read through one pod.
kubectl -n helix exec helix-0 -- helixctl -addr 127.0.0.1:7070 put health-check ok
kubectl -n helix exec helix-0 -- helixctl -addr 127.0.0.1:7070 get health-check   # expect: ok
```

---

## 6. Run Phase 10 Part 2

There is no Go test suite for this part; the deliverable is the running cluster. Running Part 2 is
applying the manifests and reaching three ready pods, which section 4 does. Confirm the end state:

```bash
kubectl -n helix get pods                       # three pods, all Running and READY 1/1
kubectl -n helix get pvc                         # three bound PersistentVolumeClaims (data-helix-0..2)
```

---

## 7. Execute each test scenario with dummy values

### Scenario A: three pods become ready

```bash
kubectl -n helix rollout status statefulset/helix --timeout=120s
kubectl -n helix get pods
```

### Scenario B: coordinated write and cross-pod read (the headline)

Write through helix-0, read the same key back through helix-2 and helix-1. This proves the pods
form one cluster.

```bash
kubectl -n helix exec helix-0 -- helixctl -addr 127.0.0.1:7070 put order:5005 shipped   # OK
kubectl -n helix exec helix-2 -- helixctl -addr 127.0.0.1:7070 get order:5005           # shipped
kubectl -n helix exec helix-1 -- helixctl -addr 127.0.0.1:7070 get order:5005           # shipped
```

### Scenario C: per-pod metrics are scrapeable

```bash
kubectl -n helix exec helix-0 -- wget -qO- http://127.0.0.1:9090/metrics | grep -E 'helix_up|helix_cluster_members'
```

Give the cluster about ten seconds after startup, then re-scrape; `helix_cluster_members{state="alive"}`
should be nonzero as SWIM converges.

### Scenario D: data survives a pod deletion (the volume persists)

Write a value, delete the pod, let the StatefulSet recreate it with the same volume, and confirm the
value is still there.

```bash
kubectl -n helix exec helix-0 -- helixctl -addr 127.0.0.1:7070 put persist:1 keep-me   # OK
kubectl -n helix delete pod helix-2
kubectl -n helix rollout status statefulset/helix --timeout=120s
kubectl -n helix exec helix-2 -- helixctl -addr 127.0.0.1:7070 get persist:1           # keep-me
```

### Scenario E: a missing key is a clean not-found

```bash
kubectl -n helix exec helix-0 -- helixctl -addr 127.0.0.1:7070 get never-written        # (not found)
```

### Scenario F: clean teardown

```bash
kubectl delete -k deploy/k8s/     # removes the workload and services
kubectl -n helix get pvc          # PVCs may remain; delete the namespace to reclaim everything
kubectl delete namespace helix
```

---

## 8. Verify the expected results

- Scenario A: three pods Running and READY 1/1 within a couple of minutes.
- Scenario B: `OK` for the write via helix-0; `shipped` reading the same key via helix-2 and
  helix-1. A different value or a not-found would mean the pods are not clustered.
- Scenario C: `/metrics` includes `helix_up 1`, and after about ten seconds
  `helix_cluster_members{state="alive"}` is nonzero.
- Scenario D: `keep-me` is returned after the pod was deleted and recreated, proving the per-pod
  volume persisted the engine data.
- Scenario E: `(not found)` printed for a key that was never written.
- Scenario F: the namespace and its resources are gone.

---

## 9. Troubleshooting

- Pods stuck in `ErrImagePull` or `ImagePullBackOff`: the image was not loaded into kind. Re-run
  `kind load docker-image helix:local --name "$KIND_CLUSTER"`. The manifests use
  `imagePullPolicy: IfNotPresent` precisely so a locally loaded image is used, not pulled.
- Pods `Pending` on `pod has unbound immediate PersistentVolumeClaims`: the cluster has no default
  StorageClass. kind ships one (`standard`); confirm with `kubectl get storageclass`. On a cluster
  without one, add a default StorageClass before applying.
- Pods `CrashLoopBackOff`: check logs (`kubectl -n helix logs helix-0`). A common cause is
  HELIX_NODE_ID not matching a HELIX_PEERS entry; here the id comes from the pod name and the peer
  list uses those same names, so this should not happen unless the manifests were edited.
- A pod is not `READY` but is `Running`: the readiness probe on /healthz is failing. Confirm the
  metrics server is up (`kubectl -n helix exec <pod> -- wget -qO- http://127.0.0.1:9090/healthz`).
- `helix_cluster_members` stays zero well past ten seconds: pods cannot resolve or reach each
  other. Confirm the headless Service exists (`kubectl -n helix get svc helix-headless`) and that
  it has `publishNotReadyAddresses: true` and `clusterIP: None`.
- `helixctl: not found` on exec: it is at /usr/local/bin/helixctl in the image; call it by name as
  shown. If the image predates the CLI, rebuild with `make docker-build` and reload into kind.
- Trying to curl or wget a gRPC port (7070): expected to fail; that plane is gRPC, not HTTP. Use
  helixctl for data and wget only for the metrics port (9090).
- `kubectl apply -k` errors with `unknown field` or a kustomize version complaint: your kubectl is
  old. Update it (section 2a); `apply -k` needs a reasonably recent kubectl.
- Cleaning up: `kind delete cluster --name "$KIND_CLUSTER"` removes everything at once, including
  the volumes, which is the fastest reset between runs.
