package citest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps"
	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps/internal/stepstest"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// pushDescendantRewrite advances the published branch to a commit the run
// produced nowhere, the way a native stack rebase or a partial stack merge
// does. The commit descends from the run's head, so it is attributable
// content - only its validation is missing.
func pushDescendantRewrite(t *testing.T, dir string) string {
	t.Helper()
	parent := stepstest.GitCmd(t, dir, "rev-parse", "HEAD")
	tree := stepstest.GitCmd(t, dir, "rev-parse", "HEAD^{tree}")
	rewritten := stepstest.GitCmd(t, dir, "commit-tree", tree, "-p", parent, "-m", "rewritten outside the run")
	stepstest.GitCmd(t, dir, "push", "origin", rewritten+":refs/heads/feature")
	return rewritten
}

// pushOrphanRewrite replaces the published branch with an unrelated root
// commit, so nothing in the run can account for it.
func pushOrphanRewrite(t *testing.T, dir string) string {
	t.Helper()
	tree := stepstest.GitCmd(t, dir, "rev-parse", "HEAD^{tree}")
	orphan := stepstest.GitCmd(t, dir, "commit-tree", tree, "-m", "stray root commit")
	stepstest.GitCmd(t, dir, "push", "origin", "--force", orphan+":refs/heads/feature")
	return orphan
}

func revalidateEnv(t *testing.T, checksJSON, prBase string) []string {
	env := stepstest.FakeCIGH(t, "OPEN", checksJSON)
	if prBase != "" {
		env = append(env, "FAKE_CLI_PR_BASE="+prBase)
	}
	return env
}

func revalidateContext(t *testing.T, dir, upstream, baseSHA, headSHA string, env []string) (*stepstest.MockAgent, *pipeline.StepContext, *[]string) {
	t.Helper()
	prURL := "https://github.com/test/repo/pull/42"
	ag := &stepstest.MockAgent{AgentName: "test"}
	sctx := stepstest.NewTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Env = env
	sctx.Run.PRURL = &prURL
	sctx.Run.Branch = "refs/heads/feature"
	sctx.Repo.UpstreamURL = upstream
	sctx.Config.CITimeout = 30 * time.Second
	logs := &[]string{}
	sctx.Log = func(s string) { *logs = append(*logs, s) }
	return ag, sctx, logs
}

// A head rewritten outside the run is adopted: the run fetches and re-verifies
// it from the push target, moves onto it through the same durable path a CI
// repair uses, aligns the run's base with the pull request's live base, and
// restarts at Review so the rewritten content is revalidated.
func TestCIStep_AdoptsAPublishedHeadRewriteAndRestartsAtReview(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIRerunRepo(t)
	rewritten := pushDescendantRewrite(t, dir)

	env := revalidateEnv(t, `[{"name":"test","state":"SUCCESS","bucket":"pass"}]`, "develop")
	_, sctx, logs := revalidateContext(t, dir, upstream, baseSHA, headSHA, env)

	outcome, err := (&steps.CIStep{}).Execute(sctx)
	if err != nil {
		t.Fatalf("adoption returned error: %v", err)
	}
	if outcome == nil || outcome.NeedsApproval {
		t.Fatalf("outcome = %#v, want a restart outcome", outcome)
	}
	if outcome.RestartFrom != types.StepReview {
		t.Fatalf("RestartFrom = %q, want review", outcome.RestartFrom)
	}
	joined := strings.Join(*logs, "\n")
	for _, want := range []string{
		"pull request branch head moved outside the run (",
		"adopted rewritten pull request head " + short(rewritten),
		`run base branch updated to the pull request's live base "develop" (was "")`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("log is missing %q:\n%s", want, joined)
		}
	}
	if sctx.Run.HeadSHA != rewritten {
		t.Fatalf("in-memory run head = %s, want the adopted %s", sctx.Run.HeadSHA, rewritten)
	}
	if got := stepstest.GitCmd(t, dir, "rev-parse", "HEAD"); got != rewritten {
		t.Fatalf("worktree head = %s, want the adopted %s", got, rewritten)
	}
	run, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.HeadSHA != rewritten {
		t.Fatalf("durable run head = %s, want the adopted %s", run.HeadSHA, rewritten)
	}
	if run.ReviewApprovedHeadSHA != nil {
		t.Fatalf("review approval = %q, want cleared for revalidation", *run.ReviewApprovedHeadSHA)
	}
	if run.PRBaseBranch == nil || *run.PRBaseBranch != "develop" {
		t.Fatalf("persisted base = %v, want the pull request's live base develop", run.PRBaseBranch)
	}
}

// A head the run cannot account for - an unrelated history nobody in the run
// produced - parks as ask-user instead of being adopted. Nothing is mutated:
// the recorded head, the worktree, and the remote stay exactly as they were.
func TestCIStep_ParksOnAPublishedHeadRewriteItCannotAttribute(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIRerunRepo(t)
	orphan := pushOrphanRewrite(t, dir)

	env := revalidateEnv(t, `[{"name":"test","state":"SUCCESS","bucket":"pass"}]`, "")
	_, sctx, logs := revalidateContext(t, dir, upstream, baseSHA, headSHA, env)

	outcome, err := (&steps.CIStep{}).Execute(sctx)
	if err != nil {
		t.Fatalf("park returned error: %v", err)
	}
	if outcome == nil || !outcome.NeedsApproval {
		t.Fatalf("outcome = %#v, want an ask-user park", outcome)
	}
	var findings types.Findings
	if err := json.Unmarshal([]byte(outcome.Findings), &findings); err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	if len(findings.Items) != 1 || findings.Items[0].Category != types.FindingCategoryCIHeadRewrite ||
		findings.Items[0].Action != types.ActionAskUser {
		t.Fatalf("findings = %+v, want one ci-head-rewrite ask-user finding", findings.Items)
	}
	if !strings.Contains(findings.Items[0].Description, "neither on the live head nor recorded as published") {
		t.Fatalf("finding = %q, want the attribution refusal", findings.Items[0].Description)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "not adopting "+short(orphan)) {
		t.Fatalf("log is missing the refusal:\n%s", strings.Join(*logs, "\n"))
	}
	if got := stepstest.GitCmd(t, dir, "rev-parse", "HEAD"); got != headSHA {
		t.Fatalf("worktree head moved to %s", got)
	}
	run, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.HeadSHA != headSHA {
		t.Fatalf("durable run head = %s, want it untouched", run.HeadSHA)
	}
}

// Adopting must never discard uncommitted work: a dirty run worktree parks
// for an operator decision even when the live head is otherwise attributable.
func TestCIStep_ParksOnAPublishedHeadRewriteWithADirtyWorktree(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIRerunRepo(t)
	rewritten := pushDescendantRewrite(t, dir)
	os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte("wip"), 0o644)

	env := revalidateEnv(t, `[{"name":"test","state":"SUCCESS","bucket":"pass"}]`, "")
	_, sctx, logs := revalidateContext(t, dir, upstream, baseSHA, headSHA, env)

	outcome, err := (&steps.CIStep{}).Execute(sctx)
	if err != nil {
		t.Fatalf("park returned error: %v", err)
	}
	if outcome == nil || !outcome.NeedsApproval {
		t.Fatalf("outcome = %#v, want an ask-user park", outcome)
	}
	if !strings.Contains(outcome.Findings, "would discard uncommitted work") {
		t.Fatalf("findings = %q, want the dirty-worktree refusal", outcome.Findings)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "not adopting "+short(rewritten)) {
		t.Fatalf("log is missing the refusal:\n%s", strings.Join(*logs, "\n"))
	}
	if got := stepstest.GitCmd(t, dir, "rev-parse", "HEAD"); got != headSHA {
		t.Fatalf("worktree head moved to %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "uncommitted.txt")); err != nil {
		t.Fatalf("uncommitted work was discarded: %v", err)
	}
}

// The checks read is the second observer of the pull request's live head: a
// rewrite that lands between the branch-head read and the checks read still
// cannot report checks for a head the run never validated. With the push target
// unreadable the adoption can verify nothing, so it adopts nothing and the
// monitor waits and reads again - the same warn-and-re-poll the branch-head
// read one call earlier does, rather than failing the whole run over a
// transient ls-remote blip. No check result is reported meanwhile.
func TestCIStep_ChecksReadWaitsRatherThanReportingChecksForAForeignHead(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIRerunRepo(t)

	// A head that exists only locally: the provider reports it, but the push
	// target no longer serves the branch at all.
	tree := stepstest.GitCmd(t, dir, "rev-parse", "HEAD^{tree}")
	ghost := stepstest.GitCmd(t, dir, "commit-tree", tree, "-m", "ghost rewrite")
	stepstest.GitCmd(t, dir, "tag", "nm-ghost", ghost)
	stepstest.GitCmd(t, dir, "push", "origin", "--delete", "feature")

	env := append(revalidateEnv(t, `[{"name":"test","state":"SUCCESS","bucket":"pass"}]`, ""), "FAKE_CLI_PR_HEAD_SHA="+ghost)
	_, sctx, logs := revalidateContext(t, dir, upstream, baseSHA, headSHA, env)

	polls := 0
	step := (&steps.CIStep{}).SetWaitForNextPoll(func(ctx context.Context, interval time.Duration) error {
		polls++
		if polls >= 2 {
			return errors.New("stop polling")
		}
		return nil
	})
	outcome, err := step.Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "stop polling") {
		t.Fatalf("outcome = %#v, err = %v, want the monitor to have kept polling", outcome, err)
	}
	joined := strings.Join(*logs, "\n")
	for _, want := range []string{"warning: could not read the published branch head", "not adopting anything this poll"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("log is missing %q:\n%s", want, joined)
		}
	}
	if got := stepstest.GitCmd(t, dir, "rev-parse", "HEAD"); got != headSHA {
		t.Fatalf("worktree head moved to %s", got)
	}
	run, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.HeadSHA != headSHA {
		t.Fatalf("durable run head = %s, want it untouched", run.HeadSHA)
	}
}

// An unreadable push target on its own only warns: the monitor keeps polling
// and the next poll reads the branch head again, instead of adopting or
// failing off one failed read.
func TestCIStep_UnreadablePublishedHeadOnlyWarns(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIRerunRepo(t)
	stepstest.GitCmd(t, dir, "push", "origin", "--delete", "feature")

	env := revalidateEnv(t, `[{"name":"test","state":"SUCCESS","bucket":"pass"}]`, "")
	_, sctx, logs := revalidateContext(t, dir, upstream, baseSHA, headSHA, env)

	ctx, cancel := context.WithCancel(context.Background())
	sctx.Ctx = ctx
	t.Cleanup(cancel)
	polls := 0
	step := (&steps.CIStep{}).SetWaitForNextPoll(func(ctx context.Context, interval time.Duration) error {
		polls++
		if polls >= 3 {
			return errors.New("stop polling")
		}
		return nil
	})
	_, err := step.Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "stop polling") {
		t.Fatalf("error = %v, want the monitor to keep polling", err)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "warning: could not read the published branch head") {
		t.Fatalf("log is missing the unreadable-target warning:\n%s", strings.Join(*logs, "\n"))
	}
	if polls < 3 {
		t.Fatalf("polls = %d, want the monitor to have kept polling", polls)
	}
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// The provider's live head and the push target are two different reads. When
// the provider names a foreign head but the push target still serves a head
// this run owns, there is nothing to revalidate: the monitor adopts nothing,
// restarts nothing, and keeps polling instead of cycling Review forever.
func TestCIStep_ForeignProviderHeadWithAnOwnedPushTargetAdoptsNothing(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIRerunRepo(t)

	tree := stepstest.GitCmd(t, dir, "rev-parse", "HEAD^{tree}")
	ghost := stepstest.GitCmd(t, dir, "commit-tree", tree, "-m", "head only the provider reports")

	env := append(revalidateEnv(t, `[{"name":"test","state":"SUCCESS","bucket":"pass"}]`, ""), "FAKE_CLI_PR_HEAD_SHA="+ghost)
	_, sctx, logs := revalidateContext(t, dir, upstream, baseSHA, headSHA, env)

	polls := 0
	step := (&steps.CIStep{}).SetWaitForNextPoll(func(ctx context.Context, interval time.Duration) error {
		polls++
		if polls >= 2 {
			return errors.New("stop polling")
		}
		return nil
	})
	outcome, err := step.Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "stop polling") {
		t.Fatalf("outcome = %#v, err = %v, want the monitor to have kept polling", outcome, err)
	}
	// The stall is what eventually parks on the generic CI timeout, so each
	// non-adopting poll has to name both heads to be diagnosable.
	joined := strings.Join(*logs, "\n")
	for _, want := range []string{"not adopting anything this poll", "reports head " + short(ghost), "push target serves " + short(headSHA)} {
		if !strings.Contains(joined, want) {
			t.Fatalf("log is missing %q:\n%s", want, joined)
		}
	}
	if got := stepstest.GitCmd(t, dir, "rev-parse", "HEAD"); got != headSHA {
		t.Fatalf("worktree head moved to %s", got)
	}
	run, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.HeadSHA != headSHA {
		t.Fatalf("durable run head = %s, want it untouched", run.HeadSHA)
	}
	if run.ReviewApprovedHeadSHA != nil && *run.ReviewApprovedHeadSHA != headSHA {
		t.Fatalf("review approval = %q, want it untouched", *run.ReviewApprovedHeadSHA)
	}
}

// A pull request whose live base the forge will not name fails the step
// closed, and closed means nothing moved: the live base is read before the
// adoption touches the worktree, the run head, or the review approval, so the
// run is still sitting on the head it validated when the step gives up.
func TestCIStep_AdoptionFailsClosedWhenTheLiveBaseIsUnreported(t *testing.T) {
	t.Parallel()
	dir, upstream, baseSHA, headSHA := setupCIRerunRepo(t)
	rewritten := pushDescendantRewrite(t, dir)

	env := revalidateEnv(t, `[{"name":"test","state":"SUCCESS","bucket":"pass"}]`, "-")
	_, sctx, _ := revalidateContext(t, dir, upstream, baseSHA, headSHA, env)
	approved := headSHA
	if err := sctx.DB.UpdateRunReviewApprovedHeadSHA(sctx.Run.ID, approved); err != nil {
		t.Fatal(err)
	}

	outcome, err := (&steps.CIStep{}).Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "no live base branch") {
		t.Fatalf("outcome = %#v, err = %v, want the base alignment to fail closed", outcome, err)
	}
	if got := stepstest.GitCmd(t, dir, "rev-parse", "HEAD"); got != headSHA {
		t.Fatalf("worktree head = %s, want the validated %s (the rewrite %s must not be adopted)", got, headSHA, rewritten)
	}
	run, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.PRBaseBranch != nil {
		t.Fatalf("persisted base = %q, want no base recorded", *run.PRBaseBranch)
	}
	if run.HeadSHA != headSHA {
		t.Fatalf("durable run head = %s, want the validated %s", run.HeadSHA, headSHA)
	}
	if run.ReviewApprovedHeadSHA == nil || *run.ReviewApprovedHeadSHA != approved {
		t.Fatalf("review approval = %v, want it untouched at %s", run.ReviewApprovedHeadSHA, approved)
	}
}
