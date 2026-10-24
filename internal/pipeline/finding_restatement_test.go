package pipeline

import (
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// Fixtures from the scout report's probe 1: an outstanding finding at
// a.go:10 restated in different words at a.go:12 used to surface as a
// second, apparently-unrelated gate row. It still does - the row is never
// merged or dropped - but it now carries an annotation naming the earlier
// finding it may restate.
const (
	restatementOutstanding = `{"findings":[{"id":"review-1","severity":"warning","file":"a.go","line":10,"description":"nil map write in Load when cfg is empty","action":"auto-fix"}],"summary":"1 finding"}`
	restatementRound       = `{"findings":[{"id":"review-1","severity":"warning","file":"a.go","line":12,"description":"Load writes to a nil map when cfg is empty","action":"auto-fix"}],"summary":"1 finding"}`
)

func restatementMergedItem(t *testing.T, merged string, id string) *types.Finding {
	t.Helper()
	parsed, err := types.ParseFindingsJSON(merged)
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	for i := range parsed.Items {
		if parsed.Items[i].ID == id {
			return &parsed.Items[i]
		}
	}
	t.Fatalf("finding %q not in merged set: %s", id, merged)
	return nil
}

// TestMergeOutstandingFindingsJSON_AnnotatesRewordedRestatement is the
// report's probe 1 turned into its reproduction: the reworded restatement on
// a moved line still appends as its own row (over-blocking beats dropping),
// but the new row names the outstanding finding it may restate instead of
// reading as an unrelated new defect.
func TestMergeOutstandingFindingsJSON_AnnotatesRewordedRestatement(t *testing.T) {
	merged := mergeOutstandingFindingsJSON(restatementOutstanding, restatementRound, []string{"a.go"})

	parsed, err := types.ParseFindingsJSON(merged)
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if len(parsed.Items) != 2 {
		t.Fatalf("rows after merge = %d, want 2 (the annotation never merges or drops a row): %s", len(parsed.Items), merged)
	}
	kept := restatementMergedItem(t, merged, "review-1")
	appended := restatementMergedItem(t, merged, "review-2")
	if kept.PossibleRestatementOf != "" {
		t.Errorf("the outstanding review-1 must not carry an annotation: %q", kept.PossibleRestatementOf)
	}
	if appended.PossibleRestatementOf != "review-1" {
		t.Errorf("appended review-2.PossibleRestatementOf = %q, want review-1", appended.PossibleRestatementOf)
	}
	if appended.Description != "Load writes to a nil map when cfg is empty" {
		t.Errorf("the annotation must not rewrite the row's own description: %q", appended.Description)
	}
}

// TestMergeOutstandingFindingsJSON_ExactRestatementStaysOneUnannotatedRow:
// an exact restatement (the G1 prompt's own instruction) folds into the
// outstanding row, so there is no second row and nothing to annotate.
func TestMergeOutstandingFindingsJSON_ExactRestatementStaysOneUnannotatedRow(t *testing.T) {
	round := `{"findings":[{"id":"review-1","severity":"warning","file":"a.go","line":12,"description":"nil map write in Load when cfg is empty","action":"auto-fix"}],"summary":"1 finding"}`
	merged := mergeOutstandingFindingsJSON(restatementOutstanding, round, []string{"a.go"})

	parsed, err := types.ParseFindingsJSON(merged)
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if len(parsed.Items) != 1 {
		t.Fatalf("rows after exact restatement = %d, want 1: %s", len(parsed.Items), merged)
	}
	if parsed.Items[0].ID != "review-1" || parsed.Items[0].PossibleRestatementOf != "" {
		t.Fatalf("merged row = %+v, want review-1 with no annotation", parsed.Items[0])
	}
}

// TestMergeOutstandingFindingsJSON_DistantSameFileReportIsNotAnnotated: a
// genuinely distinct finding in the same file, outside the line band, must
// not read as a restatement of anything.
func TestMergeOutstandingFindingsJSON_DistantSameFileReportIsNotAnnotated(t *testing.T) {
	round := `{"findings":[{"id":"review-1","severity":"error","file":"a.go","line":40,"description":"unchecked error return ignored in Save","action":"auto-fix"}],"summary":"1 finding"}`
	merged := mergeOutstandingFindingsJSON(restatementOutstanding, round, []string{"a.go"})

	appended := restatementMergedItem(t, merged, "review-2")
	if appended.PossibleRestatementOf != "" {
		t.Errorf("a finding 30 lines away must not be annotated: %q", appended.PossibleRestatementOf)
	}
}

// TestMergeOutstandingFindingsJSON_LowOverlapSameFileReportIsNotAnnotated:
// near lines alone are not enough; the description must overlap too.
func TestMergeOutstandingFindingsJSON_LowOverlapSameFileReportIsNotAnnotated(t *testing.T) {
	round := `{"findings":[{"id":"review-1","severity":"error","file":"a.go","line":11,"description":"unchecked error return ignored in Save","action":"auto-fix"}],"summary":"1 finding"}`
	merged := mergeOutstandingFindingsJSON(restatementOutstanding, round, []string{"a.go"})

	appended := restatementMergedItem(t, merged, "review-2")
	if appended.PossibleRestatementOf != "" {
		t.Errorf("a same-file finding with unrelated text must not be annotated: %q", appended.PossibleRestatementOf)
	}
}

// TestMergeOutstandingFindingsJSON_AnnotationNamesTheBestAnchor: when two
// outstanding findings both pass the matcher, the annotation names the one
// with the stronger description overlap, not an arbitrary first match.
func TestMergeOutstandingFindingsJSON_AnnotationNamesTheBestAnchor(t *testing.T) {
	outstanding := `{"findings":[` +
		`{"id":"review-1","severity":"info","file":"a.go","line":10,"description":"map write happens somewhere in here","action":"no-op"},` +
		`{"id":"review-2","severity":"warning","file":"a.go","line":11,"description":"nil map write in Load when cfg is empty","action":"auto-fix"}],` +
		`"summary":"2 findings"}`
	merged := mergeOutstandingFindingsJSON(outstanding, restatementRound, []string{"a.go"})

	appended := restatementMergedItem(t, merged, "review-3")
	if appended.PossibleRestatementOf != "review-2" {
		t.Errorf("PossibleRestatementOf = %q, want review-2 (the stronger overlap)", appended.PossibleRestatementOf)
	}
}

// TestMergeOutstandingFindingsJSON_AnnotationNeverJoinsFindingIdentity: the
// annotation is display provenance. An annotated row restated verbatim in a
// later round still folds back onto itself as one row - the field must stay
// out of findingKey, or every annotated row would mint a fresh duplicate on
// its next verbatim restatement.
func TestMergeOutstandingFindingsJSON_AnnotationNeverJoinsFindingIdentity(t *testing.T) {
	first := mergeOutstandingFindingsJSON(restatementOutstanding, restatementRound, []string{"a.go"})
	second := mergeOutstandingFindingsJSON(first, restatementRound, []string{"a.go"})

	parsed, err := types.ParseFindingsJSON(second)
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if len(parsed.Items) != 2 {
		t.Fatalf("rows after re-round = %d, want 2 (identity must ignore the annotation): %s", len(parsed.Items), second)
	}
	if kept := restatementMergedItem(t, second, "review-1"); kept.PossibleRestatementOf != "" {
		t.Errorf("outstanding review-1 annotated: %q", kept.PossibleRestatementOf)
	}
	if appended := restatementMergedItem(t, second, "review-2"); appended.PossibleRestatementOf != "review-1" {
		t.Errorf("re-appended review-2.PossibleRestatementOf = %q, want review-1", appended.PossibleRestatementOf)
	}
}

// TestFindingKeyExcludesPossibleRestatementOf pins the exclusion at the
// identity boundary itself: two findings differing only in the annotation
// are the same finding.
func TestFindingKeyExcludesPossibleRestatementOf(t *testing.T) {
	a := types.Finding{ID: "review-2", Severity: "warning", File: "a.go", Line: 12, Description: "same words", Action: "auto-fix"}
	b := a
	b.PossibleRestatementOf = "review-1"
	if !SameFinding(a, b) {
		t.Fatal("findings differing only by the restatement annotation must share identity")
	}
	if !SameFinding(a, findingKey(b)) {
		t.Fatal("findingKey must strip the annotation")
	}
}

// TestAnnotationSurvivesThePersistAndRecoveryPath proves the annotation is
// safe in the paths the executor really runs: the persisted gate record is
// remapFindingIDsJSON(outstanding, merged) (normalizeFixSelection), and a
// recovered gate re-checks its selections by content identity via
// retainFindingIDsByIdentity. The annotation must ride the round trip and
// the identity matches must hold with it present - otherwise an annotated
// row could lose its anchor on persist, or a recorded selection could stop
// aliasing its (annotated) finding after a restart.
func TestAnnotationSurvivesThePersistAndRecoveryPath(t *testing.T) {
	outstanding := mergeOutstandingFindingsJSON(restatementOutstanding, restatementRound, []string{"a.go"})
	selected := `{"findings":[{"id":"review-2","severity":"warning","file":"a.go","line":12,"description":"Load writes to a nil map when cfg is empty","action":"auto-fix","possible_restatement_of":"review-1"}],"summary":"1 finding"}`

	persisted := remapFindingIDsJSON(outstanding, selected)
	persistedRow := restatementMergedItem(t, persisted, "review-2")
	if persistedRow.PossibleRestatementOf != "review-1" {
		t.Fatalf("the annotation must survive the persist remap: %s", persisted)
	}

	identity := selectedFindingIdentities([]*db.StepRound{
		{FindingsJSON: strPtr(outstanding), SelectedFindingIDs: strPtr(`["review-2"]`)},
	})
	kept := retainFindingIDsByIdentity(outstanding, []string{"review-2"}, identity)
	if len(kept) != 1 || kept[0] != "review-2" {
		t.Fatalf("retainFindingIDsByIdentity = %v, want [review-2]: the recorded selection must still alias its annotated row", kept)
	}
}
