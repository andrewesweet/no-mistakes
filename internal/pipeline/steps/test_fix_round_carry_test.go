package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const cleanTestEvidence = `{"summary":"fixed","findings":%s,"tested":["x"],"testing_summary":"ok","artifacts":[],"scenarios":[{"name":"user runs the command","result":"pass","live":true,"evidence":"x","reason":""}],"verdict":"go"}`

// fixRoundTestContext is a Test step on an automatic fix round whose
// evidence turn comes back clean: verdict go, reporting only reported.
func fixRoundTestContext(t *testing.T, deferred, reported string) *pipeline.StepContext {
	t.Helper()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("fixed"), 0o644); err != nil {
			return nil, err
		}
		return &agent.Result{Output: json.RawMessage(fmt.Sprintf(cleanTestEvidence, reported))}, nil
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Fixing = true
	sctx.PreviousFindings = `{"findings":[{"id":"test-2","severity":"error","action":"auto-fix","category":"test-verdict","description":"live validation verdict: no-go"}],"summary":"no-go"}`
	sctx.DeferredFindings = deferred
	return sctx
}

func descriptionCount(t *testing.T, raw, description string) int {
	t.Helper()
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		t.Fatalf("parse findings %q: %v", raw, err)
	}
	count := 0
	for _, item := range findings.Items {
		if item.Description == description {
			count++
		}
	}
	return count
}

// A finding the auto-fix round did not select records what an earlier turn
// observed (here a write outside the workspace), so a clean fix round cannot
// clear it: it must survive the round and keep the step parked.
func TestTestStep_FixRoundKeepsUnselectedFindings(t *testing.T) {
	t.Parallel()
	const breach = "apply wrote outside the worktree"
	sctx := fixRoundTestContext(t, `{"findings":[{"id":"test-isolation-breach","severity":"error","action":"no-op","description":"`+breach+`"}],"summary":"no-go"}`, `[]`)

	outcome, err := (&TestStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.NeedsApproval {
		t.Fatalf("NeedsApproval = false, want the unselected finding to park; findings = %s", outcome.Findings)
	}
	if got := descriptionCount(t, outcome.Findings, breach); got != 1 {
		t.Fatalf("unselected finding appears %d times, want 1; findings = %s", got, outcome.Findings)
	}
}

func TestTestStep_FixRoundDoesNotDuplicateAReReportedFinding(t *testing.T) {
	t.Parallel()
	const breach = "apply wrote outside the worktree"
	sctx := fixRoundTestContext(t,
		`{"findings":[{"id":"test-isolation-breach","severity":"error","action":"no-op","description":"`+breach+`"}],"summary":"no-go"}`,
		`[{"id":"test-isolation-breach","severity":"error","action":"no-op","description":"`+breach+`"}]`)

	outcome, err := (&TestStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := descriptionCount(t, outcome.Findings, breach); got != 1 {
		t.Fatalf("re-reported finding appears %d times, want 1; findings = %s", got, outcome.Findings)
	}
}

// Every evidence turn derives its own verdict finding, so an earlier turn's
// inconclusive verdict is superseded by this turn's go rather than carried.
func TestTestStep_FixRoundDoesNotCarryAnEarlierVerdict(t *testing.T) {
	t.Parallel()
	sctx := fixRoundTestContext(t, `{"findings":[{"id":"test-3","severity":"warning","action":"ask-user","category":"test-verdict","description":"live validation verdict: inconclusive"}],"summary":"inconclusive"}`, `[]`)

	outcome, err := (&TestStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NeedsApproval {
		t.Fatalf("NeedsApproval = true, want the earlier verdict superseded; findings = %s", outcome.Findings)
	}
}

// End to end through the executor: a no-go verdict starts an automatic fix
// round, which defers the agent's ask-user finding. The fix round's clean
// result must still park on that finding instead of completing the run.
func TestTestStep_DeferredAskUserFindingParksAfterACleanFixRound(t *testing.T) {
	t.Parallel()
	const question = "this failing check looks intentional; confirm it should exist"
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	var mu sync.Mutex
	calls := 0
	ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			return &agent.Result{Output: json.RawMessage(`{"summary":"broken","findings":[{"id":"intent-check","severity":"warning","action":"ask-user","description":"` + question + `"}],"tested":["x"],"testing_summary":"failed","artifacts":[],"scenarios":[{"name":"user runs the command","result":"fail","live":true,"evidence":"x","reason":""}],"verdict":"no-go"}`)}, nil
		}
		if err := os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("fixed"), 0o644); err != nil {
			return nil, err
		}
		return &agent.Result{Output: json.RawMessage(fmt.Sprintf(cleanTestEvidence, "[]"))}, nil
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Config.AutoFix.Test = 3

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parked := make(chan string, 1)
	exec := pipeline.NewExecutor(sctx.DB, paths.WithRoot(t.TempDir()), sctx.Config, ag, []pipeline.Step{&TestStep{}}, func(event ipc.Event) {
		if event.Type == ipc.EventStepCompleted && event.Status != nil && (*event.Status == string(types.StepStatusAwaitingApproval) || *event.Status == string(types.StepStatusFixReview)) && event.Findings != nil {
			select {
			case parked <- *event.Findings:
			default:
			}
			cancel()
		}
	})
	done := make(chan error, 1)
	go func() { done <- exec.Execute(ctx, sctx.Run, sctx.Repo, dir) }()

	select {
	case findings := <-parked:
		if got := descriptionCount(t, findings, question); got != 1 {
			t.Fatalf("parked gate carries the deferred ask-user finding %d times, want 1; findings = %s", got, findings)
		}
	case err := <-done:
		run, getErr := sctx.DB.GetRun(sctx.Run.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		t.Fatalf("run finished as %s (err %v) without parking on the deferred ask-user finding", run.Status, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Test step neither parked nor finished")
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Error("executor did not stop")
	}
}
