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

// The generated PR Markdown must separate the origin groups with a standalone
// paragraph. CommonMark's list continuation rules require blank lines around
// that paragraph; ordering alone also accepts a label inside the preceding item.
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

	want := "- 🚨 `new.go:10` - fresh defect\n\n" +
		"**Pre-existing findings (outside this change):**\n\n" +
		"- ⚠️ `legacy.go:3` - old defect\n"
	if got != want {
		t.Fatalf("PR Markdown must keep the origin label outside both finding lists:\nwant:\n%s\ngot:\n%s", want, got)
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
