// Shared API types. The Go backend (T6) returns JSON matching these shapes.

export interface Problem {
  id: string;
  title: string;
  severity: "AVAILABILITY" | "ERROR" | "RESOURCE" | "CUSTOM";
  status: "OPEN" | "CLOSED";
  affectedUsers: number;
  startedAt: string; // ISO timestamp
  entity: string;
  occurrences?: number;
  grailScannedBytes?: number;
  dynatraceUrl?: string;
  kind?: "error" | "performance";
  metric?: string; // e.g. "p95 657 ms"
}

export interface CodeLocation {
  file: string;
  line: number;
}

export interface RootCause {
  what: string;
  where: CodeLocation;
  why: string;
  impact: string;
  summary?: string; // plain-language TL;DR
  details?: string; // deeper technical explanation ("further reading")
}

export interface ProposedPatch {
  file: string;
  unifiedDiff: string;
  rationale: string;
}

export interface Investigation {
  problemId: string;
  rootCause: RootCause;
  confidence: number; // 0..1
  alternatives: string[];
  proposedPatch: ProposedPatch;
  suggestedTest?: string;
}

export interface ApproveResult {
  writtenTo: string;
}

export type InstrumentationKind =
  | "otel-bootstrap"
  | "tracer-init"
  | "span"
  | "record-error"
  | "attributes"
  | "metric";

// One place the agent recommends adding Dynatrace/OpenTelemetry telemetry.
// unifiedDiff is a small display hunk; the full patched file is generated at apply.
export interface InstrumentationCandidate {
  id: string;
  file: string;
  symbol?: string;
  startLine: number;
  endLine?: number;
  kind: InstrumentationKind;
  rationale: string;
  snippet?: string;
  unifiedDiff?: string;
}

export interface InstrumentationScan {
  root: string;
  summary: string;
  candidates: InstrumentationCandidate[];
  truncated?: boolean; // scan was capped (long candidate-list management)
}

export interface Step {
  stage: string; // investigate | apply | test | build | deploy | verify | tool
  status: "running" | "ok" | "fail" | "info";
  message: string;
  detail?: string;
}

export interface PipelineResult {
  steps: Step[];
  success: boolean;
  files?: string[];
  verify?: string; // e.g. "500 -> 400"
}

export interface HistoryEntry {
  id: string;
  kind: "proposed" | "approved" | "pipeline" | "scan";
  problemId?: string;
  files: string[];
  summary: string;
  status: "proposed" | "written" | "success" | "failed";
  createdAt: string; // RFC3339
  diff?: string;
  steps?: Step[];
  writtenTo?: string;
  verify?: string;
}

export interface HistoryResponse {
  entries: HistoryEntry[];
}

// --- Patch consolidation batch + durable per-problem status artifacts ---

// One staged patch in the consolidation batch (GET /api/patches). patchedContent
// is intentionally not sent — only display fields.
export interface StagedPatch {
  problemId: string;
  file: string;
  unifiedDiff?: string;
  rationale?: string;
  stagedAt: string; // RFC3339
}

export interface PatchesResponse {
  patches: StagedPatch[];
}

export type ArtifactStageKey = "investigation" | "patch" | "test" | "build" | "deploy" | "verify";
export type ArtifactOverall =
  | "investigated"
  | "staged"
  | "running"
  | "deployed"
  | "failed"
  | "confirmed";

export interface ArtifactStage {
  status: "ok" | "failed" | "pending" | "running";
  at?: string;
  detail?: string;
}

// Durable per-problem lifecycle record (GET /api/artifacts), persisted server-side.
export interface ProblemArtifact {
  problemId: string;
  title?: string;
  kind?: "error" | "performance";
  overall: ArtifactOverall;
  stages: Partial<Record<ArtifactStageKey, ArtifactStage>>;
  verify?: string;
  steps?: Step[];
  updatedAt: string;

  // Git source (set when a Git source is configured with branch-per-fix).
  fixBranch?: string; // the per-problem branch this fix lives on
  pushed?: boolean; // the fix branch was pushed to the remote
  confirmed?: boolean; // a human confirmed the fix (merged to the working branch)
  mergedAt?: string;
}

export interface ArtifactsResponse {
  artifacts: ProblemArtifact[];
}

export type TestStrategy = "auto" | "reuse" | "generate" | "skip";
export type BuildStrategy = "auto" | "script" | "default";
export type DeployTarget = "local" | "docker" | "script" | "cloud-run";

export interface DeploymentSpec {
  target?: DeployTarget;
  params?: Record<string, string>;
}

export interface PipelineOptions {
  apply: boolean;
  test: boolean;
  build: boolean;
  deploy: boolean;
  scenario?: "error" | "performance";
  // Configurable test/build/deploy (backend defaults: auto / auto / local).
  testStrategy?: TestStrategy; // reuse a test or let the agent generate one (lazy gate)
  buildStrategy?: BuildStrategy;
  deployment?: DeploymentSpec;
  forceSync?: boolean;
}


export interface TestStatus {
  sourceState: "buggy" | "modified";
  reachable: boolean;
  pendingPatch: boolean;
  demoAppUrl: string;
}

// --- Autopilot (auto-patch daemon) ---

export interface AutopilotStages {
  apply: boolean;
  test: boolean;
  build: boolean;
  deploy: boolean;
}

export interface AutopilotConfig {
  enabled: boolean;
  stages: AutopilotStages;
}

export type AutopilotPhase =
  | "queued"
  | "investigating"
  | "proposed"
  | "remediating"
  | "deployed"
  | "failed"
  | "halted";

export interface AutopilotRun {
  problemId: string;
  title?: string;
  kind?: "error" | "performance";
  phase: AutopilotPhase;
  message?: string;
  steps?: Step[];
  success?: boolean;
  updatedAt: string;
}

export interface AutopilotSnapshot {
  config: AutopilotConfig;
  runs: AutopilotRun[];
  localMode: boolean; // true when apply/build/deploy is available
}

export interface TriggerResult {
  sent: number;
  codes: number[];
}

export interface AskResult {
  answer: string;
}

// --- Slack notifications (backend is source of truth; configured from Settings) ---

export interface SlackStatus {
  enabled: boolean;
  configured: boolean; // a webhook is set
  preview?: string; // masked webhook for display (never the raw secret)
}

export interface SlackConfig {
  enabled: boolean;
  webhookUrl?: string; // secret; omit/empty to leave the existing webhook unchanged
}

// --- Pipeline & deploy settings (backend is source of truth; seeded from env) ---

export interface PipelineSettings {
  mode: string; // "local" | "cloudbuild" (read-only; env-controlled)
  testStrategy: TestStrategy;
  buildStrategy: BuildStrategy;
  deployTarget: DeployTarget;
  deployParams: Record<string, string>; // image/tag/hostPort · project/region/service/sourceBucket/artifactRepo · scriptPath
  healthUrl: string; // reachability check URL (full URL or path; defaults to the demo app URL)
  runnerAvailable?: boolean; // a remediation runner (local or cloud) is wired — Deploy is usable
}

// --- Git source (branch-per-fix + confirm-to-merge; backend is source of truth) ---

export interface GitFixBranch {
  name: string; // e.g. "patchpilot/fix-error-checkout"
  problemId?: string; // problem this branch addresses, if known
}

// GitSourceConfig is the editable config POSTed to the backend. authToken is a secret;
// omit/leave empty to keep the existing token unchanged.
export interface GitSourceConfig {
  repoUrl: string;
  authToken?: string; // secret HTTPS PAT; omit to keep existing
  workingBranch: string;
  branchPrefix: string;
  branchPerFix: boolean;
  autoMergeOnConfirm: boolean;
  pushEnabled: boolean;
  commitAuthorName: string;
  commitAuthorEmail: string;
  cloneDir?: string;
  baseBranch?: string; // transient: base for creating a NEW working branch (used only at first clone)
}

// GitValidateResult is the POST /api/git-source/validate payload: whether a repo URL
// (+ optional token) is reachable and the remote branches it exposes — without cloning.
export interface GitValidateResult {
  valid: boolean;
  branches: string[];
  defaultBranch?: string;
  error?: string;
}

// GitSourceStatus is the GET /api/git-source payload. It never returns the raw token
// or a token-bearing URL — only display-safe fields.
export interface GitSourceStatus {
  enabled: boolean; // mutating ops permitted (ENABLE_GIT_SOURCE + git present)
  configured: boolean; // a repo URL is set
  connected: boolean; // a clone exists on disk
  dirty: boolean; // working tree has uncommitted changes
  tokenConfigured: boolean; // a PAT is stored
  repoUrlPreview?: string;
  workingBranch: string;
  currentBranch?: string;
  branchPrefix: string;
  branchPerFix: boolean;
  autoMergeOnConfirm: boolean;
  pushEnabled: boolean;
  commitAuthorName: string;
  commitAuthorEmail: string;
  branches: GitFixBranch[];
  lastError?: string;
}

// ConfirmFixResult is returned by POST /api/confirm-fix (the human merge gate).
export interface ConfirmFixResult {
  problemId: string;
  merged: boolean;
  mergedBranch?: string;
  intoBranch?: string;
  pushed?: boolean;
  detail?: string;
}
