package versions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/tools"
)

func patch(file, content string) tools.PatchProposal {
	return tools.PatchProposal{File: file, PatchedContent: content, UnifiedDiff: "--- " + file, Rationale: "fix " + file}
}

func ok() api.PipelineResult { return api.PipelineResult{Success: true, Verify: "healthz 200"} }

func filesOf(v Version) []string {
	out := make([]string, 0, len(v.Patches))
	for _, p := range v.Patches {
		out = append(out, p.File)
	}
	return out
}

// Each deploy's version carries the CUMULATIVE overlay, not just that run's patches.
func TestRecordDeployAccumulates(t *testing.T) {
	s := New("", 0, false)
	v1 := s.RecordDeploy("manual", []string{"error:a"}, []string{"error"}, []tools.PatchProposal{patch("a.go", "a1")}, ok())
	v2 := s.RecordDeploy("autopilot", []string{"error:b"}, []string{"error"}, []tools.PatchProposal{patch("b.go", "b1")}, ok())

	if v1.Seq != 1 || v2.Seq != 2 {
		t.Fatalf("seq should increment: %d, %d", v1.Seq, v2.Seq)
	}
	full, _ := s.Get(v2.ID)
	if len(full.Patches) != 2 {
		t.Fatalf("v2 should carry cumulative {a.go,b.go}, got %v", filesOf(full))
	}
	// Re-patching the same file replaces, not duplicates.
	v3 := s.RecordDeploy("manual", []string{"error:a"}, []string{"error"}, []tools.PatchProposal{patch("a.go", "a2")}, ok())
	full3, _ := s.Get(v3.ID)
	if len(full3.Patches) != 2 {
		t.Fatalf("same-file patch should replace in cumulative, got %v", filesOf(full3))
	}
	for _, p := range full3.Patches {
		if p.File == "a.go" && p.PatchedContent != "a2" {
			t.Errorf("cumulative should carry the latest content for a.go, got %q", p.PatchedContent)
		}
	}
}

// Revert RESETS the cumulative overlay to the target (no merge): files added after
// the target become pristine again and don't reappear in later versions.
func TestRecordRevertResetsCumulative(t *testing.T) {
	s := New("", 0, false)
	v1 := s.RecordDeploy("manual", []string{"error:a"}, []string{"error"}, []tools.PatchProposal{patch("a.go", "a1")}, ok())
	s.RecordDeploy("manual", []string{"error:b"}, []string{"error"}, []tools.PatchProposal{patch("a.go", "a2"), patch("b.go", "b1")}, ok())

	target, okGet := s.Get(v1.ID)
	if !okGet {
		t.Fatal("v1 should exist")
	}
	rv := s.RecordRevert(target, ok())
	if rv.RevertOf != v1.ID || rv.Source != "revert" || rv.Seq != 3 {
		t.Fatalf("revert version malformed: %+v", rv)
	}
	full, _ := s.Get(rv.ID)
	if len(full.Patches) != 1 || full.Patches[0].File != "a.go" || full.Patches[0].PatchedContent != "a1" {
		t.Fatalf("revert should snapshot exactly the target state, got %v", filesOf(full))
	}
	// A deploy after the revert builds on the reverted state — b.go must NOT reappear.
	v4 := s.RecordDeploy("manual", []string{"error:c"}, []string{"error"}, []tools.PatchProposal{patch("c.go", "c1")}, ok())
	full4, _ := s.Get(v4.ID)
	got := map[string]bool{}
	for _, p := range full4.Patches {
		got[p.File] = true
	}
	if !got["a.go"] || !got["c.go"] || got["b.go"] || len(full4.Patches) != 2 {
		t.Fatalf("post-revert deploy should be {a.go,c.go} without b.go, got %v", filesOf(full4))
	}
}

// Retention prunes the oldest versions and deletes their files.
func TestPrune(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, 2, false)
	v1 := s.RecordDeploy("manual", []string{"p"}, []string{"error"}, []tools.PatchProposal{patch("a.go", "1")}, ok())
	s.RecordDeploy("manual", []string{"p"}, []string{"error"}, []tools.PatchProposal{patch("a.go", "2")}, ok())
	s.RecordDeploy("manual", []string{"p"}, []string{"error"}, []tools.PatchProposal{patch("a.go", "3")}, ok())

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("retention 2 should keep 2 versions, got %d", len(list))
	}
	if list[0].Seq != 3 || list[1].Seq != 2 {
		t.Fatalf("should keep newest, got seq %d,%d", list[0].Seq, list[1].Seq)
	}
	if _, ok := s.Get(v1.ID); ok {
		t.Error("pruned version should be gone from the map")
	}
	if _, err := os.Stat(filepath.Join(dir, "versions", v1.ID+".json")); !os.IsNotExist(err) {
		t.Error("pruned version file should be deleted")
	}
}

// A reloaded store restores the list, resumes seq, and rebuilds the cumulative
// overlay from the newest version.
func TestReload(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, 0, false)
	s.RecordDeploy("manual", []string{"p1"}, []string{"error"}, []tools.PatchProposal{patch("a.go", "a1")}, ok())
	s.RecordDeploy("autopilot", []string{"p2"}, []string{"performance"}, []tools.PatchProposal{patch("b.go", "b1")}, ok())

	r := New(dir, 0, false)
	list := r.List()
	if len(list) != 2 || list[0].Seq != 2 {
		t.Fatalf("reload should restore both versions newest-first, got %+v", list)
	}
	// Seq resumes — no collision with persisted versions.
	v3 := r.RecordDeploy("manual", []string{"p3"}, []string{"error"}, []tools.PatchProposal{patch("c.go", "c1")}, ok())
	if v3.Seq != 3 {
		t.Fatalf("seq should resume at 3, got %d", v3.Seq)
	}
	// Cumulative was rebuilt from the newest version: v3 carries a, b and c.
	full, _ := r.Get(v3.ID)
	if len(full.Patches) != 3 {
		t.Fatalf("reloaded cumulative should carry {a,b,c}, got %v", filesOf(full))
	}

	// clearOnStart wipes persisted versions.
	c := New(dir, 0, true)
	if len(c.List()) != 0 {
		t.Error("clearOnStart should start with no versions")
	}
}

// Detail exposes diffs/rationale but never the full patched content; List stays lean.
func TestDetailAndListShapes(t *testing.T) {
	s := New("", 0, false)
	v := s.RecordDeploy("manual", []string{"p"}, []string{"error"}, []tools.PatchProposal{patch("a.go", "SECRET-CONTENT")}, ok())

	if got := s.List(); len(got[0].Patches) != 0 {
		t.Error("List should not carry patches")
	}
	d, okD := s.Detail(v.ID)
	if !okD || len(d.Patches) != 1 {
		t.Fatalf("Detail should carry display patches: %+v", d)
	}
	if d.Patches[0].UnifiedDiff == "" || d.Patches[0].File != "a.go" {
		t.Errorf("Detail patch should carry file+diff: %+v", d.Patches[0])
	}
	if _, missing := s.Detail("nope"); missing {
		t.Error("unknown id should report not-found")
	}
}
