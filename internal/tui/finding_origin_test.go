package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestRenderFindings_OriginTransitionsKeepCursorAndSelection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		origins    []string
		cursor     int
		maxVisible int
		want       []string
	}{
		{
			name:    "pre-existing introduced pre-existing",
			origins: []string{"pre-existing", "introduced", "pre-existing"}, cursor: 1,
			want: []string{
				"── Pre-existing (outside this change) ──", "[ ] W [pre-existing] a.go",
				"── Introduced by this change ──", "> [x] W b.go",
				"── Pre-existing (outside this change) ──", "[ ] W [pre-existing] c.go",
			},
		},
		{
			name:    "introduced pre-existing introduced",
			origins: []string{"introduced", "pre-existing", ""}, cursor: 1,
			want: []string{
				"[ ] W a.go",
				"── Pre-existing (outside this change) ──", "> [x] W [pre-existing] b.go",
				"── Introduced by this change ──", "[ ] W c.go",
			},
		},
		{
			name:    "unknown origin transitions to introduced",
			origins: []string{"pre-existing", "legacy", "pre-existing"}, cursor: 1,
			want: []string{
				"── Pre-existing (outside this change) ──", "[ ] W [pre-existing] a.go",
				"── Introduced by this change ──", "> [x] W b.go",
				"── Pre-existing (outside this change) ──", "[ ] W [pre-existing] c.go",
			},
		},
		{
			name:    "viewport starts inside a pre-existing group",
			origins: []string{"pre-existing", "pre-existing", "", "pre-existing"}, cursor: 2, maxVisible: 3,
			want: []string{
				"── Pre-existing (outside this change) ──", "[ ] W [pre-existing] b.go",
				"── Introduced by this change ──", "> [x] W c.go",
				"── Pre-existing (outside this change) ──", "[ ] W [pre-existing] d.go",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var items []types.Finding
			for i, origin := range tc.origins {
				file := fmt.Sprintf("%c.go", 'a'+i)
				items = append(items, types.Finding{ID: file, File: file, Description: "defect", Severity: "warning", Action: types.ActionAskUser, Origin: origin})
			}
			raw, err := types.MarshalFindingsJSON(types.Findings{Items: items})
			if err != nil {
				t.Fatal(err)
			}
			selected := map[string]bool{items[tc.cursor].ID: true}
			content, _ := renderFindingsWithSelection(raw, 80, tc.cursor, selected, tc.maxVisible)
			maxLines := 100
			if tc.maxVisible > 0 {
				maxLines = 14
			}
			heightContent, _ := renderFindingsWithSelectionHeight(raw, 80, tc.cursor, selected, maxLines)
			for renderer, rendered := range map[string]string{"viewport": content, "height": heightContent} {
				var got []string
				for _, line := range strings.Split(stripANSI(rendered), "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "──") || strings.HasSuffix(line, ".go") {
						got = append(got, line)
					}
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("%s origin headings and finding rows = %q, want %q", renderer, got, tc.want)
				}
			}
		})
	}
}

// The TUI findings list groups pre-existing findings under their own header so
// the operator sees which findings sit outside the diff, and tags each
// pre-existing row so the grouping survives any interleaving with introduced
// findings. Introduced rows are untagged.
func TestRenderFindings_GroupsPreExistingFindings(t *testing.T) {
	raw := `{"findings":[` +
		`{"id":"r1","severity":"error","file":"new.go","line":10,"description":"fresh defect","action":"auto-fix"},` +
		`{"id":"r2","severity":"warning","file":"legacy.go","line":3,"description":"old defect","action":"ask-user","origin":"pre-existing"}` +
		`],"summary":"2 findings","risk_level":"medium","risk_rationale":"one pre-existing"}`
	got := stripANSI(renderFindings(raw, 80))

	if !strings.Contains(got, "Pre-existing") {
		t.Errorf("expected a pre-existing group header, got:\n%s", got)
	}
	if !strings.Contains(got, "(outside this change)") {
		t.Errorf("expected the group header to say the findings are outside the change, got:\n%s", got)
	}
	// The header sits before the pre-existing row and after the introduced one.
	if strings.Index(got, "old defect") < strings.Index(got, "Pre-existing") {
		t.Errorf("pre-existing header must precede its group, got:\n%s", got)
	}
	if !strings.Contains(got, "[pre-existing]") {
		t.Errorf("expected the pre-existing row to carry the [pre-existing] tag, got:\n%s", got)
	}
}

// An introduced finding is never tagged and a list with no pre-labelled
// findings renders no group header at all - today's output is unchanged.
func TestRenderFindings_NoPreExistingGroupWithoutTheLabel(t *testing.T) {
	raw := `{"findings":[` +
		`{"id":"r1","severity":"error","file":"new.go","line":10,"description":"fresh defect","action":"auto-fix","origin":"introduced"}` +
		`],"summary":"1 finding"}`
	got := stripANSI(renderFindings(raw, 80))

	if strings.Contains(got, "Pre-existing") || strings.Contains(got, "[pre-existing]") {
		t.Errorf("introduced finding must not be grouped or tagged, got:\n%s", got)
	}
	if !strings.Contains(got, "fresh defect") {
		t.Errorf("finding lost from the list, got:\n%s", got)
	}
}

// The grouping is by effective origin: an unknown origin value counts as
// introduced, so a row nobody labelled never lands in the pre-existing group.
func TestRenderFindings_UnknownOriginStaysIntroduced(t *testing.T) {
	raw := `{"findings":[` +
		`{"id":"r1","severity":"warning","file":"odd.go","line":1,"description":"mystery label","action":"ask-user","origin":"legacy"}` +
		`],"summary":"1 finding"}`
	got := stripANSI(renderFindings(raw, 80))

	if strings.Contains(got, "Pre-existing") || strings.Contains(got, "[pre-existing]") {
		t.Errorf("unknown origin must stay in the introduced group, got:\n%s", got)
	}
}

// The effective-origin helper agrees with the types package for the TUI's own
// mirrored finding type.
func TestTUIFindingIsPreExisting(t *testing.T) {
	if !(finding{Origin: types.FindingOriginPreExisting}).IsPreExisting() {
		t.Fatal("origin pre-existing must render in the pre-existing group")
	}
	if (finding{Origin: ""}).IsPreExisting() || (finding{Origin: "legacy"}).IsPreExisting() {
		t.Fatal("missing or unknown origin must stay in the introduced group")
	}
}

func TestRenderFindings_OriginHeadersFitContentWidth(t *testing.T) {
	raw, err := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{
		{ID: "review-1", Severity: "warning", File: "internal/pipeline/steps/review.go", Line: 100, Description: "earlier finding", Origin: types.FindingOriginPreExisting},
		{ID: "review-2", Severity: "warning", File: "internal/pipeline/steps/review.go", Line: 102, Description: "nearby defect", PossibleRestatementOf: "review-1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{16, 20, 24, 28, 30, 36, 38, 40, 76} {
		for _, cursor := range []int{0, 1} {
			t.Run(fmt.Sprintf("width=%d/cursor=%d", width, cursor), func(t *testing.T) {
				viewport, _ := renderFindingsWithSelection(raw, width, cursor, nil, 0)
				window, _ := renderFindingsWithSelection(raw, width, cursor, nil, 1)
				height, _ := renderFindingsWithSelectionHeight(raw, width, cursor, nil, 100)
				limited, _ := renderFindingsWithSelectionHeight(raw, width, cursor, nil, 8)
				for renderer, content := range map[string]string{"viewport": viewport, "window": window, "height": height, "limited": limited} {
					for _, line := range strings.Split(content, "\n") {
						if got := lipgloss.Width(line); got > width {
							t.Fatalf("%s line exceeds %d columns (%d): %q", renderer, width, got, line)
						}
					}
					if renderer == "limited" {
						if lipgloss.Height(strings.TrimRight(content, "\n")) > 8 {
							t.Fatalf("height-limited output exceeds eight lines: %q", content)
						}
						continue
					}
					normalized := strings.Join(strings.Fields(stripANSI(content)), " ")
					for _, header := range []string{"── Pre-existing (outside this change) ──", "── Introduced by this change ──"} {
						if renderer == "window" && (cursor != 0 || strings.Contains(header, "Introduced")) {
							continue
						}
						if !strings.Contains(normalized, header) {
							t.Fatalf("%s lost origin header %q: %s", renderer, header, content)
						}
					}
				}
			})
		}
	}
}
