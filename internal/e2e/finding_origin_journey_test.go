//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// findingOriginScenario drives a reviewer that labels one finding introduced
// and one pre-existing. The fixer then receives only the introduced finding
// (the operator ignored the pre-existing one) under the prompt rule that keeps
// pre-existing findings outside the change, the rereview verifies the fix, and
// the run parks once more on the declined pre-existing finding until the
// operator approves it explicitly.
func findingOriginScenario(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "finding-origin-scenario.yaml")
	content := `actions:
  - match: "Fix-round provenance:"
    structured:
      findings: []
      summary: "fix verified"
      risk_level: low
      risk_rationale: "the introduced defect is gone"
      risk_scope: source-or-external
  - match: "Investigate previous review findings"
    edits:
      - path: new.go
        new: "fixed new code\n"
    structured:
      findings: []
      summary: "fix applied"
  - match: "Review the code changes and return structured findings"
    structured:
      findings:
        - id: introduced-defect
          severity: warning
          file: "new.go"
          line: 2
          description: "unbounded retry in the new wrapper"
          action: auto-fix
          review_scope: source
          origin: introduced
        - id: pre-existing-defect
          severity: warning
          file: "legacy.go"
          line: 3
          description: "old defect in code this change did not write"
          action: ask-user
          review_scope: source
          origin: pre-existing
      summary: "one finding from the change, one pre-existing"
      risk_level: medium
      risk_rationale: "one blocking finding"
      risk_scope: source-or-external
  - structured:
      findings: []
      summary: "clean"
      risk_level: low
      risk_rationale: "none"
      risk_scope: source-or-external
      tested: ["fixture"]
      testing_summary: "fixture"
      scenarios:
        - name: "fixture"
          result: pass
          live: true
          evidence: "fixture"
          reason: ""
      verdict: go
      artifacts: []
      title: "fix: new code"
      body: "Fixed"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write finding origin scenario: %v", err)
	}
	return path
}

// TestFindingOriginJourney is the end-to-end proof of the reviewer-set origin
// field: a fake reviewer labels one finding introduced and one pre-existing,
// the gate renders the pre-existing finding as its own group, the fixer is
// handed only the introduced finding under the rule that keeps pre-existing
// findings outside the change unless the operator selected them, and the run
// finishes when the operator explicitly approves the declined pre-existing
// finding.
func TestFindingOriginJourney(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: findingOriginScenario(t)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	branch := "feature/finding-origin"
	h.CommitChange(branch, "new.go", "new code\n", "new code")
	h.CommitChange(branch, "legacy.go", "legacy code\n", "legacy code")

	initial, err := h.Run("axi", "run", "--intent", "Label review findings by origin and group pre-existing ones")
	if err != nil || !strings.Contains(initial, "pre-existing-defect") {
		t.Fatalf("initial review gate: %v\n%s", err, initial)
	}

	// The review prompt carries the origin rule and the schema enum, so a real
	// reviewer learns the vocabulary this journey's canned reviewer uses.
	if !sawPromptContainingAll(h.AgentInvocations(), "Review the code changes", "set origin to exactly one of", `"pre-existing"`) {
		t.Fatal("review prompt missing the origin rule")
	}

	// The gate groups the findings: the pre-existing label renders its own
	// group marker and both findings stay visible.
	status, err := h.Run("axi", "status")
	if err != nil {
		t.Fatalf("axi status: %v\n%s", err, status)
	}
	for _, want := range []string{"status: awaiting_approval", "pre_existing_findings", "pre-existing-defect", "introduced-defect"} {
		if !strings.Contains(status, want) {
			t.Fatalf("gate missing %q in:\n%s", want, status)
		}
	}

	response, err := h.Run("axi", "respond", "--action", "fix", "--findings", "introduced-defect", "--ignore", "pre-existing-defect")
	if err != nil {
		t.Fatalf("select fix for the introduced finding: %v\n%s", err, response)
	}

	// The fixer sees only the selected introduced finding, under the rule that
	// keeps pre-existing findings outside the change unless selected.
	var fixPrompt string
	for _, inv := range h.AgentInvocations() {
		if strings.Contains(inv.Prompt, "Investigate previous review findings") {
			fixPrompt = inv.Prompt
		}
	}
	if fixPrompt == "" {
		t.Fatal("no fixer invocation recorded")
	}
	for _, want := range []string{`origin "pre-existing"`, "outside the change unless the operator selected it"} {
		if !strings.Contains(fixPrompt, want) {
			t.Errorf("fix prompt missing pre-existing scope rule %q:\n%s", want, fixPrompt)
		}
	}
	previous := strings.SplitN(fixPrompt, "Previous review findings to address:", 2)
	if len(previous) != 2 {
		t.Fatalf("fix prompt missing the findings payload:\n%s", fixPrompt)
	}
	if !strings.Contains(previous[1], "introduced-defect") {
		t.Errorf("fixer did not receive the selected introduced finding:\n%s", previous[1])
	}
	if strings.Contains(previous[1], "pre-existing-defect") {
		t.Errorf("the declined pre-existing finding must stay outside the fixer's list:\n%s", previous[1])
	}

	// The rereview verifies the fix; the declined pre-existing finding is
	// unselected and blocking, so the gate parks once more (inside the fix
	// loop, so fix_review) for an explicit operator decision on it.
	run := waitForStepStatus(t, h, branch, types.StepReview, types.StepStatusFixReview, 120*time.Second)
	carry, err := h.Run("axi", "status", "--run", run.ID)
	if err != nil || !strings.Contains(carry, "pre-existing-defect") || !strings.Contains(carry, "pre_existing_findings") {
		t.Fatalf("declined pre-existing finding not still grouped at the second park: %v\n%s", err, carry)
	}

	approve, err := h.Run("axi", "respond", "--action", "approve")
	if err != nil {
		t.Fatalf("approve the gate: %v\n%s", err, approve)
	}
	run = h.WaitForRun(branch, 120*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run did not complete after approval: status=%s error=%v", run.Status, run.Error)
	}
}
