package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// quotaTestAgent is a fallback chain member that records the session it was
// handed, so a test can prove a switch continued with a fresh session.
type quotaTestAgent struct {
	name       string
	run        func(opts RunOpts) (*Result, error)
	calls      int
	sessions   []*SessionRef
	resumable  bool
	providerOf string
}

func (a *quotaTestAgent) Name() string { return a.name }

func (a *quotaTestAgent) Run(_ context.Context, opts RunOpts) (*Result, error) {
	a.calls++
	if opts.Session != nil {
		a.sessions = append(a.sessions, opts.Session)
	}
	return a.run(opts)
}

func (a *quotaTestAgent) Close() error { return nil }

func (a *quotaTestAgent) SupportsSessionResume() bool { return a.resumable }

func (a *quotaTestAgent) SupportsSessionProvider(provider string) bool {
	return a.providerOf == provider
}

func quotaError(who string) error {
	return errors.New(who + " failed: quota exceeded")
}

func TestFallbackQuotaSwitchMovesToTheNextAgentOnAQuotaError(t *testing.T) {
	claude := &quotaTestAgent{name: "claude", run: func(RunOpts) (*Result, error) {
		return nil, quotaError("claude")
	}}
	codex := &quotaTestAgent{name: "codex", run: func(RunOpts) (*Result, error) {
		return &Result{Text: "ok"}, nil
	}}
	var events []LifecycleEvent
	result, err := NewFallback([]Agent{claude, codex}, WithQuotaSwitch()).Run(context.Background(), RunOpts{
		OnLifecycle: func(event LifecycleEvent) { events = append(events, event) },
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result == nil || result.Text != "ok" {
		t.Fatalf("Run() result = %+v, want codex's ok", result)
	}
	if claude.calls != 1 || codex.calls != 1 {
		t.Fatalf("calls = claude %d codex %d, want 1/1: each agent gets exactly one chance", claude.calls, codex.calls)
	}
	switches := 0
	for _, event := range events {
		if event.Phase != LifecyclePhaseQuotaSwitch {
			continue
		}
		switches++
		if event.Agent != "claude" || !strings.Contains(event.Message, "codex") {
			t.Fatalf("quota switch event = %+v, want claude -> codex", event)
		}
		if !strings.Contains(event.Message, "fresh session") {
			t.Fatalf("quota switch event = %+v, want it to name the fresh session", event)
		}
	}
	if switches != 1 {
		t.Fatalf("quota switch events = %d, want 1", switches)
	}
}

func TestFallbackQuotaSwitchWalksTheWholeOrderAndNeverSwitchesBack(t *testing.T) {
	claude := &quotaTestAgent{name: "claude", run: func(RunOpts) (*Result, error) {
		return nil, quotaError("claude")
	}}
	codex := &quotaTestAgent{name: "codex", run: func(RunOpts) (*Result, error) {
		return nil, errors.New(`codex failed: insufficient_quota`)
	}}
	grok := &quotaTestAgent{name: "grok", run: func(RunOpts) (*Result, error) {
		return &Result{Text: "ok"}, nil
	}}
	result, err := NewFallback([]Agent{claude, codex, grok}, WithQuotaSwitch()).Run(context.Background(), RunOpts{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result == nil || result.Text != "ok" {
		t.Fatalf("Run() result = %+v, want grok's ok", result)
	}
	if claude.calls != 1 || codex.calls != 1 || grok.calls != 1 {
		t.Fatalf("calls = claude %d codex %d grok %d, want 1/1/1", claude.calls, codex.calls, grok.calls)
	}
}

func TestFallbackQuotaSwitchContinuesWithAFreshSession(t *testing.T) {
	// A claude session exists (this invocation resumed one) and claude hits a
	// quota error: codex must run with NO session, never claude's.
	claude := &quotaTestAgent{name: "claude", resumable: true, providerOf: "claude", run: func(RunOpts) (*Result, error) {
		return nil, quotaError("claude")
	}}
	codex := &quotaTestAgent{name: "codex", run: func(RunOpts) (*Result, error) {
		return &Result{Text: "ok"}, nil
	}}
	result, err := NewFallback([]Agent{claude, codex}, WithQuotaSwitch()).Run(context.Background(), RunOpts{
		Session: &SessionRef{Agent: "claude", ID: "sess-1"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result == nil || result.Text != "ok" {
		t.Fatalf("Run() result = %+v, want codex's ok", result)
	}
	if claude.calls != 1 || codex.calls != 1 {
		t.Fatalf("calls = claude %d codex %d, want 1/1", claude.calls, codex.calls)
	}
	if len(codex.sessions) != 0 {
		t.Fatalf("codex received %+v, want no session at all: the switch continues fresh", codex.sessions)
	}
}

func TestFallbackQuotaSwitchDoesNotFireOnOtherErrors(t *testing.T) {
	parseErr := errors.New(`codex output parse: invalid JSON (output snippet: "not json")`)
	claude := &quotaTestAgent{name: "claude", run: func(RunOpts) (*Result, error) {
		return nil, quotaError("claude")
	}}
	codex := &quotaTestAgent{name: "codex", run: func(RunOpts) (*Result, error) {
		return nil, parseErr
	}}
	_, err := NewFallback([]Agent{claude, codex}, WithQuotaSwitch()).Run(context.Background(), RunOpts{})
	if !errors.Is(err, parseErr) {
		t.Fatalf("Run() error = %v, want %v: a non-quota terminal error never switches", err, parseErr)
	}
	if codex.calls != 1 {
		t.Fatalf("codex calls = %d, want 1", codex.calls)
	}
}

func TestFallbackQuotaSwitchKeepsTheUnavailableFallbackAsItWas(t *testing.T) {
	claude := &quotaTestAgent{name: "claude", run: func(RunOpts) (*Result, error) {
		return nil, errors.New(`claude start: exec: "claude": executable file not found`)
	}}
	codex := &quotaTestAgent{name: "codex", run: func(RunOpts) (*Result, error) {
		return &Result{Text: "ok"}, nil
	}}
	result, err := NewFallback([]Agent{claude, codex}, WithQuotaSwitch()).Run(context.Background(), RunOpts{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result == nil || result.Text != "ok" {
		t.Fatalf("Run() result = %+v, want the unchanged unavailable fallback", result)
	}
}

func TestFallbackWithoutQuotaSwitchNeverMovesOnAQuotaError(t *testing.T) {
	claude := &quotaTestAgent{name: "claude", run: func(RunOpts) (*Result, error) {
		return nil, quotaError("claude")
	}}
	codex := &quotaTestAgent{name: "codex", run: func(RunOpts) (*Result, error) {
		return &Result{Text: "should not run"}, nil
	}}
	_, err := NewFallback([]Agent{claude, codex}).Run(context.Background(), RunOpts{})
	if err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("Run() error = %v, want the quota error", err)
	}
	if codex.calls != 0 {
		t.Fatalf("codex calls = %d, want 0: the switch exists only when quota routing is active", codex.calls)
	}
}

func TestFallbackQuotaSwitchRecognizesEveryExistingQuotaShape(t *testing.T) {
	for name, quotaErr := range map[string]error{
		"free usage limit":         errors.New("claude failed: You have hit your free usage limit"),
		"insufficient quota":       errors.New("codex failed: insufficient quota"),
		"exceeded current quota":   errors.New("codex failed: You exceeded your current quota, please check your plan and billing details"),
		"quota exhausted":          errors.New("grok failed: quota exhausted"),
		"claude usage limit event": &claudeAPIError{Status: 429, UsageLimitReached: true, Message: "rate limited"},
	} {
		t.Run(name, func(t *testing.T) {
			if !IsQuotaExhaustionError(quotaErr) {
				t.Fatalf("IsQuotaExhaustionError(%v) = false, want true", quotaErr)
			}
		})
	}
}

func TestIsQuotaExhaustionError_RejectsNonQuotaErrors(t *testing.T) {
	for name, err := range map[string]error{
		"nil":              nil,
		"context":          context.Canceled,
		"transient":        errors.New("claude failed: overloaded_error"),
		"unavailable":      errors.New(`claude start: executable file not found`),
		"structured parse": errors.New(`codex output parse: invalid JSON`),
	} {
		t.Run(name, func(t *testing.T) {
			if IsQuotaExhaustionError(err) {
				t.Fatalf("IsQuotaExhaustionError(%v) = true, want false", err)
			}
		})
	}
}

// A plain nil QuotaSwitch option keeps NewFallback's signature friendly to
// existing callers and mirrors the pipeline's agent name type at the seam.
var _ = types.AgentName("")
