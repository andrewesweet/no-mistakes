package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
)

func TestQuotaSwitchPreservesDurableSessionRequests(t *testing.T) {
	for _, mode := range []string{"quota", "provider-mismatch", "restored"} {
		t.Run(mode, func(t *testing.T) {
			database, run := sessionTestDB(t)
			claude := newFakeSessionAgent()
			claude.name = "claude"
			pi := newFakeSessionAgent()
			pi.name = "pi"
			pi.nextID = 10
			oldProvider := "claude"
			candidates := []agent.Agent{claude, pi}
			start := ""
			switch mode {
			case "quota":
				claude.failNext = errors.New("claude exited: quota exceeded")
			case "provider-mismatch":
				candidates = []agent.Agent{pi, claude}
			case "restored":
				start = "pi"
				oldProvider = "pi"
			}
			chain := agent.NewFallback(candidates, agent.WithQuotaSwitch(start))
			if err := database.UpsertRunAgentSession(run.ID, string(SessionRoleFixer), oldProvider, "old-session"); err != nil {
				t.Fatal(err)
			}
			sessions := NewRunSessions(database, run.ID, chain, true)
			for i := 0; i < 3; i++ {
				if i == 2 {
					sessions = NewRunSessions(database, run.ID, chain, true)
				}
				result, err := sessions.Run(context.Background(), chain, SessionRoleFixer, agent.RunOpts{Prompt: "fix"}, nil)
				if err != nil || result == nil || result.SessionID != "sess-11" || result.Provider != "pi" || result.Resumed != (i > 0) {
					t.Fatalf("turn=%d result=%+v error=%v", i, result, err)
				}
			}
			if len(pi.calls) != 3 || pi.calls[0].session == nil || pi.calls[0].session.ID != "" || pi.calls[0].session.Agent != "" || pi.calls[1].session.ID != "sess-11" {
				t.Fatalf("Pi must start durably, then resume: %+v", pi.calls)
			}
			wantClaudeCalls := 0
			if mode == "quota" {
				wantClaudeCalls = 1
			}
			if len(claude.calls) != wantClaudeCalls {
				t.Fatalf("claude calls=%d want=%d", len(claude.calls), wantClaudeCalls)
			}
			stored, err := database.GetRunAgentSessions(run.ID)
			if err != nil || len(stored) != 1 || stored[0].Agent != "pi" || stored[0].SessionID != "sess-11" {
				t.Fatalf("durable provider was not replaced: %+v error=%v", stored, err)
			}
		})
	}
}
