package pipeline

import (
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// A relabel between the two origin values is the same finding: the reviewer
// corrects its own earlier judgment about whether the change introduced a
// defect, and that correction must merge onto the outstanding item instead of
// minting a second finding for the same defect. findingKey therefore zeroes
// Origin the way it zeroes the ID, action, source, and operator instructions a
// later round may restate.
func TestSameFindingOriginRelabelIsNotANewFinding(t *testing.T) {
	introduced := types.Finding{
		ID:          "review-1",
		Severity:    "warning",
		File:        "internal/retry.go",
		Line:        42,
		Description: "unbounded retry loop",
		Action:      types.ActionAutoFix,
		Origin:      types.FindingOriginIntroduced,
	}
	relabelled := introduced
	relabelled.Origin = types.FindingOriginPreExisting

	if !SameFinding(introduced, relabelled) {
		t.Fatal("a finding relabelled introduced -> pre-existing must stay the same finding")
	}
	if !SameFinding(relabelled, introduced) {
		t.Fatal("the relabel comparison must be symmetric")
	}

	// The label still differs when it is the ONLY difference, so a caller that
	// needs to notice the relabel can - identity just ignores it.
	if introduced == relabelled {
		t.Fatal("sanity: the relabel must actually change the finding value")
	}
}

// Origin still participates in nothing else: a finding whose origin is the
// only difference has the same fingerprint, so uniqueness bookkeeping that
// counts fingerprints does not double-count a relabel either.
func TestFindingFingerprintIgnoresOrigin(t *testing.T) {
	base := types.Finding{
		ID:          "review-2",
		Severity:    "error",
		File:        "main.go",
		Line:        7,
		Description: "nil deref on empty config",
		Action:      types.ActionAskUser,
	}
	relabelled := base
	relabelled.Origin = types.FindingOriginPreExisting

	counts := countFindingFingerprints([]types.Finding{base, relabelled})
	if counts[findingFingerprint(base)] != 2 {
		t.Fatalf("fingerprint count = %d, want 2 (relabel is the same defect)", counts[findingFingerprint(base)])
	}
}
