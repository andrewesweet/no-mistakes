package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestExecutor_HandoffAndPlainAbort(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cause  error
		status types.RunStatus
		step   types.StepStatus
	}{
		{"handoff", ErrHandoff, types.RunHandedOff, types.StepStatusCompleted},
		{"abort", errors.New(types.RunCancelReasonAbortedByUser), types.RunCancelled, types.StepStatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, p, run, repo := setupTest(t)
			const prURL = "https://example.test/o/r/pull/1"
			if err := database.UpdateRunPRURL(run.ID, prURL); err != nil {
				t.Fatal(err)
			}
			if err := database.UpdateRunPRState(run.ID, "open"); err != nil {
				t.Fatal(err)
			}
			workDir := t.TempDir()
			started := make(chan struct{})
			step := &adaptiveCallStep{name: types.StepCI, fn: func(sctx *StepContext) (*StepOutcome, error) {
				close(started)
				<-sctx.Ctx.Done()
				return nil, sctx.Ctx.Err()
			}}
			exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			done := make(chan error, 1)
			go func() { done <- exec.Execute(ctx, run, repo, workDir) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("CI monitor did not start")
			}
			cancel(tc.cause)
			select {
			case err := <-done:
				if tc.status == types.RunHandedOff && err != nil {
					t.Fatal(err)
				}
				if tc.status == types.RunCancelled && err == nil {
					t.Fatal("plain abort must retain cancellation error")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("CI monitor did not stop")
			}
			got, err := database.GetRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.PRURL == nil || *got.PRURL != prURL || got.PRState == nil || *got.PRState != "open" {
				t.Fatalf("PR changed during stop: %+v", got)
			}
			if got.Status != tc.status || !got.Status.Terminal() {
				t.Fatalf("status = %s, want %s terminal", got.Status, tc.status)
			}
			if tc.status == types.RunHandedOff && got.Error != nil {
				t.Fatalf("handoff recorded as error: %s", *got.Error)
			}
			if tc.status == types.RunCancelled && (got.Error == nil || *got.Error != types.RunCancelReasonAbortedByUser) {
				t.Fatalf("plain abort reason = %v", got.Error)
			}
			steps, err := database.GetStepsByRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(steps) != 1 || steps[0].Status != tc.step {
				t.Fatalf("steps = %+v; want %s", steps, tc.step)
			}
		})
	}
}

func TestExecutor_HandoffPersistsFinalRepairRound(t *testing.T) {
	for _, cancelContext := range []bool{false, true} {
		name := "readiness signal"
		if cancelContext {
			name = "context cancellation"
		}
		t.Run(name, func(t *testing.T) {
			database, p, run, repo := setupTest(t)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			calls := 0
			step := &adaptiveCallStep{name: types.StepCI, fn: func(sctx *StepContext) (*StepOutcome, error) {
				calls++
				if !sctx.Fixing {
					return &StepOutcome{
						AutoFixable: true,
						Findings:    `{"findings":[{"id":"ci-1","severity":"error","action":"auto-fix","category":"ci-check","check":"build","description":"build failed"}]}`,
					}, nil
				}
				if err := sctx.SetCIReadiness(true, false); err != nil {
					t.Fatal(err)
				}
				outcome := &StepOutcome{FixSummary: "repair the build", RepairPublished: true}
				if cancelContext {
					cancel(ErrHandoff)
					return outcome, sctx.Ctx.Err()
				}
				return outcome, ErrHandoff
			}}
			cfg := &config.Config{AutoFix: config.AutoFix{CI: 1}}
			exec := NewExecutor(database, p, cfg, nil, []Step{step}, nil)
			if err := exec.Execute(ctx, run, repo, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			got, err := database.GetRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || got.Status != types.RunHandedOff || got.CIReadyAt == nil {
				t.Fatalf("calls=%d run=%+v, want a repaired green handoff", calls, got)
			}
			steps, err := database.GetStepsByRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(steps) != 1 || steps[0].Status != types.StepStatusCompleted || steps[0].FindingsJSON != nil {
				t.Fatalf("steps=%+v, want completed CI without stale findings", steps)
			}
			rounds, err := database.GetRoundsByStep(steps[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rounds) != 2 || !rounds[1].IsFixRound() || !rounds[1].RepairPublished || rounds[1].FixSummary == nil || *rounds[1].FixSummary != "repair the build" {
				t.Fatalf("rounds=%+v, want the published repair recorded before handoff", rounds)
			}
			if rounds[0].SelectedFindingIDs == nil || *rounds[0].SelectedFindingIDs != `["ci-1"]` || rounds[1].FindingsJSON != nil {
				t.Fatalf("rounds=%+v, want the selected failure followed by a clean repair round", rounds)
			}
		})
	}
}
