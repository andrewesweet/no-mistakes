package types

import (
	"testing"
)

// A reviewer-set origin survives a full wire round trip: marshal a finding
// carrying origin "pre-existing", parse it back, and the label is still there.
// AGENTS.md requires every Finding field to ride findingWire and
// Finding.UnmarshalJSON explicitly, so the round trip is the regression that
// catches a field added to the struct but dropped from the wire.
func TestFindingOriginRoundTrips(t *testing.T) {
	in := Findings{
		Items: []Finding{
			{ID: "review-1", Severity: "warning", File: "legacy.go", Line: 3, Description: "old defect", Action: ActionAskUser, Origin: FindingOriginPreExisting},
			{ID: "review-2", Severity: "error", File: "new.go", Description: "fresh defect", Action: ActionAutoFix, Origin: FindingOriginIntroduced},
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
	if out.Items[0].Origin != FindingOriginPreExisting {
		t.Errorf("items[0].Origin = %q, want %q", out.Items[0].Origin, FindingOriginPreExisting)
	}
	if out.Items[1].Origin != FindingOriginIntroduced {
		t.Errorf("items[1].Origin = %q, want %q", out.Items[1].Origin, FindingOriginIntroduced)
	}
}

// OriginOrDefault is the single fail-safe direction: a missing or unknown
// origin counts as introduced, so every pre-existing behaviour (blocking,
// auto-fix eligibility, gate parking) keeps today's meaning for anything a
// producer or an older record failed to label.
func TestOriginOrDefaultCountsUnknownAsIntroduced(t *testing.T) {
	for _, tc := range []struct {
		name, origin, want string
	}{
		{name: "missing", origin: "", want: FindingOriginIntroduced},
		{name: "introduced", origin: FindingOriginIntroduced, want: FindingOriginIntroduced},
		{name: "pre-existing", origin: FindingOriginPreExisting, want: FindingOriginPreExisting},
		{name: "unknown value", origin: "legacy", want: FindingOriginIntroduced},
		{name: "case drift", origin: "Pre-Existing", want: FindingOriginIntroduced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := Finding{Severity: "warning", Description: "d", Action: ActionAskUser, Origin: tc.origin}
			if got := f.OriginOrDefault(); got != tc.want {
				t.Errorf("OriginOrDefault(%q) = %q, want %q", tc.origin, got, tc.want)
			}
		})
	}
}

// An origin a peer does not know must never be re-serialized as a value it
// invented: the raw label passes through untouched, and only the effective
// origin folds unknowns to introduced.
func TestFindingOriginUnknownValuePassesThroughUnchanged(t *testing.T) {
	raw := `{"findings":[{"severity":"warning","description":"d","action":"ask-user","origin":"legacy"}],"summary":"one"}`
	f, err := ParseFindingsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if f.Items[0].Origin != "legacy" {
		t.Fatalf("Origin = %q, want the raw %q through unchanged", f.Items[0].Origin, "legacy")
	}
	if f.Items[0].OriginOrDefault() != FindingOriginIntroduced {
		t.Fatalf("effective origin = %q, want introduced", f.Items[0].OriginOrDefault())
	}
}
