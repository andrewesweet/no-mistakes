// Package findmatch holds the finding-similarity matcher shared by the eval
// scorer and the pipeline's review carry-forward.
//
// Two consumers need to answer "are these two findings the same underlying
// issue, reported at roughly the same place in the same file?": internal/eval
// scores candidate findings against recorded gold, and the pipeline's
// outstanding-finding merge annotates a new round's row as a possible
// restatement of an earlier one. Both must weight the answer the same way, so
// the matcher lives here once. The cascade around it (exact ID, exact text,
// assignment optimality) stays with each consumer.
package findmatch

import (
	"path/filepath"
	"strings"
)

const (
	// LineBand is the maximum line distance two findings may sit apart and
	// still count as the same place. A restatement typically lands a few
	// lines from its original after nearby edits.
	LineBand = 3
	// JaccardMin is the minimum normalized description-token overlap for two
	// findings to count as the same issue. Rewordings keep most content
	// tokens; unrelated findings in the same file do not.
	JaccardMin = 0.5
)

// Finding is the minimal location+text view of a finding the matcher needs.
// Consumers adapt their own types (eval's FindingGold, types.Finding) into
// this shape, so the package does not depend on either.
type Finding struct {
	File        string
	Line        int
	Description string
}

// LocationMatch reports whether a and b describe the same underlying issue at
// approximately the same place: same normalized file, both lines present and
// within LineBand, and normalized description overlap of at least JaccardMin.
func LocationMatch(a, b Finding) bool {
	_, ok := LocationOverlap(a, b)
	return ok
}

// LocationOverlap reports whether a and b are the same finding at the same
// place by the LocationMatch rule, and if so their normalized description
// overlap in [0,1]. The score lets a caller pick the BEST anchor when several
// findings pass the gate; the bool is the same answer LocationMatch returns.
func LocationOverlap(a, b Finding) (float64, bool) {
	fileA, descA := NormalizeIssue(a.File, a.Description)
	fileB, descB := NormalizeIssue(b.File, b.Description)
	if fileA == "" || fileB == "" || descA == "" || descB == "" {
		return 0, false
	}
	if fileA != fileB || a.Line <= 0 || b.Line <= 0 {
		return 0, false
	}
	if absInt(a.Line-b.Line) > LineBand {
		return 0, false
	}
	overlap := TokenJaccard(descA, descB)
	return overlap, overlap >= JaccardMin
}

// NormalizeIssue canonicalizes a file path and description for comparison:
// slash-separated trimmed paths, and lowercased whitespace-collapsed
// descriptions.
func NormalizeIssue(file, description string) (string, string) {
	file = filepath.ToSlash(strings.TrimSpace(file))
	description = strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(description))), " ")
	return file, description
}

// TokenJaccard is the Jaccard similarity of two strings' whitespace tokens
// after normalization; 0 when both are empty.
func TokenJaccard(a, b string) float64 {
	left := uniqueTokens(a)
	right := uniqueTokens(b)
	if len(left) == 0 && len(right) == 0 {
		return 0
	}
	inter := 0
	for tok := range left {
		if right[tok] {
			inter++
		}
	}
	union := len(left) + len(right) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func uniqueTokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range strings.Fields(s) {
		out[tok] = true
	}
	return out
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
