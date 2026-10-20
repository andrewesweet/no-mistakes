package pipeline

import (
	"strconv"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestReviewRoundSuffix_RestatementStaysOutstandingUntilVerified(t *testing.T) {
	for _, line := range []int{10, 25} {
		t.Run(strconv.Itoa(line), func(t *testing.T) {
			original := types.Finding{ID: "review-1", Severity: "warning", File: "service.go", Line: 10, Description: "nil dereference"}
			outstanding, err := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{original}})
			if err != nil {
				t.Fatal(err)
			}
			for _, description := range []string{
				"nil dereference present at round 1's review",
				"nil dereference present at round 2's review",
				"nil dereference present at round 2's review present at round 3's review",
				"nil dereference",
			} {
				restated := original
				restated.ID = "review-9"
				restated.Line = line
				restated.Description = description
				round, err := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{restated}})
				if err != nil {
					t.Fatal(err)
				}
				paths := []string{"service.go"}
				outstanding = resolveVerifiedFindingsJSON(outstanding, []string{"review-1"}, paths, paths, round)
				outstanding = mergeOutstandingFindingsJSON(outstanding, round, paths)
				merged, err := types.ParseFindingsJSON(outstanding)
				if err != nil {
					t.Fatal(err)
				}
				if len(merged.Items) != 1 || merged.Items[0].ID != "review-1" || merged.Items[0].Description != description {
					t.Fatalf("restatement must keep one selected row and its display annotation: %s", outstanding)
				}
				remapped, err := types.ParseFindingsJSON(remapFindingIDsJSON(outstanding, round))
				if err != nil || len(remapped.Items) != 1 || remapped.Items[0].ID != "review-1" || remapped.Items[0].Description != description {
					t.Fatalf("restatement selection lost its ID or display annotation: %#v, %v", remapped, err)
				}
				if got := resolveVerifiedFindingsJSON(outstanding, []string{"review-1"}, paths, paths, ""); got != "" {
					t.Fatalf("positive verification must clear the marked finding: %s", got)
				}
			}
			paths := []string{"service.go"}
			if got := resolveVerifiedFindingsJSON(outstanding, []string{"review-1"}, nil, paths, ""); got == "" {
				t.Fatal("silence without coverage cleared the outstanding finding")
			}
			if got := resolveVerifiedFindingsJSON(outstanding, []string{"review-1"}, paths, paths, ""); got != "" {
				t.Fatalf("positive verification must clear the original selection: %s", got)
			}
		})
	}
}

func TestReviewRoundSuffix_CannotMergeUnrelatedFindings(t *testing.T) {
	original := types.Finding{ID: "review-1", Severity: "warning", File: "service.go", Line: 10, Description: "nil dereference"}
	for _, tc := range []struct {
		name        string
		description string
		file        string
		severity    string
	}{
		{"different defect", "unbounded cache present at round 1's review", "service.go", "warning"},
		{"marker in middle", "nil dereference present at round 1's review hides another defect", "service.go", "warning"},
		{"marker in prefix", "present at round 1's review nil dereference", "service.go", "warning"},
		{"missing separator", "nil dereferencepresent at round 1's review", "service.go", "warning"},
		{"invalid round", "nil dereference present at round 0's review", "service.go", "warning"},
		{"different path", "nil dereference present at round 1's review", "other.go", "warning"},
		{"different severity", "nil dereference present at round 1's review", "service.go", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := original
			other.Description, other.File, other.Severity = tc.description, tc.file, tc.severity
			if SameFinding(original, other) || SameFinding(other, original) || FindingDecisionKey(original) == FindingDecisionKey(other) {
				t.Fatal("annotation hid a substantive identity difference")
			}
			existing, err := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{original}})
			if err != nil {
				t.Fatal(err)
			}
			round, err := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{other}})
			if err != nil {
				t.Fatal(err)
			}
			merged, err := types.ParseFindingsJSON(mergeOutstandingFindingsJSON(existing, round, nil))
			if err != nil || len(merged.Items) != 2 || merged.Items[0].ID == merged.Items[1].ID {
				t.Fatalf("unrelated findings must retain separate identities: %#v, %v", merged, err)
			}
		})
	}
}

func TestReviewRoundSuffix_DecisionsAndRecoveryKeepIdentity(t *testing.T) {
	original := types.Finding{ID: "review-1", Severity: "warning", File: "service.go", Line: 10, Description: "nil dereference"}
	marked := original
	marked.Description = "nil dereference present at round 12's review"
	if !SameFinding(original, marked) || !SameFinding(marked, original) || FindingDecisionKey(original) != FindingDecisionKey(marked) {
		t.Fatal("display annotation changed the recorded decision's identity")
	}
	prior, err := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{original}})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{marked}})
	if err != nil {
		t.Fatal(err)
	}
	selection := `["review-1"]`
	identities := selectedFindingIdentities([]*db.StepRound{{FindingsJSON: &prior, SelectedFindingIDs: &selection}})
	retained := retainFindingIDsByIdentity(latest, []string{"review-1"}, identities)
	if len(retained) != 1 || retained[0] != "review-1" {
		t.Fatalf("recovery lost the selected finding: %v", retained)
	}
	marked.Description = "unbounded cache present at round 12's review"
	unrelated, err := types.MarshalFindingsJSON(types.Findings{Items: []types.Finding{marked}})
	if err != nil {
		t.Fatal(err)
	}
	if retained := retainFindingIDsByIdentity(unrelated, []string{"review-1"}, identities); len(retained) != 0 {
		t.Fatalf("marker forged the historical selection: %v", retained)
	}
}

func TestReviewRoundSuffix_LineShiftDoesNotMergeAmbiguousFindings(t *testing.T) {
	existing := `{"findings":[{"id":"review-1","severity":"warning","file":"service.go","line":10,"description":"nil dereference"},{"id":"review-2","severity":"warning","file":"service.go","line":20,"description":"nil dereference"}]}`
	round := `{"findings":[{"id":"review-1","severity":"warning","file":"service.go","line":30,"description":"nil dereference present at round 1's review"}]}`
	merged, err := types.ParseFindingsJSON(mergeOutstandingFindingsJSON(existing, round, nil))
	if err != nil || len(merged.Items) != 3 {
		t.Fatalf("annotation must not disambiguate distinct line anchors: %#v, %v", merged, err)
	}
}

func TestMergeFindingsJSON_KeepsDistinctFindingsWithSameAutoID(t *testing.T) {
	existingRaw := `{"findings":[{"id":"review-1","severity":"warning","description":"first"}],"summary":"1 finding"}`
	additionalRaw := `{"findings":[{"id":"review-1","severity":"error","description":"second"}],"summary":"1 finding"}`

	mergedRaw := mergeFindingsJSON(existingRaw, additionalRaw)
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if len(merged.Items) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(merged.Items))
	}
	if merged.Items[0].Description != "first" || merged.Items[1].Description != "second" {
		t.Fatalf("unexpected merged findings: %#v", merged.Items)
	}
}

func TestRetainMatchingFindingsJSON_DropsFindingsMissingFromLatestReview(t *testing.T) {
	existingRaw := `{"findings":[{"id":"review-1","severity":"warning","description":"first"},{"id":"review-2","severity":"error","description":"second"}],"summary":"2 findings"}`
	keepRaw := `{"findings":[{"id":"review-7","severity":"error","description":"second"},{"id":"review-8","severity":"warning","description":"third"}],"summary":"2 findings"}`

	retainedRaw := retainMatchingFindingsJSON(existingRaw, keepRaw)
	retained, err := types.ParseFindingsJSON(retainedRaw)
	if err != nil {
		t.Fatalf("parse retained findings: %v", err)
	}
	if len(retained.Items) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(retained.Items))
	}
	if retained.Items[0].Description != "second" {
		t.Fatalf("unexpected retained findings: %#v", retained.Items)
	}
}

func TestRetainMatchingFindingsJSON_MatchesFindingsAfterLineShift(t *testing.T) {
	existingRaw := `{"findings":[{"id":"dismissed-1","severity":"warning","file":"internal/pipeline/findings.go","line":42,"description":"still unresolved"}],"summary":"1 finding"}`
	keepRaw := `{"findings":[{"id":"review-9","severity":"warning","file":"internal/pipeline/findings.go","line":57,"description":"still unresolved"}],"summary":"1 finding"}`

	retainedRaw := retainMatchingFindingsJSON(existingRaw, keepRaw)
	retained, err := types.ParseFindingsJSON(retainedRaw)
	if err != nil {
		t.Fatalf("parse retained findings: %v", err)
	}
	if len(retained.Items) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(retained.Items))
	}
	if retained.Items[0].ID != "dismissed-1" {
		t.Fatalf("unexpected retained finding: %#v", retained.Items)
	}
}

func TestRetainMatchingFindingsJSON_DoesNotKeepDistinctDuplicateLines(t *testing.T) {
	existingRaw := `{"findings":[{"id":"dismissed-1","severity":"warning","file":"internal/pipeline/findings.go","line":42,"description":"still unresolved"},{"id":"dismissed-2","severity":"warning","file":"internal/pipeline/findings.go","line":57,"description":"still unresolved"}],"summary":"2 findings"}`
	keepRaw := `{"findings":[{"id":"review-9","severity":"warning","file":"internal/pipeline/findings.go","line":42,"description":"still unresolved"}],"summary":"1 finding"}`

	retainedRaw := retainMatchingFindingsJSON(existingRaw, keepRaw)
	retained, err := types.ParseFindingsJSON(retainedRaw)
	if err != nil {
		t.Fatalf("parse retained findings: %v", err)
	}
	if len(retained.Items) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(retained.Items))
	}
	if retained.Items[0].ID != "dismissed-1" {
		t.Fatalf("unexpected retained findings: %#v", retained.Items)
	}
}

func TestAutoFixableFindingsJSON_FiltersToAutoFix(t *testing.T) {
	raw := `{"findings":[{"id":"review-1","severity":"error","description":"bug","action":"auto-fix"},{"id":"review-2","severity":"warning","description":"design choice","action":"ask-user"},{"id":"review-3","severity":"warning","description":"missing check","action":"auto-fix"},{"id":"review-4","severity":"info","description":"note","action":"no-op"}],"risk_level":"medium","risk_rationale":"Mixed."}`

	fixableRaw := autoFixableFindingsJSON(raw)
	fixable, err := types.ParseFindingsJSON(fixableRaw)
	if err != nil {
		t.Fatalf("parse auto-fixable findings: %v", err)
	}
	if len(fixable.Items) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(fixable.Items))
	}
	if fixable.Items[0].ID != "review-1" || fixable.Items[1].ID != "review-3" {
		t.Fatalf("unexpected findings: %#v", fixable.Items)
	}
}

func TestAutoFixableFindingsJSON_AllAskUser(t *testing.T) {
	raw := `{"findings":[{"id":"review-1","severity":"warning","description":"choice","action":"ask-user"}],"risk_level":"high","risk_rationale":"Needs review."}`

	fixableRaw := autoFixableFindingsJSON(raw)
	if fixableRaw != "" {
		t.Fatalf("expected empty string for all-ask-user findings, got %q", fixableRaw)
	}
}

func TestAutoFixableFindingsJSON_EmptyInput(t *testing.T) {
	if got := autoFixableFindingsJSON(""); got != "" {
		t.Fatalf("expected empty string for empty input, got %q", got)
	}
}

func TestAutoFixableFindingsJSON_AllNoOp(t *testing.T) {
	raw := `{"findings":[{"id":"review-1","severity":"info","description":"note","action":"no-op"}],"risk_level":"low","risk_rationale":"Clean."}`

	fixableRaw := autoFixableFindingsJSON(raw)
	if fixableRaw != "" {
		t.Fatalf("expected empty string for all-no-op findings, got %q", fixableRaw)
	}
}

func TestMergeFindingsJSON_DeduplicatesShiftedUniqueDismissedFinding(t *testing.T) {
	existingRaw := `{"findings":[{"id":"dismissed-1","severity":"warning","file":"internal/pipeline/findings.go","line":42,"description":"still unresolved"}],"summary":"1 finding"}`
	additionalRaw := `{"findings":[{"id":"dismissed-2","severity":"warning","file":"internal/pipeline/findings.go","line":57,"description":"still unresolved"}],"summary":"1 finding"}`

	mergedRaw := mergeFindingsJSON(existingRaw, additionalRaw)
	merged, err := types.ParseFindingsJSON(mergedRaw)
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if len(merged.Items) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(merged.Items))
	}
	if merged.Items[0].ID != "dismissed-1" {
		t.Fatalf("unexpected merged findings: %#v", merged.Items)
	}
}

func TestFilterFindingsJSON_EmptySelectionReturnsEmptyFindings(t *testing.T) {
	raw := `{"findings":[{"id":"review-1","severity":"error","description":"first"}],"summary":"1 finding"}`

	filteredRaw := filterFindingsJSON(raw, nil)
	filtered, err := types.ParseFindingsJSON(filteredRaw)
	if err != nil {
		t.Fatalf("parse filtered findings: %v", err)
	}
	if len(filtered.Items) != 0 {
		t.Fatalf("expected 0 findings, got %d", len(filtered.Items))
	}
	if filtered.Summary != "0 selected findings" {
		t.Fatalf("summary = %q, want %q", filtered.Summary, "0 selected findings")
	}
}

func TestMergeFindingsJSON_LatestRiskAssessmentWinsWhenFindingsCarryForward(t *testing.T) {
	existingRaw := `{"findings":[{"id":"review-1","severity":"warning","description":"first"}],"summary":"1 finding","risk_level":"medium","risk_rationale":"first and second should be addressed","risk_scope":"scope-a"}`
	additionalRaw := `{"findings":[{"id":"review-9","severity":"info","description":"other"}],"summary":"1 finding","risk_level":"low","risk_rationale":"first and second are fixed","risk_scope":"scope-b"}`

	merged, err := types.ParseFindingsJSON(mergeFindingsJSON(existingRaw, additionalRaw))
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if len(merged.Items) != 2 {
		t.Fatalf("expected carried finding plus new finding, got %#v", merged.Items)
	}
	if merged.RiskLevel != "low" || merged.RiskRationale != "first and second are fixed" || merged.RiskScope != "scope-b" {
		t.Fatalf("expected latest assessment, got %q / %q / %q", merged.RiskLevel, merged.RiskRationale, merged.RiskScope)
	}
}

func TestMergeFindingsJSON_KeepsExistingRiskAssessmentWhenRoundHasNone(t *testing.T) {
	existingRaw := `{"findings":[{"id":"review-1","severity":"warning","description":"first"}],"summary":"1 finding","risk_level":"medium","risk_rationale":"first should be addressed","risk_scope":"scope-a"}`
	additionalRaw := `{"findings":[{"id":"review-9","severity":"info","description":"other"}],"summary":"1 finding"}`

	merged, err := types.ParseFindingsJSON(mergeFindingsJSON(existingRaw, additionalRaw))
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if merged.RiskLevel != "medium" || merged.RiskRationale != "first should be addressed" || merged.RiskScope != "scope-a" {
		t.Fatalf("expected existing assessment kept, got %q / %q / %q", merged.RiskLevel, merged.RiskRationale, merged.RiskScope)
	}
}

func TestMergeOutstandingFindingsJSON_LatestRiskAssessmentWins(t *testing.T) {
	existingRaw := `{"findings":[{"id":"review-1","severity":"warning","description":"first","file":"a.go"}],"summary":"1 finding","risk_level":"medium","risk_rationale":"first should be addressed"}`
	roundRaw := `{"findings":[{"id":"review-1","severity":"warning","description":"first","file":"a.go"}],"summary":"1 finding","risk_level":"low","risk_rationale":"only first remains"}`

	merged, err := types.ParseFindingsJSON(mergeOutstandingFindingsJSON(existingRaw, roundRaw, []string{"a.go"}))
	if err != nil {
		t.Fatalf("parse merged findings: %v", err)
	}
	if merged.RiskLevel != "low" || merged.RiskRationale != "only first remains" {
		t.Fatalf("expected latest assessment, got %q / %q", merged.RiskLevel, merged.RiskRationale)
	}
}
