package steps

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// The PR body renders the annotation as a short parenthetical after the
// description, in both formats, so a reader sees the row may duplicate an
// earlier finding without merging it away.
func TestWriteFindingItems_AnnotatedRestatementCarriesNote(t *testing.T) {
	for name, flavor := range map[string]prBodyFlavor{"markdown": prBodyMarkdown, "html": prBodyHTML} {
		t.Run(name, func(t *testing.T) {
			findings := &types.Findings{Items: []types.Finding{
				{ID: "review-1", Severity: "warning", File: "a.go", Line: 10, Description: "nil map write in Load when cfg is empty", Action: types.ActionAutoFix},
				{ID: "review-2", Severity: "warning", File: "a.go", Line: 12, Description: "Load writes to a nil map when cfg is empty", Action: types.ActionAutoFix, PossibleRestatementOf: "review-1"},
			}}
			var b strings.Builder
			writeFindingItems(&b, &db.StepResult{StepName: types.StepReview}, findings, flavor)
			got := b.String()
			if !strings.Contains(got, "Load writes to a nil map when cfg is empty (possible restatement of review-1)") {
				t.Fatalf("%s: annotated bullet must carry the note:\n%s", name, got)
			}
			if strings.Contains(got, "nil map write in Load when cfg is empty (possible restatement") {
				t.Fatalf("%s: the outstanding row must not carry a note:\n%s", name, got)
			}
		})
	}
}

// A findings list without annotations renders byte-identically to before.
func TestWriteFindingItems_NoNoteWithoutTheAnnotation(t *testing.T) {
	findings := &types.Findings{Items: []types.Finding{
		{ID: "review-1", Severity: "warning", File: "a.go", Line: 10, Description: "nil map write", Action: types.ActionAutoFix},
	}}
	var b strings.Builder
	writeFindingItems(&b, &db.StepResult{StepName: types.StepReview}, findings, prBodyMarkdown)
	got := b.String()
	if strings.Contains(got, "possible restatement") {
		t.Fatalf("unannotated bullet must not render a note:\n%s", got)
	}
	if got != "- ⚠️ `a.go:10` - nil map write\n" {
		t.Fatalf("unexpected bullet rendering:\n%s", got)
	}
}
