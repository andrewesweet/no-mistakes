package eval

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
)

// TestPersistEvaluationRecordsCacheTokens pins the registry's cache token
// columns: cache reads dominate review cost (the P0 smoke measured 23.4M
// cache-read against 556 fresh-input tokens across claude's eight replays),
// so a registry row that drops them understates real cost by orders of
// magnitude and forces cost analysis back into the per-eval JSON files.
func TestPersistEvaluationRecordsCacheTokens(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	caseDir := store.caseDir("cache-cost")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(caseDir, "labels.json"), Labels{Version: labelsVersion}); err != nil {
		t.Fatal(err)
	}
	c := Case{Manifest: Manifest{ID: "cache-cost", SourceRunID: "run", SourceRoundID: "round"}, Dir: caseDir}
	if err := store.registerCase(c); err != nil {
		t.Fatal(err)
	}
	if err := store.persistEvaluation(c, Evaluation{
		ID:               "evaluation",
		SessionID:        "session",
		CaseID:           c.ID,
		Candidate:        "claude+test",
		Repeat:           1,
		Status:           "completed",
		TokensReported:   true,
		InputTokens:      556,
		OutputTokens:     1475,
		CacheReadTokens:  2_921_704,
		CacheWriteTokens: 12_340,
		FreshInputTokens: 556,
	}); err != nil {
		t.Fatal(err)
	}

	var cacheRead, cacheWrite int64
	if err := store.db.QueryRow(
		`SELECT cache_read_tokens, cache_write_tokens FROM evaluations WHERE id = 'evaluation'`,
	).Scan(&cacheRead, &cacheWrite); err != nil {
		t.Fatalf("read cache token columns: %v", err)
	}
	if cacheRead != 2_921_704 || cacheWrite != 12_340 {
		t.Fatalf("registry cache tokens = read %d write %d, want read 2921704 write 12340", cacheRead, cacheWrite)
	}
}

// TestStoreMigrationAddsCacheTokenColumnsForward opens a registry created by
// an older binary (evaluations without cache columns and one row already in
// it) and proves the migration adds the columns without disturbing the row,
// so existing histories stay readable and new rows persist cleanly.
func TestStoreMigrationAddsCacheTokenColumnsForward(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE cases (
    id TEXT PRIMARY KEY,
    source_run_id TEXT NOT NULL,
    source_round_id TEXT NOT NULL UNIQUE,
    captured_at INTEGER NOT NULL,
    repo_fingerprint TEXT NOT NULL,
    branch TEXT NOT NULL,
    language TEXT NOT NULL,
    size_bucket TEXT NOT NULL,
    severity TEXT NOT NULL,
    gold_count INTEGER NOT NULL,
    path TEXT NOT NULL UNIQUE
);
CREATE TABLE evaluations (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    case_id TEXT NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
    candidate TEXT NOT NULL,
    repeat_number INTEGER NOT NULL,
    started_at INTEGER NOT NULL,
    completed_at INTEGER NOT NULL,
    status TEXT NOT NULL,
    gold_count INTEGER NOT NULL,
    true_positive INTEGER NOT NULL,
    false_negative INTEGER NOT NULL,
    false_positive INTEGER NOT NULL,
    pending INTEGER NOT NULL,
    tokens_reported INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    fresh_input_tokens INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    path TEXT NOT NULL UNIQUE
);
INSERT INTO cases (id, source_run_id, source_round_id, captured_at, repo_fingerprint, branch, language, size_bucket, severity, gold_count, path)
VALUES ('case-old', 'run', 'round', 1, 'fp', 'main', 'go', 's', 'sev', 0, 'p');
INSERT INTO evaluations (id, session_id, case_id, candidate, repeat_number, started_at, completed_at, status, gold_count, true_positive, false_negative, false_positive, pending, tokens_reported, input_tokens, output_tokens, fresh_input_tokens, duration_ms, path)
VALUES ('old-eval', 'session', 'case-old', 'claude+test', 1, 1, 2, 'completed', 0, 0, 0, 0, 0, 1, 10, 20, 30, 40, 'p');
`)
	if err != nil {
		t.Fatalf("seed legacy registry: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, column := range []string{"cache_read_tokens", "cache_write_tokens"} {
		var found int
		if err := store.db.QueryRow(
			`SELECT count(*) FROM pragma_table_info('evaluations') WHERE name = ?`, column,
		).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found != 1 {
			t.Fatalf("migrated evaluations table lacks the %s column", column)
		}
	}
	var input, cacheRead int64
	if err := store.db.QueryRow(
		`SELECT input_tokens, COALESCE(cache_read_tokens, -1) FROM evaluations WHERE id = 'old-eval'`,
	).Scan(&input, &cacheRead); err != nil {
		t.Fatalf("legacy row unreadable after migration: %v", err)
	}
	if input != 10 || cacheRead != 0 {
		t.Fatalf("legacy row = input %d cache-read %d, want input 10 cache-read defaulted to 0", input, cacheRead)
	}
}

// TestObservedAgentSumsCacheWriteTokens extends the honest-cost rule to
// cache-write tokens: an adapter that reports cache creation has it summed
// across attempts, and an attempt without the field leaves the sum alone
// rather than fabricating a write count.
func TestObservedAgentSumsCacheWriteTokens(t *testing.T) {
	withWrite := func(input, output, cacheRead, cacheWrite int) *agent.Result {
		result := reportedUsage(input, output, cacheRead)
		result.Usage.CacheCreationTokens = cacheWrite
		result.Usage.CacheCreationReported = true
		return result
	}
	observed := &observedAgent{inner: &retryingAgent{
		attempts: []*agent.Result{withWrite(50_000, 100, 5_000, 2_000), withWrite(50_000, 100, 5_000, 3_000)},
	}}
	if _, err := observed.Run(context.Background(), agent.RunOpts{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if observed.cacheWriteTokens != 5_000 {
		t.Fatalf("cache-write sum = %d, want 5000 across both attempts", observed.cacheWriteTokens)
	}

	unreported := &observedAgent{inner: &retryingAgent{
		attempts: []*agent.Result{reportedUsage(50_000, 100, 5_000), withWrite(50_000, 100, 5_000, 2_000)},
	}}
	if _, err := unreported.Run(context.Background(), agent.RunOpts{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if unreported.cacheWriteTokens != 2_000 {
		t.Fatalf("cache-write sum = %d, want only the attempt that reported cache creation", unreported.cacheWriteTokens)
	}
}

// reportForEvaluations drives the real report path: every evaluation is
// persisted through the store (JSON payload plus registry row) exactly as a
// replay persists one, then rendered.
func reportForEvaluations(t *testing.T, evaluations []Evaluation) string {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, evaluation := range evaluations {
		caseID := evaluation.Candidate + "-case"
		caseDir := store.caseDir(caseID)
		if err := os.MkdirAll(caseDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(caseDir, "labels.json"), Labels{Version: labelsVersion}); err != nil {
			t.Fatal(err)
		}
		c := Case{Manifest: Manifest{ID: caseID, SourceRunID: "run-" + evaluation.Candidate, SourceRoundID: "round-" + evaluation.Candidate}, Dir: caseDir}
		if err := store.registerCase(c); err != nil {
			t.Fatal(err)
		}
		evaluation.ID = "evaluation-" + evaluation.Candidate
		evaluation.SessionID = "session"
		evaluation.CaseID = c.ID
		evaluation.Repeat = 1
		if err := store.persistEvaluation(c, evaluation); err != nil {
			t.Fatal(err)
		}
	}
	reports, err := Report(store)
	if err != nil {
		t.Fatal(err)
	}
	return RenderReport(reports)
}

// TestRenderReportIncludesCacheTokensInTokenCost pins the report's token
// line to the honest total: cache reads dominate review cost, so a line that
// prints only fresh-input and output understates cost by orders of magnitude
// and ranks the recall-vs-cost frontier by the wrong number.
func TestRenderReportIncludesCacheTokensInTokenCost(t *testing.T) {
	evaluations := []Evaluation{{
		Candidate: "claude+test", Status: "completed", TokensReported: true,
		InputTokens: 70, OutputTokens: 184, CacheReadTokens: 2_921_704, CacheWriteTokens: 12_340,
		FreshInputTokens: 70, DurationMS: 1000,
	}}
	output := reportForEvaluations(t, evaluations)
	if !strings.Contains(output, "cache-read") {
		t.Fatalf("report = %q, want cache-read tokens in the token cost line", output)
	}
	if !strings.Contains(output, "cache-write") {
		t.Fatalf("report = %q, want cache-write tokens in the token cost line", output)
	}

	// A candidate whose agent never reports cache keeps the historical line
	// instead of printing fabricated zeros.
	legacy := []Evaluation{{
		Candidate: "pi+test", Status: "completed", TokensReported: true,
		InputTokens: 90, OutputTokens: 40, FreshInputTokens: 60, DurationMS: 1000,
	}}
	legacyOutput := reportForEvaluations(t, legacy)
	if strings.Contains(legacyOutput, "cache-read") {
		t.Fatalf("report = %q, want no cache segment when no replay reported cache tokens", legacyOutput)
	}
	if !strings.Contains(legacyOutput, "fresh-input + output tokens per reported replay") {
		t.Fatalf("report = %q, want the historical fresh-input + output line", legacyOutput)
	}
}

// TestRenderReportFrontierRanksOnTrueCost proves the frontier compares the
// cache-inclusive total: an arm burning millions of cache-read tokens must
// not read as cheaper than one that spent them honestly.
func TestRenderReportFrontierRanksOnTrueCost(t *testing.T) {
	cacheHeavy := Evaluation{Candidate: "cache-heavy", Status: "completed", TokensReported: true,
		InputTokens: 70, OutputTokens: 184, CacheReadTokens: 2_921_704, FreshInputTokens: 70, DurationMS: 1000, HasFindingGold: true, GoldCount: 2, TruePositive: 2}
	freshHeavy := Evaluation{Candidate: "fresh-heavy", Status: "completed", TokensReported: true,
		InputTokens: 1_500_000, OutputTokens: 40_000, FreshInputTokens: 1_500_000, DurationMS: 1000, HasFindingGold: true, GoldCount: 2, TruePositive: 2}
	output := reportForEvaluations(t, []Evaluation{cacheHeavy, freshHeavy})
	// cache-heavy: 2,921,958 total tokens vs fresh-heavy: 1,540,000. The
	// cache-inclusive total must dominate the fresh-only total, so cache-heavy
	// sits on the frontier and fresh-heavy does not.
	if !strings.Contains(output, "recall-vs-cost frontier: true") {
		t.Fatalf("report = %q, want cache-heavy (the cache-inclusive total) on the frontier", output)
	}
}
