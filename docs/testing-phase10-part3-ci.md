# Testing Phase 10 Part 3 (CI slice): GitHub Actions

This slice adds GitHub Actions CI. Unlike the Cloud Build config, it runs automatically on
GitHub-hosted runners, so it needs no GCP account, billing, or trigger, and you can watch it from
the CLI or the repo's Actions tab. It mirrors the Cloud Build gate (gofmt, vet, build, race tests)
on the pinned Go 1.22, so it is not a behavior change, just an account-independent, always-on
version.

## What applies and what does not

- No new code and no dependency change: this adds one workflow file under `.github/workflows/`.
- The build is vendored, so CI needs no network to fetch modules; `go build` and `go test` use
  `vendor/` automatically.
- The race detector needs CGO, which `ubuntu-latest` provides (gcc is preinstalled), so
  `CGO_ENABLED=1` is set on that step.
- `GOTOOLCHAIN: local` pins the toolchain so a newer Go on the runner cannot silently upgrade the
  build, matching the repo policy.

## How this is verified

CI verification is intrinsic: the proof is GitHub running the workflow and reporting green. There is
nothing to run locally beyond the gate you already run (`make check`).

1. Apply the slice and push the branch (see the commit runbook). Pushing a branch with an open PR,
   or pushing to `main`, triggers the workflow.
2. Watch it from the CLI:

```bash
gh pr checks --watch          # on the PR branch; waits for the run to finish
# or list workflow runs directly:
gh run list --workflow=CI --limit 5
gh run watch                  # attaches to the latest run
```

3. Or open the repo's Actions tab in the browser and watch the "CI" workflow.

Expected: one job, "build, vet, race tests", with all steps green (gofmt, go vet, go build, go
test -race). The first run after adding the workflow may take a few minutes while the runner
provisions and compiles.

## Relationship to Cloud Build

`cloudbuild.yaml` stays in the repo for anyone who runs CI in GCP. GitHub Actions is now the
always-on, account-independent CI that runs on every push and PR without a trigger. They run the
same gate, so keeping both is belt and suspenders; you can retire the Cloud Build config later if
you prefer a single system.

## Optional: a status badge

Once the workflow has run at least once on the default branch, add a badge to the top of
`README.md` (replace OWNER if the repo path differs):

```markdown
![CI](https://github.com/Talif787/helix/actions/workflows/ci.yml/badge.svg)
```

## Troubleshooting

- The `gofmt` step fails: a tracked, non-vendor `.go` file is not gofmt-clean. Run `make fmt`
  locally, commit, and push. The step deliberately excludes `vendor/`.
- The race step fails to link with a C compiler error: unlikely on `ubuntu-latest` (gcc is
  present), but if you change the runner, ensure a C toolchain is available for `-race`.
- The run does not start: confirm the file is at `.github/workflows/ci.yml` on the branch you
  pushed, and that Actions is enabled for the repository (Settings, Actions, General).
- A dependency error during build: the build is vendored, so this usually means `vendor/` was not
  committed or is out of sync with `go.mod`; run `go mod vendor` locally and commit `vendor/`.
