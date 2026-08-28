# Helix Terraform

Two targets, sharing the same Helm chart under `deploy/helm/helix`.

## kind (local, free, fully applyable)

`kind/` deploys Helix onto an already-running local kind cluster through the kubernetes and helm
providers. It provisions no cloud infrastructure; it drives the existing cluster. This is the
target to actually apply and verify end to end at no cost:

```bash
cd deploy/terraform/kind
terraform init
terraform apply        # installs the Helix Helm release into the kind cluster
```

Prerequisite: a running kind cluster named `helix-test` with the `helix:local` image loaded (the
same setup used elsewhere in the deployment docs).

## gke (production)

`gke/` provisions a GKE cluster, a managed node pool, and an Artifact Registry repository, then
optionally deploys Helix. Applying it needs a billing-enabled project with the GKE and Artifact
Registry APIs enabled and credentials. Without those you can still check it fully short of applying:

```bash
cd deploy/terraform/gke
terraform init
terraform fmt -check
terraform validate
```

Because the kubernetes and helm providers are configured from the cluster that this same
configuration creates, use a two-stage apply to avoid a plan-time chicken-and-egg:

```bash
terraform apply -var project_id=YOUR_PROJECT -var deploy_app=false   # cluster + registry
# build and push the image to the registry (see the runbook), then:
terraform apply -var project_id=YOUR_PROJECT                          # deploy the app
```

Both targets keep replica count, image, and N/R/W as variables, so the same chart is deployed
consistently whether locally or on GKE.
