// Package lang centralizes every language-specific decision the remediation pipeline
// makes — which test/build/run commands to issue, how to recognize build vs assertion
// errors, how to tidy uploaded source, and the language hints injected into the agent
// prompts. The pipeline (democtl, cloudbuild) and the agent service each detect a
// Profile from their source root and re-detect when a Git source re-points it.
//
// The system was originally Go-only; Detect defaults to Go so existing Go deployments
// behave exactly as before, and Python is selected when a Python manifest is present.
package lang

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"golang.org/x/tools/imports"
)

// ID identifies a supported language.
type ID string

const (
	Go     ID = "go"
	Python ID = "python"
)

// Command is one executable invocation (name + args) run in a working directory.
type Command struct {
	Name string
	Args []string
}

// Profile captures the language-specific behavior of the pipeline. The zero value is
// the Go profile (backward compatible); construct one with Detect.
type Profile struct{ id ID }

// ID returns the language identifier ("go" | "python").
func (p Profile) ID() ID {
	if p.id == "" {
		return Go
	}
	return p.id
}

func (p Profile) isPython() bool { return p.id == Python }

// Detect inspects root for a language manifest. A Python manifest (pyproject.toml,
// requirements.txt or setup.py) selects Python; anything else — including go.mod or no
// manifest at all — is treated as Go, so existing Go deployments are unaffected.
func Detect(root string) Profile {
	for _, name := range []string{"pyproject.toml", "requirements.txt", "setup.py"} {
		if fileExists(filepath.Join(root, name)) {
			return Profile{id: Python}
		}
	}
	return Profile{id: Go}
}

// CloudTestStep returns the Cloud Build test-step image, entrypoint and args that run
// the single regression test named runName as the in-build deploy gate.
func (p Profile) CloudTestStep(runName string) (image, entrypoint string, args []string) {
	if p.isPython() {
		// Install deps (when present) + pytest, then run the one selected test. runName is
		// constrained to [A-Za-z0-9_]+ by the builder, so it is shell-safe.
		script := "if [ -f requirements.txt ]; then pip install -q -r requirements.txt; fi; " +
			"pip install -q pytest; pytest -k " + runName
		return "python:3.12", "bash", []string{"-c", script}
	}
	return "golang:1.25", "go", []string{"test", "-run", runName, "./..."}
}

// BuildCommands returns the local build-gate commands, run in order in dir. For Go this
// is a single `go build`. For Python it creates/refreshes a venv, installs requirements
// (when present) plus pytest, then byte-compiles the sources as the error gate.
func (p Profile) BuildCommands(dir, binPath string) []Command {
	if p.isPython() {
		venv := filepath.Join(dir, ".venv")
		cmds := []Command{{Name: pythonExe(), Args: []string{"-m", "venv", venv}}}
		pip := venvTool(dir, "pip")
		if fileExists(filepath.Join(dir, "requirements.txt")) {
			cmds = append(cmds, Command{Name: pip, Args: []string{"install", "-r", "requirements.txt"}})
		}
		cmds = append(cmds, Command{Name: pip, Args: []string{"install", "pytest"}})
		cmds = append(cmds, Command{Name: venvTool(dir, "python"), Args: []string{"-m", "compileall", "."}})
		return cmds
	}
	return []Command{{Name: "go", Args: []string{"build", "-o", binPath, "."}}}
}

// TestCommands returns the commands that run the regression gate scoped to runFilter.
func (p Profile) TestCommands(dir, runFilter string) []Command {
	if p.isPython() {
		return []Command{{Name: p.pythonInterp(dir), Args: []string{"-m", "pytest", "-k", runFilter}}}
	}
	return []Command{{Name: "go", Args: []string{"test", "-run", runFilter, "./..."}}}
}

// VetCommands returns the static-check / compile gate used when no scoped test applies
// (Go: `go vet ./...`; Python: byte-compile every module).
func (p Profile) VetCommands(dir string) []Command {
	if p.isPython() {
		return []Command{{Name: p.pythonInterp(dir), Args: []string{"-m", "compileall", "."}}}
	}
	return []Command{{Name: "go", Args: []string{"vet", "./..."}}}
}

// RunCommand returns the command that launches the (built) service locally. For Go this
// is the prebuilt binary; for Python it runs the detected entry module via the venv
// interpreter.
func (p Profile) RunCommand(dir, binPath string) Command {
	if p.isPython() {
		return Command{Name: p.pythonInterp(dir), Args: []string{pythonEntrypoint(dir)}}
	}
	return Command{Name: binPath}
}

// ProducesBinary reports whether the build stage emits a runnable binary at binPath
// (Go). Python has no compiled binary, so the binary-existence check is skipped.
func (p Profile) ProducesBinary() bool { return !p.isPython() }

var (
	goTestFuncRE = regexp.MustCompile(`(?m)^func\s+(Test[A-Za-z0-9_]+)\s*\(`)
	pyTestFuncRE = regexp.MustCompile(`(?m)^\s*def\s+(test_[A-Za-z0-9_]+)\s*\(`)
)

// TestFuncRE matches a test function declaration so a generated file's test name can be
// recovered as the `go test -run` / `pytest -k` selector.
func (p Profile) TestFuncRE() *regexp.Regexp {
	if p.isPython() {
		return pyTestFuncRE
	}
	return goTestFuncRE
}

// Tidy repairs a single source file on the exact bytes uploaded to Cloud Build. For Go
// it removes now-unused imports and gofmt-formats (an LLM patch that drops the last use
// of an import otherwise fails the in-build `go test` with "imported and not used").
// Python has no such hard error, so it is returned unchanged. Best-effort: on a parse
// error the original bytes are returned.
func (p Profile) Tidy(rel string, content []byte) []byte {
	if p.isPython() || !strings.HasSuffix(rel, ".go") {
		return content
	}
	out, err := imports.Process(rel, content, &imports.Options{Comments: true, TabIndent: true, TabWidth: 8})
	if err != nil {
		return content
	}
	return out
}

// SkipSource reports whether a project-relative path is a build artifact or VCS data
// that must be excluded from the uploaded source.
func (p Profile) SkipSource(rel string) bool {
	rel = filepath.ToSlash(rel)
	switch {
	case strings.HasPrefix(rel, "web/node_modules/"),
		strings.HasPrefix(rel, "web/dist/"),
		strings.HasPrefix(rel, "web/.vite/"),
		strings.HasPrefix(rel, ".git/"),
		strings.HasPrefix(rel, "demo_app_run"),
		strings.HasSuffix(rel, ".test"):
		return true
	}
	if p.isPython() {
		switch {
		case strings.HasPrefix(rel, ".venv/"),
			strings.HasPrefix(rel, "venv/"),
			strings.HasPrefix(rel, ".pytest_cache/"),
			strings.HasPrefix(rel, ".mypy_cache/"),
			strings.Contains(rel, "__pycache__/"),
			strings.Contains(rel, ".egg-info/"),
			strings.HasSuffix(rel, ".pyc"):
			return true
		}
	}
	return false
}

// ErrorMarkers are the substrings that mark meaningful failure lines in a build log, so
// the UI surfaces the real error rather than the whole log.
func (p Profile) ErrorMarkers() []string {
	if p.isPython() {
		return []string{".py:", "Traceback", "Error", "ERROR", "FAILED", "FAIL", "SyntaxError",
			"IndentationError", "ModuleNotFoundError", "ImportError", "NameError", "TypeError",
			"AttributeError", "AssertionError", "assert", "exit status", "non-zero status"}
	}
	return []string{".go:", "error:", "Error", "ERROR", "FAIL", "undefined:", "cannot ", "not used",
		"expected ", "panic:", "exit status", "non-zero status"}
}

// BuildErrorMarkers distinguishes a generated test that does not COMPILE (worth one
// agent repair turn) from one that compiles but FAILS its assertion (a real gate fail).
func (p Profile) BuildErrorMarkers() []string {
	if p.isPython() {
		return []string{"SyntaxError", "IndentationError", "TabError", "ModuleNotFoundError",
			"ImportError", "NameError", "errors during collection", "collection error"}
	}
	return []string{"build failed", "[build failed]", "cannot use", "undefined:", "syntax error",
		"expected ", "imported and not used", "redeclared", "does not implement"}
}

// BuilderRole names the language for the builder agent's "You are a <X> test engineer".
func (p Profile) BuilderRole() string {
	if p.isPython() {
		return "Python"
	}
	return "Go"
}

// TestFileGuidance tells the builder how to WRITE a new regression test for this language.
func (p Profile) TestFileGuidance() string {
	if p.isPython() {
		return `call write_artifact(file="test_<name>.py", content=<full Python file>) next to the fixed ` +
			`module, using pytest. Invoke the handler/function directly (or via the framework's test client, ` +
			`e.g. Flask app.test_client() / FastAPI TestClient) and assert the fixed behavior. Name the ` +
			`function test_<something> so it can be selected with pytest -k, and mirror the style of any ` +
			`existing test_*.py files`
	}
	return `call write_artifact(file="<name>_test.go", content=<full Go file>) in the same package ` +
		`(package main), using net/http/httptest to invoke the handler/function directly and assert the ` +
		`fixed behavior (e.g. an out-of-range request returns 400 not 500; a slow op now responds under a ` +
		`threshold). Keep it deterministic and fast, and mirror the style of the existing *_test.go files`
}

// ManifestHint lists the files the builder should read_source first when generating
// build artifacts.
func (p Profile) ManifestHint() string {
	if p.isPython() {
		return "requirements.txt (or pyproject.toml) and the app's entry module"
	}
	return "go.mod, main.go, and web/package.json"
}

// DockerfileGuidance describes the Dockerfile the builder should generate.
func (p Profile) DockerfileGuidance() string {
	if p.isPython() {
		return "Generate a Dockerfile for this Python service: FROM python:3.12-slim, copy the source, " +
			"pip install -r requirements.txt (when present), and run the service listening on :9090 using " +
			"the project's entry module / framework runner. read_source " + p.ManifestHint() + " first. Call " +
			"write_artifact(file=\"Dockerfile\", content=…). Then reply: done."
	}
	return "Generate a multi-stage Dockerfile for this Go service that ALSO builds its React/TS storefront " +
		"under web/ (npm ci && npm run build → web/dist), bakes web/dist into the final image, builds the Go " +
		"binary, and runs it listening on :9090. read_source " + p.ManifestHint() + " first. Call " +
		"write_artifact(file=\"Dockerfile\", content=…). Then reply: done."
}

// BuildScriptGuidance describes the build script(s) the builder should generate.
func (p Profile) BuildScriptGuidance() string {
	if p.isPython() {
		return "Generate a build script for this Python service that creates a virtualenv, installs " +
			"dependencies (pip install -r requirements.txt when present) and byte-compiles the sources " +
			"(python -m compileall .). read_source " + p.ManifestHint() + " first. Provide BOTH build.ps1 " +
			"(PowerShell) and build.sh (bash) via two write_artifact calls. Then reply: done."
	}
	return "Generate a build script for this Go service that first builds the React/TS storefront under web/ " +
		"(npm install && npm run build) and then the Go binary with `go build -o demo_app_run<ext> .` in the " +
		"module root. read_source " + p.ManifestHint() + " first. Provide BOTH build.ps1 (PowerShell) and " +
		"build.sh (bash) via two write_artifact calls. Then reply: done."
}

// OTelGuidance describes the OpenTelemetry instrumentation conventions for this language,
// injected into the instrumenter agent's scan/apply prompts.
func (p Profile) OTelGuidance() string {
	if p.isPython() {
		return "Follow the OpenTelemetry (Python) conventions:\n" +
			"- Module tracer:    tracer = trace.get_tracer(__name__)\n" +
			"- Span per operation/handler:  with tracer.start_as_current_span(\"GET /path\") as span: ...\n" +
			"- Error recording:  span.record_exception(exc); span.set_status(Status(StatusCode.ERROR, str(exc)))\n" +
			"- Useful attributes:  span.set_attribute(\"key\", value)\n" +
			"- Bootstrap (only if missing): a TracerProvider with an OTLP/HTTP exporter " +
			"(opentelemetry-exporter-otlp) wired from OTEL_* env vars; prefer framework " +
			"auto-instrumentation (opentelemetry-instrumentation-flask/fastapi) where it exists.\n" +
			"Add any imports you introduce (from opentelemetry import trace; " +
			"from opentelemetry.trace import Status, StatusCode; …). Do NOT change code that is " +
			"already instrumented."
	}
	return "Follow the OpenTelemetry conventions already used in the codebase:\n" +
		"- Package tracer:    var tracer = otel.Tracer(\"<service>\")\n" +
		"- Span per operation/handler:  ctx, span := tracer.Start(r.Context(), \"GET /path\"); defer span.End()\n" +
		"- Error/panic recording:  span.RecordError(err, trace.WithStackTrace(true)); span.SetStatus(codes.Error, err.Error())\n" +
		"- Useful attributes:  span.SetAttributes(attribute.Int(...), attribute.String(...))\n" +
		"- Bootstrap (only if missing): an OTLP/HTTP exporter wired from OTEL_* env vars.\n" +
		"Add any imports you introduce. Do NOT change code that is already instrumented."
}

// UnusedImportHint is the patch-time reminder to keep the file compilable. It is empty
// for languages where an unused import is not a hard build error (Python).
func (p Profile) UnusedImportHint() string {
	if p.isPython() {
		return ""
	}
	return ` EXCEPT that the patched file MUST still compile — if your change removes the ` +
		`last use of an imported package, also remove that import so there is no "imported and not ` +
		`used" error`
}

// pythonInterp returns the venv interpreter when the build created one, else the system
// Python (so test/run still work if the venv is absent).
func (p Profile) pythonInterp(dir string) string {
	if py := venvTool(dir, "python"); fileExists(py) {
		return py
	}
	return pythonExe()
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func pythonExe() string {
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}

// venvTool returns the path to a tool inside dir/.venv (Scripts on Windows, bin elsewhere).
func venvTool(dir, tool string) string {
	sub := "bin"
	if runtime.GOOS == "windows" {
		sub = "Scripts"
	}
	return filepath.Join(dir, ".venv", sub, tool+exeSuffix())
}

// pythonEntrypoint picks the module to run for a local launch, preferring common names.
func pythonEntrypoint(dir string) string {
	for _, n := range []string{"main.py", "app.py", "wsgi.py", "asgi.py", "manage.py", "server.py", "__main__.py"} {
		if fileExists(filepath.Join(dir, n)) {
			return n
		}
	}
	return "main.py"
}
