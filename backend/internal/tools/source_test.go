package tools

import (
	"os"
	"path/filepath"
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
	got, err := s.ReadSource("main.go")
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
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
