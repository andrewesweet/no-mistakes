package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// Revalidation of a head rewritten outside the run. While the CI monitor
// polls an open pull request, every observation of the branch head is checked
// against the set of heads this run owns: the recorded head, the durable
// last-pushed head, and the caller worktree's HEAD. A head the run owns
// nowhere means the branch was rewritten outside the run - typically a native
// stack rebase or a partial stack merge - and continuing to report checks for
// it would validate a commit the run never reviewed. The monitor then adopts
// the live head through the same durable path a CI repair uses and restarts
// the run at Review.

// maxAdoptionFetchAttempts bounds how often the adoption path re-reads and
// re-fetches the pull request branch while it keeps moving. Two attempts
// tolerate one move between observation and fetch; a branch still moving
// after that refuses rather than adopting a head nobody can name twice.
const maxAdoptionFetchAttempts = 2

// ownValidatedHeads lists every head this run can call its own: the recorded
// head first (it is the primary head merged-proof validation expects), then
// the durable last-pushed head, then the caller worktree's HEAD. A read
// failure simply omits that source - a head that cannot be proven owned is
// not owned.
func (s *CIStep) ownValidatedHeads(sctx *pipeline.StepContext) []string {
	heads := []string{strings.TrimSpace(sctx.Run.HeadSHA)}
	if run, err := sctx.DB.GetRun(sctx.Run.ID); err == nil && run != nil && run.LastPushedSHA != nil {
		if pushed := strings.TrimSpace(*run.LastPushedSHA); pushed != "" && !containsFold(heads, pushed) {
			heads = append(heads, pushed)
		}
	}
	if worktreeHead, err := stepGitHeadSHA(sctx); err == nil {
		if head := strings.TrimSpace(worktreeHead); head != "" && !containsFold(heads, head) {
			heads = append(heads, head)
		}
	}
	return heads
}

// ciRunOwnsHead reports whether head is one of the run's own heads. The
// durable record is re-read on every call: publishRunHead advances it through
// the database, so the in-memory run alone would miss a repair this run
// published.
func (s *CIStep) ciRunOwnsHead(sctx *pipeline.StepContext, head string) bool {
	for _, owned := range s.ownValidatedHeads(sctx) {
		if owned != "" && strings.EqualFold(owned, strings.TrimSpace(head)) {
			return true
		}
	}
	return false
}

// adoptableHeadRefusal names why the run worktree cannot be adopted onto the
// live head, or "" when adoption is safe. Two conditions refuse: the worktree
// is dirty (a reset would silently discard work this run never recorded), or
// the worktree holds commits that are neither on the live head nor recorded
// as published - content no observer can attribute to this run, which only an
// operator may decide about.
func adoptableHeadRefusal(sctx *pipeline.StepContext, liveHead string) string {
	status, err := stepGitRun(sctx, "status", "--porcelain")
	if err != nil {
		return fmt.Sprintf("the run worktree state could not be read: %v", err)
	}
	if strings.TrimSpace(status) != "" {
		return "the run worktree is not clean, so adopting the live head would discard uncommitted work"
	}
	worktreeHead, err := stepGitHeadSHA(sctx)
	if err != nil {
		return fmt.Sprintf("the run worktree HEAD could not be resolved: %v", err)
	}
	exclusions := []string{"rev-list", worktreeHead, "^" + strings.TrimSpace(liveHead)}
	if run, err := sctx.DB.GetRun(sctx.Run.ID); err == nil && run != nil && run.LastPushedSHA != nil {
		if pushed := strings.TrimSpace(*run.LastPushedSHA); pushed != "" && !strings.EqualFold(pushed, liveHead) {
			exclusions = append(exclusions, "^"+pushed)
		}
	}
	orphans, err := stepGitRun(sctx, exclusions...)
	if err != nil {
		return fmt.Sprintf("the worktree's relationship to the live head could not be proven: %v", err)
	}
	if strings.TrimSpace(orphans) != "" {
		return "the run worktree holds commits that are neither on the live head nor recorded as published"
	}
	return ""
}

// parkPublishedHeadRewrite parks the run as ask-user: no head is adopted, the
// recorded head, the worktree head, and the live head are all named in the
// finding, and the decision is the operator's.
func parkPublishedHeadRewrite(recorded, worktreeHead, liveHead, reason string) *pipeline.StepOutcome {
	findings := Findings{
		Summary: "The pull request branch moved to a head this run owns nowhere; decide before anything is adopted",
		Items: []types.Finding{{
			Severity: types.FindingSeverityWarning,
			Action:   types.ActionAskUser,
			Category: types.FindingCategoryCIHeadRewrite,
			Description: fmt.Sprintf(
				"the pull request branch head is now %s, but the run recorded %s and its worktree sits at %s: %s. Nothing was adopted and no files or refs were changed; adopt the live head only once the stray commits are accounted for.",
				shortSHA(liveHead), shortSHA(recorded), shortSHA(worktreeHead), reason,
			),
		}},
	}
	encoded, _ := types.MarshalFindingsJSON(findings)
	return &pipeline.StepOutcome{NeedsApproval: true, Findings: encoded}
}

// adoptPublishedHeadRewrite moves the run onto the rewritten head the way a
// CI repair revalidates: the live head is fetched from the push target and
// verified again just before the restart, a fetched head the run already owns
// is not adopted at all (nil outcome: the caller keeps polling), the guard
// refuses anything unattributable, recordLocalRepair advances the run head durably and clears
// the review approval, the persisted per-run base is aligned with the pull
// request's live base, and the outcome restarts the run at Review. Intent and
// Rebase are deliberately skipped (their content is base-relative and the
// restart re-decides it), and the PR step will only refresh the body of the
// existing pull request.
func (s *CIStep) adoptPublishedHeadRewrite(sctx *pipeline.StepContext, host scm.Host, pr *scm.PR, liveHead string) (*pipeline.StepOutcome, error) {
	recorded := strings.TrimSpace(sctx.Run.HeadSHA)
	clearCIMonitorReady(sctx)
	sctx.Log(fmt.Sprintf("pull request branch head moved outside the run (%s -> %s); adopting the live head and restarting at Review", shortSHA(recorded), shortSHA(liveHead)))

	target, err := fetchVerifiedPublishedHead(sctx)
	if err != nil {
		return nil, err
	}
	if s.ciRunOwnsHead(sctx, target) {
		sctx.Log(fmt.Sprintf("not adopting %s: the push target serves a head this run owns, so there is nothing to revalidate", shortSHA(target)))
		return nil, nil
	}
	worktreeHead, err := stepGitHeadSHA(sctx)
	if err != nil {
		return nil, fmt.Errorf("resolve the run worktree head before adopting %s: %w", shortSHA(target), err)
	}
	if reason := adoptableHeadRefusal(sctx, target); reason != "" {
		sctx.Log(fmt.Sprintf("not adopting %s: %s", shortSHA(target), reason))
		return parkPublishedHeadRewrite(recorded, worktreeHead, target, reason), nil
	}
	if _, err := stepGitRun(sctx, "reset", "--hard", target); err != nil {
		return nil, fmt.Errorf("move the run worktree to the adopted head %s: %w", shortSHA(target), err)
	}
	if _, err := s.recordLocalRepair(sctx, target, fmt.Sprintf("adopted rewritten pull request head %s (superseding %s); revalidation from Review required", shortSHA(target), shortSHA(recorded))); err != nil {
		return nil, err
	}
	if err := alignRunBaseWithLivePRBase(sctx, host, pr); err != nil {
		return nil, err
	}
	return &pipeline.StepOutcome{RestartFrom: types.StepReview}, nil
}

// fetchVerifiedPublishedHead fetches the pull request branch from the push
// target and returns its freshly verified head. The fetch writes only
// FETCH_HEAD - no branch or worktree ref moves until the caller commits to
// the adoption - and each attempt re-reads the live head first, so the
// returned head is the branch's newest observable tip, not a stale one.
func fetchVerifiedPublishedHead(sctx *pipeline.StepContext) (string, error) {
	pushURL := resolvePushURL(sctx)
	branch := strings.TrimPrefix(normalizedBranchRef(sctx.Run.Branch), "refs/heads/")
	var lastErr error
	for attempt := 0; attempt < maxAdoptionFetchAttempts; attempt++ {
		observed, err := publishedBranchHead(sctx)
		if err != nil {
			return "", fmt.Errorf("re-read the pull request branch head before adopting it: %w", err)
		}
		ctx, cancel := context.WithTimeout(sctx.Ctx, defaultPublishedHeadResolveWindow)
		bounded := *sctx
		bounded.Ctx = ctx
		if _, err := stepGitRun(&bounded, "fetch", pushURL, branch); err != nil {
			cancel()
			return "", fmt.Errorf("fetch %s from the push target: %w", branch, err)
		}
		fetched, err := stepGitRun(&bounded, "rev-parse", "FETCH_HEAD^{commit}")
		cancel()
		if err != nil {
			return "", fmt.Errorf("resolve the fetched pull request branch head: %w", err)
		}
		fetched = strings.TrimSpace(fetched)
		if fetched == observed {
			return fetched, nil
		}
		lastErr = fmt.Errorf("the pull request branch moved from %s to %s while it was being fetched", shortSHA(observed), shortSHA(fetched))
	}
	return "", lastErr
}

// alignRunBaseWithLivePRBase sets the run's persisted per-run base to the
// pull request's live forge base whenever the two differ, before any restart
// at Review from the CI step. This covers repair revalidation as well as the
// adoption path: without it, a restarted Review scopes the change against a
// branch the pull request no longer targets, and a retargeted layer loses its
// layer-only diff. A base the forge will not report fails the step closed
// rather than restarting onto a base nobody could name.
func alignRunBaseWithLivePRBase(sctx *pipeline.StepContext, host scm.Host, pr *scm.PR) error {
	reader, ok := host.(scm.PRBaseBranchReader)
	if !ok {
		return nil
	}
	liveBase, err := reader.GetPRBaseBranch(sctx.Ctx, pr)
	if err != nil {
		return fmt.Errorf("read the pull request's live base branch before restarting at Review: %w", err)
	}
	liveBase = strings.TrimSpace(liveBase)
	if liveBase == "" {
		return fmt.Errorf("the pull request reported no live base branch before restarting at Review")
	}
	current := runPRBaseBranch(sctx)
	if current == liveBase {
		return nil
	}
	if err := sctx.DB.UpdateRunPRBaseBranch(sctx.Run.ID, liveBase); err != nil {
		return err
	}
	sctx.Run.PRBaseBranch = &liveBase
	sctx.Log(fmt.Sprintf("run base branch updated to the pull request's live base %q (was %q)", liveBase, current))
	return nil
}

func containsFold(heads []string, head string) bool {
	for _, candidate := range heads {
		if strings.EqualFold(candidate, head) {
			return true
		}
	}
	return false
}
