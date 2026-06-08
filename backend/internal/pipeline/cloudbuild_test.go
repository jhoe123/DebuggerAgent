package pipeline

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/patchpilot/backend/internal/democtl"
)

// TestSetSourceRootRepoints guards the Git-source integration: after a clone connects,
// the cloud runner must package source from the clone (re-pointed, absolute) rather than
// the original SOURCE_ROOT, so cloud builds carry the Git-tracked / accumulated source.
func TestSetSourceRootRepoints(t *testing.T) {
	r := &CloudRunner{cfg: Config{SourceRoot: "/original/demo_app", Project: "p", Region: "us-central1"}}
	r.SetSourceRoot(filepath.Join("relative", "clone"))
	want, _ := filepath.Abs(filepath.Join("relative", "clone"))
	if eff := r.effective(democtl.Options{}); eff.SourceRoot != want {
		t.Errorf("effective SourceRoot = %q; want %q (absolute clone path)", eff.SourceRoot, want)
	}
}

// TestSyncGate guards the restart-safety rule: a fresh process (or one whose source was
// re-pointed) is NOT synced, so its next deploy must upload a full base rather than an
// overlay on a possibly-stale durable base; once a full upload happens it flips to synced.
func TestSyncGate(t *testing.T) {
	r := &CloudRunner{cfg: Config{SourceRoot: "/demo_app", Project: "p", Region: "us-central1"}}
	if r.isSynced() {
		t.Fatal("a fresh runner must start unsynced so the first deploy re-syncs the base")
	}
	r.markSynced()
	if !r.isSynced() {
		t.Fatal("markSynced must flip synced=true so later deploys can ship overlays")
	}
	// Re-pointing the source invalidates the base for this runner — force a full re-sync.
	r.SetSourceRoot(filepath.Join("new", "clone"))
	if r.isSynced() {
		t.Fatal("SetSourceRoot must reset synced=false (the durable base no longer matches)")
	}
}

// TestBuildSpecPushesBeforeDeploy guards the "Image not found" regression: the image
// must be pushed to Artifact Registry by an explicit step BEFORE the deploy step (the
// Build.Images push happens only after all steps, which is too late for an in-build deploy).
func TestBuildSpecPushesBeforeDeploy(t *testing.T) {
	r := &CloudRunner{cfg: Config{}}
	eff := Config{Project: "p", Region: "us-central1", Service: "checkout-demo", ARRepo: "patchpilot"}
	opts := democtl.Options{Test: true, Build: true, Deploy: true}

	b := r.buildSpec(eff, "obj.tar.gz", "img:1", "TestReport", opts, false)


	var order []string
	for _, s := range b.Steps {
		order = append(order, s.Id)
	}
	got := strings.Join(order, ",")
	if got != "test,pull-cache,build,push,push-cache,deploy,save-updated-base,upload-updated-base" {
		t.Fatalf("step order = %q; want test,pull-cache,build,push,push-cache,deploy,save-updated-base,upload-updated-base", got)
	}

	if len(b.Images) != 0 {
		t.Errorf("Build.Images should be empty (we push explicitly), got %v", b.Images)
	}
}

// TestBuildSpecPatchOnlyOrder guards the incremental (small-upload) path used for a
// large source repo: when a base already exists we upload only the overlay, and the build
// must download+extract it over the cached base BEFORE test/build/deploy, then re-save the
// base so the next deploy can again upload just an overlay.
func TestBuildSpecPatchOnlyOrder(t *testing.T) {
	r := &CloudRunner{cfg: Config{}}
	eff := Config{Project: "p", Region: "us-central1", Service: "checkout-demo", ARRepo: "patchpilot", Bucket: "b"}
	opts := democtl.Options{Test: true, Build: true, Deploy: true}

	object := "patchpilot-source/patch-123.tar.gz"
	b := r.buildSpec(eff, object, "img:1", "TestReport", opts, true)

	var order []string
	for _, s := range b.Steps {
		order = append(order, s.Id)
	}
	got := strings.Join(order, ",")
	want := "download-patch,apply-patch,test,pull-cache,build,push,push-cache,deploy,save-updated-base,upload-updated-base"
	if got != want {
		t.Fatalf("step order = %q; want %q", got, want)
	}
	// The download step must pull the overlay object we uploaded for this run, and the
	// build source must always be the cached full base (not the overlay).
	if dl := b.Steps[0]; strings.Join(dl.Args, " ") != "cp gs://b/"+object+" patch.tar.gz" {
		t.Errorf("download-patch args = %v; want cp gs://b/%s patch.tar.gz", dl.Args, object)
	}
	if src := b.GetSource().GetStorageSource(); src == nil || src.Object != "base-source.tar.gz" {
		t.Errorf("build source = %v; want base-source.tar.gz", src)
	}
}

// TestBuildSpecNoBaseSaveWithoutDeploy guards the rule that a test-/build-only run must
// NOT mutate the durable base (the source-of-truth for the live deployed service).
func TestBuildSpecNoBaseSaveWithoutDeploy(t *testing.T) {
	r := &CloudRunner{cfg: Config{}}
	eff := Config{Project: "p", Region: "us-central1", Service: "checkout-demo", ARRepo: "patchpilot", Bucket: "b"}
	opts := democtl.Options{Test: true, Build: true, Deploy: false}

	b := r.buildSpec(eff, "obj.tar.gz", "img:1", "TestReport", opts, false)
	for _, s := range b.Steps {
		if s.Id == "save-updated-base" || s.Id == "upload-updated-base" {
			t.Fatalf("non-deploy run must not save the base, but found step %q", s.Id)
		}
	}
}

// TestTidyGoSourceDropsUnusedImport guards the Cloud Build "imported and not used"
// regression: the perf-fix patch removed the only time.Sleep but left "time" imported,
// which failed the in-build `go test`. tidyGoSource must drop the orphaned import.
func TestTidyGoSourceDropsUnusedImport(t *testing.T) {
	src := []byte(`package main

import (
	"fmt"
	"time"
)

func buildReport(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		total += i
	}
	return total
}

func main() { fmt.Println(buildReport(3)) }
`)
	out := string(tidyGoSource("main.go", src))
	if strings.Contains(out, `"time"`) {
		t.Errorf("unused \"time\" import should have been removed:\n%s", out)
	}
	if !strings.Contains(out, `"fmt"`) {
		t.Errorf("used \"fmt\" import must be kept:\n%s", out)
	}
}

// TestExtractBuildErrorsSurfacesCompileError pulls the compiler error out of a real-
// shaped Cloud Build log while dropping the FETCHSOURCE / image-pull / download noise.
func TestExtractBuildErrorsSurfacesCompileError(t *testing.T) {
	log := `starting build "abc"
FETCHSOURCE
Fetching storage object: gs://bucket/source.tar.gz
BUILD
Starting Step #0 - "test"
Step #0 - "test": Pulling image: golang:1.25
Step #0 - "test": go: downloading go.opentelemetry.io/otel v1.44.0
Step #0 - "test": ./main.go:29:2: "time" imported and not used
Step #0 - "test": FAIL	github.com/patchpilot/demo_app [build failed]
Finished Step #0 - "test"
ERROR: build step 0 "golang:1.25" failed: step exited with non-zero status: 1`
	out := extractBuildErrors(log)
	if !strings.Contains(out, `"time" imported and not used`) {
		t.Errorf("expected the compile error in output:\n%s", out)
	}
	if strings.Contains(out, "go: downloading") || strings.Contains(out, "Fetching storage object") {
		t.Errorf("expected download/fetch noise to be filtered out:\n%s", out)
	}
}

// TestTidyGoSourceIgnoresNonGo leaves non-Go files (and unparseable Go) untouched.
func TestTidyGoSourceIgnoresNonGo(t *testing.T) {
	raw := []byte("FROM golang:1.25\n# not go source\n")
	if got := tidyGoSource("Dockerfile", raw); string(got) != string(raw) {
		t.Errorf("non-Go file must pass through unchanged, got: %s", got)
	}
	bad := []byte("package main\nfunc (")
	if got := tidyGoSource("broken.go", bad); string(got) != string(bad) {
		t.Errorf("unparseable Go must pass through unchanged, got: %s", got)
	}
}
