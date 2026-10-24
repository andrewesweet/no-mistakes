package cli

import (
	"strings"
	"testing"

	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

type decodedRestatementFindingRow struct {
	ID                    string `toon:"id"`
	PossibleRestatementOf string `toon:"possible_restatement_of"`
}

// A finding the merge flagged as a possible restatement renders the anchor
// ID on its own gate row, so an operator (and an agent reading `axi`) sees
// the row may duplicate an earlier finding instead of reading it as an
// unrelated new defect.
func TestGateRowsCarryPossibleRestatementAnnotation(t *testing.T) {
	gate := stepView{
		Name:   string(types.StepReview),
		Status: string(types.StepStatusAwaitingApproval),
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "review-1", Severity: "warning", File: "a.go", Line: 10, Action: types.ActionAutoFix, Description: "nil map write in Load when cfg is empty"},
			{ID: "review-2", Severity: "warning", File: "a.go", Line: 12, Action: types.ActionAutoFix, Description: "Load writes to a nil map when cfg is empty", PossibleRestatementOf: "review-1"},
		}, "two findings"),
	}

	var doc struct {
		Gate struct {
			Findings []decodedRestatementFindingRow `toon:"findings"`
		} `toon:"gate"`
	}
	out := axiDoc(gateFieldsWithHelp(gate, nil)...)
	if err := toon.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("decode gate: %v\n%s", err, out)
	}
	if len(doc.Gate.Findings) != 2 {
		t.Fatalf("findings = %d rows, want 2:\n%s", len(doc.Gate.Findings), out)
	}
	if doc.Gate.Findings[0].ID != "review-1" || doc.Gate.Findings[0].PossibleRestatementOf != "" {
		t.Fatalf("outstanding row must be unannotated: %+v", doc.Gate.Findings[0])
	}
	if doc.Gate.Findings[1].ID != "review-2" || doc.Gate.Findings[1].PossibleRestatementOf != "review-1" {
		t.Fatalf("appended row must name review-1: %+v", doc.Gate.Findings[1])
	}
	if !strings.Contains(out, "possible_restatement_of") {
		t.Fatalf("the annotation must ride the rendered gate:\n%s", out)
	}
}

// The same annotation survives into `axi logs`' recorded-findings rendering.
func TestRecordedFindingsCarryPossibleRestatementAnnotation(t *testing.T) {
	findings := findingsJSON(t, []types.Finding{
		{ID: "review-2", Severity: "warning", File: "a.go", Line: 12, Action: types.ActionAutoFix, Description: "Load writes to a nil map when cfg is empty", PossibleRestatementOf: "review-1"},
	}, "one finding")

	fields, _ := recordedFindingsFields(findings, true)
	out := axiDoc(fields...)
	if !strings.Contains(out, "review-1") {
		t.Fatalf("recorded findings must keep the annotation:\n%s", out)
	}
}
