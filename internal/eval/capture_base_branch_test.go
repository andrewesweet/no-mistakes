package eval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const capturableFindings = `{"findings":[{"id":"real-bug","severity":"error","file":"main.go","line":3,"description":"bug","action":"ask-user","review_scope":"source"}],"risk_level":"high","risk_rationale":"bug","risk_scope":"source-or-external"}`

// A review scoped against a base branch other than the repository default
// cannot be replayed faithfully: restoreCase pins only
// refs/remotes/origin/<DefaultBranch>, so the replayed review would be scored
// against a different diff than the captured one. Capture refuses such a run
// through the unsupported-case path instead.
func TestCaptureRefusesARunScopedToAnotherBaseBranch(t *testing.T) {
	for _, tc := range []struct {
		name           string
		runBaseBranch  string
		repoConfigYAML string
		wantBranch     string
	}{
		{name: "per-run override", runBaseBranch: "epic/feature", repoConfigYAML: "ignore_patterns: ['vendor']\n", wantBranch: "epic/feature"},
		{name: "repository pr.base_branch", repoConfigYAML: "pr:\n  base_branch: develop\n", wantBranch: "develop"},
		{name: "per-run override wins over config", runBaseBranch: "epic/feature", repoConfigYAML: "pr:\n  base_branch: develop\n", wantBranch: "epic/feature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p, sourceDB, sourceRun, repo, _ := setupCapturedRun(t, ctx)
			defer sourceDB.Close()

			stackedRun, err := sourceDB.InsertRunWithIntent(repo.ID, "feature/stacked", sourceRun.HeadSHA, sourceRun.BaseSHA, nil, tc.runBaseBranch)
			if err != nil {
				t.Fatal(err)
			}
			step, err := sourceDB.InsertStepResult(stackedRun.ID, types.StepReview)
			if err != nil {
				t.Fatal(err)
			}
			findings := capturableFindings
			round, err := sourceDB.InsertReviewStepRoundWithProvenance(step.ID, 1, "initial", &findings, nil, stackedRun.HeadSHA, stackedRun.HeadSHA, stackedRun.BaseSHA, []byte("{}\n"), []byte(tc.repoConfigYAML), 50)
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

			_, err = Capture(ctx, store, p, sourceDB, stackedRun.ID)
			if !errors.Is(err, ErrNoCapturableReview) {
				t.Fatalf("capture error = %v, want ErrNoCapturableReview", err)
			}
			if !strings.Contains(err.Error(), tc.wantBranch) {
				t.Fatalf("capture error = %q, want the scoping base branch %q named", err, tc.wantBranch)
			}
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

// The ordinary path is unaffected: a run on the repository default branch, with
// or without a pr.base_branch naming that same default, still captures.
func TestCaptureAcceptsABaseBranchEqualToTheRepositoryDefault(t *testing.T) {
	ctx := context.Background()
	p, sourceDB, sourceRun, repo, _ := setupCapturedRun(t, ctx)
	defer sourceDB.Close()

	run, err := sourceDB.InsertRunWithIntent(repo.ID, "feature/explicit-main", sourceRun.HeadSHA, sourceRun.BaseSHA, nil, "main")
	if err != nil {
		t.Fatal(err)
	}
	step, err := sourceDB.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	findings := capturableFindings
	round, err := sourceDB.InsertReviewStepRoundWithProvenance(step.ID, 1, "initial", &findings, nil, run.HeadSHA, run.HeadSHA, run.BaseSHA, []byte("{}\n"), []byte("pr:\n  base_branch: main\n"), 50)
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

	cases, err := Capture(ctx, store, p, sourceDB, run.ID)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(cases) != 1 || cases[0].SourceRunID != run.ID {
		t.Fatalf("captured cases = %#v, want one case for run %q", cases, run.ID)
	}
}
