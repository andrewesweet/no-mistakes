package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type drainReconciliationStep struct {
	Step
	reconcile func() (bool, error)
}

func (s *drainReconciliationStep) ReconcileApprovalGate(*StepContext) (bool, error) {
	return s.reconcile()
}

func TestExecutor_DrainPreservesAcceptedGateAborts(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		entry := "live"
		if recovered {
			entry = "recovered"
		}
		for _, handoff := range []string{"queued", "received_after_reconciliation", "reconciliation_error"} {
			for _, response := range []struct {
				name   string
				action types.ApprovalAction
				reason string
				expire bool
			}{
				{name: "no_response"},
				{name: "abort", action: types.ActionAbort, reason: "unsafe to land"},
				{name: "abort_without_reason", action: types.ActionAbort},
				{name: "abort_at_deadline", action: types.ActionAbort, reason: "unsafe to land", expire: true},
				{name: "approve", action: types.ActionApprove},
				{name: "skip", action: types.ActionSkip},
				{name: "fix", action: types.ActionFix},
				{name: "answer", action: types.ActionAnswer},
			} {
				if handoff == "reconciliation_error" && response.action != types.ActionAbort {
					continue
				}
				t.Run(entry+"/"+handoff+"/"+response.name, func(t *testing.T) {
					database, p, run, repo := setupTest(t)
					workDir := t.TempDir()
					calls, laterCalls := 0, 0
					step := &adaptiveCallStep{name: types.StepReview, fn: func(*StepContext) (*StepOutcome, error) {
						calls++
						return &StepOutcome{NeedsApproval: true, Findings: `{"findings":[{"id":"decision","severity":"warning","description":"needs operator","action":"ask-user"}]}`}, nil
					}}
					later := &adaptiveCallStep{name: types.StepTest, fn: func(*StepContext) (*StepOutcome, error) {
						laterCalls++
						return &StepOutcome{}, nil
					}}
					if recovered {
						var seed *Executor
						seed = NewExecutor(database, p, nil, nil, []Step{step, later}, func(event ipc.Event) {
							if event.Status != nil && *event.Status == string(types.StepStatusAwaitingApproval) {
								seed.BeginDrain()
							}
						})
						if err := seed.Execute(context.Background(), run, repo, workDir); !errors.Is(err, ErrParkedAtShutdown) {
							t.Fatalf("seed parked gate = %v", err)
						}
						var err error
						run, err = database.GetRun(run.ID)
						if err != nil {
							t.Fatal(err)
						}
					}
					ctx, cancel := context.WithCancelCause(context.Background())
					defer cancel(nil)
					var executor *Executor
					respondAndDrain := func() {
						if response.action != "" {
							var ids []string
							if response.action == types.ActionFix {
								ids = []string{"decision"}
							}
							if _, err := executor.RespondWithOverrides(types.StepReview, response.action, ids, nil, nil, nil, response.reason); err != nil {
								t.Fatalf("respond = %v", err)
							}
						}
						executor.BeginDrain()
						if response.expire {
							cancel(ErrDaemonShutdown)
						}
					}
					var gate Step = step
					announced := false
					if handoff != "queued" && response.action != "" {
						gate = &drainReconciliationStep{Step: step, reconcile: func() (bool, error) {
							if !announced {
								return false, nil
							}
							respondAndDrain()
							if handoff == "reconciliation_error" {
								return false, ErrFatalGateReconciliation
							}
							return true, nil
						}}
					}
					executor = NewExecutor(database, p, nil, nil, []Step{gate, later}, func(event ipc.Event) {
						if event.Status != nil && *event.Status == string(types.StepStatusAwaitingApproval) {
							announced = true
							if handoff == "queued" || response.action == "" {
								respondAndDrain()
							}
						}
					})
					executor.SetGateReconcileTimings(time.Millisecond, time.Second)
					var err error
					if recovered {
						err = executor.Resume(ctx, run, repo, workDir)
					} else {
						err = executor.Execute(ctx, run, repo, workDir)
					}
					got, dbErr := database.GetRun(run.ID)
					if dbErr != nil {
						t.Fatal(dbErr)
					}
					steps, dbErr := database.GetStepsByRun(run.ID)
					if dbErr != nil || len(steps) != 2 {
						t.Fatalf("steps = %+v, error = %v", steps, dbErr)
					}
					if response.action == types.ActionAbort {
						wantStep := "aborted by user"
						if response.reason != "" {
							wantStep += ": " + response.reason
						}
						wantRun := "step review: " + wantStep
						if err == nil || err.Error() != wantRun || got.Status != types.RunFailed || got.Error == nil || *got.Error != wantRun || got.AwaitingAgentSince != nil {
							t.Fatalf("accepted abort lost: error = %v, run = %+v", err, got)
						}
						if steps[0].Status != types.StepStatusFailed || steps[0].Error == nil || *steps[0].Error != wantStep {
							t.Fatalf("step did not record abort: %+v", steps[0])
						}
					} else if !errors.Is(err, ErrParkedAtShutdown) || got.Status != types.RunRunning || got.AwaitingAgentSince == nil || steps[0].Status != types.StepStatusAwaitingApproval {
						t.Fatalf("shutdown released gate: error = %v, run = %+v, step = %+v", err, got, steps[0])
					}
					if calls != 1 || laterCalls != 0 || steps[1].Status != types.StepStatusPending {
						t.Fatalf("shutdown started more work: calls = %d, later calls = %d, later step = %+v", calls, laterCalls, steps[1])
					}
				})
			}
		}
	}
}

func TestExecutor_DrainDoesNotOverrideAnEarlierOperatorAbort(t *testing.T) {
	database, p, run, repo := setupTest(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	var executor *Executor
	executor = NewExecutor(database, p, nil, nil, []Step{newApprovalStep(types.StepDocument, `{"findings":[{"id":"decision","severity":"warning","description":"needs operator","action":"ask-user"}]}`)}, func(event ipc.Event) {
		if event.Status != nil && *event.Status == string(types.StepStatusAwaitingApproval) {
			cancel(errors.New(types.RunCancelReasonAbortedByUser))
			executor.BeginDrain()
		}
	})
	err := executor.Execute(ctx, run, repo, t.TempDir())
	if err == nil {
		t.Fatalf("execute = %v, want the original cancellation", err)
	}
	recorded, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Status != types.RunCancelled || recorded.Error == nil || *recorded.Error != types.RunCancelReasonAbortedByUser || recorded.AwaitingAgentSince != nil {
		t.Fatalf("earlier cancellation was replaced by a resumable shutdown park: %+v", recorded)
	}
}
