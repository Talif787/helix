# Development Guide (GitHub + GCP Cloud Shell)

This guide sets up Helix for development entirely inside Google Cloud Shell, connects the repository to GitHub, and wires up Cloud Build as GCP-native continuous integration. It also describes the day-to-day commit, push, branch, and release workflow for Phase 1.

Deployment to GCP (Cloud Run or GKE) is not part of this guide; that arrives in a later phase. What "connecting with GCP" means during Phase 1 is two concrete things: you develop on a GCP VM (Cloud Shell), and every push runs your tests on Cloud Build.

## 0. How Cloud Shell behaves (read this once)

- Cloud Shell gives you a Debian VM with `git`, `gh`, `gcloud`, `docker`, and `go` preinstalled. The image is rebuilt weekly, so the toolchain stays current.
- Only your home directory (`$HOME`, about 5 GB) persists between sessions. Anything installed outside `$HOME` with `apt` is lost when the VM is recycled, and the VM is recycled after roughly an hour of inactivity. Keep the repository under `$HOME` (the default) and you are fine. This project needs no `apt` packages.
- Your git identity (`~/.gitconfig`) and GitHub credentials (`~/.config/gh`) live in `$HOME`, so you authenticate once and it sticks.

Verify the toolchain:

```bash
go version      # must be 1.22 or newer; Cloud Shell is normally well ahead of this
gh --version
gcloud --version | head -1
```

If `go version` is somehow older than 1.22, install a newer Go into `$HOME` and prepend it to your PATH in `~/.bashrc`:

```bash
cd ~ && curl -sSLO https://go.dev/dl/go1.22.12.linux-amd64.tar.gz
mkdir -p ~/.local && tar -C ~/.local -xzf go1.22.12.linux-amd64.tar.gz
echo 'export PATH=$HOME/.local/go/bin:$PATH' >> ~/.bashrc && source ~/.bashrc
go version
```

## 1. One-time account setup

### 1a. Select your GCP project

Cloud Shell is already authenticated as your Google account. Point it at the project you want to bill and run Cloud Build in:

```bash
gcloud config set project YOUR_PROJECT_ID
gcloud config get-value project
```

### 1b. Set your git identity

```bash
git config --global user.name  "Your Name"
git config --global user.email "you@example.com"
git config --global init.defaultBranch main
git config --global pull.rebase true      # linear history on pull
```

### 1c. Authenticate to GitHub

Cloud Shell has no local browser, so use the device-code web flow. `gh` prints a one-time code and a URL; open that URL on your laptop, paste the code, and authorize.

```bash
gh auth login
#  ? Where do you use GitHub?        GitHub.com
#  ? Preferred protocol              HTTPS
#  ? Authenticate Git with gh creds  Yes
#  ? How would you like to authenticate?  Login with a web browser
#  -> copy the one-time code, open https://github.com/login/device on any browser
gh auth setup-git      # makes git use gh for HTTPS credentials
gh auth status
```

If the login command appears to hang (a known keyring quirk on headless VMs), cancel it and authenticate with a Personal Access Token instead:

```bash
# create a classic PAT at https://github.com/settings/tokens with repo + workflow scopes
echo "YOUR_TOKEN" | gh auth login --with-token
gh auth setup-git
```

## 2. Get the Phase 1 code into Cloud Shell

You already have `helix-phase1.zip`. In the Cloud Shell terminal, use the three-dot menu and choose Upload to send the zip to your home directory, then:

```bash
cd ~
unzip -o helix-phase1.zip     # extracts to ~/helix
cd helix
go test ./...                 # confirm the foundation is green before anything else
```

Running the tests first matters: the code was written and reviewed without a compiler available, so your first act should be to prove it builds and passes on a real toolchain.

## 3. Create the GitHub repository and push

From inside `~/helix`, initialize the repo, make the first commit, then let `gh` create the remote and push in one step:

```bash
cd ~/helix
git init -b main
git add .
git commit -m "chore: phase 1 storage engine foundation"

gh repo create helix --private --source=. --remote=origin --push
```

`--source=.` uses the current directory, `--remote=origin` names the remote, and `--push` pushes `main`. Use `--public` instead of `--private` when you are ready to show it off.

If you prefer to create the remote first and wire it manually:

```bash
gh repo create helix --private
git remote add origin https://github.com/YOUR_USERNAME/helix.git
git push -u origin main
```

## 4. Branch and release strategy

Use a light GitHub Flow. `main` stays always-green (it is what CI protects and what you tag). Do each unit of work on a short-lived branch and merge it back through a pull request so CI runs and your history reads well.

```bash
# start work on the next phase
git switch -c phase-2/sstables

# ... edit, then gate locally before pushing ...
make check                      # fmt + vet + race
git add -p                      # stage in reviewable hunks
git commit -m "feat(storage): add SSTable writer and reader"
git push -u origin phase-2/sstables

# open a PR; CI runs against it
gh pr create --fill --base main

# once the Cloud Build check is green
gh pr merge --squash --delete-branch
```

Branch naming: `phase-N/short-topic` for phase work, `fix/short-topic` for fixes. Commit messages: Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`, `refactor:`, `chore:`), optionally scoped (`feat(storage): ...`). This produces a history that reads cleanly to a reviewer.

Tag each completed phase, which matches semantic versioning:

```bash
git switch main && git pull
git tag -a v0.1.0-phase1 -m "Phase 1: storage engine foundation"
git push origin v0.1.0-phase1
```

Optional but recommended once CI exists: protect `main` on GitHub (Settings, then Branches or Rules) so merges require the Cloud Build status check to pass.

## 5. Environment configuration

The node reads configuration from `HELIX_` environment variables. For local runs, copy the template and source it:

```bash
cp .env.example .env            # .env is gitignored
# edit .env if you want, then:
set -a; source .env; set +a
make run                        # starts the interactive shell
```

Never commit `.env`. Commit only `.env.example`, which documents every variable.

## 6. Connect the repository to GCP (Cloud Build CI)

This is the GCP connection for Phase 1: Cloud Build runs `cloudbuild.yaml` (format check, vet, build, race tests) on every push.

### 6a. Enable the required APIs

```bash
gcloud services enable \
  cloudbuild.googleapis.com \
  developerconnect.googleapis.com \
  secretmanager.googleapis.com
```

### 6b. Verify the pipeline immediately, without a trigger

You can run the exact CI pipeline from Cloud Shell right now. This uploads the source (honoring `.gcloudignore`) and runs the build on GCP:

```bash
gcloud builds submit --config=cloudbuild.yaml .
```

Getting a green build here confirms `cloudbuild.yaml` is correct before you automate it.

### 6c. Connect GitHub and create the push trigger

The one-time GitHub authorization needs a browser, so the console is the smoothest path. Cloud Source Repositories is deprecated for new projects; use the Cloud Build GitHub App (2nd gen, backed by Developer Connect).

1. Console, then Cloud Build, then Triggers. Pick a region (for example `us-central1`; a 2nd gen connection cannot be global).
2. Click Connect Repository, choose GitHub (Cloud Build GitHub App), and authorize. Grant access to the `helix` repository only.
3. Click Create Trigger:
   - Event: Push to a branch.
   - Source: the connected `helix` repository, branch `^main$`.
   - Configuration: Cloud Build configuration file, location `/cloudbuild.yaml`.
4. Save. Add a second trigger on Pull Request if you want CI on PRs before merge.

After this, every push to `main` (and PRs, if you added that trigger) runs the pipeline, and the status appears both in the GitHub PR and in the Cloud Build history.

## 7. The daily loop

```bash
# open Cloud Shell, then
cd ~/helix
git switch main && git pull

git switch -c phase-2/sstables      # or the branch you are on
# ... write code and tests ...
make check                          # fmt + vet + race, locally, before committing
git add -p
git commit -m "feat(storage): ..."
git push
gh pr create --fill --base main     # first push of the branch
# CI runs on Cloud Build; when green:
gh pr merge --squash --delete-branch
```

Run `make check` before every commit and `make race` before every push. Because the initial code was authored without a compiler, treat a clean local `make check` plus a green Cloud Build as the real definition of done for each change.

## 8. Looking ahead: deploying to GCP (later phase)

When the project reaches the deployment phase and you want GitHub Actions to deploy to GCP, do not create long-lived service account keys. Use Workload Identity Federation so GitHub Actions authenticates to GCP over OIDC with no stored secrets. That setup belongs with the DevOps phase and is out of scope here; this guide intentionally stops at CI.

## 9. Troubleshooting

- `gh auth login` hangs: use the `--with-token` path in section 1c.
- Cloud Build fails writing logs: `cloudbuild.yaml` already sets `logging: CLOUD_LOGGING_ONLY`, which avoids the logs-bucket requirement; make sure you did not remove it.
- `go test -race` fails to build in CI: the race detector needs cgo; the pipeline sets `CGO_ENABLED=1` and the `golang` image ships gcc, so keep both.
- Work disappeared after a break: only `$HOME` persists and the VM recycles after inactivity. Keep everything under `~/helix` and push often.
- Wrong project billed: run `gcloud config get-value project` and reset it with `gcloud config set project`.
