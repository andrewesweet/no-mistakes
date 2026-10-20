package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// The review schema teaches the reviewer the origin vocabulary: an OPTIONAL
// enum on each finding, never a required field, because a reviewer that omits
// origin means "introduced" and older fixtures omit it legitimately.
func TestReviewFindingsSchema_OriginIsAnOptionalEnum(t *testing.T) {
	t.Parallel()
	var parsed map[string]interface{}
	if err := json.Unmarshal(reviewFindingsSchema, &parsed); err != nil {
		t.Fatal(err)
	}
	props := parsed["properties"].(map[string]interface{})
	items := props["findings"].(map[string]interface{})["items"].(map[string]interface{})
	itemProps := items["properties"].(map[string]interface{})
	origin, ok := itemProps["origin"].(map[string]interface{})
	if !ok {
		t.Fatal("reviewFindingsSchema missing origin property on finding items")
	}
	enum, ok := origin["enum"].([]interface{})
	if !ok || len(enum) != 2 {
		t.Fatalf("origin enum = %#v, want exactly [introduced pre-existing]", origin["enum"])
	}
	seen := map[string]bool{}
	for _, v := range enum {
		s, _ := v.(string)
		seen[s] = true
	}
	if !seen[types.FindingOriginIntroduced] || !seen[types.FindingOriginPreExisting] {
		t.Fatalf("origin enum = %v, want introduced and pre-existing", enum)
	}
	for _, r := range items["required"].([]interface{}) {
		if r.(string) == "origin" {
			t.Error("origin must not be required: omitting it counts as introduced")
		}
	}
}

// The reviewer is told when to set pre-existing: a finding entirely in code
// this change did not introduce. The rule rides the same action-vocabulary
// block as review_scope, so the prompt stays one reviewer contract.
func TestReviewStep_PromptSetsOriginForPreExistingCode(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)

	findingsJSON, _ := json.Marshal(cleanReviewFindings())
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			return &agent.Result{Output: findingsJSON}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	prompt := ag.calls[0].Prompt
	for _, want := range []string{
		"set origin to exactly one of",
		`"introduced"`,
		`"pre-existing"`,
		"code this change did not introduce",
		"Omitting origin means",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("review prompt missing origin rule fragment %q:\n%s", want, prompt)
		}
	}
	// The rule belongs beside review_scope in the reviewer's field vocabulary.
	if !strings.Contains(prompt, `- For each finding, set review_scope to exactly one of:`) {
		t.Fatalf("review prompt lost the review_scope block the origin rule extends:\n%s", prompt)
	}
}

// The fixer is told pre-existing findings are outside the change unless the
// operator selected them - and anything listed below is only there because it
// was selected, so a listed pre-origin finding gets the smallest correction
// while the fixer never expands into other pre-existing code it notices.
func TestReviewStep_FixPromptTreatsPreExistingAsOutsideTheChange(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)

	callCount := 0
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			callCount++
			if callCount == 1 {
				return &agent.Result{Output: json.RawMessage(`{"summary":"address findings"}`)}, nil
			}
			j, _ := json.Marshal(cleanReviewFindings())
			return &agent.Result{Output: j}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Fixing = true
	sctx.PreviousFindings = `{"findings":[{"id":"review-1","severity":"warning","file":"main.go","description":"old nil deref","action":"auto-fix","origin":"pre-existing"}],"summary":"1 issue"}`

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	fixPrompt := ag.calls[0].Prompt
	for _, want := range []string{
		`origin "pre-existing"`,
		"outside the change",
		"unless the operator selected",
	} {
		if !strings.Contains(fixPrompt, want) {
			t.Errorf("review fix prompt missing pre-existing scope rule fragment %q:\n%s", want, fixPrompt)
		}
	}
}

// The fail-safe direction holds at the step boundary too: a reviewer that
// emits an unknown origin value is not a schema failure - unknown folds to
// introduced, the same as a missing one, so a finding is never lost or
// reclassified out of the change's group by a label nobody defined.
func TestUnmarshalRequiredFindings_UnknownOriginCountsAsIntroduced(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"findings":[{"severity":"warning","description":"d","action":"ask-user","origin":"mystery"}],"summary":"one"}`)
	var findings Findings
	if err := unmarshalRequiredFindings(raw, &findings, true); err != nil {
		t.Fatalf("unknown origin must not fail validation: %v", err)
	}
	if len(findings.Items) != 1 || findings.Items[0].OriginOrDefault() != types.FindingOriginIntroduced {
		t.Fatalf("unexpected findings: %#v", findings.Items)
	}
}
