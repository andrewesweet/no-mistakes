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
)

// setupStackedRepo builds a real stacked-branch repository against a bare
// upstream:
//
//	main          base commit (mainTip)
//	 └─ epic/feature   its own commit (epicTip), pushed
//	     └─ task       its own commit (headTip), the layer under validation
//
// epic/feature carries a commit absent from main, so a scope computed against
// main wrongly contains the epic's file while a scope computed against
// epic/feature sees only the task layer.
func setupStackedRepo(t *testing.T) (dir, upstream, mainTip, epicTip, headTip string) {
	t.Helper()
	upstream = t.TempDir()
	gitCmd(t, upstream, "init", "--bare", "-b", "main")
	gitCmd(t, upstream, "config", "gc.auto", "0")

	dir = t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "base")
	mainTip = gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "epic/feature")
	if err := os.WriteFile(filepath.Join(dir, "epic.txt"), []byte("epic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "epic layer")
	epicTip = gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "epic/feature")

	gitCmd(t, dir, "checkout", "-b", "task")
	if err := os.WriteFile(filepath.Join(dir, "task.txt"), []byte("task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "task layer")
	headTip = gitCmd(t, dir, "rev-parse", "HEAD")
	return dir, upstream, mainTip, epicTip, headTip
}

// stackedSctx wraps newTestContextWithDBRecords for the stacked fixture and
// selects epic/feature as the run's per-run base branch.
func stackedSctx(t *testing.T, ag agent.Agent, dir, upstream, baseSHA, headSHA string, cmds config.Commands) *pipeline.StepContext {
	t.Helper()
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, cmds)
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/task"
	runBase := "epic/feature"
	sctx.Run.PRBaseBranch = &runBase
	return sctx
}

// assertScopedBaseCommit pins one step prompt to the layer's own base: the
// base commit must be the merge-base with epic/feature's live tip (here the
// epic tip itself), never the repository default branch's tip.
func assertScopedBaseCommit(t *testing.T, prompt, epicTip, mainTip string) {
	t.Helper()
	if !strings.Contains(prompt, "base commit: "+epicTip) {
		t.Errorf("prompt did not scope to the per-run base tip %s:\n%s", epicTip, prompt)
	}
	if strings.Contains(prompt, "base commit: "+mainTip) {
		t.Errorf("prompt scoped against the repository default branch tip %s:\n%s", mainTip, prompt)
	}
}

// assertScopedPrompt additionally pins the prompt's scope-base branch line to
// the effective base branch (only prompts that named the repository default
// branch today carry it: the Review asking and fix prompts, and Document).
func assertScopedPrompt(t *testing.T, prompt, epicTip, mainTip string) {
	t.Helper()
	assertScopedBaseCommit(t, prompt, epicTip, mainTip)
	if !strings.Contains(prompt, "base branch (scope base): epic/feature") {
		t.Errorf("prompt did not name the effective base branch:\n%s", prompt)
	}
	if strings.Contains(prompt, "base branch (scope base): main") {
		t.Errorf("prompt still names the repository default branch as the scope base:\n%s", prompt)
	}
}

func TestReviewStep_ScopesToPerRunBaseBranch(t *testing.T) {
	t.Parallel()
	dir, upstream, mainTip, epicTip, headTip := setupStackedRepo(t)

	cleanReview := `{"findings":[],"reviewed_paths":["task.txt"],"risk_level":"low","risk_rationale":"clean","risk_scope":"source-or-external"}`
	var prompt string
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			prompt = opts.Prompt
			return &agent.Result{Output: json.RawMessage(cleanReview)}, nil
		},
	}
	sctx := stackedSctx(t, ag, dir, upstream, mainTip, headTip, config.Commands{})

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NeedsApproval {
		t.Fatalf("unexpected approval: %s", outcome.Findings)
	}
	// The layer's reviewable set must hold only the task layer's own file;
	// the epic's file belongs to the parent branch and must not be reviewed
	// (or covered) here.
	if len(outcome.ReviewablePaths) != 1 || outcome.ReviewablePaths[0] != "task.txt" {
		t.Fatalf("reviewable paths = %v, want only task.txt", outcome.ReviewablePaths)
	}
	if strings.Contains(prompt, "epic.txt") {
		t.Errorf("review prompt pulled in the parent branch's file:\n%s", prompt)
	}
	assertScopedPrompt(t, prompt, epicTip, mainTip)
}

func TestReviewStep_ScopesToTrustedPRBaseBranch(t *testing.T) {
	t.Parallel()
	dir, upstream, mainTip, epicTip, headTip := setupStackedRepo(t)

	cleanReview := `{"findings":[],"reviewed_paths":["task.txt"],"risk_level":"low","risk_rationale":"clean","risk_scope":"source-or-external"}`
	var prompt string
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			prompt = opts.Prompt
			return &agent.Result{Output: json.RawMessage(cleanReview)}, nil
		},
	}
	sctx := stackedSctx(t, ag, dir, upstream, mainTip, headTip, config.Commands{})
	// No per-run base: the trusted pr.base_branch selects the scope branch.
	sctx.Run.PRBaseBranch = nil
	sctx.Config.PR.ScopingBaseBranch = "epic/feature"

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NeedsApproval {
		t.Fatalf("unexpected approval: %s", outcome.Findings)
	}
	if len(outcome.ReviewablePaths) != 1 || outcome.ReviewablePaths[0] != "task.txt" {
		t.Fatalf("reviewable paths = %v, want only task.txt", outcome.ReviewablePaths)
	}
	assertScopedPrompt(t, prompt, epicTip, mainTip)
}

func TestReviewStep_FixTurnScopesToPerRunBaseBranch(t *testing.T) {
	t.Parallel()
	dir, upstream, mainTip, epicTip, headTip := setupStackedRepo(t)

	cleanReview := `{"findings":[],"reviewed_paths":["task.txt"],"risk_level":"low","risk_rationale":"clean","risk_scope":"source-or-external"}`
	var fixPrompt string
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			if fixPrompt == "" {
				fixPrompt = opts.Prompt
				return &agent.Result{Output: json.RawMessage(`{"summary":"fixed the finding"}`)}, nil
			}
			return &agent.Result{Output: json.RawMessage(cleanReview)}, nil
		},
	}
	sctx := stackedSctx(t, ag, dir, upstream, mainTip, headTip, config.Commands{})
	sctx.Fixing = true
	sctx.PreviousFindings = `{"items":[{"id":"r-1","severity":"warning","action":"auto-fix","description":"tidy task.txt"}],"summary":"one finding"}`

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NeedsApproval {
		t.Fatalf("unexpected approval: %s", outcome.Findings)
	}
	if !strings.Contains(fixPrompt, "Investigate previous review findings") {
		t.Fatalf("first agent turn was not the fix turn:\n%s", fixPrompt)
	}
	assertScopedPrompt(t, fixPrompt, epicTip, mainTip)
}

func TestTestStep_ScopesToPerRunBaseBranch(t *testing.T) {
	t.Parallel()
	dir, upstream, mainTip, epicTip, headTip := setupStackedRepo(t)

	var prompt string
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			prompt = opts.Prompt
			return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"","tested":["ok"],"testing_summary":"ok","artifacts":[],"scenarios":[{"name":"user runs the command","result":"pass","live":true,"evidence":"ok","reason":""}],"verdict":"go"}`)}, nil
		},
	}
	sctx := stackedSctx(t, ag, dir, upstream, mainTip, headTip, config.Commands{})

	if _, err := (&TestStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	assertScopedBaseCommit(t, prompt, epicTip, mainTip)
}

func TestDocumentStep_ScopesToPerRunBaseBranch(t *testing.T) {
	t.Parallel()
	dir, upstream, mainTip, epicTip, headTip := setupStackedRepo(t)

	var prompt string
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			prompt = opts.Prompt
			return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"docs current"}`)}, nil
		},
	}
	sctx := stackedSctx(t, ag, dir, upstream, mainTip, headTip, config.Commands{})

	if _, err := (&DocumentStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	assertScopedPrompt(t, prompt, epicTip, mainTip)
}

func TestLintStep_ScopesToPerRunBaseBranch(t *testing.T) {
	t.Parallel()
	dir, upstream, mainTip, epicTip, headTip := setupStackedRepo(t)

	var prompt string
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			prompt = opts.Prompt
			return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"lint clean"}`)}, nil
		},
	}
	sctx := stackedSctx(t, ag, dir, upstream, mainTip, headTip, config.Commands{})

	if _, err := (&LintStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	// The Lint agent prompt carries the base commit but no scope-base branch
	// line today, so only the commit assertion applies here.
	assertScopedBaseCommit(t, prompt, epicTip, mainTip)
}

func TestCustomGateStep_FixTurnScopesToPerRunBaseBranch(t *testing.T) {
	t.Parallel()
	dir, upstream, mainTip, epicTip, headTip := setupStackedRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headTip)

	ag := &mockAgent{name: "test", runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(dir, "gate-satisfied.txt"), []byte("ok"), 0o644); err != nil {
			return nil, err
		}
		return &agent.Result{Output: json.RawMessage(`{"summary":"satisfy the gate"}`)}, nil
	}}
	sctx := stackedSctx(t, ag, dir, upstream, mainTip, headTip, config.Commands{})
	sctx.Fixing = true
	sctx.PreviousFindings = `{"items":[{"id":"g-1","severity":"error","description":"gate \"mutation-budget\" failed with exit code 7"}],"summary":"mutation score 41% below the 60% budget"}`

	step := &CustomGateStep{Gate: config.Gate{
		Name:    "mutation-budget",
		After:   "test",
		Command: fileGateCommand("gate-satisfied.txt"),
	}}
	outcome, err := step.Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NeedsApproval {
		t.Fatalf("unexpected approval: %s", outcome.Findings)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("agent calls = %d, want exactly the fix turn", len(ag.calls))
	}
	assertScopedBaseCommit(t, ag.calls[0].Prompt, epicTip, mainTip)
}

// Under allow_repo_commands the pushed branch supplies pr.base_branch for PR
// targeting, but it must never move the gate's own diff scope: a pushed value
// that narrowed the scope would hide the commits it points past from Review,
// and from the trusted review.path_instructions matched against the changed
// set. Scoping keeps using the trusted value (here absent), so the layer is
// reviewed against the repository default branch and the parent layer's file
// stays in scope.
func TestReviewStep_PushedPRBaseBranchDoesNotMoveTheScope(t *testing.T) {
	t.Parallel()
	dir, upstream, mainTip, epicTip, headTip := setupStackedRepo(t)

	cleanReview := `{"findings":[],"reviewed_paths":["epic.txt","task.txt"],"risk_level":"low","risk_rationale":"clean","risk_scope":"source-or-external"}`
	var prompt string
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			prompt = opts.Prompt
			return &agent.Result{Output: json.RawMessage(cleanReview)}, nil
		},
	}
	sctx := stackedSctx(t, ag, dir, upstream, mainTip, headTip, config.Commands{})
	sctx.Run.PRBaseBranch = nil
	// What allow_repo_commands lets the pushed branch set, with no trusted
	// default-branch value behind it.
	sctx.Config.PR.BaseBranch = "epic/feature"
	sctx.Config.PR.ScopingBaseBranch = ""
	sctx.Config.Review.PathInstructions = []config.PathInstruction{{Path: "epic.txt", Instructions: "audit the epic layer"}}

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NeedsApproval {
		t.Fatalf("unexpected approval: %s", outcome.Findings)
	}
	if len(outcome.ReviewablePaths) != 2 {
		t.Fatalf("reviewable paths = %v, want both the epic and task files", outcome.ReviewablePaths)
	}
	if !strings.Contains(prompt, "base commit: "+mainTip) {
		t.Errorf("a pushed pr.base_branch moved the scope off the repository default tip %s:\n%s", mainTip, prompt)
	}
	if strings.Contains(prompt, "base commit: "+epicTip) {
		t.Errorf("a pushed pr.base_branch narrowed the review scope to %s:\n%s", epicTip, prompt)
	}
	if !strings.Contains(prompt, "audit the epic layer") {
		t.Errorf("trusted path instructions for the hidden file were not selected:\n%s", prompt)
	}
}
