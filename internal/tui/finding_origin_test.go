package tui

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

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
