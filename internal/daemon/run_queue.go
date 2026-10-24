package daemon

import (
	"context"
	"log/slog"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// queuedRun is a fully set-up run waiting for an execution slot under the
// max_concurrent_runs cap. Setup (run row, worktree, trusted-config load,
// agent, pinned gates, executor) already happened at submit time, so a queued
// run holds only disk and in-memory state - no agent subprocess starts until
// its executor is dequeued. The DB row stays pending, which is the visible
// queued state: pending never holds a slot.
type queuedRun struct {
	runID    string
	repoID   string
	branch   string
	gateDir  string
	wtDir    string
	executor *pipeline.Executor
	cancel   context.CancelCauseFunc
	done     chan struct{}
	agent    agent.Agent
	spawn    func()
}

// slotFreeLocked reports whether a new execution may start under the given
// cap. A non-positive cap means unlimited. Callers must hold m.mu: the
// executors count and the queue change only under it, so check-and-register
// stays atomic.
func (m *RunManager) slotFreeLocked(maxConc int) bool {
	return maxConc <= 0 || len(m.executors) < maxConc
}

// executionCap reads the current daemon-wide execution cap from global
// config. A missing or unreadable config falls back to unlimited, matching
// the startup-recovery fallback to defaults: the trust decision for each
// queued run was already made at submit time, so a broken config must not
// stall admitted work, and new submissions fail closed on their own load.
func (m *RunManager) executionCap() int {
	global, err := config.LoadGlobal(m.paths.ConfigFile())
	if err != nil {
		slog.Warn("failed to load global config for execution cap; treating as unlimited", "error", err)
		return 0
	}
	return global.MaxConcurrentRuns
}

// pumpQueue starts queued runs in submission order while the execution cap
// leaves a free slot. Every executor-goroutine exit calls it after releasing
// its slot, so a freed slot is never left idle while work waits. Registration
// happens under the same lock as the pop, so a run the pump took is
// immediately visible to HandleCancel and Shutdown.
func (m *RunManager) pumpQueue() {
	for {
		maxConc := m.executionCap()
		m.mu.Lock()
		if m.shuttingDown.Load() || len(m.runQueue) == 0 || !m.slotFreeLocked(maxConc) {
			m.mu.Unlock()
			return
		}
		next := m.runQueue[0]
		copy(m.runQueue, m.runQueue[1:])
		m.runQueue[len(m.runQueue)-1] = queuedRun{}
		m.runQueue = m.runQueue[:len(m.runQueue)-1]
		m.executors[next.runID] = next.executor
		m.cancels[next.runID] = next.cancel
		m.dones[next.runID] = next.done
		m.mu.Unlock()
		slog.Info("starting queued run", "run_id", next.runID, "branch", next.branch)
		next.spawn()
	}
}

// extractQueuedLocked removes and returns the queued run with the given id,
// preserving the order of the rest. Callers must hold m.mu.
func (m *RunManager) extractQueuedLocked(runID string) (queuedRun, bool) {
	for i, qe := range m.runQueue {
		if qe.runID == runID {
			copy(m.runQueue[i:], m.runQueue[i+1:])
			m.runQueue[len(m.runQueue)-1] = queuedRun{}
			m.runQueue = m.runQueue[:len(m.runQueue)-1]
			return qe, true
		}
	}
	return queuedRun{}, false
}

// extractQueuedForBranchLocked removes and returns every queued run for the
// given repo and branch, in queue order. Callers must hold m.mu.
func (m *RunManager) extractQueuedForBranchLocked(repoID, branch string) []queuedRun {
	var out []queuedRun
	kept := m.runQueue[:0]
	for _, qe := range m.runQueue {
		if qe.repoID == repoID && qe.branch == branch {
			out = append(out, qe)
		} else {
			kept = append(kept, qe)
		}
	}
	for i := len(kept); i < len(m.runQueue); i++ {
		m.runQueue[i] = queuedRun{}
	}
	m.runQueue = kept
	return out
}

// cancelQueuedRun records a queued run terminal without ever starting it and
// releases everything its submit-time setup holds. It frees no execution
// slot - a queued run never held one - so no pump follows.
func (m *RunManager) cancelQueuedRun(qe queuedRun, cause string) {
	if err := m.db.UpdateRunErrorStatus(qe.runID, cause, types.RunCancelled); err != nil {
		slog.Error("failed to mark queued run cancelled", "run_id", qe.runID, "error", err)
	}
	m.removeRunWorktree(qe.repoID, qe.runID, qe.gateDir, qe.wtDir, "queued_run_cancelled")
	qe.agent.Close()
	m.closeSubscribers(qe.runID)
	slog.Info("cancelled queued run", "run_id", qe.runID, "branch", qe.branch)
}
