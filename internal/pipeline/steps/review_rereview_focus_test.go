package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// seedReviewRound inserts one review round for sctx's step result, carrying
// the given reviewed head ("" seeds a round without one).
func seedReviewRound(t *testing.T, sctx *pipeline.StepContext, round int, trigger, reviewedHead string) {
	t.Helper()
	findings := `{"findings":[{"id":"review-1","severity":"warning","file":"feature.txt","description":"prior round finding","action":"auto-fix"}],"summary":"1 issue","risk_level":"low","risk_rationale":"seeded","risk_scope":"source-or-external"}`
	if _, err := sctx.DB.InsertReviewStepRound(sctx.StepResultID, round, trigger, &findings, nil, reviewedHead, 0); err != nil {
		t.Fatal(err)
	}
}

// fixAndRereviewRunsExecute drives ReviewStep.Execute through one fix round
// (the mock agent writes review-fix.txt and returns a fix summary) and one
// rereview returning a clean full-coverage record.
func fixAndRereviewRunsExecute(t *testing.T, sctx *pipeline.StepContext) []agent.RunOpts {
	t.Helper()
	callCount := 0
	ag := sctx.Agent.(*mockAgent)
	ag.runFn = func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		callCount++
		if callCount == 1 {
			if err := os.WriteFile(filepath.Join(sctx.WorkDir, "review-fix.txt"), []byte("fixed"), 0o644); err != nil {
				t.Fatal(err)
			}
			return &agent.Result{Output: json.RawMessage(`{"summary":"address findings"}`)}, nil
		}
		findings := cleanReviewFindings()
		findings.ReviewedPaths = []string{"feature.txt", "review-fix.txt"}
		j, _ := json.Marshal(findings)
		return &agent.Result{Output: j}, nil
	}
	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	return ag.calls
}

const (
	rereviewFocusHeader        = "\n\nRereview focus:"
	rereviewFocusMarkerPast    = `add "present at round 1's review" to its description as an exact suffix preceded by a space`
	rereviewFocusFullCoverDuty = "Review the complete change exactly as before"
)

// TestReviewStep_RereviewFocusSection_NamesThePriorReviewedHead pins the G2
// rereview focus contract: on a fix-round rerun whose step already recorded a
// reviewed head, the rereview prompt names that head, carries the
// `git diff --stat <prevReviewedHead> <head>` summary of what changed since
// it, directs attention there first while still reviewing the complete
// change under the unchanged coverage duty, and asks for the "present at
// round N's review" description marker on findings over unchanged code.
func TestReviewStep_RereviewFocusSection_NamesThePriorReviewedHead(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)

	ag := &mockAgent{name: "test"}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	sctx.StepResultID = stepResult.ID
	seedReviewRound(t, sctx, 1, "initial", headSHA)
	sctx.Fixing = true
	sctx.PreviousFindings = `{"findings":[{"id":"review-1","severity":"warning","file":"feature.txt","description":"possible nil deref","action":"auto-fix"}],"summary":"1 issue"}`

	calls := fixAndRereviewRunsExecute(t, sctx)
	if len(calls) != 2 {
		t.Fatalf("expected fix + rereview calls, got %d", len(calls))
	}
	rereviewPrompt := calls[1].Prompt
	for _, want := range []string{
		rereviewFocusHeader,
		"Round 1 of this step reviewed head " + headSHA,
		"Changes since that review (git diff --stat " + headSHA,
		"review-fix.txt", // the fixer's own commit is the delta the section summarizes
		rereviewFocusFullCoverDuty,
		rereviewFocusMarkerPast,
	} {
		if !strings.Contains(rereviewPrompt, want) {
			t.Errorf("rereview prompt missing rereview-focus content %q:\n%s", want, rereviewPrompt)
		}
	}
	// The focus section is rereview framing, never fixer instruction.
	if strings.Contains(calls[0].Prompt, rereviewFocusHeader) {
		t.Errorf("fixer prompt must not carry the rereview-focus section:\n%s", calls[0].Prompt)
	}
}

// TestReviewStep_RereviewFocusSection_InitialReviewPromptIsUnchanged pins the
// byte-identical guarantee: an initial review (not fixing, no prior rounds)
// carries none of the rereview-focus content.
func TestReviewStep_RereviewFocusSection_InitialReviewPromptIsUnchanged(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)

	findingsJSON, _ := json.Marshal(cleanReviewFindings())
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			return &agent.Result{Output: findingsJSON}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("expected one review call, got %d", len(ag.calls))
	}
	prompt := ag.calls[0].Prompt
	if strings.Contains(prompt, rereviewFocusHeader) {
		t.Errorf("initial review prompt must not carry the rereview-focus section:\n%s", prompt)
	}
	if strings.Contains(prompt, rereviewFocusMarkerPast) || strings.Contains(prompt, rereviewFocusFullCoverDuty) {
		t.Errorf("initial review prompt must not carry rereview-focus instructions:\n%s", prompt)
	}
}

// TestReviewStep_RereviewFocusSection_FixingWithoutAPriorReviewedHead pins the
// second emission condition: a fixing rerun whose step has no prior round
// with a reviewed head gets today's prompt, not an empty focus section.
func TestReviewStep_RereviewFocusSection_FixingWithoutAPriorReviewedHead(t *testing.T) {
	t.Parallel()
	t.Run("no_rounds_at_all", func(t *testing.T) {
		t.Parallel()
		dir, baseSHA, headSHA := setupGitRepo(t)
		gitCmd(t, dir, "checkout", "--detach", headSHA)

		ag := &mockAgent{name: "test"}
		sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
		stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepReview)
		if err != nil {
			t.Fatal(err)
		}
		sctx.StepResultID = stepResult.ID
		sctx.Fixing = true
		sctx.PreviousFindings = `{"findings":[{"id":"review-1","severity":"warning","file":"feature.txt","description":"possible nil deref","action":"auto-fix"}],"summary":"1 issue"}`

		calls := fixAndRereviewRunsExecute(t, sctx)
		if len(calls) != 2 {
			t.Fatalf("expected fix + rereview calls, got %d", len(calls))
		}
		if strings.Contains(calls[1].Prompt, rereviewFocusHeader) {
			t.Errorf("rereview with no prior round must not carry the focus section:\n%s", calls[1].Prompt)
		}
	})

	t.Run("prior_round_without_a_reviewed_head", func(t *testing.T) {
		t.Parallel()
		dir, baseSHA, headSHA := setupGitRepo(t)
		gitCmd(t, dir, "checkout", "--detach", headSHA)

		ag := &mockAgent{name: "test"}
		sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
		stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepReview)
		if err != nil {
			t.Fatal(err)
		}
		sctx.StepResultID = stepResult.ID
		seedReviewRound(t, sctx, 1, "initial", "")
		sctx.Fixing = true
		sctx.PreviousFindings = `{"findings":[{"id":"review-1","severity":"warning","file":"feature.txt","description":"possible nil deref","action":"auto-fix"}],"summary":"1 issue"}`

		calls := fixAndRereviewRunsExecute(t, sctx)
		if len(calls) != 2 {
			t.Fatalf("expected fix + rereview calls, got %d", len(calls))
		}
		if strings.Contains(calls[1].Prompt, rereviewFocusHeader) {
			t.Errorf("rereview whose prior round has no reviewed head must not carry the focus section:\n%s", calls[1].Prompt)
		}
	})
}

// TestReviewStep_RereviewFocusSection_NonFixingReviewWithPriorHead pins that
// the section keys on the fixing rerun, not merely on a stored reviewed head:
// a non-fixing review of a step that happens to carry prior rounds is an
// initial review and stays unchanged.
func TestReviewStep_RereviewFocusSection_NonFixingReviewWithPriorHead(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)

	findingsJSON, _ := json.Marshal(cleanReviewFindings())
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			return &agent.Result{Output: findingsJSON}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	sctx.StepResultID = stepResult.ID
	seedReviewRound(t, sctx, 1, "initial", headSHA)

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("expected one review call, got %d", len(ag.calls))
	}
	if strings.Contains(ag.calls[0].Prompt, rereviewFocusHeader) {
		t.Errorf("non-fixing review must not carry the focus section even with a prior reviewed head:\n%s", ag.calls[0].Prompt)
	}
}

// TestReviewStep_RereviewFocusSection_UnchangedHeadReportsNoDelta pins the
// empty-delta rendering: when the head equals the prior round's reviewed head
// (an answer round re-entering the step with SkipFixExecution, or a no-op fix
// round), the section still names the head but reports that no files changed.
func TestReviewStep_RereviewFocusSection_UnchangedHeadReportsNoDelta(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)

	findingsJSON, _ := json.Marshal(cleanReviewFindings())
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			return &agent.Result{Output: findingsJSON}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	sctx.StepResultID = stepResult.ID
	seedReviewRound(t, sctx, 1, "initial", headSHA)
	sctx.Fixing = true
	sctx.SkipFixExecution = true

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("expected one rereview call (fixer skipped), got %d", len(ag.calls))
	}
	prompt := ag.calls[0].Prompt
	for _, want := range []string{
		rereviewFocusHeader,
		"Round 1 of this step reviewed head " + headSHA,
		"(no files changed since that review)",
		rereviewFocusMarkerPast,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("unchanged-head rereview prompt missing %q:\n%s", want, prompt)
		}
	}
}

// The intent keeps full-coverage certification unchanged. A reviewer that
// follows the focus list exclusively must therefore leave the original file
// unverified, even after the focused coverage-completion turn.
func TestReviewStep_RereviewFocusSection_DeltaOnlyCoverageCannotCertify(t *testing.T) {
	t.Parallel()
	dir, baseSHA, priorHead := setupGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "review-fix.txt"), []byte("fixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "review-fix.txt")
	gitCmd(t, dir, "commit", "-m", "fix another file")
	headSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	ag := newCoverageScriptedAgent(t, []coverageScript{
		{onCompletion: false, output: coverageFindingJSON([]string{"review-fix.txt"})},
		{onCompletion: true, output: coverageFindingJSON([]string{"review-fix.txt"})},
	})
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	sctx.StepResultID = stepResult.ID
	seedReviewRound(t, sctx, 1, "initial", priorHead)
	sctx.Fixing = true
	sctx.SkipFixExecution = true

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.NeedsApproval {
		t.Fatalf("delta-only coverage certified the full change: %+v", outcome)
	}
	if len(outcome.ReviewablePaths) != 2 || outcome.ReviewablePaths[0] != "feature.txt" || outcome.ReviewablePaths[1] != "review-fix.txt" {
		t.Fatalf("focus narrowed the full review scope: %v", outcome.ReviewablePaths)
	}
	if len(outcome.ReviewedPaths) != 1 || outcome.ReviewedPaths[0] != "review-fix.txt" {
		t.Fatalf("completion fabricated coverage for the original file: %v", outcome.ReviewedPaths)
	}
}

// TestTruncateRereviewFocusStat pins the diff summary's bounding: a long stat
// is cut to the line cap with an explicit omission note, and an empty stat
// renders the no-delta line.
func TestTruncateRereviewFocusStat(t *testing.T) {
	t.Parallel()
	if got := truncateRereviewFocusStat(""); got != "  (no files changed since that review)\n" {
		t.Errorf("empty stat should render the no-delta line, got %q", got)
	}
	if got := truncateRereviewFocusStat("   \n"); got != "  (no files changed since that review)\n" {
		t.Errorf("blank stat should render the no-delta line, got %q", got)
	}
	short := " feature.txt | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)"
	if got := truncateRereviewFocusStat(short); !strings.Contains(got, " feature.txt | 2 +-") {
		t.Errorf("short stat should pass through, got %q", got)
	}
	var long strings.Builder
	for i := 0; i < maxRereviewFocusStatLines+25; i++ {
		long.WriteString("generated.txt | 2 +-\n")
	}
	got := truncateRereviewFocusStat(long.String())
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != maxRereviewFocusStatLines+1 {
		t.Errorf("truncated stat should hold the cap plus one omission note, got %d lines", len(lines))
	}
	if !strings.Contains(got, "omitted for length") {
		t.Errorf("truncated stat must disclose the cut:\n%s", got)
	}
}
