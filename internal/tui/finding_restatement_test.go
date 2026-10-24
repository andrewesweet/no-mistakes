package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/muesli/termenv"
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

func TestRenderFindings_TaggedRowsFitContentWidth(t *testing.T) {
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })
	for _, tc := range []struct {
		name   string
		source string
		origin string
		anchor string
	}{
		{name: "plain"},
		{name: "user", source: types.FindingSourceUser},
		{name: "origin", origin: types.FindingOriginPreExisting},
		{name: "restatement", anchor: "review-1"},
		{name: "all tags", source: types.FindingSourceUser, origin: types.FindingOriginPreExisting, anchor: "review-1"},
		{name: "long anchor", anchor: "review-" + strings.Repeat("9", 80)},
	} {
		for _, width := range []int{30, 36, 40, 41, 55, 56, 76, 120} {
			for _, file := range []string{"internal/pipeline/steps/review.go", "内部/pipeline/steps/review.go", ""} {
				t.Run(fmt.Sprintf("%s/%d/%s", tc.name, width, file), func(t *testing.T) {
					raw, err := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{
						{ID: "review-1", Severity: "warning", Description: "earlier finding"},
						{ID: "review-2", Severity: "warning", Description: "nearby defect", File: file, Line: 100, Source: tc.source, Origin: tc.origin, PossibleRestatementOf: tc.anchor},
					}})
					if err != nil {
						t.Fatal(err)
					}
					for _, cursor := range []int{0, 1} {
						viewport, _ := renderFindingsWithSelection(raw, width, cursor, nil, 0)
						height, _ := renderFindingsWithSelectionHeight(raw, width, cursor, nil, 100)
						limited, _ := renderFindingsWithSelectionHeight(raw, width, cursor, nil, 6)
						for renderer, content := range map[string]string{"viewport": viewport, "height": height, "limited": limited} {
							for _, line := range strings.Split(content, "\n") {
								if got := lipgloss.Width(line); got > width {
									t.Fatalf("%s line exceeds %d columns (%d): %q", renderer, width, got, line)
								}
							}
							if renderer == "limited" {
								if lipgloss.Height(strings.TrimRight(content, "\n")) > 6 {
									t.Fatalf("height-limited output exceeds six lines: %q", content)
								}
								continue
							}
							normalized := strings.Join(strings.Fields(stripANSI(content)), " ")
							for _, tag := range []struct {
								present bool
								text    string
							}{
								{tc.source != "", "[user]"},
								{tc.origin != "", "[pre-existing]"},
								{tc.anchor == "review-1", "[possible restatement of review-1]"},
							} {
								if tag.present && !strings.Contains(normalized, tag.text) {
									t.Fatalf("%s lost tag %q: %s", renderer, tag.text, content)
								}
							}
						}
					}
				})
			}
		}
	}
}
