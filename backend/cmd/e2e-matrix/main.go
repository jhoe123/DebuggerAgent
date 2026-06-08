// Command e2e-matrix is a THROWAWAY test harness (not committed) that exercises
// PatchPilot's repo-agnostic pipeline against several real Go HTTP-service repos:
//
//	clone → instrument (real instrumenter agent) → patch (real propose_patch) →
//	test+build+deploy (real pipeline.CloudRunner → Cloud Build → Cloud Run)
//
// The builder agent's test/Dockerfile generation is hardcoded for demo_app, so this
// harness substitutes a tailored per-repo Dockerfile + a minimal valid go-test gate
// (via a custom genTest). All cloud scratch (bucket / AR repo / Cloud Run services) is
// isolated under a ppe2e namespace and cleaned up by scripts outside this program.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/patchpilot/backend/internal/agent"
	"github.com/patchpilot/backend/internal/api"
	"github.com/patchpilot/backend/internal/democtl"
	"github.com/patchpilot/backend/internal/pipeline"
	"github.com/patchpilot/backend/internal/tools"
)

type repoSpec struct {
	name       string
	url        string
	dockerfile string
	extra      map[string]string // extra files written into the clone root (Caddyfile, configs)
}

const (
	scratchBucket = "emogent-demo-2026_ppe2e"
	scratchARRepo = "ppe2e"
	region        = "us-central1"
	project       = "emogent-demo-2026"
)

func repos() []repoSpec {
	alpine := "FROM alpine:3.20\nRUN apk add --no-cache ca-certificates\n"
	return []repoSpec{
		{
			name: "caddy",
			url:  "https://github.com/caddyserver/caddy.git",
			dockerfile: "FROM golang:1.25 AS build\nWORKDIR /src\nCOPY . .\n" +
				"RUN CGO_ENABLED=0 go build -o /out/caddy ./cmd/caddy\n" +
				alpine +
				"COPY --from=build /out/caddy /usr/bin/caddy\nCOPY Caddyfile /Caddyfile\n" +
				"ENV XDG_DATA_HOME=/tmp XDG_CONFIG_HOME=/tmp\nEXPOSE 8080\n" +
				"ENTRYPOINT [\"caddy\"]\nCMD [\"run\",\"--config\",\"/Caddyfile\",\"--adapter\",\"caddyfile\"]\n",
			extra: map[string]string{"Caddyfile": ":8080 {\n\trespond \"OK - PatchPilot e2e (caddy)\"\n}\n"},
		},
		{
			name: "nats-server",
			url:  "https://github.com/nats-io/nats-server.git",
			dockerfile: "FROM golang:1.25 AS build\nWORKDIR /src\nCOPY . .\n" +
				"RUN CGO_ENABLED=0 go build -o /out/nats-server .\n" +
				alpine +
				"COPY --from=build /out/nats-server /usr/bin/nats-server\nEXPOSE 8080\n" +
				"ENTRYPOINT [\"nats-server\"]\nCMD [\"-m\",\"8080\",\"-p\",\"4222\",\"-a\",\"0.0.0.0\"]\n",
		},
		{
			name: "frp",
			url:  "https://github.com/fatedier/frp.git",
			dockerfile: "FROM golang:1.25 AS build\nWORKDIR /src\nCOPY . .\n" +
				"RUN CGO_ENABLED=0 go build -o /out/frps ./cmd/frps\n" +
				alpine +
				"COPY --from=build /out/frps /usr/bin/frps\nCOPY frps.toml /frps.toml\nEXPOSE 8080\n" +
				"ENTRYPOINT [\"frps\"]\nCMD [\"-c\",\"/frps.toml\"]\n",
			extra: map[string]string{"frps.toml": "bindPort = 7000\nwebServer.addr = \"0.0.0.0\"\nwebServer.port = 8080\n"},
		},
		{
			name: "minio",
			url:  "https://github.com/minio/minio.git",
			dockerfile: "FROM golang:1.25 AS build\nWORKDIR /src\nCOPY . .\n" +
				"RUN CGO_ENABLED=0 go build -o /out/minio .\n" +
				alpine +
				"RUN mkdir -p /tmp/data\nCOPY --from=build /out/minio /usr/bin/minio\n" +
				"ENV MINIO_ROOT_USER=minioadmin MINIO_ROOT_PASSWORD=minioadmin\nEXPOSE 8080\n" +
				"ENTRYPOINT [\"minio\"]\nCMD [\"server\",\"/tmp/data\",\"--address\",\":8080\"]\n",
		},
		{
			name: "prometheus",
			url:  "https://github.com/prometheus/prometheus.git",
			dockerfile: "FROM golang:1.25 AS build\nWORKDIR /src\nCOPY . .\n" +
				"RUN CGO_ENABLED=0 go build -o /out/prometheus ./cmd/prometheus\n" +
				alpine +
				"RUN mkdir -p /tmp/tsdb\nCOPY --from=build /out/prometheus /usr/bin/prometheus\n" +
				"COPY prometheus.yml /etc/prometheus/prometheus.yml\nEXPOSE 8080\n" +
				"ENTRYPOINT [\"prometheus\"]\n" +
				"CMD [\"--config.file=/etc/prometheus/prometheus.yml\",\"--web.listen-address=:8080\",\"--storage.tsdb.path=/tmp/tsdb\"]\n",
			extra: map[string]string{"prometheus.yml": "global:\n  scrape_interval: 15s\nscrape_configs: []\n"},
		},
		{
			name: "opa",
			url:  "https://github.com/open-policy-agent/opa.git",
			dockerfile: "FROM golang:1.25 AS build\nWORKDIR /src\nCOPY . .\n" +
				"RUN CGO_ENABLED=0 go build -o /out/opa .\n" +
				alpine +
				"COPY --from=build /out/opa /usr/bin/opa\nEXPOSE 8080\n" +
				"ENTRYPOINT [\"opa\"]\nCMD [\"run\",\"-s\",\"-a\",\"0.0.0.0:8080\"]\n",
		},
		{
			name: "distribution",
			url:  "https://github.com/distribution/distribution.git",
			dockerfile: "FROM golang:1.25 AS build\nWORKDIR /src\nCOPY . .\n" +
				"RUN CGO_ENABLED=0 go build -o /out/registry ./cmd/registry\n" +
				alpine +
				"RUN mkdir -p /tmp/registry\nCOPY --from=build /out/registry /usr/bin/registry\n" +
				"COPY config.yml /config.yml\nEXPOSE 8080\n" +
				"ENTRYPOINT [\"registry\"]\nCMD [\"serve\",\"/config.yml\"]\n",
			extra: map[string]string{"config.yml": "version: 0.1\nstorage:\n  filesystem:\n    rootdirectory: /tmp/registry\nhttp:\n  addr: :8080\n"},
		},
	}
}

var (
	pkgRE = regexp.MustCompile(`(?m)^package\s+(\w+)`)

	// set per-repo before Remediate; read by the custom genTest closure.
	curTestDir string
	curTestPkg string
)

type stageResult struct {
	stage  string
	ok     bool
	detail string
}

func main() {
	repoFlag := flag.String("repo", "", "run only this repo (by name); empty = all")
	deploy := flag.Bool("deploy", true, "run the cloud build/deploy stage")
	instr := flag.Bool("instr", true, "run the instrumentation stage")
	prep := flag.Bool("prep", true, "run the agent prep stages (instrument+patch); false reuses an already-prepared clone and only deploys")
	workdir := flag.String("workdir", filepath.Join(os.TempDir(), "ppe2e"), "clone workdir")
	flag.Parse()

	ctx := context.Background()
	cfg := agent.LoadConfig()
	fmt.Printf("config: project=%s model=%s sourceRoot=%s\n", cfg.GCPProject, cfg.GeminiModel, cfg.SourceRoot)

	svc, err := agent.New(ctx, cfg)
	if err != nil {
		fatal("build agent: %v", err)
	}

	// Custom genTest: a minimal, guaranteed-valid regression test in the edited file's
	// package. Substitutes the demo_app-hardcoded builder test gen so the in-build
	// `go test -run TestPPE2ESmoke ./...` still compiles the WHOLE repo (the real gate).
	genTest := func(ctx context.Context, file, rationale, errOut string) (string, map[string]string, error) {
		pkg := curTestPkg
		if pkg == "" {
			pkg = "main"
		}
		rel := filepath.ToSlash(filepath.Join(curTestDir, "pp_e2e_smoke_test.go"))
		content := fmt.Sprintf("package %s\n\nimport \"testing\"\n\n"+
			"// TestPPE2ESmoke is PatchPilot's e2e deploy gate: the whole module must\n"+
			"// compile and this test must pass before the image is built and deployed.\nfunc TestPPE2ESmoke(t *testing.T) {}\n", pkg)
		return "TestPPE2ESmoke", map[string]string{rel: content}, nil
	}
	genBuild := func(ctx context.Context, kind, errOut string) (map[string]string, error) {
		return nil, fmt.Errorf("genBuild called unexpectedly (kind=%s) — a tailored Dockerfile should already exist", kind)
	}

	runner, err := pipeline.New(ctx, pipeline.Config{
		Project: project, Region: region, Bucket: scratchBucket, ARRepo: scratchARRepo,
		Service: "pp-e2e", SourceRoot: cfg.SourceRoot,
	}, svc.Patches(), genTest, genBuild)
	if err != nil {
		fatal("build cloud runner: %v", err)
	}
	defer runner.Close()

	if err := os.MkdirAll(*workdir, 0o755); err != nil {
		fatal("mkdir workdir: %v", err)
	}

	all := repos()
	results := map[string][]stageResult{}
	for _, rs := range all {
		if *repoFlag != "" && rs.name != *repoFlag {
			continue
		}
		fmt.Printf("\n======== %s ========\n", rs.name)
		results[rs.name] = runRepo(ctx, svc, runner, rs, *workdir, *prep, *instr, *deploy)
	}

	fmt.Printf("\n\n================ SUMMARY ================\n")
	names := make([]string, 0, len(results))
	for n := range results {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("%s:\n", n)
		for _, r := range results[n] {
			mark := "FAIL"
			if r.ok {
				mark = "ok"
			}
			fmt.Printf("  [%-4s] %-12s %s\n", mark, r.stage, r.detail)
		}
	}
}

func runRepo(ctx context.Context, svc *agent.Service, runner *pipeline.CloudRunner, rs repoSpec, workdir string, doPrep, doInstr, doDeploy bool) []stageResult {
	var res []stageResult
	add := func(stage string, ok bool, detail string) {
		res = append(res, stageResult{stage, ok, detail})
		mark := "FAIL"
		if ok {
			mark = "ok"
		}
		fmt.Printf(">>> [%s] %s: %s\n", mark, stage, detail)
	}

	clone := filepath.Join(workdir, rs.name)

	// --- clone ---
	if _, err := os.Stat(clone); err == nil {
		fmt.Printf("    reusing existing clone %s\n", clone)
	} else {
		fmt.Printf("    cloning %s …\n", rs.url)
		if out, err := run(ctx, workdir, 10*time.Minute, "git", "clone", "--depth", "1", rs.url, rs.name); err != nil {
			add("clone", false, trim(out))
			return res
		}
	}
	add("clone", true, clone)

	if err := svc.SetSourceRoot(clone); err != nil {
		add("setup", false, err.Error())
		return res
	}

	// edited holds file -> full content for the cloud overlay (also written to disk).
	edited := map[string]string{}
	var primaryFile string

	// --- instrument (real instrumenter agent) ---
	if doPrep && doInstr {
		sctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		scan, err := svc.ScanInstrumentation(sctx, "scan-"+rs.name, logStep)
		cancel()
		if err != nil {
			add("instrument-scan", false, err.Error())
		} else {
			n := len(scan.Candidates)
			take := n
			if take > 2 {
				take = 2 // tight budget: at most 2 candidates
			}
			add("instrument-scan", true, fmt.Sprintf("%d candidate(s); applying %d", n, take))
			if take > 0 {
				actx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				files, err := svc.ApplyInstrumentation(actx, "apply-"+rs.name, scan.Candidates[:take], "", logStep)
				cancel()
				if err != nil {
					add("instrument-apply", false, err.Error())
				} else {
					for f, c := range files {
						edited[f] = c
					}
					ok, detail := writeAndCompile(ctx, clone, files)
					if ok {
						add("instrument-apply", true, detail)
						if primaryFile == "" {
							for f := range files {
								primaryFile = f
								break
							}
						}
					} else {
						// instrumentation didn't compile — revert it so it can't break the deploy
						for f := range files {
							delete(edited, f)
							_, _ = run(ctx, clone, time.Minute, "git", "checkout", "--", f)
						}
						add("instrument-apply", false, "did not compile, reverted: "+detail)
					}
				}
			}
		}
	}

	// --- patch (real investigator propose_patch, Dynatrace-free) ---
	target := primaryFile
	if target == "" {
		target = pickGoFile(clone)
	}
	if !doPrep {
		add("patch", true, "skipped (-prep=false; reusing prepared clone on disk)")
	} else if target == "" {
		add("patch", false, "no suitable .go file found")
	} else {
		pctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		prompt := fmt.Sprintf("IMPORTANT: Do NOT use any Dynatrace tools (no execute_dql, no MCP). Work only from the source.\n"+
			"In the file %q, add a small, self-contained, exported function with NO new imports:\n"+
			"  func PatchPilotE2E() string { return \"patchpilot-e2e\" }\n"+
			"Read the file with read_source first, then call propose_patch with the FULL patched file content "+
			"(keep everything else byte-identical) and a short unified diff. Do not change anything else. "+
			"After proposing, reply with the JSON object.", target)
		_, patch, err := svc.Investigate(pctx, "patch-"+rs.name, prompt, logStep)
		cancel()
		if err != nil || patch == nil {
			detail := "no patch proposed"
			if err != nil {
				detail = err.Error()
			}
			add("patch", false, detail)
		} else {
			edited[patch.File] = patch.PatchedContent
			ok, detail := writeAndCompile(ctx, clone, map[string]string{patch.File: patch.PatchedContent})
			if ok {
				primaryFile = patch.File
				add("patch", true, "propose_patch → "+patch.File+"; "+detail)
			} else {
				delete(edited, patch.File)
				_, _ = run(ctx, clone, time.Minute, "git", "checkout", "--", patch.File)
				add("patch", false, "patch did not compile, reverted: "+detail)
			}
		}
	}

	// Write the tailored Dockerfile + extra config files into the clone so the pipeline
	// uses them (and they ride along in the full-source upload).
	_ = os.Remove(filepath.Join(clone, ".dockerignore"))
	if err := os.WriteFile(filepath.Join(clone, "Dockerfile"), []byte(rs.dockerfile), 0o644); err != nil {
		add("dockerfile", false, err.Error())
		return res
	}
	for name, content := range rs.extra {
		_ = os.WriteFile(filepath.Join(clone, name), []byte(content), 0o644)
	}
	add("dockerfile", true, "tailored Dockerfile + config written")

	if !doDeploy {
		add("deploy", true, "skipped (-deploy=false)")
		return res
	}
	// Determine the package/dir for the smoke test (+ any synthetic marker) from the
	// primary edited file; fall back to a discovered main.go (e.g. when -prep=false).
	if primaryFile == "" {
		primaryFile = pickGoFile(clone)
	}
	curTestDir = filepath.ToSlash(filepath.Dir(primaryFile))
	curTestPkg = packageOf(filepath.Join(clone, filepath.FromSlash(primaryFile)))
	if curTestPkg == "" {
		curTestPkg = "main"
	}

	if len(edited) == 0 {
		// Need at least one overlay file; synthesize a benign marker in the test package
		// (the prepared clone's real instrumentation/patch still ride along via ForceSync).
		marker := filepath.ToSlash(filepath.Join(curTestDir, "pp_e2e_marker.go"))
		edited[marker] = fmt.Sprintf("package %s\n\n// PatchPilotE2EMarker marks a synthetic e2e deploy.\nconst PatchPilotE2EMarker = \"ppe2e\"\n", curTestPkg)
		_ = os.WriteFile(filepath.Join(clone, filepath.FromSlash(marker)), []byte(edited[marker]), 0o644)
	}

	runner.SetSourceRoot(clone)
	var patches []tools.PatchProposal
	for f, c := range edited {
		patches = append(patches, tools.PatchProposal{File: f, PatchedContent: c, Rationale: "e2e mechanics validation"})
	}
	opts := democtl.Options{
		Apply: true, Test: true, Build: true, Deploy: true, ForceSync: true,
		Deployment: democtl.DeploymentSpec{Target: "cloud-run", Params: map[string]string{
			"project": project, "region": region, "service": "pp-e2e-" + rs.name,
			"sourceBucket": scratchBucket, "artifactRepo": scratchARRepo,
		}},
		Patches: patches,
	}
	dctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	fmt.Printf("    deploying %d file(s) → service pp-e2e-%s …\n", len(patches), rs.name)
	result := runner.Remediate(dctx, opts, func(s api.Step) {
		fmt.Printf("    {pipeline %s/%s} %s %s\n", s.Stage, s.Status, s.Message, oneline(s.Detail))
	})
	if result.Success {
		add("deploy", true, "Cloud Run pp-e2e-"+rs.name+" ("+result.Verify+")")
	} else {
		add("deploy", false, lastFail(result))
	}
	return res
}

// writeAndCompile writes files to disk under root and tries to compile their packages,
// running `go mod tidy` if the first build fails (instrumentation may add imports).
func writeAndCompile(ctx context.Context, root string, files map[string]string) (bool, string) {
	dirs := map[string]bool{}
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			return false, "write " + rel + ": " + err.Error()
		}
		dirs["./"+filepath.ToSlash(filepath.Dir(rel))] = true
	}
	build := func() (string, error) {
		args := []string{"build"}
		for d := range dirs {
			args = append(args, d)
		}
		return run(ctx, root, 8*time.Minute, "go", args...)
	}
	if out, err := build(); err == nil {
		return true, "compiles"
	} else {
		// try tidy then rebuild
		if _, terr := run(ctx, root, 8*time.Minute, "go", "mod", "tidy"); terr == nil {
			if out2, err2 := build(); err2 == nil {
				return true, "compiles (after go mod tidy)"
			} else {
				return false, trim(out2)
			}
		}
		return false, trim(out)
	}
}

func pickGoFile(root string) string {
	var best string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".git/") || strings.HasSuffix(rel, "_test.go") || !strings.HasSuffix(rel, ".go") {
			return nil
		}
		// prefer a main.go near the top of the tree
		if filepath.Base(rel) == "main.go" && (best == "" || strings.Count(rel, "/") < strings.Count(best, "/")) {
			best = rel
		}
		return nil
	})
	return best
}

func packageOf(absPath string) string {
	b, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}
	if m := pkgRE.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

func logStep(stage, status, message string) {
	fmt.Printf("    [%s/%s] %s\n", stage, status, message)
}

func run(ctx context.Context, dir string, timeout time.Duration, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		return "…" + s[len(s)-600:]
	}
	return s
}

func oneline(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

func lastFail(r api.PipelineResult) string {
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Status == "fail" {
			return r.Steps[i].Stage + ": " + oneline(r.Steps[i].Message+" "+r.Steps[i].Detail)
		}
	}
	return "failed (no fail step recorded)"
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
