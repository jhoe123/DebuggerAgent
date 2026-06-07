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
