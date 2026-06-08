# Cloud-native remediation pipeline (Cloud Build → Cloud Run)

PatchPilot runs the apply → test → build → deploy pipeline in one of two modes
(`PIPELINE_MODE`):

- **`local`** (default) — `democtl` runs the stages in-process: it writes the patch,
  resolves/generates the regression test, builds, and deploys (local process / docker /
  script). Requires `ENABLE_TEST_CONSOLE=true` and the local toolchain for the project's
  language (Go/Node/Docker, or Python — see [Language support](#language-support)).
- **`cloudbuild`** — the hosted path. The agent runs on **Cloud Run** (which has no
  toolchain/docker/git), so it **delegates** the mutating work to **Cloud Build**: it
  assembles the patched `demo_app` source, uploads it to GCS, and submits a build that
  runs the regression test, builds the image, and deploys it to **Cloud Run**.

Implementation: [backend/internal/pipeline/cloudbuild.go](../backend/internal/pipeline/cloudbuild.go),
selected in [backend/cmd/server/main.go](../backend/cmd/server/main.go) when `PIPELINE_MODE=cloudbuild`.

## What a cloud remediation does
1. Requires a proposed patch (Investigate → approve). Never auto-deploys.
2. Resolves the regression test via the builder agent (reuse an existing test, else
   generate one) and overlays it + the patch(es) onto the source.
3. Generates a `Dockerfile` for the detected language if `demo_app/Dockerfile` is absent
   (Go: multi-stage frontend + Go binary; Python: `python:3.12-slim` + `pip install`).
4. Uploads the source to GCS — full source on the first deploy, then just the patch
   overlay (see [Upload strategy](#upload-strategy-keeping-uploads-small-for-a-large-repo)).
5. Ensures the Artifact Registry Docker repo exists (auto-creates it if absent).
6. Submits a Cloud Build, streamed back as pipeline steps. Incremental (overlay) builds run:
   - **download-patch / apply-patch** — pull the overlay from GCS and extract it over the
     cached base (skipped on a full-source build)
   - **test** — the deploy gate, on a language-specific image: Go `golang:1.25` →
     `go test -run <TestName> ./...`; Python `python:3.12` → `pip install` then
     `pytest -k <TestName>` (see [Language support](#language-support))
   - **build** — `docker build -t <REGION>-docker.pkg.dev/<PROJECT>/<REPO>/<SERVICE>:<ts> .`
   - **push** — `docker push <image>` (explicit, so the image lands BEFORE deploy; we do
     not also list `Build.Images`, which would re-push too late for an in-build deploy)
   - **deploy** — `gcloud run deploy <SERVICE> --image … --region … --set-env-vars OTEL_*`
   - **save-updated-base / upload-updated-base** — re-tar the patched workspace and
     overwrite `gs://<bucket>/base-source.tar.gz` so the next deploy needs only a small
     overlay. Only added when the run actually deploys, so a test-/build-only run never
     mutates the durable base.
7. The deployed `checkout-demo` service IS the ShopFlow storefront, with OTLP env set so
   it reports to Dynatrace — closing the loop.

## Language support
The pipeline is language-aware. The language is **auto-detected from the source root's
manifest** — `pyproject.toml` / `requirements.txt` / `setup.py` ⇒ **Python**; anything else
(including `go.mod`, or no manifest) ⇒ **Go** (the backward-compatible default). Detection
re-runs whenever a Git source connects and re-points the source root, so a connected Python
repo flows through the same investigate → patch → test → build → deploy stages as Go. The
per-language behavior lives in [backend/internal/lang](../backend/internal/lang/lang.go):

| Stage | Go | Python |
|---|---|---|
| **patch** | full-file diff; drop now-unused imports to keep it compiling | full-file diff (no unused-import gate) |
| **test** (cloud) | `golang:1.25` → `go test -run <name> ./...` | `python:3.12` → `pip install` + `pytest -k <name>` |
| **test** (local) | `go test -run <name> ./...` | `pytest -k <name>` (venv interpreter) |
| **build** (local) | `go build -o <bin> .` | venv + `pip install -r requirements.txt` + `python -m compileall .` |
| **build** (cloud) | generated multi-stage Go Dockerfile | generated `python:3.12-slim` Dockerfile |
| **deploy** (local) | run the built binary | run the detected entry module (`python main.py` etc.) |
| **deploy** (cloud) | `gcloud run deploy` the built image (language-agnostic) | same |

The builder agent generates language-appropriate regression tests (`*_test.go` with
`httptest` / `package main` for Go; `test_*.py` with pytest for Python) and Dockerfiles.

## Upload strategy (keeping uploads small for a large repo)
A full re-upload of a large source tree on every deploy is wasteful, so the runner keeps a
durable **base** in GCS and ships only what changed:

- **First deploy of each process lifetime**: the full source is tarred and uploaded as
  `gs://<bucket>/base-source.tar.gz` (build artifacts, `node_modules`, `dist`, and `.git`
  are excluded — see `skipSource`). This happens on the first deploy after **every (re)start**,
  not just the very first ever: the GCS base is durable but the agent runs on Cloud Run
  (ephemeral), so after a restart — or a redeploy with a new container image — the persisted
  base can be **stale** relative to the source the agent now reads and patches against.
  Re-syncing in full once per session keeps the base consistent before any overlay is layered
  on it. (Tracked by an in-memory `synced` flag; a `SetSourceRoot`/Git-connect also resets it.)
- **Subsequent deploys**: only the changed files (the patch + any generated test/Dockerfile)
  are uploaded as `gs://<bucket>/patch-source.tar.gz`. The build's source is still the cached
  base; `download-patch`/`apply-patch` lay the overlay on top. On success the base is
  re-saved, so fixes **accumulate across deploys**.
- **Force full source upload** (the `forceSync` option / "Force full source upload"
  checkbox): re-uploads the full local source as a fresh base. Use this to deliberately
  re-sync the base from local source — e.g. after editing files outside the agent, or if the
  cached base looks stale.
- **Failure handling never re-uploads a large repo by surprise.** A transient blip during an
  overlay upload retries the *same* overlay (it does not escalate to a full upload), and a
  build that runs but fails (bad patch/test) surfaces the error rather than re-running from a
  full pristine upload. Force full source upload is the only path that re-uploads everything.
- **Git source**: when a managed Git source is connected, the runner packages the **clone**
  (re-pointed via `SetSourceRoot` on connect) instead of the original `SOURCE_ROOT`, so the
  upload carries whatever the clone's working branch holds — the recommended way to accumulate
  fixes (confirm→merge into the working branch, then later deploys build on it).

**Caveat — same-file fixes.** Each patch is a *full-file* replacement generated against the
current source. Across deploys, fixes to **different** files accumulate correctly. Multiple
fixes to the **same** file do not auto-merge: within one batch `StagedForApply` keeps the
latest-staged patch per file (the UI warns), and a later deploy's overlay replaces that whole
file. To stack two fixes in one file, fix them in a single patch, or use the Git-source
confirm→merge flow so a later investigation builds on the already-merged earlier fix.

## Configuration (env)
| Var | Purpose |
|---|---|
| `PIPELINE_MODE` | `cloudbuild` to enable this path |
| `GOOGLE_CLOUD_PROJECT` | GCP project (reused from the Vertex/Gemini config) |
| `CLOUD_RUN_REGION` | Cloud Run + Artifact Registry region (e.g. `us-central1`) |
| `CLOUD_BUILD_SOURCE_BUCKET` | GCS bucket for build source (default `<project>_cloudbuild`) |
| `ARTIFACT_REGISTRY_REPO` | Docker repo name (default `patchpilot`) |
| `DEMO_RUN_SERVICE` | Cloud Run service name (default `checkout-demo`) |
| `DT_ENVIRONMENT`, `DT_API_TOKEN` | derive the OTLP env set on the deployed service |

Credentials use Application Default Credentials (the same ADC the Gemini client uses).

## One-time GCP setup
```bash
PROJECT=your-project ; REGION=us-central1 ; REPO=patchpilot
gcloud config set project $PROJECT

# Enable APIs
gcloud services enable cloudbuild.googleapis.com run.googleapis.com artifactregistry.googleapis.com

# Artifact Registry Docker repo (matches ARTIFACT_REGISTRY_REPO + CLOUD_RUN_REGION)
gcloud artifacts repositories create $REPO --repository-format=docker --location=$REGION

# Source bucket (or rely on the default <project>_cloudbuild that `gcloud builds` creates)
gsutil mb -l $REGION gs://${PROJECT}_cloudbuild 2>/dev/null || true

# IAM — the AGENT's runtime service account (the SA the Cloud Run agent runs as):
AGENT_SA=<agent-cloud-run-service-account-email>
gcloud projects add-iam-policy-binding $PROJECT --member="serviceAccount:$AGENT_SA" --role="roles/cloudbuild.builds.editor"
gcloud projects add-iam-policy-binding $PROJECT --member="serviceAccount:$AGENT_SA" --role="roles/storage.objectAdmin"
# If you precreate the bucket + repo above, objectAdmin + AR writer suffice. To let the
# agent auto-create them on first use instead, grant the admin roles:
#   roles/storage.admin           (create the staging bucket)
#   roles/artifactregistry.admin  (create the Docker repo)

# IAM — the CLOUD BUILD service account (needs to deploy to Cloud Run + push images):
CB_SA=$(gcloud projects describe $PROJECT --format='value(projectNumber)')@cloudbuild.gserviceaccount.com
gcloud projects add-iam-policy-binding $PROJECT --member="serviceAccount:$CB_SA" --role="roles/run.admin"
gcloud projects add-iam-policy-binding $PROJECT --member="serviceAccount:$CB_SA" --role="roles/iam.serviceAccountUser"
gcloud projects add-iam-policy-binding $PROJECT --member="serviceAccount:$CB_SA" --role="roles/artifactregistry.writer"
```

## Safety (read before enabling on a public endpoint)
- Remediation **only runs on an explicit request** (`POST /api/remediate` for a single fix,
  `POST /api/pipeline/run` for the consolidated batch) and **requires a proposed patch** — it
  never auto-deploys. The autopilot daemon is **not** wired to the cloud runner, so it cannot
  trigger a cloud deploy. (The Deploy button is enabled whenever a runner is wired, reported
  by `runnerAvailable` from `GET /api/pipeline/config` — it does **not** require
  `ENABLE_TEST_CONSOLE`.)
- The hosted demo agent is typically `--allow-unauthenticated`. **Protect `/api/remediate`
  and `/api/pipeline/run` before enabling `cloudbuild`** (Cloud Run IAP, an auth proxy, or
  require auth on the service); otherwise anyone could trigger a build+deploy. The server
  logs a warning at startup when cloud mode is on.
- The OTLP auth token is passed to the deployed service via `--set-env-vars`. For
  production, move it to Secret Manager and reference it with `--set-secrets`.

## Verify (in your GCP project)
1. Set `PIPELINE_MODE=cloudbuild` + the vars above; start the backend (ADC available).
   Startup logs `Cloud Build remediation ENABLED …`.
2. Investigate a problem and approve the patch, then run the pipeline. The SSE step stream
   shows `test → build → deploy` from Cloud Build.
3. Check the build: `gcloud builds list --limit 1` (and the log URL surfaced in the result).
4. Confirm the deploy: `gcloud run services describe $DEMO_RUN_SERVICE --region $REGION`,
   open its URL (the storefront), and confirm new telemetry for service `checkout-demo` in Dynatrace.
