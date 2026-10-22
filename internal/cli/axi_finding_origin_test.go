package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
			{ID: "r4", Severity: "warning", File: "legacy.go", Action: types.ActionAskUser, Description: "another old defect", Origin: types.FindingOriginPreExisting},
			{ID: "r5", Severity: "warning", File: "new.go", Action: types.ActionAskUser, Description: "unknown origin", Origin: "legacy"},
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
	if len(doc.Gate.Findings) != 3 {
		t.Fatalf("findings group = %d rows, want the 3 introduced ones:\n%s", len(doc.Gate.Findings), out)
	}
	if doc.Gate.Findings[0].ID != "r2" || doc.Gate.Findings[1].ID != "r3" || doc.Gate.Findings[2].ID != "r5" {
		t.Fatalf("findings group = %v, want [r2 r3 r5]", doc.Gate.Findings)
	}
	if len(doc.Gate.PreExistingFindings) != 2 || doc.Gate.PreExistingFindings[0].ID != "r1" || doc.Gate.PreExistingFindings[1].ID != "r4" {
		t.Fatalf("pre_existing_findings group = %v, want [r1 r4]", doc.Gate.PreExistingFindings)
	}
}

func TestAxiLogsGroupsPreExistingFindings(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)
	run, err := database.InsertRun(repo.ID, "feature/origins", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunStatus(run.ID, types.RunCompleted); err != nil {
		t.Fatal(err)
	}
	step, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateStepStatus(step.ID, types.StepStatusCompleted); err != nil {
		t.Fatal(err)
	}
	old := types.Finding{ID: "old", Severity: "warning", Description: "old defect", Action: types.ActionAskUser, Origin: types.FindingOriginPreExisting}
	fresh := types.Finding{ID: "fresh", Severity: "error", Description: "fresh defect", Action: types.ActionAutoFix}
	for _, tc := range []struct {
		name  string
		items []types.Finding
		fresh bool
	}{
		{"mixed", []types.Finding{old, fresh}, true},
		{"only pre-existing", []types.Finding{old}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := database.SetStepFindings(step.ID, findingsJSON(t, tc.items, "recorded origins")); err != nil {
				t.Fatal(err)
			}
			for _, full := range []bool{false, true} {
				var out bytes.Buffer
				cmd := &cobra.Command{}
				cmd.SetContext(context.Background())
				cmd.SetOut(&out)
				if err := runAxiLogs(cmd, string(types.StepReview), run.ID, full); err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Findings            []decodedFindingRow `toon:"findings"`
					PreExistingFindings []decodedFindingRow `toon:"pre_existing_findings"`
				}
				if err := toon.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatalf("decode logs: %v\n%s", err, out.String())
				}
				if len(doc.PreExistingFindings) != 1 || doc.PreExistingFindings[0].ID != old.ID || doc.PreExistingFindings[0].Description != old.Description || doc.PreExistingFindings[0].Action != old.Action {
					t.Fatalf("logs (full=%v) lost the pre-existing finding:\n%s", full, out.String())
				}
				if tc.fresh {
					if len(doc.Findings) != 1 || doc.Findings[0].ID != fresh.ID {
						t.Fatalf("logs (full=%v) lost the introduced finding:\n%s", full, out.String())
					}
				} else if len(doc.Findings) != 0 {
					t.Fatalf("pre-existing finding leaked into introduced group:\n%s", out.String())
				}
			}
		})
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
