# Testing Phase 10 Part 3 (Terraform slice)

This slice adds Terraform under `deploy/terraform/` with two targets: a local `kind` target you can
apply for free and verify end to end, and a `gke` target for production that you verify with
`terraform validate` (applying it needs a billing-enabled project). Cloud Shell has Terraform
preinstalled.

## What applies and what does not

- No Go code and no dependency change: this adds HCL and docs only.
- Both targets deploy the same Helm chart from `deploy/helm/helix`, so replica count, image, and
  N/R/W stay consistent with the chart.
- The `kind` target provisions no infrastructure; it drives your existing kind cluster. The `gke`
  target provisions a cluster, a node pool, and an Artifact Registry repository.

## A. kind target (apply this and verify for free)

Prerequisite: the kind cluster and image from the deployment docs.

```bash
# Ensure the cluster exists and the image is loaded.
kind get clusters | grep -qx helix-test || kind create cluster --name helix-test
cd ~/helix && make docker-build && kind load docker-image helix:local --name helix-test
```

Then apply with Terraform:

```bash
cd ~/helix/deploy/terraform/kind
terraform init
terraform fmt -check          # formatting gate
terraform validate            # schema and reference checks
terraform plan                # shows the helm_release that will be created
terraform apply               # installs the release; type yes
```

Verify the release Terraform created:

```bash
terraform output
kubectl -n helix rollout status statefulset/helix --timeout=120s
kubectl -n helix get pods
kubectl -n helix exec helix-0 -- helixctl -addr 127.0.0.1:7070 put order:5005 shipped   # OK
kubectl -n helix exec helix-2 -- helixctl -addr 127.0.0.1:7070 get order:5005           # shipped
```

Expected: three pods ready, and the cross-pod read returns `shipped`, the same acceptance test as
the Helm slice, now driven by Terraform.

Tear down:

```bash
terraform destroy             # removes the Helm release
```

Note: if you already have a manual `helm install helix` in the `helix` namespace, uninstall it
first (`helm uninstall helix -n helix`), since Terraform manages its own release and the two would
collide on the same name.

## B. gke target (validate now, apply when you have a billing-enabled project)

What you can do on any account, no billing required:

```bash
cd ~/helix/deploy/terraform/gke
terraform init                # downloads the google, kubernetes, and helm providers
terraform fmt -check
terraform validate            # this is the verification for this target
```

Expected: `terraform validate` reports success. That confirms the configuration is syntactically
valid and schema-correct against the real provider schemas, which is the meaningful check short of
applying.

To actually apply it (needs a billing-enabled project, the container and artifactregistry APIs
enabled, and credentials), use the recommended two-stage apply:

```bash
gcloud auth application-default login
gcloud services enable container.googleapis.com artifactregistry.googleapis.com --project YOUR_PROJECT

cd ~/helix/deploy/terraform/gke
terraform apply -var project_id=YOUR_PROJECT -var deploy_app=false    # cluster + node pool + registry
terraform output                                                      # note artifact_registry and get_credentials_command

# Build and push the image to the registry, then deploy the app:
gcloud auth configure-docker "$(terraform output -raw artifact_registry | cut -d/ -f1)"
docker build -t "$(terraform output -raw artifact_registry)/helix:v0.12.0" ~/helix
docker push "$(terraform output -raw artifact_registry)/helix:v0.12.0"

terraform apply -var project_id=YOUR_PROJECT -var image_tag=v0.12.0   # deploy_app defaults to true
```

The two-stage apply avoids a plan-time chicken-and-egg: the kubernetes and helm providers are
configured from the cluster that this same configuration creates, so the cluster must exist before
the release is planned.

## Expected results

- kind: `terraform validate` passes, `terraform apply` installs the release, three pods become
  ready, and the cross-pod read returns `shipped`.
- gke: `terraform init` and `terraform validate` succeed. Applying is optional and gated on having
  a billing-enabled project; it is not required to consider this slice verified.

## Troubleshooting

- `terraform validate` errors about an unknown provider: run `terraform init` first in that
  directory so the providers are downloaded.
- kind apply fails to reach the cluster: confirm the context name with `kubectl config get-contexts`
  and pass it, for example `terraform apply -var kube_context=kind-helix-test`.
- kind apply collides with an existing release: uninstall the manual Helm release first
  (`helm uninstall helix -n helix`), since Terraform owns its own release of the same name.
- gke `terraform apply` fails on billing or API errors: the project needs billing enabled and the
  container and artifactregistry APIs enabled; this is expected on a scratch project and is why the
  verification for this target is `validate`, not `apply`.
- gke plan errors that a provider attribute is unknown: use the two-stage apply above
  (`-var deploy_app=false` first), since the app providers depend on the created cluster.
