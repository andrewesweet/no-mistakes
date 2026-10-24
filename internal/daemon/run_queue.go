package daemon

import (
	"context"
	"log/slog"
	"slices"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type queuedRun struct {
	sequence  uint64
	recovered bool
	runID     string
	repoID    string
	branch    string
	gateDir   string
	wtDir     string
	executor  *pipeline.Executor
	cancel    context.CancelCauseFunc
	done      chan struct{}
	agent     agent.Agent
	spawn     func()
}

// slotFreeLocked reports whether a new execution may start under the given
// cap. A non-positive cap means unlimited. Callers must hold m.mu: the
// executors count and the queue change only under it, so check-and-register
// stays atomic.
func (m *RunManager) slotFreeLocked(maxConc int) bool {
	return maxConc <= 0 || len(m.executors) < maxConc
}

func (m *RunManager) executionCapLocked() int {
	global, err := config.LoadGlobal(m.paths.ConfigFile())
	if err != nil {
		slog.Warn("failed to load global config for execution cap; retaining previous limit", "error", err)
	} else {
		m.maxConcurrentRuns = global.MaxConcurrentRuns
	}
	return m.maxConcurrentRuns
}

func (m *RunManager) reserveAdmission() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executionCapLocked()
	m.nextAdmission++
	sequence := m.nextAdmission
	m.runQueue = append(m.runQueue, queuedRun{sequence: sequence})
	return sequence
}

func (m *RunManager) releaseAdmission(sequence uint64) {
	m.mu.Lock()
	for i, qe := range m.runQueue {
		if qe.sequence == sequence && qe.spawn == nil {
			m.runQueue = slices.Delete(m.runQueue, i, i+1)
			m.mu.Unlock()
			m.pumpQueue()
			return
		}
	}
	m.mu.Unlock()
}

func (m *RunManager) admitRun(sequence uint64, qe queuedRun) {
	m.mu.Lock()
	if m.shuttingDown.Load() {
		m.mu.Unlock()
		m.shutdownQueuedRun(qe)
		return
	}
	for i := range m.runQueue {
		if m.runQueue[i].sequence == sequence {
			qe.sequence = sequence
			m.runQueue[i] = qe
			break
		}
	}
	m.mu.Unlock()
	m.pumpQueue()
}

func (m *RunManager) pumpQueue() {
	m.mu.Lock()
	defer m.mu.Unlock()
	maxConc := m.executionCapLocked()
	for !m.shuttingDown.Load() && len(m.runQueue) > 0 && m.slotFreeLocked(maxConc) {
		i := 0
		if maxConc <= 0 {
			for i < len(m.runQueue) && m.runQueue[i].spawn == nil {
				i++
			}
		}
		if i == len(m.runQueue) || m.runQueue[i].spawn == nil {
			return
		}
		next := m.runQueue[i]
		m.runQueue = slices.Delete(m.runQueue, i, i+1)
		m.executors[next.runID] = next.executor
		m.cancels[next.runID] = next.cancel
		m.dones[next.runID] = next.done
		slog.Info("starting admitted run", "run_id", next.runID, "branch", next.branch)
		next.spawn()
	}
}

func (m *RunManager) shutdownQueuedRun(qe queuedRun) {
	if qe.spawn == nil {
		return
	}
	if qe.recovered {
		qe.cancel(pipeline.ErrDaemonShutdown)
		_ = qe.agent.Close()
		return
	}
	m.cancelQueuedRun(qe, "daemon shutting down")
}

// extractQueuedLocked removes and returns the queued run with the given id,
// preserving the order of the rest. Callers must hold m.mu.
func (m *RunManager) extractQueuedLocked(runID string) (queuedRun, bool) {
	for i, qe := range m.runQueue {
		if qe.spawn != nil && qe.runID == runID {
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
		if qe.spawn != nil && qe.repoID == repoID && qe.branch == branch {
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
	qe.cancel(nil)
	qe.agent.Close()
	m.closeSubscribers(qe.runID)
	slog.Info("cancelled queued run", "run_id", qe.runID, "branch", qe.branch)
}
