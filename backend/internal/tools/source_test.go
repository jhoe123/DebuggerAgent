package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSource(t *testing.T) {
	root := t.TempDir()
	want := "package main\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSandbox(root)
	if err != nil {
		t.Fatal(err)
	}
	// Small file, no options: whole file (back-compat), trailing newline preserved.
	got, err := s.ReadSource("main.go", ReadOptions{})
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	if got.Content != want {
		t.Fatalf("got %q, want %q", got.Content, want)
	}
	if got.TotalLines != 1 || got.Truncated {
		t.Fatalf("got TotalLines=%d Truncated=%v, want 1/false", got.TotalLines, got.Truncated)
	}
	if full, err := s.ReadFull("main.go"); err != nil || full != want {
		t.Fatalf("ReadFull = %q, %v", full, err)
	}
}

func writeLines(t *testing.T, n int) *Sandbox {
	t.Helper()
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "big.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSandbox(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReadSourceRange(t *testing.T) {
	s := writeLines(t, 1000)
	r, err := s.ReadSource("big.go", ReadOptions{Offset: 100, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if r.StartLine != 100 || r.EndLine != 102 {
		t.Fatalf("range = %d-%d, want 100-102", r.StartLine, r.EndLine)
	}
	if r.Content != "line 100\nline 101\nline 102" {
		t.Fatalf("content = %q", r.Content)
	}
	if r.TotalLines != 1000 || !r.Truncated {
		t.Fatalf("TotalLines=%d Truncated=%v, want 1000/true", r.TotalLines, r.Truncated)
	}
}

func TestReadSourceSearch(t *testing.T) {
	s := writeLines(t, 1000)
	r, err := s.ReadSource("big.go", ReadOptions{Query: "line 500"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Matches) != 1 || r.Matches[0].Line != 500 {
		t.Fatalf("matches = %+v", r.Matches)
	}
	if !strings.Contains(r.Content, "line 500") || !strings.Contains(r.Content, "line 495") {
		t.Fatalf("search content missing context: %q", r.Content)
	}
}

func TestReadSourceGuardTruncates(t *testing.T) {
	s := writeLines(t, MaxUnrangedLines+50)
	r, err := s.ReadSource("big.go", ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated {
		t.Fatalf("expected Truncated for file > %d lines", MaxUnrangedLines)
	}
	if r.EndLine != MaxUnrangedLines {
		t.Fatalf("EndLine = %d, want %d", r.EndLine, MaxUnrangedLines)
	}
	if !strings.Contains(r.Content, "use offset/limit or query") {
		t.Fatalf("expected steer note in content")
	}
}

func TestResolveRejectsTraversal(t *testing.T) {
	s, err := NewSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../secret", "../../etc/passwd", "sub/../../escape"} {
		if _, err := s.Resolve(bad); err == nil {
			t.Errorf("Resolve(%q) = nil error, want escape error", bad)
		}
	}
}

func TestResolveAllowsNested(t *testing.T) {
	s, err := NewSandbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve("pkg/sub/file.go"); err != nil {
		t.Errorf("Resolve nested path: unexpected error %v", err)
	}
}
