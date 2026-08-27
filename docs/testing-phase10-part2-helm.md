# Testing Phase 10 Part 2 Slice 2: the Helm chart

This runbook takes a fresh Google Cloud Shell session to a full verification of the Helix Helm
chart. It is self-contained: every command can be copied and run as written. It uses kind for a
throwaway local cluster, the same as the raw-manifests runbook.

## What applies to Helix and what does not

Slice 2 packages the Part 2 Slice 1 manifests as a Helm chart, so the cluster becomes
installable and configurable with one command: replica count, image, N/R/W, ports, storage, and
resources are all values. The rendered output is the same shape as the raw manifests, so a default
install produces the same three-node cluster you already verified. What applies:

- helm and kubectl, plus a Kubernetes cluster: the dependency and runtime for this slice.
- The Part 1 image: the chart runs helix:local by default; there is no new build here beyond
  loading that image into the cluster.
- Everything the raw manifests provided: headless Service for peer DNS, a StatefulSet with a
  per-pod PersistentVolume, probes on /healthz, and a non-root security context. The chart just
  parameterizes them.

What is different this slice:

- The chart generates HELIX_PEERS for exactly replicaCount pods, so resizing the cluster is a
  single value change rather than hand-editing a peer list.
- Namespace creation is Helm's job (helm install --create-namespace), so the chart does not ship
  a Namespace resource.

Boundaries worth stating so results are not misread:

- helixctl and the client plane are gRPC, not HTTP. wget applies only to the metrics/health
  endpoint (9090), not the data path (7070).
- helix_cluster_members reads zero for a few seconds after startup until SWIM converges. Expected.
- A default install (release name "helix" in namespace "helix") renders pod names helix-0/1/2 and
  peer DNS identical to the raw manifests, so the exec commands below match Slice 1 exactly.

What this slice adds and this runbook verifies: the chart lints; it renders valid manifests; a
default install brings up three ready pods; a write through one pod reads back through another; and
an override (for example replicaCount) re-renders a correctly wired cluster.

## 0. One-time shell setup used by every section

```bash
export HELIX_HOME="$HOME/helix"
export KIND_CLUSTER="helix-test"
export CHART="$HELIX_HOME/deploy/helm/helix"
```

---

## 1. Verify the existing environment

```bash
docker version --format '{{.Server.Version}}' 2>/dev/null && echo "present: docker" || echo "MISSING: docker"
kubectl version --client 2>/dev/null | head -1 || echo "MISSING: kubectl"
kind version 2>/dev/null || echo "MISSING: kind"
helm version --short 2>/dev/null || echo "MISSING: helm"

# Chart present and the Part 1 image built.
[ -f "$CHART/Chart.yaml" ] && echo "present: chart" || echo "MISSING: chart at $CHART"
docker images helix:local --format 'image: {{.Repository}}:{{.Tag}}' 2>/dev/null | grep . \
  || echo "MISSING: helix:local (build with make docker-build)"
```

Interpretation: MISSING helm -> 2a; MISSING kubectl/kind -> see the Slice 1 runbook section 2a;
MISSING image -> `cd "$HELIX_HOME" && make docker-build`; MISSING chart -> pull the branch.

---

## 2. Install or initialize anything missing

### 2a. helm (only if missing)

```bash
curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
helm version --short
```

### 2b. kind cluster and image (if not already up from Slice 1)

```bash
kind create cluster --name "$KIND_CLUSTER"
cd "$HELIX_HOME" && make docker-build
kind load docker-image helix:local --name "$KIND_CLUSTER"
```

---

## 3. Configure

All configuration is in the chart's values. The defaults reproduce the verified three-node
cluster; override with `--set key=value` or `-f myvalues.yaml`. The knobs that matter most:

| Value | Default | Meaning |
| --- | --- | --- |
| replicaCount | 3 | number of nodes; HELIX_PEERS is generated for exactly this many |
| image.repository/tag | helix/local | the node image (loaded into kind, not pulled) |
| cluster.n/r/w | 3/2/2 | replication factor and quorums |
| cluster.vnodes | 128 | virtual nodes per node on the ring |
| ports.grpc/metrics | 7070/9090 | container ports; probes hit /healthz on the metrics port |
| persistence.size | 1Gi | per-pod PersistentVolume at /data |
| persistence.storageClassName | "" | empty uses the cluster default (kind ships "standard") |
| service.type | ClusterIP | client-facing Service type |

Keep replicaCount at or above cluster.n so quorums are reachable.

---

## 4. Lint and render before installing

```bash
# Lint the chart.
helm lint "$CHART"

# Render to plain manifests and eyeball the wiring (peers, ports, volumes) without a cluster.
helm template helix "$CHART" -n helix | sed -n '1,80p'

# Confirm the generated peer list is the expected three-node DNS wiring.
helm template helix "$CHART" -n helix | grep -A1 'name: HELIX_PEERS'
```

The HELIX_PEERS value should read
`helix-0=helix-0.helix-headless.helix.svc.cluster.local:7070,helix-1=...,helix-2=...`, which is
identical to the raw manifests.

---

## 5. Install and verify health

```bash
# Install (creates the namespace). Release name "helix" in namespace "helix" matches Slice 1 names.
helm install helix "$CHART" -n helix --create-namespace
helm -n helix status helix

# Wait for the StatefulSet to become ready.
kubectl -n helix rollout status statefulset/helix --timeout=120s
kubectl -n helix get pods,svc,pvc

# Node health.
kubectl -n helix exec helix-0 -- wget -qO- http://127.0.0.1:9090/healthz
```

---

## 6. Run the slice

Installing the chart and reaching three ready pods is running this slice. Confirm the end state:

```bash
kubectl -n helix get pods                        # three pods Running and READY 1/1
helm -n helix get manifest helix | grep -c 'kind: '   # rendered resources exist
```

---

## 7. Execute each test scenario

### Scenario A: lint and render are clean

```bash
helm lint "$CHART"
helm template helix "$CHART" -n helix >/dev/null && echo "renders OK"
```

### Scenario B: default install, cross-pod read (the headline)

```bash
kubectl -n helix exec helix-0 -- helixctl -addr 127.0.0.1:7070 put order:5005 shipped   # OK
kubectl -n helix exec helix-2 -- helixctl -addr 127.0.0.1:7070 get order:5005           # shipped
kubectl -n helix exec helix-1 -- helixctl -addr 127.0.0.1:7070 get order:5005           # shipped
```

### Scenario C: metrics are scrapeable

```bash
kubectl -n helix exec helix-0 -- wget -qO- http://127.0.0.1:9090/metrics | grep -E 'helix_up|helix_cluster_members'
```

### Scenario D: an override re-renders a correctly wired cluster

Render with five replicas and confirm HELIX_PEERS lists five correctly named nodes (no install
needed to check the wiring).

```bash
helm template helix "$CHART" -n helix --set replicaCount=5 | grep -A1 'name: HELIX_PEERS'
# Expect helix-0..helix-4 with matching headless DNS. To actually run it:
#   helm upgrade helix "$CHART" -n helix --set replicaCount=5
#   kubectl -n helix rollout status statefulset/helix --timeout=180s
```

### Scenario E: upgrade and rollback are clean

```bash
helm upgrade helix "$CHART" -n helix --set resources.requests.cpu=75m
helm -n helix history helix
helm rollback helix 1 -n helix
```

### Scenario F: clean teardown

```bash
helm uninstall helix -n helix
kubectl delete namespace helix        # reclaims the PersistentVolumeClaims too
```

---

## 8. Verify the expected results

- Scenario A: `helm lint` reports no failures; `helm template` renders without error.
- Scenario B: `OK` for the write via helix-0; `shipped` reading the same key via helix-2 and
  helix-1. A different value or not-found would mean the pods are not clustered.
- Scenario C: `/metrics` includes `helix_up 1`, and after about ten seconds
  `helix_cluster_members{state="alive"}` is nonzero.
- Scenario D: the rendered HELIX_PEERS lists helix-0 through helix-4, each with the matching
  headless DNS name and :7070.
- Scenario E: the upgrade and rollback both report success and the pods stay healthy.
- Scenario F: the release and namespace are gone.

---

## 9. Troubleshooting

- `helm lint` warns about icon or missing fields: informational for an internal chart; only errors
  block. Fix reported errors before committing.
- Pods `ErrImagePull` or `ImagePullBackOff`: the image was not loaded into kind. Re-run
  `kind load docker-image helix:local --name "$KIND_CLUSTER"`. The chart sets
  `imagePullPolicy: IfNotPresent` so a locally loaded image is used, not pulled.
- Pods `Pending` on unbound PVCs: no default StorageClass. kind ships "standard"; confirm with
  `kubectl get storageclass`, or set `persistence.storageClassName` explicitly.
- `HELIX_PEERS must include this node`: only possible if the chart was edited so the peer keys and
  pod names diverge. The helpers derive both from the same fullname, so a stock chart cannot hit
  this; revert local template edits.
- A pod is `Running` but not `READY`: the /healthz readiness probe is failing. Confirm the metrics
  server is up (`kubectl -n helix exec <pod> -- wget -qO- http://127.0.0.1:9090/healthz`).
- `helix_cluster_members` stays zero past ten seconds: pods cannot reach each other. Confirm the
  headless Service exists and has `clusterIP: None` with `publishNotReadyAddresses: true`
  (`kubectl -n helix get svc helix-headless -o yaml`).
- `helm upgrade` complains about immutable StatefulSet fields (for example changing the volume
  claim template): some StatefulSet fields cannot be updated in place. Uninstall and reinstall, or
  delete the StatefulSet with `--cascade=orphan` first, for changes to immutable fields.
- Full reset between runs: `kind delete cluster --name "$KIND_CLUSTER"` removes everything at once.
