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
export type ArtifactOverall = "investigated" | "staged" | "running" | "deployed" | "failed";

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
  updatedAt: string;
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
