package findmatch

import "testing"

func TestLocationMatchRewordedRestatementInsideBand(t *testing.T) {
	a := Finding{File: "a.go", Line: 10, Description: "nil map write in Load when cfg is empty"}
	b := Finding{File: "a.go", Line: 12, Description: "Load writes to a nil map when cfg is empty"}
	if !LocationMatch(a, b) {
		t.Fatal("a reworded restatement two lines away must match")
	}
}

func TestLocationMatchRejectsOutsideBand(t *testing.T) {
	a := Finding{File: "a.go", Line: 10, Description: "nil map write in Load when cfg is empty"}
	b := Finding{File: "a.go", Line: 14, Description: "nil map write in Load when cfg is empty"}
	if LocationMatch(a, b) {
		t.Fatal("four lines away is outside the band and must not match")
	}
}

func TestLocationMatchRejectsLowTextOverlap(t *testing.T) {
	a := Finding{File: "a.go", Line: 10, Description: "nil map write in Load when cfg is empty"}
	b := Finding{File: "a.go", Line: 11, Description: "unchecked error return ignored in Save"}
	if LocationMatch(a, b) {
		t.Fatal("nearby lines with unrelated descriptions must not match")
	}
}

func TestLocationMatchRejectsDifferentFile(t *testing.T) {
	a := Finding{File: "a.go", Line: 10, Description: "nil map write"}
	b := Finding{File: "b.go", Line: 10, Description: "nil map write"}
	if LocationMatch(a, b) {
		t.Fatal("a different file is a different place and must not match")
	}
}

func TestLocationMatchRequiresBothLines(t *testing.T) {
	a := Finding{File: "a.go", Line: 10, Description: "nil map write"}
	b := Finding{File: "a.go", Description: "nil map write"}
	if LocationMatch(a, b) {
		t.Fatal("a finding without a line has no place and must not location-match")
	}
	if LocationMatch(b, a) {
		t.Fatal("symmetry: a zero line on either side refuses the match")
	}
}

func TestLocationMatchCaseAndWhitespaceNormalize(t *testing.T) {
	a := Finding{File: "internal/pkg/a.go", Line: 10, Description: "Nil Map WRITE in load"}
	b := Finding{File: "internal/pkg/a.go", Line: 12, Description: "nil map write in LOAD"}
	if !LocationMatch(a, b) {
		t.Fatal("case and whitespace differences must normalize away")
	}
}

func TestLocationOverlapScoresTheBestAnchor(t *testing.T) {
	strong := Finding{File: "a.go", Line: 11, Description: "nil map write in Load when cfg is empty"}
	weak := Finding{File: "a.go", Line: 10, Description: "nil map write in Load when cfg is empty though"}
	candidate := Finding{File: "a.go", Line: 12, Description: "Load writes to a nil map when cfg is empty"}

	strongScore, strongOK := LocationOverlap(candidate, strong)
	weakScore, weakOK := LocationOverlap(candidate, weak)
	if !strongOK || !weakOK {
		t.Fatalf("both anchors must pass the gate (strong=%v weak=%v)", strongOK, weakOK)
	}
	if strongScore <= weakScore {
		t.Fatalf("stronger overlap = %v, want above weaker %v", strongScore, weakScore)
	}

	missScore, missOK := LocationOverlap(candidate, Finding{File: "a.go", Line: 40, Description: "unchecked error return ignored in Save"})
	if missOK || missScore != 0 {
		t.Fatalf("a missed anchor returns (0, false), got (%v, %v)", missScore, missOK)
	}
}

func TestTokenJaccardEmptyStrings(t *testing.T) {
	if got := TokenJaccard("", ""); got != 0 {
		t.Fatalf("TokenJaccard(\"\", \"\") = %v, want 0", got)
	}
}
