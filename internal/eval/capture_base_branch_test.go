package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const capturableFindings = `{"findings":[{"id":"real-bug","severity":"error","file":"main.go","line":3,"description":"bug","action":"ask-user","review_scope":"source"}],"risk_level":"high","risk_rationale":"bug","risk_scope":"source-or-external"}`

// commitTrustedRepoConfig advances the default branch with a new
// .no-mistakes.yaml and returns that commit, so a round can pin it as its
// trusted-config SHA the way a real run does.
func commitTrustedRepoConfig(t *testing.T, ctx context.Context, repo *db.Repo, yaml string) string {
	t.Helper()
	workDir := repo.WorkingPath
	mustGit(t, ctx, workDir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(workDir, ".no-mistakes.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, ctx, workDir, "add", ".no-mistakes.yaml")
	mustGit(t, ctx, workDir, "commit", "-m", "trusted config")
	mustGit(t, ctx, workDir, "push", "origin", "main")
	return mustGit(t, ctx, workDir, "rev-parse", "HEAD")
}

// captureSecondRun records one review round for another run on the same
// repository and returns the capture result. trustedSHA pins the round's
// trusted configuration; recordedRepoYAML is the effective repo config frozen
// with the round (under allow_repo_commands that copy carries the PUSHED
// pr.base_branch, which targets the PR and scopes nothing).
func captureSecondRun(t *testing.T, ctx context.Context, p *paths.Paths, sourceDB *db.DB, repo *db.Repo, branch, headSHA, baseSHA, runBaseBranch, trustedSHA, recordedRepoYAML string) ([]Case, error) {
	t.Helper()
	run, err := sourceDB.InsertRunWithIntent(repo.ID, branch, headSHA, baseSHA, nil, runBaseBranch)
	if err != nil {
		t.Fatal(err)
	}
	step, err := sourceDB.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	findings := capturableFindings
	round, err := sourceDB.InsertReviewStepRoundWithProvenance(step.ID, 1, "initial", &findings, nil, headSHA, headSHA, trustedSHA, []byte("{}\n"), []byte(recordedRepoYAML), 50)
	if err != nil {
		t.Fatal(err)
	}
	selected := `["real-bug"]`
	if err := sourceDB.SetStepRoundSelection(round.ID, &selected, db.RoundSelectionSourceUser); err != nil {
		t.Fatal(err)
	}
	store, err := Open(p.EvalDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	return Capture(ctx, store, p, sourceDB, run.ID)
}

// A review scoped against a base branch other than the repository default
// cannot be replayed faithfully: restoreCase pins only
// refs/remotes/origin/<DefaultBranch>, so the replayed review would be scored
// against a different diff than the captured one. Capture refuses such a run
// through the unsupported-case path instead. The scoping base is resolved the
// way the validation steps resolve it - per-run override, else the TRUSTED
// pr.base_branch - so a trusted value the round's own effective config does not
// carry (the allow_repo_commands case) still refuses.
func TestCaptureRefusesARunScopedToAnotherBaseBranch(t *testing.T) {
	for _, tc := range []struct {
		name             string
		runBaseBranch    string
		trustedYAML      string
		recordedRepoYAML string
		wantBranch       string
	}{
		{name: "per-run override", runBaseBranch: "epic/feature", recordedRepoYAML: "ignore_patterns: ['vendor']\n", wantBranch: "epic/feature"},
		{name: "trusted pr.base_branch", trustedYAML: "pr:\n  base_branch: develop\n", recordedRepoYAML: "ignore_patterns: ['vendor']\n", wantBranch: "develop"},
		{name: "per-run override wins over the trusted value", runBaseBranch: "epic/feature", trustedYAML: "pr:\n  base_branch: develop\n", recordedRepoYAML: "ignore_patterns: ['vendor']\n", wantBranch: "epic/feature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p, sourceDB, sourceRun, repo, _ := setupCapturedRun(t, ctx)
			defer sourceDB.Close()

			trustedSHA := sourceRun.BaseSHA
			if tc.trustedYAML != "" {
				trustedSHA = commitTrustedRepoConfig(t, ctx, repo, tc.trustedYAML)
			}

			_, err := captureSecondRun(t, ctx, p, sourceDB, repo, "feature/stacked", sourceRun.HeadSHA, sourceRun.BaseSHA, tc.runBaseBranch, trustedSHA, tc.recordedRepoYAML)
			if !errors.Is(err, ErrNoCapturableReview) {
				t.Fatalf("capture error = %v, want ErrNoCapturableReview", err)
			}
			if !strings.Contains(err.Error(), tc.wantBranch) {
				t.Fatalf("capture error = %q, want the scoping base branch %q named", err, tc.wantBranch)
			}
			store, err := Open(p.EvalDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			cases, err := store.ListCases("all")
			if err != nil {
				t.Fatal(err)
			}
			if len(cases) != 0 {
				t.Fatalf("refused capture registered %d cases", len(cases))
			}
		})
	}
}

// A pr.base_branch that reached the round's effective config from the PUSHED
// branch under allow_repo_commands targets the PR only; scoping stayed on the
// repository default, so the case is faithfully replayable and must capture.
func TestCaptureAcceptsAPushedOnlyPRBaseBranch(t *testing.T) {
	ctx := context.Background()
	p, sourceDB, sourceRun, repo, _ := setupCapturedRun(t, ctx)
	defer sourceDB.Close()

	cases, err := captureSecondRun(t, ctx, p, sourceDB, repo, "feature/pushed-target", sourceRun.HeadSHA, sourceRun.BaseSHA, "", sourceRun.BaseSHA, "pr:\n  base_branch: develop\n")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("captured cases = %d, want 1", len(cases))
	}
}

// Ordinary path: a run on the repository default branch, with no base branch
// configured anywhere, still captures.
func TestCaptureAcceptsADefaultBranchScopedRun(t *testing.T) {
	ctx := context.Background()
	p, sourceDB, sourceRun, repo, _ := setupCapturedRun(t, ctx)
	defer sourceDB.Close()

	cases, err := captureSecondRun(t, ctx, p, sourceDB, repo, "feature/plain", sourceRun.HeadSHA, sourceRun.BaseSHA, "", sourceRun.BaseSHA, "ignore_patterns: ['vendor']\n")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("captured cases = %d, want 1", len(cases))
	}
}

// A round whose pinned trusted-config commit is no longer in the gate (an
// orphaned commit after the default branch was rewritten, or a pruned object)
// cannot establish the base branch its review scoped against, so it degrades to
// the documented skip rather than failing the whole capture with a bare error.
func TestCaptureSkipsARoundWhoseTrustedConfigCommitIsGone(t *testing.T) {
	ctx := context.Background()
	p, sourceDB, sourceRun, repo, _ := setupCapturedRun(t, ctx)
	defer sourceDB.Close()

	_, err := captureSecondRun(t, ctx, p, sourceDB, repo, "feature/pruned-trust", sourceRun.HeadSHA, sourceRun.BaseSHA, "", strings.Repeat("0", 40), "ignore_patterns: ['vendor']\n")
	if !errors.Is(err, ErrNoCapturableReview) {
		t.Fatalf("capture error = %v, want ErrNoCapturableReview so auto-capture reports a skip", err)
	}
}
