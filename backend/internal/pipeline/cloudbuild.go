// Package pipeline contains the cloud-native remediation runner. Where democtl runs
// the apply→test→build→deploy pipeline in-process on a local machine, CloudRunner
// delegates the mutating work to Google Cloud Build: it assembles the (patched) demo_app
// source, uploads it to GCS, and submits a Cloud Build that runs the regression test,
// builds the container image, and deploys it to Cloud Run — streaming each step.
//
// This is the runner used by the hosted agent (Cloud Run can't run go/npm/docker/gcloud
// in-process). It is gated behind PIPELINE_MODE=cloudbuild and an explicit approval; it
// never auto-deploys.
package pipeline

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	artifactregistry "cloud.google.com/go/artifactregistry/apiv1"
	"cloud.google.com/go/artifactregistry/apiv1/artifactregistrypb"
	cloudbuild "cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/storage"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/democtl"
	"github.com/patchpilot/backend/internal/lang"
	"github.com/patchpilot/backend/internal/tools"
)

// Config holds the GCP settings the cloud runner needs.
type Config struct {
	Project    string // GOOGLE_CLOUD_PROJECT
	Region     string // CLOUD_RUN_REGION (also the Artifact Registry location)
	Bucket     string // CLOUD_BUILD_SOURCE_BUCKET (defaults to <project>_cloudbuild)
	ARRepo     string // ARTIFACT_REGISTRY_REPO (Docker repo name)
	Service    string // DEMO_RUN_SERVICE (Cloud Run service name; default checkout-demo)
	SourceRoot string // local demo_app dir to package
	OTLPEnv    map[string]string
}

// CloudRunner submits Cloud Build jobs that build → test → deploy demo_app to Cloud Run.
type CloudRunner struct {
	cb       *cloudbuild.Client
	gcs      *storage.Client
	patches  *tools.PatchStore
	genTest  democtl.TestGenFunc
	genBuild democtl.BuildGenFunc

	mu     sync.RWMutex // guards cfg (SetSourceRoot mutates it), lang, synced and active*
	cfg    Config
	lang   lang.Profile // language profile detected from cfg.SourceRoot (re-detected on SetSourceRoot)
	synced bool         // whether THIS process has uploaded a full base yet (see Remediate)

	// In-flight build, so a halt can cancel it server-side: cancelling the Go context
	// only stops our polling — the Cloud Build itself keeps running without this.
	activeBuildID string
	activeProject string
}

// New constructs a CloudRunner. It dials the Cloud Build + Storage clients with ADC
// (the same credentials the Vertex/Gemini client uses).
func New(ctx context.Context, cfg Config, patches *tools.PatchStore, genTest democtl.TestGenFunc, genBuild democtl.BuildGenFunc) (*CloudRunner, error) {
	if cfg.Project == "" {
		return nil, fmt.Errorf("cloud runner: GOOGLE_CLOUD_PROJECT not set")
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("cloud runner: CLOUD_RUN_REGION not set")
	}
	if cfg.Bucket == "" {
		cfg.Bucket = cfg.Project + "_cloudbuild"
	}
	if cfg.ARRepo == "" {
		cfg.ARRepo = "patchpilot"
	}
	if cfg.Service == "" {
		cfg.Service = "checkout-demo"
	}
	cb, err := cloudbuild.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloud build client: %w", err)
	}
	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage client: %w", err)
	}
	return &CloudRunner{cfg: cfg, lang: lang.Detect(cfg.SourceRoot), cb: cb, gcs: gcs, patches: patches, genTest: genTest, genBuild: genBuild}, nil
}

// language snapshots the current language profile under the read lock (SetSourceRoot may
// re-detect it concurrently when a Git source connects).
func (r *CloudRunner) language() lang.Profile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lang
}

// Language reports the detected language ("go" | "python") for operator-facing logging.
func (r *CloudRunner) Language() string { return string(r.language().ID()) }

// Close releases the GCP clients.
func (r *CloudRunner) Close() {
	if r.cb != nil {
		_ = r.cb.Close()
	}
	if r.gcs != nil {
		_ = r.gcs.Close()
	}
}

// SetSourceRoot re-points the runner at a newly-connected Git source clone, so cloud
// builds package the clone (which carries the accumulated fixes on the working branch)
// rather than the original SOURCE_ROOT. Mirrors democtl.Controller.SetSourceRoot and is
// safe to call at runtime while a build is in flight (effective() snapshots cfg).
func (r *CloudRunner) SetSourceRoot(dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	r.mu.Lock()
	r.cfg.SourceRoot = abs
	// The connected clone may be a different language than the original SOURCE_ROOT.
	r.lang = lang.Detect(abs)
	// A re-point changes what a full upload would contain, so the durable GCS base no
	// longer reflects this runner's source — force the next deploy to re-sync it in full.
	r.synced = false
	r.mu.Unlock()
}

// isSynced reports whether THIS process has already uploaded a full base. It is false
// after a (re)start or a SetSourceRoot, so the next deploy refreshes the durable base.
func (r *CloudRunner) isSynced() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.synced
}

// markSynced records that a full base upload has happened this process lifetime, so
// subsequent deploys can ship a small overlay on top of it.
func (r *CloudRunner) markSynced() {
	r.mu.Lock()
	r.synced = true
	r.mu.Unlock()
}

// setActiveBuild records (or clears, with empty args) the in-flight build so
// CancelActive can target it.
func (r *CloudRunner) setActiveBuild(project, buildID string) {
	r.mu.Lock()
	r.activeProject, r.activeBuildID = project, buildID
	r.mu.Unlock()
}

// CancelActive cancels the in-flight Cloud Build server-side (no-op when idle).
// The autopilot calls this on halt: cancelling the pipeline context only stops our
// polling, while the build itself would otherwise run to completion and deploy.
func (r *CloudRunner) CancelActive(ctx context.Context) error {
	r.mu.RLock()
	project, buildID := r.activeProject, r.activeBuildID
	r.mu.RUnlock()
	if buildID == "" {
		return nil
	}
	_, err := r.cb.CancelBuild(ctx, &cloudbuildpb.CancelBuildRequest{ProjectId: project, Id: buildID})
	return err
}

// Remediate assembles the patched source, submits a Cloud Build (test → build → deploy
// to Cloud Run), and streams each step. It mirrors democtl.Controller.Remediate's shape
// so the server can pick a runner by PIPELINE_MODE. Requires a proposed patch.
func (r *CloudRunner) Remediate(ctx context.Context, opts democtl.Options, emit func(api.Step)) api.PipelineResult {
	var steps []api.Step
	step := func(s api.Step) {
		steps = append(steps, s)
		if emit != nil {
			emit(s)
		}
	}
	fail := func(stage, msg, detail string) api.PipelineResult {
		step(api.Step{Stage: stage, Status: "fail", Message: msg, Detail: detail})
		return api.PipelineResult{Steps: steps, Success: false}
	}

	// Resolve the patch set: the explicit consolidation batch, or the single pending patch.
	applyList := opts.Patches
	if len(applyList) == 0 {
		if prop := r.patches.Latest(); prop != nil {
			applyList = []tools.PatchProposal{*prop}
		}
	}
	if len(applyList) == 0 {
		return fail("apply", "No proposed patch to deploy", "run Investigate + add to batch first")
	}

	// Overlay every fix + (resolved) regression test + a Dockerfile onto the source.
	overlay := map[string]string{}
	var files []string
	for _, p := range applyList {
		overlay[p.File] = p.PatchedContent
		files = append(files, p.File)
	}
	// Drive test/rationale resolution from the first patch (one combined gate per build).
	primary := applyList[0]

	runName := ""
	if opts.Test {
		step(api.Step{Stage: "test", Status: "running", Message: "Resolving regression test (reuse or generate)…"})
		rn, gen, err := r.genTest(ctx, primary.File, primary.Rationale, "")
		if err != nil {
			return fail("test", "Test generation failed", err.Error())
		}
		runName = rn
		for k, v := range gen {
			overlay[k] = v
			files = append(files, k)
		}
		step(api.Step{Stage: "test", Status: "ok", Message: "Regression test ready: " + runName})
	}

	// Effective deploy config: UI/Options params (project/region/service/bucket/repo)
	// override the construction defaults so Settings can retarget the deploy. Also holds
	// the current SourceRoot snapshot (which a Git-source connect may have re-pointed).
	eff := r.effective(opts)

	if _, err := os.Stat(filepath.Join(eff.SourceRoot, "Dockerfile")); err != nil {
		step(api.Step{Stage: "build", Status: "running", Message: "No Dockerfile — generating one…"})
		gen, err := r.genBuild(ctx, "dockerfile", "")
		if err != nil {
			return fail("build", "Dockerfile generation failed", err.Error())
		}
		for k, v := range gen {
			overlay[k] = v
		}
		step(api.Step{Stage: "build", Status: "info", Message: "Generated Dockerfile"})
	}

	if err := r.ensureBucket(ctx, eff); err != nil {
		return fail("build", "Staging bucket unavailable", err.Error())
	}
	if opts.Build {
		if err := r.ensureARRepo(ctx, eff); err != nil {
			return fail("build", "Artifact Registry repo unavailable", err.Error())
		}
	}

	// Patch-only (small overlay) is safe only when a durable base exists AND this process
	// uploaded it. After a (re)start the GCS base survives but the container's local source
	// is reset — and may even be a newly-deployed image — so the persisted base can be stale
	// relative to the source the agent now reads and patches against. So the FIRST deploy of
	// each process lifetime re-syncs the base in full; later deploys overlay on top of it.
	patchOnly := !opts.ForceSync && r.isSynced() && r.baseExists(ctx, eff)

	// Upload the source Cloud Build will use. Incremental (overlay) when we have a fresh
	// base, otherwise the full source as a fresh base. A transient blip retries the SAME
	// upload mode — we never auto-escalate an overlay to a full re-upload of a large repo;
	// "Force full source upload" (ForceSync) is the manual re-sync.
	var object string
	if patchOnly {
		object = "patch-source.tar.gz"
		step(api.Step{Stage: "build", Status: "running", Message: "Uploading patch overlay → gs://" + eff.Bucket + "/" + object})
		if err := retryTransient(ctx, 3, func() error { return r.uploadOverlayOnly(ctx, eff, overlay, object) }); err != nil {
			return fail("build", "Patch overlay upload failed", err.Error())
		}
	} else {
		object = "base-source.tar.gz"
		why := "full source"
		if !r.isSynced() {
			why = "full source (first deploy this session — re-syncing the durable base)"
		}
		step(api.Step{Stage: "build", Status: "running", Message: "Uploading " + why + " → gs://" + eff.Bucket + "/" + object})
		if err := retryTransient(ctx, 3, func() error { return r.uploadSource(ctx, eff, overlay, object) }); err != nil {
			return fail("build", "Full source upload failed", err.Error())
		}
		// The base now reflects this process's source; later deploys can ship overlays.
		r.markSynced()
	}

	// Submit the build (createBuild already retries a transient submit internally).
	imageRef := fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s:%d", eff.Region, eff.Project, eff.ARRepo, eff.Service, time.Now().Unix())
	build := r.buildSpec(eff, object, imageRef, runName, opts, patchOnly)
	step(api.Step{Stage: "build", Status: "running", Message: "Submitting Cloud Build (patchOnly=" + fmt.Sprintf("%t", patchOnly) + ")…"})
	buildID, err := r.createBuild(ctx, eff, build)
	if err != nil {
		return fail("build", "Cloud Build submit failed", err.Error())
	}
	r.setActiveBuild(eff.Project, buildID)
	defer r.setActiveBuild("", "")

	logURL, ok := r.pollBuild(ctx, eff, buildID, step)
	if !ok {
		// A build that ran but failed is almost always a bad patch or a real test/build
		// error (surfaced by pollBuild via fetchBuildErrors) — not a stale base. Re-running
		// the whole pipeline from a full PRISTINE upload wouldn't fix that, and for a large
		// source repo it means an expensive, pointless re-upload. So don't auto-escalate;
		// "Force full source upload" (ForceSync) is the way to deliberately re-sync the base.
		if patchOnly {
			step(api.Step{Stage: "build", Status: "info", Message: "Patch deploy failed. If the cached base looks stale, retry with “Force full source upload”."})
		}
		return api.PipelineResult{Steps: steps, Success: false, Files: files, Verify: logURL}
	}

	step(api.Step{Stage: "verify", Status: "ok", Message: fmt.Sprintf("Deployed to Cloud Run service %q (region %s); test gate passed in-build", eff.Service, eff.Region)})
	return api.PipelineResult{Steps: steps, Success: true, Files: files, Verify: "cloud build " + logURL}
}

// effective overlays per-run deploy params (from Options/Settings) onto the construction
// Config, so the UI's deploy parameters take effect without reconstructing the runner.
func (r *CloudRunner) effective(opts democtl.Options) Config {
	r.mu.RLock()
	c := r.cfg // snapshot (SourceRoot may be re-pointed by SetSourceRoot concurrently)
	r.mu.RUnlock()
	p := opts.Deployment.Params
	c.Project = paramOr(p, "project", c.Project)
	c.Region = paramOr(p, "region", c.Region)
	c.Service = paramOr(p, "service", c.Service)
	c.Bucket = paramOr(p, "sourceBucket", c.Bucket)
	c.ARRepo = paramOr(p, "artifactRepo", c.ARRepo)
	return c
}

func paramOr(m map[string]string, key, def string) string {
	if m != nil {
		if v := strings.TrimSpace(m[key]); v != "" {
			return v
		}
	}
	return def
}

// buildSpec assembles the inline Cloud Build: test → docker build → deploy to Cloud Run.
func (r *CloudRunner) buildSpec(eff Config, object, imageRef, runName string, opts democtl.Options, patchOnly bool) *cloudbuildpb.Build {
	var bsteps []*cloudbuildpb.BuildStep

	if patchOnly {
		// Download the patch overlay from GCS
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:   "download-patch",
			Name: "gcr.io/cloud-builders/gsutil",
			Args: []string{"cp", "gs://" + eff.Bucket + "/" + object, "patch.tar.gz"},
		})
		// Extract it, overwriting the base source
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:         "apply-patch",
			Name:       "gcr.io/google.com/cloudsdktool/cloud-sdk",
			Entrypoint: "tar",
			Args:       []string{"-xzf", "patch.tar.gz", "-C", "."},
		})
	}

	if opts.Test && runName != "" {
		image, entrypoint, args := r.language().CloudTestStep(runName)
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:         "test",
			Name:       image,
			Entrypoint: entrypoint,
			Args:       args,
		})
	}
	if opts.Build {
		cacheImageRef := fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s:latest", eff.Region, eff.Project, eff.ARRepo, eff.Service)
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:         "pull-cache",
			Name:       "gcr.io/cloud-builders/docker",
			Entrypoint: "bash",
			Args:       []string{"-c", fmt.Sprintf("docker pull %s || true", cacheImageRef)},
		})
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:   "build",
			Name: "gcr.io/cloud-builders/docker",
			Env:  []string{"DOCKER_BUILDKIT=1"},
			Args: []string{
				"build",
				"--cache-from", cacheImageRef,
				"-t", imageRef,
				"-t", cacheImageRef,
				"--build-arg", "BUILDKIT_INLINE_CACHE=1",
				".",
			},
		})
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:   "push",
			Name: "gcr.io/cloud-builders/docker",
			Args: []string{"push", imageRef},
		})
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:   "push-cache",
			Name: "gcr.io/cloud-builders/docker",
			Args: []string{"push", cacheImageRef},
		})
	}
	if opts.Deploy {
		deployArgs := []string{
			"run", "deploy", eff.Service,
			"--image", imageRef,
			"--region", eff.Region,
			"--platform", "managed",
			"--quiet",
		}
		if env := r.envVarsArg(); env != "" {
			deployArgs = append(deployArgs, "--set-env-vars", env)
		}
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:         "deploy",
			Name:       "gcr.io/google.com/cloudsdktool/cloud-sdk",
			Entrypoint: "gcloud",
			Args:       deployArgs,
		})
	}

	// Accumulate: overwrite base-source.tar.gz with the (patched) workspace so the NEXT
	// deploy uploads only a small overlay on top of it — this is what keeps uploads cheap
	// for a large source repo (the big base is uploaded once, then cached in GCS). Steps
	// run sequentially, so these are reached only on complete success. Gated on an actual
	// deploy: a test-/build-only run must not mutate the durable base, since that base is
	// the source-of-truth for the live, deployed service.
	if opts.Deploy {
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:         "save-updated-base",
			Name:       "gcr.io/google.com/cloudsdktool/cloud-sdk",
			Entrypoint: "bash",
			Args:       []string{"-c", "mkdir -p tmp_build_cache && tar -czf tmp_build_cache/base-updated.tar.gz --exclude=tmp_build_cache --exclude='*.tar.gz' --exclude='patch.tar.gz' . ; err=$? ; [ $err -eq 0 ] || [ $err -eq 1 ]"},
		})
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:   "upload-updated-base",
			Name: "gcr.io/cloud-builders/gsutil",
			Args: []string{"cp", "tmp_build_cache/base-updated.tar.gz", "gs://" + eff.Bucket + "/base-source.tar.gz"},
		})
	}

	return &cloudbuildpb.Build{
		Source: &cloudbuildpb.Source{
			Source: &cloudbuildpb.Source_StorageSource{
				StorageSource: &cloudbuildpb.StorageSource{Bucket: eff.Bucket, Object: "base-source.tar.gz"},
			},
		},
		Steps: bsteps,

		// Image push is an explicit "push" step (above) so it lands before the deploy
		// step; we deliberately don't also list Build.Images (that would re-push at the end).
		// Write the combined log to a bucket we control so a failure can surface the
		// actual compiler/test output in the UI (gs://<bucket>/log-<id>.txt). Left at the
		// default logging mode to avoid an org-policy submit error; if the log isn't in
		// GCS we fall back to StatusDetail.
		LogsBucket: eff.Bucket,
		Timeout:    durationpb.New(20 * time.Minute),
	}
}

// envVarsArg encodes the OTLP env for `gcloud run deploy --set-env-vars` using a custom
// "^##^" delimiter so values containing "=" or spaces (the OTLP auth header) are safe.
func (r *CloudRunner) envVarsArg() string {
	if len(r.cfg.OTLPEnv) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.cfg.OTLPEnv))
	for k, v := range r.cfg.OTLPEnv {
		parts = append(parts, k+"="+v)
	}
	return "^##^" + strings.Join(parts, "##")
}

// createBuild submits the build and returns its id (from the operation metadata).
// The submit is retried on transient connection failures (a dropped TLS connection
// — e.g. a flaky network or a TLS-intercepting AV — surfaces as gRPC Unavailable).
func (r *CloudRunner) createBuild(ctx context.Context, eff Config, build *cloudbuildpb.Build) (string, error) {
	var op *cloudbuild.CreateBuildOperation
	err := retryTransient(ctx, 3, func() error {
		var e error
		op, e = r.cb.CreateBuild(ctx, &cloudbuildpb.CreateBuildRequest{ProjectId: eff.Project, Build: build})
		return e
	})
	if err != nil {
		return "", err
	}
	meta, err := op.Metadata()
	if err != nil || meta == nil || meta.Build == nil {
		return "", fmt.Errorf("could not read build id from operation: %v", err)
	}
	return meta.Build.Id, nil
}

// isTransient reports whether err is a retryable connection blip rather than a real
// failure — chiefly gRPC Unavailable from a dropped TLS connection ("wsarecv: An
// existing connection was forcibly closed by the remote host"), which a flaky network
// or a TLS-intercepting antivirus can cause mid-call.
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.Aborted, codes.ResourceExhausted:
		return true
	}
	// Some proxies/AV surface a reset as Unknown/Internal carrying a socket message.
	msg := err.Error()
	return strings.Contains(msg, "forcibly closed") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe")
}

// retryTransient runs fn with capped exponential backoff while it returns a transient
// error, up to attempts times. Non-transient errors (and success) return immediately;
// a cancelled ctx stops early.
func retryTransient(ctx context.Context, attempts int, fn func() error) error {
	backoff := 500 * time.Millisecond
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil || !isTransient(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff):
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
	return err
}

// pollBuild polls the build to completion, emitting a step as each Cloud Build step
// transitions. Returns the log URL and whether the build succeeded.
func (r *CloudRunner) pollBuild(ctx context.Context, eff Config, buildID string, step func(api.Step)) (string, bool) {
	seen := map[string]cloudbuildpb.Build_Status{}
	logURL := ""
	transient := 0
	const maxTransient = 5 // ~tolerate a brief network blip without abandoning the build
	for {
		b, err := r.cb.GetBuild(ctx, &cloudbuildpb.GetBuildRequest{ProjectId: eff.Project, Id: buildID})
		if err != nil {
			// A dropped connection here doesn't mean the build failed — it's still
			// running server-side. Back off and re-poll a few times before giving up.
			if isTransient(err) && transient < maxTransient {
				transient++
				step(api.Step{Stage: "build", Status: "info", Message: fmt.Sprintf("Network blip polling Cloud Build — retrying (%d/%d)", transient, maxTransient)})
				select {
				case <-ctx.Done():
					return logURL, false
				case <-time.After(4 * time.Second):
				}
				continue
			}
			step(api.Step{Stage: "build", Status: "fail", Message: "Lost contact with Cloud Build", Detail: err.Error()})
			return logURL, false
		}
		transient = 0
		logURL = b.LogUrl
		for _, s := range b.Steps {
			stage := s.Id
			if stage == "" {
				stage = "build"
			}
			if seen[stage] == s.Status {
				continue
			}
			seen[stage] = s.Status
			switch s.Status {
			case cloudbuildpb.Build_WORKING:
				step(api.Step{Stage: stage, Status: "running", Message: "Cloud Build: " + stage + "…"})
			case cloudbuildpb.Build_SUCCESS:
				step(api.Step{Stage: stage, Status: "ok", Message: "Cloud Build: " + stage + " succeeded"})
			case cloudbuildpb.Build_FAILURE, cloudbuildpb.Build_INTERNAL_ERROR, cloudbuildpb.Build_TIMEOUT:
				step(api.Step{Stage: stage, Status: "fail", Message: "Cloud Build: " + stage + " failed"})
			}
		}
		switch b.Status {
		case cloudbuildpb.Build_SUCCESS:
			return b.LogUrl, true
		case cloudbuildpb.Build_FAILURE, cloudbuildpb.Build_INTERNAL_ERROR, cloudbuildpb.Build_TIMEOUT,
			cloudbuildpb.Build_CANCELLED, cloudbuildpb.Build_EXPIRED:
			failStage := failedStepID(b)
			detail := strings.TrimSpace(r.fetchBuildErrors(ctx, eff, buildID))
			if detail == "" {
				detail = strings.TrimSpace(b.StatusDetail)
			}
			if b.LogUrl != "" {
				if detail != "" {
					detail += "\n\n"
				}
				detail += "Full log: " + b.LogUrl
			}
			step(api.Step{Stage: failStage, Status: "fail", Message: "Cloud Build " + b.Status.String() + " (" + failStage + " step)", Detail: detail})
			return b.LogUrl, false
		}
		select {
		case <-ctx.Done():
			return b.LogUrl, false
		case <-time.After(4 * time.Second):
		}
	}
}

// failedStepID returns the id of the first build step that failed, so the UI can
// attribute the failure to the right stage (test/build/deploy). Falls back to "deploy".
func failedStepID(b *cloudbuildpb.Build) string {
	for _, s := range b.Steps {
		switch s.Status {
		case cloudbuildpb.Build_FAILURE, cloudbuildpb.Build_INTERNAL_ERROR, cloudbuildpb.Build_TIMEOUT:
			if s.Id != "" {
				return s.Id
			}
		}
	}
	return "deploy"
}

// fetchBuildErrors reads the build's combined log from GCS (gs://<bucket>/log-<id>.txt)
// and returns the lines that explain the failure. Cloud Build may flush the log slightly
// after the status flips, so it retries once. Returns "" if the log can't be read (e.g.
// the project logs to Cloud Logging only) — the caller falls back to StatusDetail.
func (r *CloudRunner) fetchBuildErrors(ctx context.Context, eff Config, buildID string) string {
	obj := r.gcs.Bucket(eff.Bucket).Object("log-" + buildID + ".txt")
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ""
			case <-time.After(1500 * time.Millisecond):
			}
		}
		rc, err := obj.NewReader(ctx)
		if err != nil {
			continue
		}
		raw, rerr := io.ReadAll(io.LimitReader(rc, 512*1024))
		_ = rc.Close()
		if rerr == nil && len(raw) > 0 {
			return extractBuildErrors(string(raw), r.language().ErrorMarkers())
		}
	}
	return ""
}

// extractBuildErrors pulls the meaningful failure lines out of a Cloud Build log —
// compiler/test/tooling errors — keeping their "Step #N" prefix for context. markers are
// the language-specific substrings that flag a failure line. If nothing matches it returns
// the tail. The result is capped so it stays readable in the UI.
func extractBuildErrors(log string, markers []string) string {
	var hits []string
	for _, ln := range strings.Split(log, "\n") {
		t := strings.TrimRight(ln, "\r")
		if strings.TrimSpace(t) == "" {
			continue
		}
		for _, m := range markers {
			if strings.Contains(t, m) {
				hits = append(hits, t)
				break
			}
		}
	}
	lines := hits
	if len(lines) == 0 { // nothing matched — fall back to the non-empty tail
		for _, ln := range strings.Split(log, "\n") {
			if t := strings.TrimRight(ln, "\r"); strings.TrimSpace(t) != "" {
				lines = append(lines, t)
			}
		}
	}
	const maxLines = 30
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := strings.Join(lines, "\n")
	const maxChars = 4000
	if len(out) > maxChars {
		out = "…" + out[len(out)-maxChars:]
	}
	return out
}

// ensureBucket makes sure the Cloud Build staging bucket exists, creating it in the
// configured region if absent (mirroring what `gcloud builds submit` does). Creation
// needs storage.buckets.create on the caller's identity (roles/storage.admin); writing
// objects into an already-present bucket only needs objectAdmin, so a precreated bucket
// requires no extra permission.
func (r *CloudRunner) ensureBucket(ctx context.Context, eff Config) error {
	b := r.gcs.Bucket(eff.Bucket)
	if _, err := b.Attrs(ctx); err == nil {
		return nil
	} else if !errors.Is(err, storage.ErrBucketNotExist) {
		return err
	}
	attrs := &storage.BucketAttrs{
		Location:                 eff.Region,
		UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
	}
	if err := b.Create(ctx, eff.Project, attrs); err != nil {
		return fmt.Errorf("create staging bucket %q: %w (grant the agent service account roles/storage.admin, or precreate the bucket)", eff.Bucket, err)
	}
	return nil
}

// ensureARRepo makes sure the Artifact Registry Docker repo exists, creating it in the
// configured region if absent. The push step's Cloud Build SA can upload to the repo but
// cannot create it, and AR does not reliably auto-create a Docker repo on push (push
// fails with `name unknown: Repository "<repo>" not found`), so we create it here with
// the backend's own credentials (mirroring ensureBucket). Creation needs
// roles/artifactregistry.admin; pushing to an existing repo only needs writer.
func (r *CloudRunner) ensureARRepo(ctx context.Context, eff Config) error {
	ar, err := artifactregistry.NewClient(ctx)
	if err != nil {
		return nil // can't check — let the push step surface a clear error instead of blocking
	}
	defer ar.Close()

	name := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", eff.Project, eff.Region, eff.ARRepo)
	if _, gerr := ar.GetRepository(ctx, &artifactregistrypb.GetRepositoryRequest{Name: name}); gerr == nil {
		return nil // already exists
	}
	op, err := ar.CreateRepository(ctx, &artifactregistrypb.CreateRepositoryRequest{
		Parent:       fmt.Sprintf("projects/%s/locations/%s", eff.Project, eff.Region),
		RepositoryId: eff.ARRepo,
		Repository:   &artifactregistrypb.Repository{Format: artifactregistrypb.Repository_DOCKER},
	})
	if err != nil {
		return fmt.Errorf("create Artifact Registry repo %q in %s: %w (grant the agent roles/artifactregistry.admin, or precreate: gcloud artifacts repositories create %s --repository-format=docker --location=%s)", eff.ARRepo, eff.Region, err, eff.ARRepo, eff.Region)
	}
	if _, err := op.Wait(ctx); err != nil {
		return fmt.Errorf("waiting for Artifact Registry repo %q creation: %w", eff.ARRepo, err)
	}
	return nil
}

// uploadSource tars+gzips the demo_app source (with overlay files replacing/adding on-
// disk ones) into the GCS source object Cloud Build will unpack.
func (r *CloudRunner) uploadSource(ctx context.Context, eff Config, overlay map[string]string, object string) error {
	w := r.gcs.Bucket(eff.Bucket).Object(object).NewWriter(ctx)
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	prof := r.language()

	added := map[string]bool{}
	writeEntry := func(rel string, content []byte) error {
		content = prof.Tidy(rel, content)
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(content)
		added[rel] = true
		return err
	}

	root := eff.SourceRoot
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if prof.SkipSource(rel) {
			return nil
		}
		content, ok := overlay[rel]
		if ok {
			return writeEntry(rel, []byte(content))
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return writeEntry(rel, b)
	})
	if walkErr != nil {
		_ = tw.Close()
		_ = gz.Close()
		_ = w.Close()
		return walkErr
	}
	// Overlay files that didn't exist on disk (e.g. a generated test/Dockerfile).
	for rel, content := range overlay {
		if !added[filepath.ToSlash(rel)] {
			if err := writeEntry(filepath.ToSlash(rel), []byte(content)); err != nil {
				_ = tw.Close()
				_ = gz.Close()
				_ = w.Close()
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		_ = w.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// baseExists reports whether base-source.tar.gz exists in GCS.
func (r *CloudRunner) baseExists(ctx context.Context, eff Config) bool {
	_, err := r.gcs.Bucket(eff.Bucket).Object("base-source.tar.gz").Attrs(ctx)
	return err == nil
}

// uploadOverlayOnly packages and uploads ONLY the patch overlay files to GCS.
func (r *CloudRunner) uploadOverlayOnly(ctx context.Context, eff Config, overlay map[string]string, object string) error {
	w := r.gcs.Bucket(eff.Bucket).Object(object).NewWriter(ctx)
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	prof := r.language()

	writeEntry := func(rel string, content []byte) error {
		content = prof.Tidy(rel, content)
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(content)
		return err
	}

	for rel, content := range overlay {
		if err := writeEntry(filepath.ToSlash(rel), []byte(content)); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			_ = w.Close()
			return err
		}
	}

	if err := tw.Close(); err != nil {
		_ = w.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

