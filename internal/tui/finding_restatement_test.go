package tui

import (
	"strings"
	"testing"
)

// A possible-restatement annotation renders as a short dim tag riding the
// row, so the operator reads "this row may be the earlier defect again"
// without opening any detail view.
func TestRenderFindings_AnnotatedRestatementRowCarriesTag(t *testing.T) {
	raw := `{"findings":[` +
		`{"id":"review-1","severity":"warning","file":"a.go","line":10,"description":"nil map write in Load when cfg is empty","action":"auto-fix"},` +
		`{"id":"review-2","severity":"warning","file":"a.go","line":12,"description":"Load writes to a nil map when cfg is empty","action":"auto-fix","possible_restatement_of":"review-1"}],` +
		`"summary":"2 findings"}`
	content := renderFindings(raw, 120)
	if !strings.Contains(content, "[possible restatement of review-1]") {
		t.Fatalf("annotated row must carry the restatement tag:\n%s", content)
	}
	// The unannotated outstanding row carries no tag of its own.
	rest := strings.Replace(content, "[possible restatement of review-1]", "", 1)
	if strings.Count(rest, "possible restatement") != 0 {
		t.Fatalf("tag must render once, on the annotated row only:\n%s", content)
	}
}

// Rows without the annotation render byte-identically to before: no empty
// tag, no extra chrome.
func TestRenderFindings_UnannotatedRowsStayUnchanged(t *testing.T) {
	raw := `{"findings":[{"id":"review-1","severity":"warning","file":"a.go","line":10,"description":"nil map write","action":"auto-fix"}],"summary":"1 finding"}`
	content := renderFindings(raw, 120)
	if strings.Contains(content, "possible restatement") {
		t.Fatalf("unannotated row must not render a restatement tag:\n%s", content)
	}
}
