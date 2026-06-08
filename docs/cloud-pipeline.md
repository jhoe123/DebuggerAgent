# Cloud-native remediation pipeline (Cloud Build → Cloud Run)

PatchPilot runs the apply → test → build → deploy pipeline in one of two modes
(`PIPELINE_MODE`):

- **`local`** (default) — `democtl` runs the stages in-process: it writes the patch,
  resolves/generates the regression test, builds, and deploys (local process / docker /
  script). Requires `ENABLE_TEST_CONSOLE=true` and a local Go/Node/Docker toolchain.
- **`cloudbuild`** — the hosted path. The agent runs on **Cloud Run** (which has no
  toolchain/docker/git), so it **delegates** the mutating work to **Cloud Build**: it
  assembles the patched `demo_app` source, uploads it to GCS, and submits a build that
  runs the regression test, builds the image, and deploys it to **Cloud Run**.

Implementation: [backend/internal/pipeline/cloudbuild.go](../backend/internal/pipeline/cloudbuild.go),
selected in [backend/cmd/server/main.go](../backend/cmd/server/main.go) when `PIPELINE_MODE=cloudbuild`.

## What a cloud remediation does
1. Requires a proposed patch (Investigate → approve). Never auto-deploys.
2. Resolves the regression test via the builder agent (reuse an existing test, else
   generate one) and overlays it + the patch onto the source.
3. Generates a `Dockerfile` (frontend + Go) if `demo_app/Dockerfile` is absent.
4. Tars the source → uploads to `gs://<bucket>/patchpilot-source/<ts>.tar.gz`.
5. Submits a Cloud Build with three steps, streamed back as pipeline steps:
   - **test** — `golang:1.25` → `go test -run <TestName> ./...` (the deploy gate)
   - **build** — `docker build -t <REGION>-docker.pkg.dev/<PROJECT>/<REPO>/<SERVICE>:<ts> .`
   - **deploy** — `gcloud run deploy <SERVICE> --image … --region … --set-env-vars OTEL_*`
6. The deployed `checkout-demo` service IS the ShopFlow storefront, with OTLP env set so
   it reports to Dynatrace — closing the loop.

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

# IAM — the CLOUD BUILD service account (needs to deploy to Cloud Run + push images):
CB_SA=$(gcloud projects describe $PROJECT --format='value(projectNumber)')@cloudbuild.gserviceaccount.com
gcloud projects add-iam-policy-binding $PROJECT --member="serviceAccount:$CB_SA" --role="roles/run.admin"
gcloud projects add-iam-policy-binding $PROJECT --member="serviceAccount:$CB_SA" --role="roles/iam.serviceAccountUser"
gcloud projects add-iam-policy-binding $PROJECT --member="serviceAccount:$CB_SA" --role="roles/artifactregistry.writer"
```

## Safety (read before enabling on a public endpoint)
- Remediation **only runs on an explicit request** (`POST /api/remediate`) and **requires a
  proposed patch** — it never auto-deploys. The autopilot daemon is **not** wired to the
  cloud runner, so it cannot trigger a cloud deploy.
- The hosted demo agent is typically `--allow-unauthenticated`. **Protect `/api/remediate`
  before enabling `cloudbuild`** (Cloud Run IAP, an auth proxy, or require auth on the
  service); otherwise anyone could trigger a build+deploy. The server logs a warning at
  startup when cloud mode is on.
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
