package steps

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestWriteFindingItems_InterleavedOriginsPartitionInBothFormats(t *testing.T) {
	for name, flavor := range map[string]prBodyFlavor{"markdown": prBodyMarkdown, "html": prBodyHTML} {
		t.Run(name, func(t *testing.T) {
			findings := &types.Findings{Items: []types.Finding{
				{Severity: "warning", Description: "old first", Origin: "pre-existing"},
				{Severity: "warning", Description: "fresh first"},
				{Severity: "warning", Description: "old second", Origin: "pre-existing"},
				{Severity: "warning", Description: "fresh second", Origin: "legacy"},
			}}
			var b strings.Builder
			writeFindingItems(&b, &db.StepResult{StepName: types.StepReview}, findings, flavor)
			got := b.String()
			markers := []string{
				"fresh first", "fresh second", "Pre-existing findings (outside this change)", "old first", "old second",
			}
			previous := -1
			for _, marker := range markers {
				index := strings.Index(got, marker)
				if index <= previous || strings.Count(got, marker) != 1 {
					t.Fatalf("expected each finding once in origin groups, ordered %q:\n%s", markers, got)
				}
				previous = index
			}
		})
	}
}

// The PR body groups pre-existing review findings under their own marker
// inside the step's findings list, so a PR reader sees which findings the
// change did not introduce. Bullets render exactly as before otherwise.
func TestWriteFindingItems_GroupsPreExistingFindings(t *testing.T) {
	sr := &db.StepResult{ID: "review", StepName: types.StepReview}
	findings := &types.Findings{
		Items: []types.Finding{
			{ID: "r1", Severity: "error", File: "new.go", Line: 10, Description: "fresh defect", Action: types.ActionAutoFix},
			{ID: "r2", Severity: "warning", File: "legacy.go", Line: 3, Description: "old defect", Action: types.ActionAskUser, Origin: types.FindingOriginPreExisting},
		},
	}
	var b strings.Builder
	writeFindingItems(&b, sr, findings, prBodyMarkdown)
	got := b.String()

	group := strings.Index(got, "Pre-existing findings (outside this change)")
	if group < 0 {
		t.Fatalf("PR body missing the pre-existing group marker:\n%s", got)
	}
	old := strings.Index(got, "old defect")
	if old < group {
		t.Fatalf("pre-existing finding must render under the group marker:\n%s", got)
	}
	fresh := strings.Index(got, "fresh defect")
	if fresh > group {
		t.Fatalf("introduced finding must render above the group marker:\n%s", got)
	}
	// The pre-existing bullet keeps the ordinary finding rendering.
	if !strings.Contains(got, "`legacy.go:3` - old defect") {
		t.Fatalf("pre-existing bullet lost its location rendering:\n%s", got)
	}
}

// A findings list without pre-existing labels renders no group marker, so the
// PR body of every review that never sets origin is byte-identical to today.
func TestWriteFindingItems_NoPreExistingGroupWithoutTheLabel(t *testing.T) {
	sr := &db.StepResult{ID: "review", StepName: types.StepReview}
	findings := &types.Findings{
		Items: []types.Finding{
			{ID: "r1", Severity: "info", File: "new.go", Description: "note", Action: types.ActionNoOp, Origin: types.FindingOriginIntroduced},
		},
	}
	var b strings.Builder
	writeFindingItems(&b, sr, findings, prBodyMarkdown)
	if strings.Contains(b.String(), "Pre-existing findings") {
		t.Fatalf("introduced-only findings must not grow a group marker:\n%s", b.String())
	}
}

// Grouping keys on the effective origin, so a pre-existing marker is never
// minted from a label nobody defined.
func TestWriteFindingItems_UnknownOriginStaysInIntroducedGroup(t *testing.T) {
	sr := &db.StepResult{ID: "review", StepName: types.StepReview}
	findings := &types.Findings{
		Items: []types.Finding{
			{ID: "r1", Severity: "warning", File: "odd.go", Description: "mystery label", Action: types.ActionAskUser, Origin: "legacy"},
		},
	}
	var b strings.Builder
	writeFindingItems(&b, sr, findings, prBodyMarkdown)
	if strings.Contains(b.String(), "Pre-existing findings") {
		t.Fatalf("unknown origin must stay in the introduced group:\n%s", b.String())
	}
}
