package pipeline

import (
	"fmt"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestMergeFindingsOriginRelabelPreservesIdentityAndSelection(t *testing.T) {
	for name, merge := range map[string]func(string, string) string{
		"merge": mergeFindingsJSON,
		"outstanding": func(existing, current string) string {
			return mergeOutstandingFindingsJSON(existing, current, []string{"retry.go"})
		},
	} {
		for _, origins := range [][2]string{
			{types.FindingOriginIntroduced, types.FindingOriginPreExisting},
			{types.FindingOriginPreExisting, types.FindingOriginIntroduced},
			{types.FindingOriginPreExisting, ""},
			{types.FindingOriginPreExisting, "legacy"},
			{"", types.FindingOriginPreExisting},
			{"legacy", types.FindingOriginPreExisting},
		} {
			for _, line := range []int{42, 57} {
				t.Run(fmt.Sprintf("%s/%s-to-%s/line-%d", name, origins[0], origins[1], line), func(t *testing.T) {
					original := types.Finding{
						ID: "review-1", Severity: "warning", File: "retry.go", Line: 42,
						Description: "unbounded retry", Action: types.ActionAskUser,
						UserInstructions: "preserve backoff", Origin: origins[0],
					}
					current := original
					current.ID, current.Line = "review-9", line
					current.Origin = origins[1]
					current.Action, current.UserInstructions = types.ActionAutoFix, ""
					encode := func(f types.Finding) string {
						t.Helper()
						raw, err := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{f}})
						if err != nil {
							t.Fatal(err)
						}
						return raw
					}
					raw := merge(encode(original), encode(current))
					got, err := types.ParseFindingsJSON(raw)
					if err != nil {
						t.Fatal(err)
					}
					want := original
					want.Origin = origins[1]
					if len(got.Items) != 1 || got.Items[0] != want {
						t.Fatalf("merged findings = %#v, want only %#v", got.Items, want)
					}
					selected := retainFindingIDsByIdentity(raw, []string{original.ID}, map[string]types.Finding{original.ID: original})
					if len(selected) != 1 || selected[0] != original.ID {
						t.Fatalf("relabel lost recorded selection: %v", selected)
					}
				})
			}
		}
	}
}

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
