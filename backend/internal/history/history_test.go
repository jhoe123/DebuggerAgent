package history

import (
	"path/filepath"
	"testing"

	"github.com/patchpilot/backend/internal/api"
)

func TestRecordFillsAndOrders(t *testing.T) {
	s := New(200, "")
	s.RecordProposed("error:svc", "main.go", "--- a\n+++ b", "bounds check")
	s.RecordApproved("error:svc", "main.go", "--- a\n+++ b", "/tmp/patches/main.go")

	entries := s.List()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	// Newest first.
	if entries[0].Kind != "approved" || entries[1].Kind != "proposed" {
		t.Fatalf("order wrong: %s, %s", entries[0].Kind, entries[1].Kind)
	}
	if entries[0].ID == "" || entries[0].CreatedAt == "" {
		t.Fatalf("ID/CreatedAt not populated: %+v", entries[0])
	}
	if len(entries[1].Files) != 1 || entries[1].Files[0] != "main.go" {
		t.Fatalf("affected files wrong: %+v", entries[1].Files)
	}
}

func TestRingCap(t *testing.T) {
	s := New(3, "")
	for i := 0; i < 10; i++ {
		s.RecordProposed("p", "f.go", "d", "r")
	}
	if got := len(s.List()); got != 3 {
		t.Fatalf("ring cap not enforced: got %d, want 3", got)
	}
}

func TestPipelineSummaryAndVerify(t *testing.T) {
	s := New(10, "")
	s.RecordPipeline("error:svc", api.PipelineResult{
		Success: true, Files: []string{"main.go"}, Verify: "500 -> 400",
		Steps: []api.Step{{Stage: "apply", Status: "ok", Message: "applied"}},
	})
	e := s.List()[0]
	if e.Kind != "pipeline" || e.Status != "success" {
		t.Fatalf("pipeline entry wrong: %+v", e)
	}
	if e.Verify != "500 -> 400" || len(e.Steps) != 1 {
		t.Fatalf("verify/steps not carried: %+v", e)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New(200, dir)
	s.RecordProposed("error:svc", "main.go", "diff", "fix")

	// Reload from the same dir; the jsonl should repopulate.
	s2 := New(200, dir)
	entries := s2.List()
	if len(entries) != 1 || entries[0].Kind != "proposed" {
		t.Fatalf("reload failed: %+v", entries)
	}
	if _, err := filepath.Abs(filepath.Join(dir, "history.jsonl")); err != nil {
		t.Fatal(err)
	}
}
