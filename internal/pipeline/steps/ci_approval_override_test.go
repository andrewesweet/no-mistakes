package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// TestCIStep_VerifyApprovalOverride pins CIStep's implementation of
// pipeline.ApprovalOverrideVerifier against the real scm.Host/gh plumbing
// (via the fakecli gh double every other CI step test uses), not just the
// executor-level fake used in internal/pipeline's regression tests. See
// pipeline.ApprovalOverrideVerifier's doc for the incident this exists for:
// a human approving a CI gate must never let a still-failing live check read
// as a clean pass.
func TestCIStep_VerifyApprovalOverride(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		checksJSON     string
		wantUnresolved bool
		wantContains   string
	}{
		{
			name:           "still failing",
			checksJSON:     `[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"PR must be raised via no-mistakes","state":"FAILURE","bucket":"fail"}]`,
			wantUnresolved: true,
			wantContains:   "PR must be raised via no-mistakes",
		},
		{
			name:           "became green",
			checksJSON:     `[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"PR must be raised via no-mistakes","state":"SUCCESS","bucket":"pass"}]`,
			wantUnresolved: false,
		},
		// Regression for the upstream review P1 "unresolved checks become
		// clean passes": before this fix, VerifyApprovalOverride used
		// !hasFailingChecks, which reads pending/cancelled/unknown-bucket
		// checks (none of them Failing()) as a clean pass. It must instead
		// use allChecksPassed, the same trusted all-green semantics the CI
		// step's own polling loop uses, so anything short of every check
		// being pass/skip is reported as unresolved.
		{
			name:           "still pending",
			checksJSON:     `[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"deploy","state":"IN_PROGRESS","bucket":"pending"}]`,
			wantUnresolved: true,
			wantContains:   "deploy",
		},
		{
			name:           "cancelled",
			checksJSON:     `[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"deploy","state":"CANCELLED","bucket":"cancel"}]`,
			wantUnresolved: true,
			wantContains:   "deploy",
		},
		{
			name:           "unknown bucket",
			checksJSON:     `[{"name":"build","state":"SUCCESS","bucket":"pass"},{"name":"legacy","state":"SOMETHING_NEW","bucket":"weird"}]`,
			wantUnresolved: true,
			wantContains:   "legacy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			env := fakeCIGH(t, "OPEN", tc.checksJSON)

			prURL := "https://github.com/test/repo/pull/42"
			sctx := newTestContext(t, nil, dir, "base", "deadbeef", config.Commands{})
			sctx.Env = env
			sctx.Run.PRURL = &prURL

			step := &CIStep{}
			unresolved, err := step.VerifyApprovalOverride(sctx)
			if err != nil {
				t.Fatalf("VerifyApprovalOverride() error = %v", err)
			}
			if tc.wantUnresolved && unresolved == "" {
				t.Fatal("unresolved = \"\", want a reason naming the still-failing check")
			}
			if !tc.wantUnresolved && unresolved != "" {
				t.Fatalf("unresolved = %q, want \"\" once every check passed", unresolved)
			}
			if tc.wantContains != "" && !strings.Contains(unresolved, tc.wantContains) {
				t.Errorf("unresolved = %q, want it to name %q", unresolved, tc.wantContains)
			}
		})
	}
}

// TestCIStep_VerifyApprovalOverride_NoPRURL covers the "cannot verify" fail-
// closed path: a run with no PR URL yet cannot have a live state to check
// against, so this must report an unresolved reason (never silently clear),
// matching ApprovalOverrideVerifier's documented fail-closed contract.
func TestCIStep_VerifyApprovalOverride_NoPRURL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sctx := newTestContext(t, nil, dir, "base", "deadbeef", config.Commands{})

	step := &CIStep{}
	unresolved, err := step.VerifyApprovalOverride(sctx)
	if err != nil {
		t.Fatalf("VerifyApprovalOverride() error = %v", err)
	}
	if unresolved == "" {
		t.Fatal("unresolved = \"\", want a fail-closed reason when there is no PR URL to verify")
	}
}

// TestCIStep_VerifyApprovalOverride_EmptyChecks is the other half of the
// upstream review P1 "unresolved checks become clean passes": before this
// fix, !hasFailingChecks(nil) is true (an empty slice contains no failing
// check), so a PR reporting zero live checks at all read as a clean pass.
// allChecksPassed correctly treats an empty check list as NOT passed, and
// VerifyApprovalOverride must report that as unresolved rather than clear.
func TestCIStep_VerifyApprovalOverride_EmptyChecks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := fakeCIGHNoChecks(t)

	prURL := "https://github.com/test/repo/pull/42"
	sctx := newTestContext(t, nil, dir, "base", "deadbeef", config.Commands{})
	sctx.Env = env
	sctx.Run.PRURL = &prURL

	step := &CIStep{}
	unresolved, err := step.VerifyApprovalOverride(sctx)
	if err != nil {
		t.Fatalf("VerifyApprovalOverride() error = %v", err)
	}
	if unresolved == "" {
		t.Fatal("unresolved = \"\", want a fail-closed reason when the PR reports no checks at all")
	}
}

// A human may approve the published-head-rewrite park - that is the verdict the
// gate offers - but the checks being approved are the forge's for a head this
// run validated nowhere, so the completion must be recorded as
// passed-with-override however green they are. Before this, green checks for the
// foreign head returned "" and the run completed reporting CI passed for a
// commit Review and Test never saw.
func TestCIStep_VerifyApprovalOverride_HeadRewriteParkIsNeverACleanPass(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	prURL := "https://github.com/test/repo/pull/42"
	const foreign = "1111111111111111111111111111111111111111"
	sctx := newTestContextWithDBRecords(t, nil, dir, "base", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", config.Commands{})
	sctx.Env = append(fakeCIGH(t, "OPEN", `[{"name":"build","state":"SUCCESS","bucket":"pass"}]`), "FAKE_CLI_PR_HEAD_SHA="+foreign)
	sctx.Run.PRURL = &prURL

	stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepCI)
	if err != nil {
		t.Fatal(err)
	}
	sctx.StepResultID = stepResult.ID
	park := parkPublishedHeadRewrite(sctx.Run.HeadSHA, sctx.Run.HeadSHA, foreign, "the run worktree holds commits that are neither on the live head nor recorded as published")
	if err := sctx.DB.SetStepFindings(stepResult.ID, park.Findings); err != nil {
		t.Fatal(err)
	}

	unresolved, err := (&CIStep{}).VerifyApprovalOverride(sctx)
	if err != nil {
		t.Fatalf("VerifyApprovalOverride() error = %v", err)
	}
	if unresolved == "" {
		t.Fatal("unresolved = \"\", want the head rewrite recorded as an unresolved condition despite the green checks")
	}
	for _, want := range []string{shortSHA(sctx.Run.HeadSHA), shortSHA(foreign)} {
		if !strings.Contains(unresolved, want) {
			t.Fatalf("unresolved = %q, want it to name %q", unresolved, want)
		}
	}
}

// The same invariant on the reconciliation path: a PR closed while the gate sat
// parked on a head rewrite resolves the gate, but the completion carries the
// override record rather than reading as an ordinary pass.
func TestCIStep_ReconcileApprovalGate_ClosedPRRecordsTheHeadRewriteOverride(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	headSHA := gitCmd(t, dir, "rev-parse", "HEAD")

	prURL := "https://github.com/test/repo/pull/42"
	const foreign = "1111111111111111111111111111111111111111"
	sctx := newTestContextWithDBRecords(t, nil, dir, headSHA, headSHA, config.Commands{})
	sctx.Env = fakeCIGH(t, "CLOSED", `[]`)
	sctx.Run.PRURL = &prURL

	stepResult, err := sctx.DB.InsertStepResult(sctx.Run.ID, types.StepCI)
	if err != nil {
		t.Fatal(err)
	}
	sctx.StepResultID = stepResult.ID
	park := parkPublishedHeadRewrite(headSHA, headSHA, foreign, "the run worktree holds commits that are neither on the live head nor recorded as published")
	if err := sctx.DB.SetStepFindings(stepResult.ID, park.Findings); err != nil {
		t.Fatal(err)
	}

	resolved, err := (&CIStep{}).ReconcileApprovalGate(sctx)
	if err != nil || !resolved {
		t.Fatalf("ReconcileApprovalGate() = (%v, %v), want the closed PR to resolve the gate", resolved, err)
	}
	after, err := sctx.DB.GetStepResult(stepResult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.OverrideReason == nil || !strings.Contains(*after.OverrideReason, "owns nowhere") {
		t.Fatalf("override reason = %v, want the head rewrite recorded as unresolved", after.OverrideReason)
	}
}
