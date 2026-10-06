package eval

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

const (
	matchExactText            = "exact-text"
	matchLocation             = "location"
	locationLineBand          = 3
	locationJaccardMin        = 0.5
	paraphraseJaccardMin      = 0.175
	paraphraseMinSharedTokens = 12
	// ubiquitousMinDescriptions is how many descriptions one file needs before
	// its shared vocabulary can be recognized as location vocabulary. Below
	// this, prevalence is meaningless (two findings share wording by chance)
	// and nothing is down-weighted.
	ubiquitousMinDescriptions = 3
)

// Score is one candidate's finding-level confusion matrix against gold.
// Pending is unmatched candidate findings: queued, never punished as FP.
type Score struct {
	TruePositive      int
	TruePositiveExact int
	TruePositiveFuzzy int
	FalseNegative     int
	FalsePositive     int
	FalsePositiveGold int
	Pending           int
}

// ScoreCandidate matches a candidate finding list against recorded gold.
//
//   - TP: the candidate raises the same underlying issue as a true-issue gold
//     (human-accepted Fix, auto-fix that landed in a merged PR, a
//     human-added miss, or a confirmed post-PR miss)
//   - FN: the candidate misses a true-issue gold
//   - FP: only an explicit false-positive gold that the candidate still raised
//   - Pending: unmatched candidate findings, never inferred as invalid
//
// Matching is a documented cascade of strengths: exact-text and nearby-line
// similarity. Assignment is one globally
// optimal assignment over the whole graph (see assignMatches), so neither
// candidate ordering nor a tier boundary can consume a candidate another gold
// needed. Headline recall uses the full cascade; exact vs fuzzy counts are
// reported separately so a threshold change is visible.
func ScoreCandidate(labels Labels, findingsJSON string) Score {
	candidate := parseFindingItems(findingsJSON)
	assigned := assignMatches(labels.Findings, candidate, newClaimContexts(labels.Findings, candidate))
	used := make([]bool, len(candidate))
	var score Score
	for i, gold := range labels.Findings {
		if gold.Kind == GoldFalsePositive {
			score.FalsePositiveGold++
		}
		match := assigned[i]
		switch {
		case isTrueIssueGold(gold.Kind) && match.cand >= 0:
			score.TruePositive++
			if match.strength == matchExactText {
				score.TruePositiveExact++
			} else {
				score.TruePositiveFuzzy++
			}
			used[match.cand] = true
		case isTrueIssueGold(gold.Kind):
			score.FalseNegative++
		case gold.Kind == GoldFalsePositive && match.cand >= 0:
			score.FalsePositive++
			used[match.cand] = true
		}
	}
	for i := range candidate {
		if !used[i] {
			score.Pending++
		}
	}
	return score
}

func parseFindingItems(raw string) []types.Finding {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return nil
	}
	return findings.Items
}

type assignedMatch struct {
	cand     int
	strength string
}

// assignMatches pairs gold findings with candidate findings as one globally
// optimal assignment rather than a per-strength-tier pass.
//
// Tiered assignment - maximum matching on exact edges, then on the residual for
// each weaker strength - is not optimal for the whole graph, because WHICH
// maximum exact matching it happens to pick decides what the weaker tiers can
// still reach. With gold A matching candidate 1 exactly and candidate 2 by
// location, and gold B matching only candidate 1 exactly, the exact tier can
// hand candidate 1 to A and strand B forever, scoring one match where two exist.
// That understates recall for reasons that have nothing to do with the review
// under test, so the assignment is solved once over every edge at once.
//
// Optimality is lexicographic by strength: an exact pair is worth strictly more
// than every possible combination of weaker pairs (weightFor scales each tier by
// a base larger than any achievable count), so the optimum never trades one
// exact match for two fuzzy ones, and among the assignments with the most exact
// pairs it takes the one with the most location pairs.
// hungarianMinCost solves that max-weight assignment exactly.
func assignMatches(golds []FindingGold, candidate []types.Finding, contexts map[string]*claimContext) []assignedMatch {
	out := make([]assignedMatch, len(golds))
	for i := range out {
		out[i].cand = -1
	}
	if len(golds) == 0 || len(candidate) == 0 {
		return out
	}
	base := int64(len(golds)+len(candidate)) + 1
	weight := make([][]int64, len(golds))
	strength := make([][]string, len(golds))
	for gi, gold := range golds {
		weight[gi] = make([]int64, len(candidate))
		strength[gi] = make([]string, len(candidate))
		for ci, finding := range candidate {
			for _, s := range []string{matchExactText, matchLocation} {
				if matchAt(gold, finding, s, contexts) {
					weight[gi][ci] = weightFor(s, base)
					strength[gi][ci] = s
					break
				}
			}
		}
	}
	for gi, ci := range maxWeightAssignment(weight) {
		// A zero-weight pair is the absence of an edge, not a match: the
		// assignment is over a complete matrix so every row gets a column.
		if ci < 0 || weight[gi][ci] == 0 {
			continue
		}
		out[gi] = assignedMatch{cand: ci, strength: strength[gi][ci]}
	}
	return out
}

// weightFor ranks the strengths so that no number of weaker pairs can outweigh
// a single stronger one. base exceeds the largest possible pair count, so the
// total weight of an assignment reads as a positional number whose digits are
// the per-strength counts.
func weightFor(strength string, base int64) int64 {
	switch strength {
	case matchExactText:
		return base * base
	case matchLocation:
		return base
	default:
		return 0
	}
}

// maxWeightAssignment returns, for each gold row, the candidate column assigned
// to it (-1 when there are no columns), maximizing total weight. It solves the
// rectangular assignment problem exactly, transposing when there are more rows
// than columns because the solver requires rows <= columns.
func maxWeightAssignment(weight [][]int64) []int {
	rows, cols := len(weight), len(weight[0])
	if rows <= cols {
		return hungarianMinCost(negate(weight))
	}
	transposed := make([][]int64, cols)
	for j := range transposed {
		transposed[j] = make([]int64, rows)
		for i := range weight {
			transposed[j][i] = -weight[i][j]
		}
	}
	colToRow := hungarianMinCost(transposed)
	rowToCol := make([]int, rows)
	for i := range rowToCol {
		rowToCol[i] = -1
	}
	for j, i := range colToRow {
		if i >= 0 {
			rowToCol[i] = j
		}
	}
	return rowToCol
}

func negate(weight [][]int64) [][]int64 {
	out := make([][]int64, len(weight))
	for i, row := range weight {
		out[i] = make([]int64, len(row))
		for j, w := range row {
			out[i][j] = -w
		}
	}
	return out
}

// hungarianMinCost is the O(n^2*m) Hungarian (Kuhn-Munkres) algorithm for the
// rectangular assignment problem: it returns the minimum-cost assignment of
// every row to a distinct column, which is the exact optimum rather than a
// greedy approximation. It requires len(cost) <= len(cost[0]).
func hungarianMinCost(cost [][]int64) []int {
	n := len(cost)
	if n == 0 {
		return nil
	}
	m := len(cost[0])
	const inf = int64(1) << 62
	// Potentials u/v and the column-to-row matching p are 1-indexed; index 0 is
	// the algorithm's virtual starting column.
	u := make([]int64, n+1)
	v := make([]int64, m+1)
	p := make([]int, m+1)
	way := make([]int, m+1)
	for i := 1; i <= n; i++ {
		p[0] = i
		j0 := 0
		minv := make([]int64, m+1)
		used := make([]bool, m+1)
		for j := range minv {
			minv[j] = inf
		}
		for {
			used[j0] = true
			i0 := p[j0]
			delta := inf
			j1 := 0
			for j := 1; j <= m; j++ {
				if used[j] {
					continue
				}
				cur := cost[i0-1][j-1] - u[i0] - v[j]
				if cur < minv[j] {
					minv[j] = cur
					way[j] = j0
				}
				if minv[j] < delta {
					delta = minv[j]
					j1 = j
				}
			}
			for j := 0; j <= m; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for j0 != 0 {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
		}
	}
	rowToCol := make([]int, n)
	for i := range rowToCol {
		rowToCol[i] = -1
	}
	for j := 1; j <= m; j++ {
		if p[j] > 0 {
			rowToCol[p[j]-1] = j - 1
		}
	}
	return rowToCol
}

// claimContext is the per-file vocabulary the location tier down-weights
// before comparing two descriptions, so the gate measures shared defect-claim
// words instead of shared location vocabulary: tokens drawn from the finding
// file path, plus tokens that most findings in that file share (present in
// more than half of them, when the file has enough findings for prevalence to
// mean anything). Both halves are computed from data the score already has -
// the finding's own file and the case's finding pool - so matching stays
// deterministic and model-free.
type claimContext struct {
	pathTokens map[string]bool
	ubiquitous map[string]bool
}

// newClaimContexts builds one claimContext per file named by any gold or
// candidate finding, from every description recorded for that file.
func newClaimContexts(golds []FindingGold, candidate []types.Finding) map[string]*claimContext {
	descs := map[string][]string{}
	add := func(file, description string) {
		file, desc := normalizeIssue(file, description)
		if file == "" || desc == "" {
			return
		}
		descs[file] = append(descs[file], desc)
	}
	for _, gold := range golds {
		add(gold.File, gold.Description)
	}
	for _, finding := range candidate {
		add(finding.File, finding.Description)
	}
	out := map[string]*claimContext{}
	for file, list := range descs {
		ctx := &claimContext{pathTokens: claimPathTokens(file)}
		if len(list) >= ubiquitousMinDescriptions {
			df := map[string]int{}
			for _, desc := range list {
				seen := map[string]bool{}
				for tok := range similarityTokens(desc) {
					seen[tok] = true
				}
				for tok := range seen {
					df[tok]++
				}
			}
			ctx.ubiquitous = map[string]bool{}
			for tok, count := range df {
				if float64(count) > float64(len(list))/2 {
					ctx.ubiquitous[tok] = true
				}
			}
		}
		out[file] = ctx
	}
	return out
}

// claimPathTokens splits the finding's file path into its identifier words.
// Extension tokens and single letters are dropped; they never carry defect
// content, and their presence in both descriptions is pure location echo.
func claimPathTokens(file string) map[string]bool {
	out := map[string]bool{}
	for _, seg := range splitIdentifierWords(filepath.ToSlash(file)) {
		switch seg {
		case "go", "ts", "sh", "md", "json", "yaml", "yml", "test":
			continue
		}
		if len(seg) > 1 {
			out[seg] = true
		}
	}
	return out
}

func matchAt(gold FindingGold, finding types.Finding, strength string, contexts map[string]*claimContext) bool {
	goldFile, goldDesc := normalizeIssue(gold.File, gold.Description)
	candFile, candDesc := normalizeIssue(finding.File, finding.Description)
	if goldFile == "" || candFile == "" || goldDesc == "" || candDesc == "" {
		return false
	}
	if goldFile != candFile || gold.Line <= 0 || finding.Line <= 0 {
		return false
	}
	if absInt(gold.Line-finding.Line) > locationLineBand {
		return false
	}
	switch strength {
	case matchExactText:
		return strings.EqualFold(goldDesc, candDesc)
	case matchLocation:
		return similarDescriptions(goldDesc, candDesc, contexts[goldFile])
	default:
		return false
	}
}

// similarDescriptions is an automatic, model-free comparison of the two
// descriptions' defect-claim vocabulary: similarityTokens minus the file's
// location vocabulary (path words and tokens most findings in that file
// share; see claimContext). Calibrated on the captured corpus with this
// down-weighting, all twelve hand-adjudicated same-defect pairs score
// 0.179-0.403 with 19-34 shared claim tokens, while the reviewer's distinct
// nearby-defect pairs - gold "parse_config accepts duplicate keys silently
// overwriting values" against "accepts unknown keys silently allowing
// misspelled option values" and against "accepts unknown keys rather than
// rejecting typos" - share only incidental tokens (one, "values", for the
// first) once parse/config/keys/accepts/silently are recognized as the
// file's shared vocabulary, far below every calibrated pair. Residual limit:
// two truly distinct defects at one anchor can still share claim words (the
// corpus's unadjudicated backstop pair still matches at 0.257); the gate no
// longer credits shared location wording on its own.
// In a file with exactly three descriptions, a shared claim can be treated
// as ubiquitous and a true paraphrase can go unmatched.
// In files with fewer than three descriptions, no ubiquity filtering applies.
func similarDescriptions(a, b string, ctx *claimContext) bool {
	left := claimTokens(a, ctx)
	right := claimTokens(b, ctx)
	inter := 0
	for tok := range left {
		if right[tok] {
			inter++
		}
	}
	union := len(left) + len(right) - inter
	if union == 0 {
		return false
	}
	similarity := float64(inter) / float64(union)
	return similarity >= locationJaccardMin ||
		(inter >= paraphraseMinSharedTokens && similarity >= paraphraseJaccardMin)
}

// claimTokens drops the file's location vocabulary from a description's
// tokens, leaving what the location tier may credit a match on.
func claimTokens(description string, ctx *claimContext) map[string]bool {
	out := map[string]bool{}
	for tok := range similarityTokens(description) {
		if ctx != nil && (ctx.pathTokens[tok] || ctx.ubiquitous[tok]) {
			continue
		}
		out[tok] = true
	}
	return out
}

// similarityTokens normalizes a finding description into the vocabulary the
// location tier compares: identifiers split into words (snake_case, dotted
// paths, and camelCase boundaries), English function words dropped, and
// single-character tokens dropped. Two independently worded descriptions of
// one defect share that defect's identifier and claim vocabulary even when
// their sentence structures differ, which is the signal the location tier's
// semantic gate needs; literal whitespace tokens tied the gate to wording.
func similarityTokens(description string) map[string]bool {
	out := map[string]bool{}
	for _, field := range strings.Fields(description) {
		for _, word := range splitIdentifierWords(field) {
			if len(word) > 1 && !similarityStopword(word) {
				out[word] = true
			}
		}
	}
	return out
}

// splitIdentifierWords breaks one whitespace field into its identifier words:
// non-alphanumeric characters separate (so bin/lease-minter.sh yields bin,
// lease, minter, sh) and case or letter-digit boundaries split the remainder
// (so VDETAIL2nd stays vdetail, nd; HTTPServer yields http, server).
func splitIdentifierWords(field string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(field, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		out = append(out, splitCaseWords(part)...)
	}
	return out
}

func splitCaseWords(part string) []string {
	runes := []rune(part)
	if len(runes) == 0 {
		return nil
	}
	var out []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}
		split := false
		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(cur):
			split = true // camelCase boundary
		case unicode.IsUpper(prev) && unicode.IsUpper(cur) && unicode.IsLower(next):
			split = true // acronym-to-word boundary (HTTPServer)
		case unicode.IsLetter(prev) && unicode.IsDigit(cur):
			split = true // v2 boundary
		case unicode.IsDigit(prev) && unicode.IsLetter(cur):
			split = true // 2nd boundary
		}
		if split {
			out = append(out, strings.ToLower(string(runes[start:i])))
			start = i
		}
	}
	return append(out, strings.ToLower(string(runes[start:])))
}

// similarityStopword reports the English function words that carry wording,
// not defect content. Dropping them is what lets two paraphrases of one bug
// clear the gate on their shared claim vocabulary.
var similarityStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "been": true, "being": true, "that": true,
	"this": true, "these": true, "those": true, "with": true, "for": true,
	"and": true, "or": true, "not": true, "no": true, "nor": true, "but": true,
	"if": true, "then": true, "than": true, "as": true, "at": true, "by": true,
	"of": true, "on": true, "in": true, "to": true, "from": true, "it": true,
	"its": true, "into": true, "over": true, "under": true, "after": true,
	"before": true, "between": true, "during": true, "through": true,
	"while": true, "when": true, "where": true, "which": true, "who": true,
	"whose": true, "what": true, "how": true, "why": true, "all": true,
	"any": true, "both": true, "each": true, "few": true, "more": true,
	"most": true, "other": true, "some": true, "such": true, "only": true,
	"own": true, "same": true, "so": true, "too": true, "very": true,
	"can": true, "will": true, "just": true, "should": true, "now": true,
	"still": true, "every": true, "however": true, "because": true,
	"about": true, "against": true, "above": true, "below": true, "off": true,
	"out": true, "up": true, "down": true, "again": true, "further": true,
	"once": true, "here": true, "there": true, "also": true, "thus": true,
	"hence": true, "therefore": true, "may": true, "might": true, "must": true,
	"shall": true, "would": true, "could": true, "do": true, "does": true,
	"did": true, "done": true, "doing": true, "having": true, "has": true,
	"have": true, "had": true,
}

func similarityStopword(word string) bool { return similarityStopwords[word] }

func normalizeIssue(file, description string) (string, string) {
	file = filepath.ToSlash(strings.TrimSpace(file))
	description = strings.Join(strings.Fields(description), " ")
	return file, description
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
