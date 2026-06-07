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

export interface Step {
  stage: string; // investigate | apply | test | build | deploy | verify | tool
  status: "running" | "ok" | "fail" | "info";
  message: string;
  detail?: string;
}

export interface PipelineResult {
  steps: Step[];
  success: boolean;
}

export interface PipelineOptions {
  apply: boolean;
  test: boolean;
  build: boolean;
  deploy: boolean;
}

export interface TestStatus {
  sourceState: "buggy" | "modified";
  reachable: boolean;
  pendingPatch: boolean;
  demoAppUrl: string;
}

export interface TriggerResult {
  sent: number;
  codes: number[];
}

export interface AskResult {
  answer: string;
}
