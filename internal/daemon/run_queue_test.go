package daemon

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// startTestDaemonWithMaxConcurrentRuns starts a daemon whose global config
// caps concurrent runs, mirroring startTestDaemonWithSteps.
func startTestDaemonWithMaxConcurrentRuns(t *testing.T, maxConcurrent int, sf StepFactory) (*paths.Paths, *db.DB) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "dtest")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	p := paths.WithRoot(tmpDir)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	mockClaude := writeMockClaude(t, t.TempDir())
	configYAML := "agent: claude\nagent_path_override:\n  claude: " + mockClaude + fmt.Sprintf("\nmax_concurrent_runs: %d\n", maxConcurrent)
	if err := os.WriteFile(p.ConfigFile(), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	runTestDaemon(t, p, d, sf, 15*time.Second)
	return p, d
}

func submitBranchRun(t *testing.T, p *paths.Paths, gateDir, branch, headSHA string) string {
	t.Helper()
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: gateDir,
		Ref:  "refs/heads/" + branch,
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &result); err != nil {
		t.Fatal(err)
	}
	return result.RunID
}

// waitForRunTerminalStatus waits for any terminal status (completed, failed,
// or cancelled): unlike waitForRunTerminalState, it also settles cancelled
// runs, which is how queue tests end their runs.
func waitForRunTerminalStatus(t *testing.T, d *db.DB, runID string) *db.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := d.GetRun(runID)
		if err != nil {
			t.Fatal(err)
		}
		if run != nil && run.Status.Terminal() {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not reach terminal state", runID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func cancelRun(t *testing.T, p *paths.Paths, runID string) {
	t.Helper()
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.CancelRunResult
	if err := client.Call(ipc.MethodCancelRun, &ipc.CancelRunParams{RunID: runID}, &result); err != nil {
		t.Fatalf("cancel run %s: %v", runID, err)
	}
}

func runStatus(t *testing.T, d *db.DB, runID string) types.RunStatus {
	t.Helper()
	run, err := d.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatalf("run %s not found", runID)
	}
	return run.Status
}

// Queued runs start in submission order as slots free.
func TestMaxConcurrentRuns_QueuedRunsStartFIFO(t *testing.T) {
	started := make(chan string, 8)
	p, d := startTestDaemonWithMaxConcurrentRuns(t, 1, func() []pipeline.Step {
		return []pipeline.Step{&notifyBlockStep{name: types.StepReview, started: started}}
	})

	_, headSHA := setupTestGitRepo(t, p, d, "max-conc-fifo-repo")
	gateDir := p.RepoDir("max-conc-fifo-repo")

	firstID := submitBranchRun(t, p, gateDir, "feature/one", headSHA)
	waitForStartedBranch(t, started, "feature/one")

	queuedIDs := []string{
		submitBranchRun(t, p, gateDir, "feature/two", headSHA),
		submitBranchRun(t, p, gateDir, "feature/three", headSHA),
		submitBranchRun(t, p, gateDir, "feature/four", headSHA),
	}
	for _, id := range queuedIDs {
		if got := runStatus(t, d, id); got != types.RunPending {
			t.Fatalf("queued run %s status = %s, want pending", id, got)
		}
	}

	// No queued run may start while the slot is held.
	select {
	case got := <-started:
		t.Fatalf("queued run started while the slot was held: %s", got)
	case <-time.After(500 * time.Millisecond):
	}

	for i, want := range []string{"feature/two", "feature/three", "feature/four"} {
		if i == 0 {
			cancelRun(t, p, firstID)
		} else {
			cancelRun(t, p, queuedIDs[i-1])
		}
		waitForStartedBranch(t, started, want)
	}
	// The loop already cancelled every run but the last one as it started
	// the next; only feature/four is still executing.
	cancelRun(t, p, queuedIDs[2])
	waitForRunTerminalStatus(t, d, firstID)
	for _, id := range queuedIDs {
		waitForRunTerminalStatus(t, d, id)
	}
}

// Cancelling a queued run removes it cleanly: it never starts, and the queue
// drains around it.
func TestMaxConcurrentRuns_CancelQueuedRunNeverStarts(t *testing.T) {
	started := make(chan string, 4)
	p, d := startTestDaemonWithMaxConcurrentRuns(t, 1, func() []pipeline.Step {
		return []pipeline.Step{&notifyBlockStep{name: types.StepReview, started: started}}
	})

	_, headSHA := setupTestGitRepo(t, p, d, "max-conc-cancel-repo")
	gateDir := p.RepoDir("max-conc-cancel-repo")

	firstID := submitBranchRun(t, p, gateDir, "feature/one", headSHA)
	waitForStartedBranch(t, started, "feature/one")

	queuedID := submitBranchRun(t, p, gateDir, "feature/two", headSHA)
	if got := runStatus(t, d, queuedID); got != types.RunPending {
		t.Fatalf("queued run status = %s, want pending", got)
	}

	cancelRun(t, p, queuedID)
	if got := waitForRunTerminalStatus(t, d, queuedID); got.Status != types.RunCancelled {
		t.Fatalf("cancelled queued run status = %s, want cancelled", got.Status)
	}

	// Freeing the slot must not start the cancelled run.
	cancelRun(t, p, firstID)
	waitForRunTerminalStatus(t, d, firstID)
	select {
	case got := <-started:
		t.Fatalf("cancelled queued run started after the slot freed: %s", got)
	case <-time.After(time.Second):
	}
}

// An explicit zero keeps today's unlimited behavior.
func TestMaxConcurrentRuns_ZeroMeansUnlimited(t *testing.T) {
	started := make(chan string, 2)
	p, d := startTestDaemonWithMaxConcurrentRuns(t, 0, func() []pipeline.Step {
		return []pipeline.Step{&notifyBlockStep{name: types.StepReview, started: started}}
	})

	_, headSHA := setupTestGitRepo(t, p, d, "max-conc-zero-repo")
	gateDir := p.RepoDir("max-conc-zero-repo")

	firstID := submitBranchRun(t, p, gateDir, "feature/one", headSHA)
	waitForStartedBranch(t, started, "feature/one")
	secondID := submitBranchRun(t, p, gateDir, "feature/two", headSHA)
	waitForStartedBranch(t, started, "feature/two")
	if got := runStatus(t, d, secondID); got != types.RunRunning {
		t.Fatalf("second run status = %s, want running", got)
	}
	cancelRun(t, p, firstID)
	cancelRun(t, p, secondID)
	waitForRunTerminalStatus(t, d, firstID)
	waitForRunTerminalStatus(t, d, secondID)
}

// A second submitted run waits in pending while the cap is held, then starts
// once the first run finishes.
func TestMaxConcurrentRuns_SecondRunWaitsForSlot(t *testing.T) {
	started := make(chan string, 4)
	p, d := startTestDaemonWithMaxConcurrentRuns(t, 1, func() []pipeline.Step {
		return []pipeline.Step{&notifyBlockStep{name: types.StepReview, started: started}}
	})

	_, headSHA := setupTestGitRepo(t, p, d, "max-conc-wait-repo")
	gateDir := p.RepoDir("max-conc-wait-repo")

	firstID := submitBranchRun(t, p, gateDir, "feature/one", headSHA)
	waitForStartedBranch(t, started, "feature/one")

	secondID := submitBranchRun(t, p, gateDir, "feature/two", headSHA)

	select {
	case got := <-started:
		t.Fatalf("second run started while the slot was held: %s", got)
	case <-time.After(500 * time.Millisecond):
	}
	if got := runStatus(t, d, secondID); got != types.RunPending {
		t.Fatalf("queued run status = %s, want pending", got)
	}

	cancelRun(t, p, firstID)
	waitForStartedBranch(t, started, "feature/two")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := runStatus(t, d, secondID); got == types.RunRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued run status = %s, want running after the slot freed", runStatus(t, d, secondID))
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancelRun(t, p, secondID)
	if got := waitForRunTerminalStatus(t, d, secondID); got.Status != types.RunCancelled {
		t.Fatalf("second run status = %s, want cancelled", got.Status)
	}
}
