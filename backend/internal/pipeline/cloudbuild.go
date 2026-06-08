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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	cloudbuild "cloud.google.com/go/cloudbuild/apiv1/v2"
	"cloud.google.com/go/cloudbuild/apiv1/v2/cloudbuildpb"
	"cloud.google.com/go/storage"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/democtl"
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
	cfg      Config
	cb       *cloudbuild.Client
	gcs      *storage.Client
	patches  *tools.PatchStore
	genTest  democtl.TestGenFunc
	genBuild democtl.BuildGenFunc
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
	return &CloudRunner{cfg: cfg, cb: cb, gcs: gcs, patches: patches, genTest: genTest, genBuild: genBuild}, nil
}

// Close releases the GCP clients.
func (r *CloudRunner) Close() {
	if r.cb != nil {
		_ = r.cb.Close()
	}
	if r.gcs != nil {
		_ = r.gcs.Close()
	}
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

	if _, err := os.Stat(filepath.Join(r.cfg.SourceRoot, "Dockerfile")); err != nil {
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

	// Package + upload the source to GCS.
	object := fmt.Sprintf("patchpilot-source/%d.tar.gz", time.Now().UnixNano())
	step(api.Step{Stage: "build", Status: "running", Message: "Packaging source → gs://" + r.cfg.Bucket + "/" + object})
	if err := r.uploadSource(ctx, overlay, object); err != nil {
		return fail("build", "Source upload failed", err.Error())
	}

	// Submit the build.
	imageRef := fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s:%d", r.cfg.Region, r.cfg.Project, r.cfg.ARRepo, r.cfg.Service, time.Now().Unix())
	build := r.buildSpec(object, imageRef, runName, opts)
	step(api.Step{Stage: "build", Status: "running", Message: "Submitting Cloud Build…"})
	buildID, err := r.createBuild(ctx, build)
	if err != nil {
		return fail("build", "Cloud Build submit failed", err.Error())
	}

	logURL, ok := r.pollBuild(ctx, buildID, step)
	if !ok {
		return api.PipelineResult{Steps: steps, Success: false, Files: files, Verify: logURL}
	}
	step(api.Step{Stage: "verify", Status: "ok", Message: fmt.Sprintf("Deployed to Cloud Run service %q (region %s); test gate passed in-build", r.cfg.Service, r.cfg.Region)})
	return api.PipelineResult{Steps: steps, Success: true, Files: files, Verify: "cloud build " + logURL}
}

// buildSpec assembles the inline Cloud Build: test → docker build → deploy to Cloud Run.
func (r *CloudRunner) buildSpec(object, imageRef, runName string, opts democtl.Options) *cloudbuildpb.Build {
	var bsteps []*cloudbuildpb.BuildStep
	if opts.Test && runName != "" {
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:         "test",
			Name:       "golang:1.25",
			Entrypoint: "go",
			Args:       []string{"test", "-run", runName, "./..."},
		})
	}
	if opts.Build {
		bsteps = append(bsteps, &cloudbuildpb.BuildStep{
			Id:   "build",
			Name: "gcr.io/cloud-builders/docker",
			Args: []string{"build", "-t", imageRef, "."},
		})
	}
	if opts.Deploy {
		deployArgs := []string{
			"run", "deploy", r.cfg.Service,
			"--image", imageRef,
			"--region", r.cfg.Region,
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
	return &cloudbuildpb.Build{
		Source: &cloudbuildpb.Source{
			Source: &cloudbuildpb.Source_StorageSource{
				StorageSource: &cloudbuildpb.StorageSource{Bucket: r.cfg.Bucket, Object: object},
			},
		},
		Steps:   bsteps,
		Images:  []string{imageRef},
		Timeout: durationpb.New(20 * time.Minute),
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
func (r *CloudRunner) createBuild(ctx context.Context, build *cloudbuildpb.Build) (string, error) {
	op, err := r.cb.CreateBuild(ctx, &cloudbuildpb.CreateBuildRequest{ProjectId: r.cfg.Project, Build: build})
	if err != nil {
		return "", err
	}
	meta, err := op.Metadata()
	if err != nil || meta == nil || meta.Build == nil {
		return "", fmt.Errorf("could not read build id from operation: %v", err)
	}
	return meta.Build.Id, nil
}

// pollBuild polls the build to completion, emitting a step as each Cloud Build step
// transitions. Returns the log URL and whether the build succeeded.
func (r *CloudRunner) pollBuild(ctx context.Context, buildID string, step func(api.Step)) (string, bool) {
	seen := map[string]cloudbuildpb.Build_Status{}
	for {
		b, err := r.cb.GetBuild(ctx, &cloudbuildpb.GetBuildRequest{ProjectId: r.cfg.Project, Id: buildID})
		if err != nil {
			step(api.Step{Stage: "build", Status: "fail", Message: "Lost contact with Cloud Build", Detail: err.Error()})
			return "", false
		}
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
			step(api.Step{Stage: "deploy", Status: "fail", Message: "Cloud Build " + b.Status.String(), Detail: b.StatusDetail + " " + b.LogUrl})
			return b.LogUrl, false
		}
		select {
		case <-ctx.Done():
			return b.LogUrl, false
		case <-time.After(4 * time.Second):
		}
	}
}

// uploadSource tars+gzips the demo_app source (with overlay files replacing/adding on-
// disk ones) into the GCS source object Cloud Build will unpack.
func (r *CloudRunner) uploadSource(ctx context.Context, overlay map[string]string, object string) error {
	w := r.gcs.Bucket(r.cfg.Bucket).Object(object).NewWriter(ctx)
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	added := map[string]bool{}
	writeEntry := func(rel string, content []byte) error {
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(content)
		added[rel] = true
		return err
	}

	root := r.cfg.SourceRoot
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
		if skipSource(rel) {
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

// skipSource excludes build artifacts and VCS data from the uploaded source.
func skipSource(rel string) bool {
	switch {
	case strings.HasPrefix(rel, "web/node_modules/"),
		strings.HasPrefix(rel, "web/dist/"),
		strings.HasPrefix(rel, "web/.vite/"),
		strings.HasPrefix(rel, ".git/"),
		strings.HasPrefix(rel, "demo_app_run"),
		strings.HasSuffix(rel, ".test"):
		return true
	}
	return false
}
