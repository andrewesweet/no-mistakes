package cli

import (
	"strings"
	"testing"

	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

type decodedOriginFindingRow struct {
	ID     string `toon:"id"`
	Origin string `toon:"origin"`
}

// The review gate groups pre-existing findings separately from the change's
// own findings, so an operator sees at a glance which findings are outside
// the diff: introduced (effective - everything not labelled pre-existing)
// under `findings`, and pre-existing under `pre_existing_findings`.
func TestGateGroupsPreExistingFindingsSeparately(t *testing.T) {
	gate := stepView{
		Name:   string(types.StepReview),
		Status: string(types.StepStatusAwaitingApproval),
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "r1", Severity: "warning", File: "legacy.go", Action: types.ActionAskUser, Description: "old defect", Origin: types.FindingOriginPreExisting},
			{ID: "r2", Severity: "error", File: "new.go", Action: types.ActionAutoFix, Description: "fresh defect"},
			{ID: "r3", Severity: "info", File: "new.go", Action: types.ActionNoOp, Description: "note", Origin: types.FindingOriginIntroduced},
		}, "mixed origins"),
	}

	var doc struct {
		Gate struct {
			Findings            []decodedOriginFindingRow `toon:"findings"`
			PreExistingFindings []decodedOriginFindingRow `toon:"pre_existing_findings"`
		} `toon:"gate"`
	}
	out := axiDoc(gateFieldsWithHelp(gate, nil)...)
	if err := toon.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("decode gate: %v\n%s", err, out)
	}
	if len(doc.Gate.Findings) != 2 {
		t.Fatalf("findings group = %d rows, want the 2 introduced ones:\n%s", len(doc.Gate.Findings), out)
	}
	if doc.Gate.Findings[0].ID != "r2" || doc.Gate.Findings[1].ID != "r3" {
		t.Fatalf("findings group = %v, want [r2 r3]", doc.Gate.Findings)
	}
	if len(doc.Gate.PreExistingFindings) != 1 || doc.Gate.PreExistingFindings[0].ID != "r1" {
		t.Fatalf("pre_existing_findings group = %v, want [r1]", doc.Gate.PreExistingFindings)
	}
}

// A gate with no pre-labelled findings renders exactly one findings field and
// no pre-existing group, so today's single-group output is unchanged for every
// review that never sets origin.
func TestGateWithoutPreExistingFindingsHasNoPreExistingGroup(t *testing.T) {
	gate := stepView{
		Name:   string(types.StepReview),
		Status: string(types.StepStatusAwaitingApproval),
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "r2", Severity: "error", File: "new.go", Action: types.ActionAutoFix, Description: "fresh defect"},
		}, "one finding"),
	}

	out := axiDoc(gateFieldsWithHelp(gate, nil)...)
	if strings.Contains(out, "pre_existing_findings") {
		t.Fatalf("gate without pre-existing findings must not emit the group:\n%s", out)
	}
	if !strings.Contains(out, "r2") {
		t.Fatalf("gate lost its finding:\n%s", out)
	}
}
