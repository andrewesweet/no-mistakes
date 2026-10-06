package eval

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// paraphraseGold and paraphraseCandidate describe the same defect in
// independently worded prose: they share the defect's identifiers and core
// vocabulary but almost no sentence structure, the shape real replays produce
// against recorded gold. Their raw whitespace-token Jaccard is far below the
// threshold that made every independent replay score zero, so these fixtures
// pin the calibrated similarity, not literal word overlap.
const paraphraseGold = "session_reaper never releases a lease in production. bin/lease-minter.sh arm writes the lease as `l<epoch>.<pid>.<random>` (line 159), but the verifier rejects any id matching `*[!0-9]*`, so every real worker record returns 1. lease_sweep then only sees teardown, and stale-worker-suppress fires over a relaunched worker are counted correct."

const paraphraseCandidate = "session_reaper rejects every real worker lease token: the only lease minter is bin/lease-minter.sh:159 (`L=$(date +%s).$$.$RANDOM`, echoed verbatim into `v1 lease=<token>` records), and the guard `case \"$l\" in ''|*[!0-9]*) return 1` fails on the prefix letter and dots of every production token, so renewals never validate."

func paraphraseGoldLabels(line int) Labels {
	return Labels{Findings: []FindingGold{{
		ID:          "lease-format",
		Kind:        GoldTruePositive,
		File:        "internal/lease/reap.go",
		Line:        line,
		Description: paraphraseGold,
	}}}
}

func paraphraseCandidateJSON(line int) string {
	return fmt.Sprintf(`{"findings":[{"id":"lease-token-never-validates","file":"internal/lease/reap.go","line":%d,"description":%s}]}`,
		line, mustJSONString(paraphraseCandidate))
}

// mustJSONString embeds a fixture description into a findings JSON payload.
func mustJSONString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err) // json.Marshal of a string cannot fail
	}
	return string(encoded)
}

// TestScoreCandidateMatcherTable walks the documented match classes: the
// exact tiers, the location band, and the boundaries that must stay unmatched.
// One gold finding matches at most one candidate finding.
func TestScoreCandidateMatcherTable(t *testing.T) {
	unrelated := "the retry loop re-enqueues a job whose deadline already passed instead of failing it, so a poison message circulates until the queue depth alert trips and the worker pool starves."
	candidate := func(id, file string, line int, description string) string {
		return fmt.Sprintf(`{"findings":[{"id":%s,"file":%s,"line":%d,"description":%s}]}`,
			mustJSONString(id), mustJSONString(file), line, mustJSONString(description))
	}
	for _, tc := range []struct {
		name        string
		candidate   string
		wantTP      int
		wantExact   int
		wantFuzzy   int
		wantFN      int
		wantPending int
	}{
		{name: "same id in a different file is a miss",
			candidate: candidate("lease-format", "elsewhere/other.go", 900, paraphraseCandidate),
			wantFN:    1, wantPending: 1},
		{name: "same id beyond the line band is a miss",
			candidate: candidate("lease-format", "internal/lease/reap.go", 900, paraphraseCandidate),
			wantFN:    1, wantPending: 1},
		{name: "same id with a different claim is a miss",
			candidate: candidate("lease-format", "internal/lease/reap.go", 276, unrelated),
			wantFN:    1, wantPending: 1},
		{name: "exact text beyond the line band is a miss",
			candidate: candidate("other", "internal/lease/reap.go", 280, paraphraseGold),
			wantFN:    1, wantPending: 1},
		{name: "contained text beyond the line band is a miss",
			candidate: candidate("other", "internal/lease/reap.go", 280, paraphraseGold+" Additional context."),
			wantFN:    1, wantPending: 1},
		{name: "missing candidate line is a miss",
			candidate: candidate("lease-format", "internal/lease/reap.go", 0, paraphraseGold),
			wantFN:    1, wantPending: 1},
		{name: "paraphrase at the same line matches",
			candidate: paraphraseCandidateJSON(276),
			wantTP:    1, wantFuzzy: 1},
		{name: "paraphrase at the top of the line band matches",
			candidate: paraphraseCandidateJSON(279),
			wantTP:    1, wantFuzzy: 1},
		{name: "paraphrase at the bottom of the line band matches",
			candidate: paraphraseCandidateJSON(273),
			wantTP:    1, wantFuzzy: 1},
		{name: "same defect paraphrase beyond the line band is a miss",
			candidate: paraphraseCandidateJSON(280),
			wantFN:    1, wantPending: 1},
		{name: "same defect paraphrase in a different file is a miss",
			candidate: candidate("lease-token-never-validates", "internal/lease/other.go", 276, paraphraseCandidate),
			wantFN:    1, wantPending: 1},
		{name: "a different defect at the same file and line stays unmatched",
			candidate: candidate("retry-loop-poison", "internal/lease/reap.go", 276, unrelated),
			wantFN:    1, wantPending: 1},
		{name: "two exact candidates competing for one gold match exactly one",
			candidate: fmt.Sprintf(`{"findings":[%s,%s]}`,
				strings.TrimSuffix(strings.TrimPrefix(candidate("replay-a", "internal/lease/reap.go", 276, paraphraseGold), `{"findings":[`), `]}`),
				strings.TrimSuffix(strings.TrimPrefix(candidate("replay-b", "internal/lease/reap.go", 277, paraphraseGold), `{"findings":[`), `]}`)),
			wantTP: 1, wantExact: 1, wantPending: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			score := ScoreCandidate(paraphraseGoldLabels(276), tc.candidate)
			if score.TruePositive != tc.wantTP || score.TruePositiveExact != tc.wantExact ||
				score.TruePositiveFuzzy != tc.wantFuzzy || score.FalseNegative != tc.wantFN ||
				score.Pending != tc.wantPending {
				t.Fatalf("score = %#v, want tp %d exact %d fuzzy %d fn %d pending %d",
					score, tc.wantTP, tc.wantExact, tc.wantFuzzy, tc.wantFN, tc.wantPending)
			}
		})
	}
}

func TestScoreCandidateMatchesNearbyLineWithSimilarDescription(t *testing.T) {
	labels := Labels{Findings: []FindingGold{{
		ID:          "gold",
		Kind:        GoldTruePositive,
		File:        "internal/eval/score.go",
		Line:        10,
		Description: "drops an HTTP error on the handler path",
	}}}
	candidate := `{"findings":[{"id":"other","file":"internal/eval/score.go","line":12,"description":"drops an HTTP error on handler path"}]}`

	score := ScoreCandidate(labels, candidate)
	if score.TruePositive != 1 || score.TruePositiveFuzzy != 1 || score.TruePositiveExact != 0 || score.FalseNegative != 0 || score.Pending != 0 {
		t.Fatalf("score = %#v, want a fuzzy location match", score)
	}
}

func TestScoreCandidateSplitsIdentifierCaseBeforeNormalization(t *testing.T) {
	for _, descriptions := range [][2]string{
		{"LoadUserProfile neglects tokenExpiry", "User profile ignores token expiry"},
		{"HTTPServer ignores tokenExpiry", "HTTP server neglects token expiry"},
	} {
		for _, reverse := range []bool{false, true} {
			gold, candidate := descriptions[0], descriptions[1]
			if reverse {
				gold, candidate = candidate, gold
			}
			labels := Labels{Findings: []FindingGold{{Kind: GoldTruePositive, File: "main.go", Line: 10, Description: gold}}}
			raw := fmt.Sprintf(`{"findings":[{"id":"independent","file":"main.go","line":12,"description":%s}]}`, mustJSONString(candidate))
			score := ScoreCandidate(labels, raw)
			if score.TruePositive != 1 || score.TruePositiveFuzzy != 1 || score.FalseNegative != 0 || score.Pending != 0 {
				t.Fatalf("%q against %q: score = %#v, want fuzzy match", candidate, gold, score)
			}
		}
	}
}

func TestScoreCandidateDoesNotCreditSharedContextForDistinctDefects(t *testing.T) {
	descriptions := [2]string{
		"parse_config accepts duplicate keys silently overwriting values",
		"parse_config accepts unknown keys rather than rejecting typos",
	}
	for _, kind := range []string{GoldTruePositive, GoldFalseNegative, GoldFalsePositive} {
		for _, reverse := range []bool{false, true} {
			gold, candidate := descriptions[0], descriptions[1]
			if reverse {
				gold, candidate = candidate, gold
			}
			labels := Labels{Findings: []FindingGold{{ID: "R1", Kind: kind, File: "main.go", Line: 10, Description: gold}}}
			raw := fmt.Sprintf(`{"findings":[{"id":"independent","file":"main.go","line":10,"description":%s}]}`, mustJSONString(candidate))
			score := ScoreCandidate(labels, raw)
			if score.TruePositive != 0 || score.FalsePositive != 0 || score.Pending != 1 {
				t.Fatalf("%s: %q against %q: score = %#v, want no defect match", kind, candidate, gold, score)
			}
		}
	}
}

func TestScoreCandidateRequiresLocationsForEveryGoldKind(t *testing.T) {
	for _, kind := range []string{GoldTruePositive, GoldFalseNegative, GoldFalsePositive} {
		for _, lines := range [][2]int{{0, 10}, {10, 0}, {0, 0}, {10, 14}, {10, 6}} {
			labels := Labels{Findings: []FindingGold{{ID: "R1", Kind: kind, File: "main.go", Line: lines[0], Description: "real bug"}}}
			raw := fmt.Sprintf(`{"findings":[{"id":"R1","file":"main.go","line":%d,"description":"real bug"}]}`, lines[1])
			score := ScoreCandidate(labels, raw)
			if score.TruePositive != 0 || score.FalsePositive != 0 || score.Pending != 1 {
				t.Fatalf("%s at lines %v: score = %#v, want no location match", kind, lines, score)
			}
		}
	}
}

func TestScoreCandidateDoesNotMatchShortContainment(t *testing.T) {
	labels := Labels{Findings: []FindingGold{{
		ID:          "gold",
		Kind:        GoldTruePositive,
		File:        "main.go",
		Line:        1,
		Description: "bug in the widget factory initialization sequence during startup",
	}}}
	candidate := `{"findings":[{"id":"other","file":"main.go","line":1,"description":"bug"}]}`

	score := ScoreCandidate(labels, candidate)
	if score.TruePositive != 0 || score.FalseNegative != 1 || score.Pending != 1 {
		t.Fatalf("score = %#v, want short containment left unmatched", score)
	}
}

func TestScoreCandidatePrefersExactOverFuzzy(t *testing.T) {
	labels := Labels{Findings: []FindingGold{{
		ID:          "error-handling",
		Kind:        GoldTruePositive,
		File:        "old.go",
		Line:        4,
		Description: "drops an HTTP error",
	}}}
	candidate := `{"findings":[{"id":"nearby","file":"old.go","line":5,"description":"drops an HTTP error on the handler"},{"id":"different","file":"old.go","line":4,"description":"drops an HTTP error"}]}`

	score := ScoreCandidate(labels, candidate)
	if score.TruePositive != 1 || score.TruePositiveExact != 1 || score.TruePositiveFuzzy != 0 || score.Pending != 1 {
		t.Fatalf("score = %#v, want exact text to win over a nearby fuzzy candidate", score)
	}
}

func TestScoreCandidateDoesNotLetFuzzyEarlierGoldStealExactLaterMatch(t *testing.T) {
	labels := Labels{Findings: []FindingGold{
		{
			ID:          "nil-deref",
			Kind:        GoldTruePositive,
			File:        "main.go",
			Line:        10,
			Description: "nil pointer dereference in the request handler when the retry budget is exhausted",
		},
		{
			ID:          "shutdown-deref",
			Kind:        GoldTruePositive,
			File:        "main.go",
			Line:        12,
			Description: "nil pointer dereference in the request handler during graceful shutdown",
		},
	}}
	candidate := `{"findings":[` +
		`{"id":"independent","file":"main.go","line":12,"description":"nil pointer dereference in the request handler during graceful shutdown"},` +
		`{"id":"other","file":"main.go","line":11,"description":"handler nil dereference while the retry budget is exhausted"}` +
		`]}`

	score := ScoreCandidate(labels, candidate)
	if score.TruePositive != 2 || score.FalseNegative != 0 {
		t.Fatalf("score = %#v, want exact text for the later gold and fuzzy cover for the earlier gold", score)
	}
}

// Both gold items describe distinct defect claims. Candidate 1 matches the
// second gold exactly by text, and candidate 2 covers the first gold fuzzily
// through shared claim vocabulary. A greedy tier cascade that let candidate 1
// satisfy the first gold would strand the second; the globally optimal
// assignment pairs each candidate with the gold it actually claims.
func TestScoreCandidateRecoversMatchTheTieredMatcherLost(t *testing.T) {
	labels := Labels{Findings: []FindingGold{
		{
			ID:          "shared-id",
			Kind:        GoldTruePositive,
			File:        "main.go",
			Line:        10,
			Description: "nil pointer dereference in the request handler when the retry budget is exhausted",
		},
		{
			ID:          "shared-id",
			Kind:        GoldTruePositive,
			File:        "main.go",
			Line:        12,
			Description: "nil pointer dereference in the request handler while tracing drops span attributes",
		},
	}}
	candidate := `{"findings":[` +
		`{"id":"shared-id","file":"main.go","line":12,"description":"nil pointer dereference in the request handler while tracing drops span attributes"},` +
		`{"id":"other","file":"main.go","line":9,"description":"request handler nil dereference once the retry budget is exhausted"}` +
		`]}`

	score := ScoreCandidate(labels, candidate)
	if score.TruePositive != 2 || score.FalseNegative != 0 {
		t.Fatalf("score = %#v, want both gold items matched rather than one consumed by a tier boundary", score)
	}
	if score.TruePositiveExact != 1 || score.TruePositiveFuzzy != 1 {
		t.Fatalf("score = %#v, want the exact match kept and the second gold covered fuzzily", score)
	}
	if score.Pending != 0 {
		t.Fatalf("score = %#v, want both candidates consumed by the assignment", score)
	}
}

// TestScoreCandidateRecoversMatchTheTieredMatcherLost exists to pin assignment
// optimality; this one pins the maintainer-reported precision case.
//
// The maintainer pair (and the reviewer's first negative) share only location
// vocabulary with the gold: parse_config, keys, accepts, silently are words
// most findings in this file use. With that vocabulary down-weighted, the
// distinct-defect replay shares one incidental token (values) with the gold
// and stays unmatched, while a true duplicate-keys replay still matches on its
// defect claim.
func TestScoreCandidateMaintainerPairStaysUnmatched(t *testing.T) {
	labels := Labels{Findings: []FindingGold{
		{
			ID:          "dup-keys-overwrite",
			Kind:        GoldTruePositive,
			File:        "internal/config/parse.go",
			Line:        40,
			Description: "parse_config accepts duplicate keys silently overwriting values",
		},
		{
			ID:          "malformed-lines",
			Kind:        GoldTruePositive,
			File:        "internal/config/parse.go",
			Line:        80,
			Description: "parse_config rejects malformed lines with a usable error",
		},
		{
			ID:          "unknown-keys-accepted",
			Kind:        GoldTruePositive,
			File:        "internal/config/parse.go",
			Line:        120,
			Description: "parse_config accepts unknown keys rather than rejecting typos",
		},
	}}
	candidate := `{"findings":[` +
		`{"id":"replay-dup-keys","file":"internal/config/parse.go","line":40,"description":"the last duplicate wins because parse_config is silently overwriting values"},` +
		`{"id":"replay-unknown-keys","file":"internal/config/parse.go","line":41,"description":"parse_config accepts unknown keys silently allowing misspelled option values"}` +
		`]}`

	score := ScoreCandidate(labels, candidate)
	if score.TruePositive != 1 || score.TruePositiveFuzzy != 1 {
		t.Fatalf("score = %#v, want the true duplicate-keys replay matched fuzzily", score)
	}
	if score.FalseNegative != 2 {
		t.Fatalf("score = %#v, want the two unmatched golds counted as misses", score)
	}
	if score.Pending != 1 {
		t.Fatalf("score = %#v, want the maintainer pair (replay-unknown-keys) left pending, not credited", score)
	}
}

// The assignment must be the exact optimum, not a good heuristic, so it is
// checked against exhaustive enumeration on random small weight matrices of
// both orientations.
func TestMaxWeightAssignmentMatchesBruteForceOptimum(t *testing.T) {
	rng := rand.New(rand.NewSource(20260816))
	for trial := 0; trial < 400; trial++ {
		rows := 1 + rng.Intn(5)
		cols := 1 + rng.Intn(5)
		weight := make([][]int64, rows)
		for i := range weight {
			weight[i] = make([]int64, cols)
			for j := range weight[i] {
				// Zero is the common case on purpose: it is how a missing edge
				// is spelled, and it is where a greedy solver goes wrong.
				if rng.Intn(3) == 0 {
					weight[i][j] = int64(rng.Intn(4)) * 9
				}
			}
		}
		got := totalWeight(weight, maxWeightAssignment(weight))
		want := bruteForceMaxWeight(weight)
		if got != want {
			t.Fatalf("assignment weight = %d, want the optimum %d for %v", got, want, weight)
		}
	}
}

func totalWeight(weight [][]int64, rowToCol []int) int64 {
	var total int64
	seen := map[int]bool{}
	for i, j := range rowToCol {
		if j < 0 {
			continue
		}
		if seen[j] {
			panic("assignment reused a column")
		}
		seen[j] = true
		total += weight[i][j]
	}
	return total
}

func bruteForceMaxWeight(weight [][]int64) int64 {
	cols := len(weight[0])
	used := make([]bool, cols)
	var best func(row int) int64
	best = func(row int) int64 {
		if row == len(weight) {
			return 0
		}
		top := best(row + 1)
		for j := 0; j < cols; j++ {
			if used[j] {
				continue
			}
			used[j] = true
			if got := weight[row][j] + best(row+1); got > top {
				top = got
			}
			used[j] = false
		}
		return top
	}
	return best(0)
}

func TestScoreCandidateKeepsUnmatchedPendingUntilAdjudicated(t *testing.T) {
	labels := Labels{Findings: []FindingGold{{
		ID:          "gold",
		Kind:        GoldTruePositive,
		File:        "main.go",
		Line:        1,
		Description: "real bug",
	}}}
	candidate := `{"findings":[{"id":"gold","file":"main.go","line":1,"description":"real bug"},{"id":"extra","file":"main.go","line":1,"description":"new later issue"}]}`

	score := ScoreCandidate(labels, candidate)
	if score.TruePositive != 1 || score.FalsePositive != 0 || score.Pending != 1 {
		t.Fatalf("score = %#v, want unmatched extra queued as pending, not FP", score)
	}
}

func TestScoreCandidateCountsExplicitFalsePositiveGold(t *testing.T) {
	labels := Labels{Findings: []FindingGold{{
		ID:          "noise",
		Kind:        GoldFalsePositive,
		File:        "main.go",
		Line:        1,
		Description: "style nit",
	}}}
	candidate := `{"findings":[{"id":"noise","file":"main.go","line":1,"description":"style nit"}]}`

	score := ScoreCandidate(labels, candidate)
	if score.FalsePositive != 1 || score.FalsePositiveGold != 1 || score.Pending != 0 || score.TruePositive != 0 {
		t.Fatalf("score = %#v, want explicit FP gold counted", score)
	}
}

func TestEvaluationSummaryWithholdsHeadlineF1WithoutFalsePositiveGold(t *testing.T) {
	summary := SummarizeEvaluations([]Evaluation{{
		Candidate: "claude+test", Status: "completed", HasFindingGold: true, GoldCount: 2,
		TruePositive: 2, Pending: 1,
	}})
	if summary.Recall() != 1 {
		t.Fatalf("recall = %v, want 1", summary.Recall())
	}
	if summary.HasFalsePositiveGold() {
		t.Fatal("summary reported FP gold when none existed")
	}
	output := RenderReport([]CandidateReport{{Cohort: "c", Summary: summary, RepeatCount: 1}})
	if !strings.Contains(output, "recall: 100.0%") {
		t.Fatalf("report = %q, want recall as the headline", output)
	}
	if !strings.Contains(output, "precision") || !strings.Contains(output, "pending") {
		t.Fatalf("report = %q, want precision bounds and pending", output)
	}
	if strings.Contains(output, "F1:") && !strings.Contains(output, "F1: withheld") {
		t.Fatalf("report = %q, want F1 withheld when there is no false-positive gold", output)
	}
}

func TestEvaluationSummaryHeadlinesF1WhenFalsePositiveGoldExists(t *testing.T) {
	summary := SummarizeEvaluations([]Evaluation{{
		Candidate: "claude+test", Status: "completed", HasFindingGold: true, GoldCount: 2,
		TruePositive: 2, FalsePositive: 1, FalsePositiveGold: 1,
	}})
	if !summary.HasFalsePositiveGold() {
		t.Fatal("summary missing FP gold")
	}
	if got := summary.Precision(); got != 2.0/3.0 {
		t.Fatalf("precision = %v, want 2/3", got)
	}
	output := RenderReport([]CandidateReport{{Cohort: "c", Summary: summary, RepeatCount: 1}})
	if !strings.Contains(output, "F1:") || strings.Contains(output, "F1: withheld") {
		t.Fatalf("report = %q, want headline F1 once false-positive gold exists", output)
	}
}

func TestEvaluationSummaryPrecisionBoundsTreatPendingAsWorstCase(t *testing.T) {
	summary := EvaluationSummary{TruePositive: 1, FalseNegative: 1, FalsePositive: 0, Pending: 1, Labeled: 1}
	if got := summary.Precision(); got != 1 {
		t.Fatalf("precision_adj = %v, want 1 (no adjudicated FP)", got)
	}
	if got := summary.PrecisionLower(); got != 0.5 {
		t.Fatalf("precision_lower = %v, want 0.5 (pending treated as FP)", got)
	}
}
