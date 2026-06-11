// Package api defines the JSON shapes exchanged with the React frontend.
// Field names (camelCase json tags) match frontend/src/types.ts.
package api

type Problem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Severity      string `json:"severity"`
	Status        string `json:"status"`
	AffectedUsers int    `json:"affectedUsers"`
	StartedAt     string `json:"startedAt"`
	Entity        string `json:"entity"`

	// Richer Dynatrace context.
	Occurrences       int    `json:"occurrences,omitempty"`
	GrailScannedBytes int64  `json:"grailScannedBytes,omitempty"`
	DynatraceURL      string `json:"dynatraceUrl,omitempty"`

	// Classification for performance-vs-error problems.
	Kind   string `json:"kind,omitempty"`   // "error" | "performance"
	Metric string `json:"metric,omitempty"` // e.g. "p95 612 ms" (performance only)
}

type CodeLocation struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

type RootCause struct {
	What   string       `json:"what"`
	Where  CodeLocation `json:"where"`
	Why    string       `json:"why"`
	Impact string       `json:"impact"`

	// Layered detail: a plain-language TL;DR anyone can grasp, plus an optional
	// deeper technical explanation for further reading. Both optional/back-compat.
	Summary string `json:"summary,omitempty"`
	Details string `json:"details,omitempty"`
}

type ProposedPatch struct {
	File        string `json:"file"`
	UnifiedDiff string `json:"unifiedDiff"`
	Rationale   string `json:"rationale"`
}

type Investigation struct {
	ProblemID     string        `json:"problemId"`
	RootCause     RootCause     `json:"rootCause"`
	Confidence    float64       `json:"confidence"`
	Alternatives  []string      `json:"alternatives"`
	ProposedPatch ProposedPatch `json:"proposedPatch"`
	SuggestedTest string        `json:"suggestedTest,omitempty"`
}

type ApproveResult struct {
	WrittenTo string `json:"writtenTo"`
}

// InstrumentationCandidate is one place the agent recommends adding Dynatrace /
// OpenTelemetry telemetry. Kind is one of:
// otel-bootstrap | tracer-init | span | record-error | attributes | metric.
// UnifiedDiff is a small display hunk only — the FULL patched file content is
// generated at apply time (keeps a scan's payload lean for long candidate lists).
type InstrumentationCandidate struct {
	ID          string `json:"id"`                    // stable id assigned by the backend
	File        string `json:"file"`                  // path relative to the source root
	Symbol      string `json:"symbol,omitempty"`      // function/handler the edit targets
	StartLine   int    `json:"startLine"`             // 1-based anchor line
	EndLine     int    `json:"endLine,omitempty"`     // inclusive end of the affected region
	Kind        string `json:"kind"`                  // see above
	Rationale   string `json:"rationale"`             // why this telemetry matters
	Snippet     string `json:"snippet,omitempty"`     // a few lines of context where it lands
	UnifiedDiff string `json:"unifiedDiff,omitempty"` // small display hunk
}

// InstrumentationScan is the result of a (read-only) instrumentation review.
// Truncated flags that the scan was capped (long candidate-list management).
type InstrumentationScan struct {
	Root       string                     `json:"root"`
	Summary    string                     `json:"summary"`
	Candidates []InstrumentationCandidate `json:"candidates"`
	Truncated  bool                       `json:"truncated,omitempty"`
}

// Step is a single milestone in a live stream (agent reasoning or a pipeline stage).
type Step struct {
	Stage   string `json:"stage"`            // investigate | apply | test | build | deploy | verify
	Status  string `json:"status"`           // running | ok | fail | info
	Message string `json:"message"`          // short human-readable line
	Detail  string `json:"detail,omitempty"` // optional logs/output
}

// PipelineResult is the terminal result of an auto-remediation run.
type PipelineResult struct {
	Steps   []Step   `json:"steps"`
	Success bool     `json:"success"`
	Files   []string `json:"files,omitempty"`  // source files the pipeline touched
	Verify  string   `json:"verify,omitempty"` // before→after, e.g. "500 -> 400"
}

// StagedPatch is one patch in the consolidation batch (GET /api/patches). The
// bulky patched_content is intentionally omitted — only display fields are sent.
type StagedPatch struct {
	ProblemID   string `json:"problemId"`
	File        string `json:"file"`
	UnifiedDiff string `json:"unifiedDiff,omitempty"`
	Rationale   string `json:"rationale,omitempty"`
	StagedAt    string `json:"stagedAt"` // RFC3339
}

// PatchesResponse is the GET /api/patches payload (the current batch).
type PatchesResponse struct {
	Patches []StagedPatch `json:"patches"`
}

// ArtifactStage is one lifecycle step's status on a ProblemArtifact.
type ArtifactStage struct {
	Status string `json:"status"`           // ok | failed | pending | running
	At     string `json:"at,omitempty"`     // RFC3339
	Detail string `json:"detail,omitempty"` // short note (root-cause summary, error, etc.)
}

// ProblemArtifact is the durable per-problem lifecycle record persisted server-side
// (PATCH_OUTPUT_DIR/artifacts/<id>.json) and surfaced via GET /api/artifacts. Stages
// keys: investigation | patch | test | build | deploy | verify.
type ProblemArtifact struct {
	ProblemID string                   `json:"problemId"`
	Title     string                   `json:"title,omitempty"`
	Kind      string                   `json:"kind,omitempty"`
	Overall   string                   `json:"overall"` // investigated|staged|running|deployed|failed|confirmed
	Stages    map[string]ArtifactStage `json:"stages"`
	Verify    string                   `json:"verify,omitempty"`
	Steps     []Step                   `json:"steps,omitempty"`
	UpdatedAt string                   `json:"updatedAt"` // RFC3339


	// Git source (set only when a Git source is configured with branch-per-fix).
	FixBranch string `json:"fixBranch,omitempty"` // the per-problem branch this fix lives on
	Pushed    bool   `json:"pushed,omitempty"`    // the fix branch was pushed to the remote
	Confirmed bool   `json:"confirmed,omitempty"` // a human confirmed the fix (merged to working branch)
	MergedAt  string `json:"mergedAt,omitempty"`  // RFC3339 when the fix branch was merged
}

// ArtifactsResponse is the GET /api/artifacts payload (newest-updated first).
type ArtifactsResponse struct {
	Artifacts []ProblemArtifact `json:"artifacts"`
}

// HistoryEntry is one audited change: a proposed patch, an approval, or a
// pipeline run. Surfaced read-only via GET /api/history (hosted-safe).
type HistoryEntry struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"` // "proposed" | "approved" | "pipeline" | "scan"
	ProblemID string   `json:"problemId,omitempty"`
	Files     []string `json:"files"`               // affected file paths
	Summary   string   `json:"summary"`             // short human-readable line
	Status    string   `json:"status"`              // "proposed" | "written" | "success" | "failed"
	CreatedAt string   `json:"createdAt"`           // RFC3339
	Diff      string   `json:"diff,omitempty"`      // proposed/approved
	Steps     []Step   `json:"steps,omitempty"`     // pipeline
	WrittenTo string   `json:"writtenTo,omitempty"` // approved
	Verify    string   `json:"verify,omitempty"`    // pipeline, e.g. "500 -> 400"
}

// HistoryResponse is the GET /api/history payload (newest first).
type HistoryResponse struct {
	Entries []HistoryEntry `json:"entries"`
}

// VersionPatch is one file's change inside a deploy version — display fields only
// (the full patched content stays server-side).
type VersionPatch struct {
	File        string `json:"file"`
	UnifiedDiff string `json:"unifiedDiff,omitempty"`
	Rationale   string `json:"rationale,omitempty"`
}

// DeployVersion is one tracked deployment (GET /api/versions). Every successful
// deploy records a version carrying the CUMULATIVE patch state, so reverting to it
// restores the source exactly as of that deploy. Patches is populated only by the
// detail endpoint (GET /api/versions/{id}) to keep the list payload lean.
type DeployVersion struct {
	ID         string         `json:"id"`
	Seq        int            `json:"seq"`
	CreatedAt  string         `json:"createdAt"`          // RFC3339
	Source     string         `json:"source"`             // "manual" | "autopilot" | "revert"
	Summary    string         `json:"summary"`            // short human-readable line
	ProblemIDs []string       `json:"problemIds"`         // problems fixed in the run that created this version
	Files      []string       `json:"files"`              // cumulative set of modified files
	Verify     string         `json:"verify,omitempty"`   // pipeline verify note
	RevertOf   string         `json:"revertOf,omitempty"` // version id this deploy restored
	Patches    []VersionPatch `json:"patches,omitempty"`  // detail endpoint only
}

// VersionsResponse is the GET /api/versions payload (newest first).
type VersionsResponse struct {
	Versions []DeployVersion `json:"versions"`
}

// AutopilotStages selects which pipeline stages the autopilot runs after a fix is
// proposed (mirrors democtl.Options without the scenario field).
type AutopilotStages struct {
	Apply  bool `json:"apply"`
	Test   bool `json:"test"`
	Build  bool `json:"build"`
	Deploy bool `json:"deploy"`
}

// AutopilotConfig is the live auto-patch configuration (backend is source of truth).
type AutopilotConfig struct {
	Enabled bool            `json:"enabled"`
	Stages  AutopilotStages `json:"stages"`
}

// AutopilotRun is the current automation state for one problem.
// Phase: queued | investigating | proposed | remediating | deployed | failed | halted.
type AutopilotRun struct {
	ProblemID string `json:"problemId"`
	Title     string `json:"title,omitempty"`
	Kind      string `json:"kind,omitempty"` // "error" | "performance"
	Phase     string `json:"phase"`
	Message   string `json:"message,omitempty"` // latest step, e.g. "testing"
	Steps     []Step `json:"steps,omitempty"`
	Success   *bool  `json:"success,omitempty"` // set on terminal pipeline runs
	UpdatedAt string `json:"updatedAt"`         // RFC3339
}

// AutopilotSnapshot is the GET /api/autopilot payload (runs newest-first).
type AutopilotSnapshot struct {
	Config    AutopilotConfig `json:"config"`
	Runs      []AutopilotRun  `json:"runs"`
	LocalMode bool            `json:"localMode"`           // true when apply/build/deploy is available
	ActiveIDs []string        `json:"activeIds,omitempty"` // problems in the in-flight batch (sorted)
}

// TestStatus is the Test Console status snapshot (local only).
type TestStatus struct {
	SourceState  string `json:"sourceState"` // "buggy" | "modified"
	Reachable    bool   `json:"reachable"`   // demo_app responding on /healthz
	PendingPatch bool   `json:"pendingPatch"`
	DemoAppURL   string `json:"demoAppUrl"`
}

// TriggerResult reports the outcome of firing demo requests.
type TriggerResult struct {
	Sent  int   `json:"sent"`
	Codes []int `json:"codes"`
}

// AskResult is the answer to a natural-language follow-up question.
type AskResult struct {
	Answer string `json:"answer"`
}

// SlackConfig sets the Slack notifier at runtime (POST /api/slack/config).
// WebhookURL is a secret; an empty value leaves the existing webhook unchanged
// (so toggling Enabled doesn't wipe a configured webhook).
type SlackConfig struct {
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhookUrl,omitempty"`
}

// SlackStatus is the GET /api/slack payload. It never returns the raw webhook —
// only whether one is configured plus a masked preview.
type SlackStatus struct {
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`        // a webhook is set
	Preview    string `json:"preview,omitempty"` // masked webhook for display
}

// PipelineSettings is the runtime-configurable test/build/deploy configuration shown in
// Settings. The backend is the source of truth (seeded from env), so the server-side
// reachability check can read the configured health URL. Mode is read-only (env-controlled).
type PipelineSettings struct {
	Mode          string            `json:"mode"`          // PIPELINE_MODE: "local" | "cloudbuild" (read-only)
	TestStrategy  string            `json:"testStrategy"`  // auto | reuse | generate | skip
	BuildStrategy string            `json:"buildStrategy"` // auto | script | default
	DeployTarget  string            `json:"deployTarget"`  // local | docker | script | cloud-run
	DeployParams  map[string]string `json:"deployParams"`  // image/tag/hostPort · project/region/service/sourceBucket/artifactRepo · scriptPath
	HealthURL     string            `json:"healthUrl"`     // reachability check URL (full URL or path; defaults to the demo app URL)
	AppURL        string            `json:"appUrl"`        // public URL of the deployed app/web portal, surfaced as the "Open app" link; blank => auto-detected (local) or the health URL

	// RunnerAvailable reports whether a remediation runner (local democtl or the cloud
	// build runner) is wired, so the UI can enable Deploy without depending on the Test
	// Console being on. Response-only: set by the handler, never stored/accepted on Set.
	RunnerAvailable bool `json:"runnerAvailable"`
}

// --- Git source (branch-per-fix + confirm-to-merge; backend is source of truth) ---

// GitSourceConfig configures the managed Git source (POST /api/git-source/config).
// AuthToken is a secret (HTTPS PAT); an empty value leaves the stored token unchanged
// so toggling a flag never wipes it. The raw token is never returned by any GET.
type GitSourceConfig struct {
	RepoURL            string `json:"repoUrl"`
	AuthToken          string `json:"authToken,omitempty"` // secret (HTTPS PAT); empty = keep existing
	WorkingBranch      string `json:"workingBranch"`       // integration branch fixes merge into (default "patchpilot")
	BranchPrefix       string `json:"branchPrefix"`        // prefix for per-fix branches (default "patchpilot/fix-")
	BranchPerFix       bool   `json:"branchPerFix"`        // create an isolated branch per fix
	AutoMergeOnConfirm bool   `json:"autoMergeOnConfirm"`  // on confirm, merge the fix branch then delete it
	PushEnabled        bool   `json:"pushEnabled"`         // permission gate: push to the remote (else local-only)
	CommitAuthorName   string `json:"commitAuthorName"`
	CommitAuthorEmail  string `json:"commitAuthorEmail"`
	CloneDir           string `json:"cloneDir,omitempty"`   // where the repo is cloned (default <PATCH_OUTPUT_DIR>/gitsrc)
	BaseBranch         string `json:"baseBranch,omitempty"` // transient: base for creating a NEW working branch (only used at first checkout)
}

// GitValidateResult is the POST /api/git-source/validate payload: it reports whether a
// repo URL (+ optional token) is reachable and lists its remote branches, without cloning.
type GitValidateResult struct {
	Valid         bool     `json:"valid"`
	Branches      []string `json:"branches"`
	DefaultBranch string   `json:"defaultBranch,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// GitFixBranch is one active per-problem fix branch.
type GitFixBranch struct {
	Name      string `json:"name"`
	ProblemID string `json:"problemId,omitempty"`
}

// GitSourceStatus is the GET /api/git-source payload. It never returns the raw token
// or a token-bearing URL — only display-safe fields.
type GitSourceStatus struct {
	Enabled            bool           `json:"enabled"`         // mutating ops permitted (ENABLE_GIT_SOURCE + git present)
	Configured         bool           `json:"configured"`      // a repo URL is set
	Connected          bool           `json:"connected"`       // a clone exists on disk
	Dirty              bool           `json:"dirty"`           // working tree has uncommitted changes
	TokenConfigured    bool           `json:"tokenConfigured"` // a PAT is stored
	RepoURLPreview     string         `json:"repoUrlPreview,omitempty"`
	WorkingBranch      string         `json:"workingBranch"`
	CurrentBranch      string         `json:"currentBranch,omitempty"`
	BranchPrefix       string         `json:"branchPrefix"`
	BranchPerFix       bool           `json:"branchPerFix"`
	AutoMergeOnConfirm bool           `json:"autoMergeOnConfirm"`
	PushEnabled        bool           `json:"pushEnabled"`
	CommitAuthorName   string         `json:"commitAuthorName"`
	CommitAuthorEmail  string         `json:"commitAuthorEmail"`
	Branches           []GitFixBranch `json:"branches"`
	LastError          string         `json:"lastError,omitempty"`
}

// GitSourceApplyResult is the POST /api/git-source/config response: the resolved
// status, plus what the server stopped/reset when the update re-targeted the managed
// source (a different repo URL or working branch than the one being processed).
type GitSourceApplyResult struct {
	GitSourceStatus      // embedded → flat JSON, backward-compatible with GitSourceStatus consumers
	TargetChanged   bool `json:"targetChanged,omitempty"`
	HaltedRuns      int  `json:"haltedRuns,omitempty"`
	WorkspaceReset  bool `json:"workspaceReset,omitempty"`
	PatchesCleared  bool `json:"patchesCleared,omitempty"`
}

// DemoResetResult is the POST /api/demo/reset response (the judge-facing "reset
// testing" action): what was stopped/cleared and how the original demo source was
// restored — a fresh clone of the configured repo (git) or the local console reset.
type DemoResetResult struct {
	Mode            string `json:"mode"` // "git" | "local" | "none"
	HaltedRuns      int    `json:"haltedRuns"`
	WorkspaceReset  bool   `json:"workspaceReset,omitempty"`  // clone deleted
	Reconnected     bool   `json:"reconnected,omitempty"`     // fresh clone succeeded
	SourceReset     bool   `json:"sourceReset,omitempty"`     // local democtl source restored
	AutopatchPaused bool   `json:"autopatchPaused,omitempty"` // autopatch was ON and is now paused
	RedeployStarted bool   `json:"redeployStarted,omitempty"` // background redeploy of the original app kicked off
	Error           string `json:"error,omitempty"`
}

// ConfirmFixResult is returned by POST /api/confirm-fix (the human merge gate).
type ConfirmFixResult struct {
	ProblemID    string `json:"problemId"`
	Merged       bool   `json:"merged"`
	MergedBranch string `json:"mergedBranch,omitempty"` // the fix branch merged + deleted
	IntoBranch   string `json:"intoBranch,omitempty"`   // the working branch it merged into
	Pushed       bool   `json:"pushed,omitempty"`
	Detail       string `json:"detail,omitempty"`
}
