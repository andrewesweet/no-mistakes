package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

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
