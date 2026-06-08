package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		name     string
		manifest string // file to drop in the temp dir ("" => none)
		want     ID
	}{
		{"empty defaults to go", "", Go},
		{"go.mod is go", "go.mod", Go},
		{"requirements.txt is python", "requirements.txt", Python},
		{"pyproject.toml is python", "pyproject.toml", Python},
		{"setup.py is python", "setup.py", Python},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.manifest), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := Detect(dir).ID(); got != tc.want {
				t.Fatalf("Detect(%s)=%q, want %q", tc.manifest, got, tc.want)
			}
		})
	}
}

func TestDetectPythonWinsOverGo(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"go.mod", "requirements.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := Detect(dir).ID(); got != Python {
		t.Fatalf("a repo with both manifests should detect Python, got %q", got)
	}
}

func TestCloudTestStep(t *testing.T) {
	goImg, goEntry, goArgs := Profile{id: Go}.CloudTestStep("TestFoo")
	if goImg != "golang:1.25" || goEntry != "go" {
		t.Fatalf("go test step = %q/%q, want golang:1.25/go", goImg, goEntry)
	}
	if strings.Join(goArgs, " ") != "test -run TestFoo ./..." {
		t.Fatalf("go test args = %v", goArgs)
	}

	pyImg, pyEntry, pyArgs := Profile{id: Python}.CloudTestStep("test_foo")
	if pyImg != "python:3.12" || pyEntry != "bash" {
		t.Fatalf("python test step = %q/%q, want python:3.12/bash", pyImg, pyEntry)
	}
	joined := strings.Join(pyArgs, " ")
	if !strings.Contains(joined, "pytest -k test_foo") {
		t.Fatalf("python test args missing pytest selector: %v", pyArgs)
	}
	if !strings.Contains(joined, "pip install") {
		t.Fatalf("python test step should install deps: %v", pyArgs)
	}
}

func TestBuildCommands(t *testing.T) {
	goCmds := Profile{id: Go}.BuildCommands("/src", "/src/bin")
	if len(goCmds) != 1 || goCmds[0].Name != "go" || strings.Join(goCmds[0].Args, " ") != "build -o /src/bin ." {
		t.Fatalf("go build commands = %+v", goCmds)
	}

	// Python build in a dir with requirements.txt: venv -> pip install -r -> pip install pytest -> compileall.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("flask"), 0o644); err != nil {
		t.Fatal(err)
	}
	pyCmds := Profile{id: Python}.BuildCommands(dir, "")
	if len(pyCmds) != 4 {
		t.Fatalf("python build with requirements should be 4 steps, got %d: %+v", len(pyCmds), pyCmds)
	}
	if !strings.Contains(strings.Join(pyCmds[0].Args, " "), "venv") {
		t.Fatalf("first python build step should create a venv: %+v", pyCmds[0])
	}
	if got := strings.Join(pyCmds[len(pyCmds)-1].Args, " "); !strings.Contains(got, "compileall") {
		t.Fatalf("last python build step should compileall: %q", got)
	}

	// Without requirements.txt the pip-install-r step is dropped (3 steps).
	pyCmds = Profile{id: Python}.BuildCommands(t.TempDir(), "")
	if len(pyCmds) != 3 {
		t.Fatalf("python build without requirements should be 3 steps, got %d: %+v", len(pyCmds), pyCmds)
	}
}

func TestTestCommands(t *testing.T) {
	goCmds := Profile{id: Go}.TestCommands("/src", "TestFoo")
	if len(goCmds) != 1 || strings.Join(goCmds[0].Args, " ") != "test -run TestFoo ./..." {
		t.Fatalf("go test commands = %+v", goCmds)
	}
	pyCmds := Profile{id: Python}.TestCommands(t.TempDir(), "test_foo")
	if len(pyCmds) != 1 || strings.Join(pyCmds[0].Args, " ") != "-m pytest -k test_foo" {
		t.Fatalf("python test commands = %+v", pyCmds)
	}
}

func TestProfileFlags(t *testing.T) {
	if !(Profile{id: Go}).ProducesBinary() {
		t.Fatal("go should produce a binary")
	}
	if (Profile{id: Python}).ProducesBinary() {
		t.Fatal("python should not produce a binary")
	}
	if (Profile{}).ID() != Go {
		t.Fatal("zero-value Profile should be Go")
	}
}

func TestTestFuncRE(t *testing.T) {
	if m := (Profile{id: Go}).TestFuncRE().FindStringSubmatch("func TestThing(t *testing.T) {"); m == nil || m[1] != "TestThing" {
		t.Fatalf("go test func regex failed: %v", m)
	}
	if m := (Profile{id: Python}).TestFuncRE().FindStringSubmatch("    def test_thing():"); m == nil || m[1] != "test_thing" {
		t.Fatalf("python test func regex failed: %v", m)
	}
}

// TestTidyDropsUnusedGoImport guards the Cloud Build "imported and not used" regression:
// a perf-fix patch that removes the only time.Sleep but leaves "time" imported otherwise
// fails the in-build `go test`. Tidy must drop the orphaned import for the Go profile.
func TestTidyDropsUnusedGoImport(t *testing.T) {
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
	out := string(Profile{id: Go}.Tidy("main.go", src))
	if strings.Contains(out, `"time"`) {
		t.Errorf("unused \"time\" import should have been removed:\n%s", out)
	}
	if !strings.Contains(out, `"fmt"`) {
		t.Errorf("used \"fmt\" import must be kept:\n%s", out)
	}
}

// TestTidyIgnoresNonGoAndPython leaves non-Go files, unparseable Go, and all Python
// source untouched (Python has no "imported and not used" build error to repair).
func TestTidyIgnoresNonGoAndPython(t *testing.T) {
	raw := []byte("FROM golang:1.25\n# not go source\n")
	if got := (Profile{id: Go}).Tidy("Dockerfile", raw); string(got) != string(raw) {
		t.Errorf("non-Go file must pass through unchanged, got: %s", got)
	}
	bad := []byte("package main\nfunc (")
	if got := (Profile{id: Go}).Tidy("broken.go", bad); string(got) != string(bad) {
		t.Errorf("unparseable Go must pass through unchanged, got: %s", got)
	}
	py := []byte("import os\nimport sys\n\nprint('hi')\n")
	if got := (Profile{id: Python}).Tidy("main.py", py); string(got) != string(py) {
		t.Errorf("python source must pass through unchanged, got: %s", got)
	}
}

func TestOTelGuidance(t *testing.T) {
	goG := Profile{id: Go}.OTelGuidance()
	if !strings.Contains(goG, "otel.Tracer") || !strings.Contains(goG, "span.RecordError") {
		t.Fatalf("go OTel guidance missing Go conventions:\n%s", goG)
	}
	pyG := Profile{id: Python}.OTelGuidance()
	if !strings.Contains(pyG, "start_as_current_span") || !strings.Contains(pyG, "record_exception") {
		t.Fatalf("python OTel guidance missing Python conventions:\n%s", pyG)
	}
	if goG == pyG {
		t.Fatal("Go and Python OTel guidance must differ")
	}
}

func TestUnusedImportHint(t *testing.T) {
	if (Profile{id: Go}).UnusedImportHint() == "" {
		t.Fatal("go should carry the unused-import compile hint")
	}
	if (Profile{id: Python}).UnusedImportHint() != "" {
		t.Fatal("python should not carry the unused-import hint")
	}
}
