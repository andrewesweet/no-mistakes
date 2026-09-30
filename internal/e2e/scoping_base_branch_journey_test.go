//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// stackedGateRepo builds a real stacked pull request against the harness's
// upstream and returns the default branch's tip and the parent layer's tip:
//
//	main            initial commit (mainTip)
//	 └─ epic/layer  epic.txt (epicTip), pushed to origin
//	     └─ <branch>  task.txt, the layer pushed through the gate
//
// pushedRepoConfig, when non-empty, is committed as the pushed branch's own
// .no-mistakes.yaml so a test can exercise what a contributor branch may and
// may not move.
func stackedGateRepo(t *testing.T, h *Harness, branch, trustedPRBaseBranch, pushedRepoConfig string) (mainTip, epicTip string) {
	t.Helper()
	ctx := context.Background()
	mustGit := func(args ...string) string {
		t.Helper()
		out, err := h.runGit(ctx, h.WorkDir, args...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	if trustedPRBaseBranch != "" {
		// The trusted default-branch copy of .no-mistakes.yaml is the only
		// place pr.base_branch can steer the gate's own diff scope.
		cfg := fmt.Sprintf("ignore_patterns:\n  - '*.generated.go'\n  - 'vendor/**'\nallow_repo_commands: true\npr:\n  base_branch: %s\n", trustedPRBaseBranch)
		if err := os.WriteFile(filepath.Join(h.WorkDir, ".no-mistakes.yaml"), []byte(cfg), 0o644); err != nil {
			t.Fatalf("write trusted repo config: %v", err)
		}
		mustGit("add", ".no-mistakes.yaml")
		mustGit("commit", "-m", "trust a stacked base branch")
		mustGit("push", "origin", "main")
	}
	mainTip = mustGit("rev-parse", "refs/heads/main")

	epicTip = h.CommitChange("epic/layer", "epic.txt", "epic\n", "epic layer")
	mustGit("push", "origin", "epic/layer")

	mustGit("checkout", "-b", branch, "epic/layer")
	if pushedRepoConfig != "" {
		if err := os.WriteFile(filepath.Join(h.WorkDir, ".no-mistakes.yaml"), []byte(pushedRepoConfig), 0o644); err != nil {
			t.Fatalf("write pushed repo config: %v", err)
		}
		mustGit("add", ".no-mistakes.yaml")
		mustGit("commit", "-m", "pushed branch config")
	}
	h.CommitChange(branch, "task.txt", "task\n", "task layer")
	return mainTip, epicTip
}

// scopedStepPrompts returns the Review, Test, Document and Lint prompts of a
// finished run, keyed by step name, failing when any is missing.
func scopedStepPrompts(t *testing.T, h *Harness) map[string]string {
	t.Helper()
	needles := map[string]string{
		"review":   "Review the code changes and return structured findings",
		"test":     "You are validating a code change by driving the product itself",
		"document": "Perform the combined documentation and lint housekeeping pass for this change.",
	}
	prompts := make(map[string]string, len(needles))
	invocations := h.AgentInvocations()
	for step, needle := range needles {
		prompt := findInvocationContaining(invocations, needle)
		if prompt == "" {
			t.Fatalf("no %s prompt in agent invocations:\n%s", step, dumpPrompts(invocations))
		}
		prompts[step] = prompt
	}
	return prompts
}

// TestStackedLayerIsValidatedAgainstItsParentBranch drives a real stacked pull
// request through a gate push: the trusted default branch names the parent
// layer as pr.base_branch, so Review, Test and the combined document+lint pass
// must all scope to the parent's tip and never see the parent layer's own file.
func TestStackedLayerIsValidatedAgainstItsParentBranch(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: cleanReviewScenario(t)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	const branch = "stacked-task"
	mainTip, epicTip := stackedGateRepo(t, h, branch, "epic/layer", "")
	h.PushToGate(branch)

	run := h.WaitForRun(branch, 120*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %s, want completed (error=%v)", run.Status, deref(run.Error))
	}

	for step, prompt := range scopedStepPrompts(t, h) {
		if !strings.Contains(prompt, "base commit: "+epicTip) {
			t.Errorf("%s prompt did not scope to the parent layer tip %s:\n%s", step, epicTip, prompt)
		}
		if strings.Contains(prompt, "base commit: "+mainTip) {
			t.Errorf("%s prompt scoped against the repository default branch tip %s:\n%s", step, mainTip, prompt)
		}
	}

	// Only the Review prompt enumerates the layer's changed files, so the
	// reviewable set is asserted there.
	review := scopedStepPrompts(t, h)["review"]
	if strings.Contains(review, "epic.txt") {
		t.Errorf("review prompt pulled in the parent layer's own file:\n%s", review)
	}
	if !strings.Contains(review, "task.txt") {
		t.Errorf("review prompt lost the layer's own file:\n%s", review)
	}
	if !strings.Contains(review, "base branch (scope base): epic/layer") {
		t.Errorf("review prompt did not name the effective base branch:\n%s", review)
	}

	t.Logf("review prompt scope block:\n%s", promptScopeBlock(review))

	// Eval replay restores only the repository default branch, so a round
	// reviewed against a parent layer must stay out of the local corpus
	// rather than be replayed and scored against a different diff.
	out, err := h.Run("eval", "capture", run.ID)
	if err == nil {
		t.Fatalf("eval capture accepted a round scoped to %s:\n%s", "epic/layer", out)
	}
	if !strings.Contains(out, `reviewed against base branch "epic/layer"`) {
		t.Fatalf("eval capture refusal did not name the scoping base branch:\n%s", out)
	}
	t.Logf("eval capture refusal:\n%s", out)
}

// TestPushedBranchCannotMoveTheValidationScope is the adversarial half: with
// allow_repo_commands opted in, a contributor branch may pick the PR target,
// but a pushed pr.base_branch must never move the gate's own diff scope - that
// would hide the branch's commits from Review, Test and Lint.
func TestPushedBranchCannotMoveTheValidationScope(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: cleanReviewScenario(t)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	const branch = "stacked-hostile"
	pushedCfg := "ignore_patterns:\n  - '*.generated.go'\n  - 'vendor/**'\nallow_repo_commands: true\npr:\n  base_branch: epic/layer\n"
	mainTip, epicTip := stackedGateRepo(t, h, branch, "", pushedCfg)
	h.PushToGate(branch)

	run := h.WaitForRun(branch, 120*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %s, want completed (error=%v)", run.Status, deref(run.Error))
	}

	for step, prompt := range scopedStepPrompts(t, h) {
		if !strings.Contains(prompt, "base commit: "+mainTip) {
			t.Errorf("%s prompt left the repository default branch scope %s:\n%s", step, mainTip, prompt)
		}
		if strings.Contains(prompt, "base commit: "+epicTip) {
			t.Errorf("SECURITY REGRESSION: %s prompt took its scope from the pushed branch's pr.base_branch (%s):\n%s", step, epicTip, prompt)
		}
	}

	// The Review prompt is the one that enumerates the changed files: the
	// parent layer's file must still be in the reviewable set.
	review := scopedStepPrompts(t, h)["review"]
	if !strings.Contains(review, "epic.txt") {
		t.Errorf("SECURITY REGRESSION: review prompt no longer covers the commits the pushed config tried to hide:\n%s", review)
	}
	if !strings.Contains(review, "base branch (scope base): main") {
		t.Errorf("review prompt did not name the repository default branch as the scope base:\n%s", review)
	}
}

// promptScopeBlock returns a prompt's Context block, the reviewer-visible
// record of what the step was scoped to.
func promptScopeBlock(prompt string) string {
	start := strings.Index(prompt, "Context:")
	if start < 0 {
		return prompt
	}
	block := prompt[start:]
	if end := strings.Index(block, "\n\n"); end > 0 {
		block = block[:end]
	}
	return block
}

// TestAxiRunBaseBranchScopesTheValidationSteps drives the per-run override the
// operator types: `axi run --base-branch <parent layer>` on a stacked layer
// must scope Review, Test and the document+lint pass to that parent branch,
// with no repository configuration involved.
func TestAxiRunBaseBranchScopesTheValidationSteps(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: cleanReviewScenario(t)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	const branch = "stacked-per-run"
	mainTip, epicTip := stackedGateRepo(t, h, branch, "", "")
	operator := h.AddWorktree(branch)
	if out, err := h.RunInDir(operator, "axi", "run",
		"--intent", "validate the top layer of a stacked pull request",
		"--base-branch", "epic/layer"); err != nil {
		t.Fatalf("axi run --base-branch: %v\n%s", err, out)
	}

	run := h.WaitForRun(branch, 120*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %s, want completed (error=%v)", run.Status, deref(run.Error))
	}

	for step, prompt := range scopedStepPrompts(t, h) {
		if !strings.Contains(prompt, "base commit: "+epicTip) {
			t.Errorf("%s prompt did not scope to the per-run base branch tip %s:\n%s", step, epicTip, prompt)
		}
		if strings.Contains(prompt, "base commit: "+mainTip) {
			t.Errorf("%s prompt scoped against the repository default branch tip %s:\n%s", step, mainTip, prompt)
		}
	}
	review := scopedStepPrompts(t, h)["review"]
	if strings.Contains(review, "epic.txt") {
		t.Errorf("review prompt pulled in the parent layer's own file:\n%s", review)
	}
	t.Logf("review prompt scope block:\n%s", promptScopeBlock(review))
}
