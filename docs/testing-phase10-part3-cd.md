# Testing Phase 10 Part 3 (CD slice): GitHub Actions deploy to GKE

This slice adds `.github/workflows/cd.yml`: on a version tag or a manual dispatch, it builds the
image, pushes it to Artifact Registry, and runs `helm upgrade` against GKE. It reuses the same Helm
chart and the same registry path as the GKE Terraform, so the pieces line up.

## Honest scope

Unlike CI, CD cannot be run to completion without a cloud target: it needs an Artifact Registry to
push to, a GKE cluster to deploy to, and GCP credentials, which require a billing-enabled project
and IAM your current account does not have. So the workflow here is complete and reviewable, and it
is gated so it never runs (or fails) on ordinary pushes, but its live end-to-end run is deferred
until you have a billing-enabled project (the GKE Terraform target provisions exactly what it
needs). Nothing about this blocks starting the frontend.

## When it runs

- On a pushed tag matching `v*` (a release).
- On a manual dispatch from the Actions tab, with an optional image tag input.

It never runs on a normal push or PR, so it will not produce failing runs before you configure the
credentials below.

## One-time setup (only when you have a billing-enabled project)

### 1. Configuration values (GitHub repository or environment variables)

Set these as repository variables, or scope them to a `production` GitHub Environment (the workflow
uses `environment: production`, which also lets you add a required-reviewer gate):

| Variable | Example | Meaning |
| --- | --- | --- |
| GCP_PROJECT_ID | my-project | target project |
| GCP_REGION | us-central1 | Artifact Registry region |
| ARTIFACT_REPO | helix | Artifact Registry repo id (created by the GKE Terraform) |
| GKE_CLUSTER | helix | cluster name |
| GKE_LOCATION | us-central1 | cluster region or zone |
| HELM_RELEASE | helix | Helm release name |
| HELM_NAMESPACE | helix | namespace |

Set them with the CLI:

```bash
gh variable set GCP_PROJECT_ID --body "my-project"
gh variable set GCP_REGION --body "us-central1"
gh variable set ARTIFACT_REPO --body "helix"
gh variable set GKE_CLUSTER --body "helix"
gh variable set GKE_LOCATION --body "us-central1"
gh variable set HELM_RELEASE --body "helix"
gh variable set HELM_NAMESPACE --body "helix"
```

### 2. Credentials, Workload Identity Federation (recommended)

WIF lets GitHub Actions authenticate to GCP with no long-lived key. One-time, replacing the
placeholders:

```bash
PROJECT=my-project
POOL=github-pool
PROVIDER=github-provider
SA=helix-deployer
REPO=Talif787/helix

gcloud iam service-accounts create "$SA" --project "$PROJECT"

gcloud iam workload-identity-pools create "$POOL" --project "$PROJECT" --location=global \
  --display-name="GitHub Actions"

gcloud iam workload-identity-pools providers create-oidc "$PROVIDER" --project "$PROJECT" \
  --location=global --workload-identity-pool="$POOL" \
  --display-name="GitHub" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository" \
  --attribute-condition="assertion.repository=='${REPO}'" \
  --issuer-uri="https://token.actions.githubusercontent.com"

# Let the repo impersonate the service account.
PROJECT_NUMBER="$(gcloud projects describe "$PROJECT" --format='value(projectNumber)')"
gcloud iam service-accounts add-iam-policy-binding "${SA}@${PROJECT}.iam.gserviceaccount.com" \
  --project "$PROJECT" --role=roles/iam.workloadIdentityUser \
  --member="principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL}/attribute.repository/${REPO}"

# Deploy permissions: push images and deploy to GKE.
gcloud projects add-iam-policy-binding "$PROJECT" \
  --member="serviceAccount:${SA}@${PROJECT}.iam.gserviceaccount.com" --role=roles/artifactregistry.writer
gcloud projects add-iam-policy-binding "$PROJECT" \
  --member="serviceAccount:${SA}@${PROJECT}.iam.gserviceaccount.com" --role=roles/container.developer
```

Then set the two secrets the workflow reads:

```bash
gh secret set GCP_SERVICE_ACCOUNT --body "${SA}@${PROJECT}.iam.gserviceaccount.com"
gh secret set GCP_WORKLOAD_IDENTITY_PROVIDER --body \
  "projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${POOL}/providers/${PROVIDER}"
```

### 2b. Credentials, service-account key (simpler alternative)

If you prefer a key over WIF, create a key, store it as a secret, and change the auth step's `with:`
from the WIF inputs to `credentials_json: ${{ secrets.GCP_SA_KEY }}`:

```bash
gcloud iam service-accounts keys create key.json \
  --iam-account "${SA}@${PROJECT}.iam.gserviceaccount.com"
gh secret set GCP_SA_KEY < key.json
rm key.json
```

A key is a long-lived credential; prefer WIF where you can.

## How to run it

Once the cluster and registry exist (via the GKE Terraform target) and the variables and secrets
are set:

```bash
# Option A: cut a release tag.
git tag v0.12.1
git push origin v0.12.1

# Option B: manual dispatch.
gh workflow run CD -f image_tag=v0.12.1
```

Watch it:

```bash
gh run list --workflow=CD --limit 5
gh run watch
```

## How this is verified

- Structural verification (available now, no cloud): the workflow parses, and its nine steps are
  wired in order (checkout, resolve tag, GCP auth, gcloud, docker auth, build and push, GKE
  credentials, Helm, deploy). This is the review you can do on any account.
- Live verification (deferred to a billing-enabled project): a tag push produces a green CD run, the
  image appears in Artifact Registry, and `kubectl -n helix get pods` on the GKE cluster shows the
  new revision. The final step already runs `kubectl rollout status`, so a green run means the
  rollout succeeded.

## Expected results (on a configured project)

- The run authenticates without a stored key (WIF), builds and pushes
  `REGION-docker.pkg.dev/PROJECT/helix/helix:TAG`, and the Helm step reports the release upgraded.
- `kubectl -n helix rollout status statefulset/helix` succeeds within the timeout.

## Troubleshooting

- The run fails at "Authenticate to Google Cloud": the WIF provider or service account secret is
  wrong, or the attribute condition does not match the repo. Recheck step 2, especially the
  `attribute.repository` value and the two secrets.
- "Permission denied" pushing the image: the service account lacks `roles/artifactregistry.writer`,
  or the repository does not exist yet (create it with the GKE Terraform target).
- "Permission denied" on GKE: the service account lacks `roles/container.developer`, or
  GKE_CLUSTER/GKE_LOCATION do not match the cluster.
- The Helm step fails on an immutable field: some StatefulSet fields cannot change in place; this
  is Kubernetes behavior, not a workflow bug. Uninstall and reinstall for those.
- The workflow does not trigger: it runs only on `v*` tags and manual dispatch, not on branch
  pushes. Push a tag or use `gh workflow run CD`.
- You want a required approval before deploys: add protection rules to the `production` GitHub
  Environment (Settings, Environments), which this workflow already targets.
