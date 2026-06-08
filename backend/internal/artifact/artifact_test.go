package artifact

import (
	"testing"

	"github.com/patchpilot/backend/internal/api"
)

func TestLifecycleAndOverall(t *testing.T) {
	s := New(t.TempDir())
	s.RecordInvestigation("error:checkout", "Checkout panic", "error", true, "index out of range")
	if got := s.List(); len(got) != 1 || got[0].Overall != "investigated" {
		t.Fatalf("expected overall=investigated, got %+v", got)
	}
	s.RecordStaged("error:checkout", "", "")
	if a := s.List()[0]; a.Overall != "staged" || a.Stages["patch"].Status != "ok" {
		t.Fatalf("expected staged with patch stage ok, got %+v", a)
	}
	s.RecordRun([]string{"error:checkout"}, api.PipelineResult{
		Success: true,
		Verify:  "500 -> 200",
		Steps: []api.Step{
			{Stage: "test", Status: "ok"},
			{Stage: "build", Status: "ok"},
			{Stage: "deploy", Status: "ok"},
			{Stage: "verify", Status: "ok"},
		},
	})
	a := s.List()[0]
	if a.Overall != "deployed" {
		t.Errorf("expected overall=deployed, got %q", a.Overall)
	}
	if a.Stages["test"].Status != "ok" || a.Stages["verify"].Status != "ok" {
		t.Errorf("expected test+verify ok, got %+v", a.Stages)
	}
	if a.Verify != "500 -> 200" {
		t.Errorf("expected verify carried through, got %q", a.Verify)
	}
}

func TestRecordRunSplitsAcrossProblemsAndFails(t *testing.T) {
	s := New(t.TempDir())
	ids := []string{"error:a", "perf:b"}
	s.RecordRun(ids, api.PipelineResult{
		Success: false,
		Steps: []api.Step{
			{Stage: "test", Status: "ok"},
			{Stage: "build", Status: "fail"},
		},
	})
	got := s.List()
	if len(got) != 2 {
		t.Fatalf("expected 2 artifacts, got %d", len(got))
	}
	for _, a := range got {
		if a.Overall != "failed" {
			t.Errorf("%s: expected failed, got %q", a.ProblemID, a.Overall)
		}
		if a.Stages["build"].Status != "failed" {
			t.Errorf("%s: expected build failed, got %+v", a.ProblemID, a.Stages["build"])
		}
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	s.RecordInvestigation("error:checkout", "Panic", "error", true, "fix")
	s.RecordStaged("error:checkout", "", "")

	// A fresh store over the same dir should reload the artifact.
	s2 := New(dir)
	got := s2.List()
	if len(got) != 1 || got[0].ProblemID != "error:checkout" || got[0].Overall != "staged" {
		t.Fatalf("expected reloaded staged artifact, got %+v", got)
	}
}
