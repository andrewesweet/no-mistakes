package types

import (
	"encoding/json"
	"testing"
)

// A pipeline-written possible-restatement annotation survives a full wire
// round trip: marshal a finding carrying the field, parse it back, and the
// anchor ID is still there. AGENTS.md requires every Finding field to ride
// findingWire and Finding.UnmarshalJSON explicitly, so the round trip is the
// regression that catches a field added to the struct but dropped from the
// wire.
func TestFindingPossibleRestatementOfRoundTrips(t *testing.T) {
	in := Findings{
		Items: []Finding{
			{ID: "review-2", Severity: "warning", File: "a.go", Line: 12, Description: "reworded later report", Action: ActionAutoFix, PossibleRestatementOf: "review-1"},
			{ID: "review-1", Severity: "warning", File: "a.go", Line: 10, Description: "the outstanding original", Action: ActionAutoFix},
		},
		Summary: "two findings",
	}
	raw, err := MarshalFindingsJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseFindingsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(out.Items))
	}
	if out.Items[0].PossibleRestatementOf != "review-1" {
		t.Errorf("items[0].PossibleRestatementOf = %q, want %q", out.Items[0].PossibleRestatementOf, "review-1")
	}
	if out.Items[1].PossibleRestatementOf != "" {
		t.Errorf("items[1].PossibleRestatementOf = %q, want empty", out.Items[1].PossibleRestatementOf)
	}
}

// The annotation is pipeline provenance, never agent input: a payload that
// arrives carrying the field still parses, but the pipeline is the only
// writer that matters downstream, and the field never alters identity (the
// pipeline's findingKey zeroes it - pinned in internal/pipeline).
func TestFindingPossibleRestatementOfParsesFromRawJSON(t *testing.T) {
	var f Finding
	if err := json.Unmarshal([]byte(`{"id":"review-2","severity":"warning","description":"x","action":"auto-fix","possible_restatement_of":"review-1"}`), &f); err != nil {
		t.Fatal(err)
	}
	if f.PossibleRestatementOf != "review-1" {
		t.Fatalf("PossibleRestatementOf = %q, want review-1", f.PossibleRestatementOf)
	}
}
