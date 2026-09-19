package cli

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
)

func TestFormatOmitIntentPushOption(t *testing.T) {
	if got := formatOmitIntentPushOption(true); got != "no-mistakes.omit-intent" {
		t.Fatalf("formatOmitIntentPushOption(true) = %q", got)
	}
	if got := formatOmitIntentPushOption(false); got != "" {
		t.Fatalf("unrequested omit = %q, want no option at all", got)
	}
}

func TestParseOmitIntentPushOptions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options []string
		want    bool
	}{
		{"absent", []string{"no-mistakes.pr-base-branch=epic"}, false},
		{"present once", []string{"no-mistakes.omit-intent"}, true},
		{"repeated", []string{"no-mistakes.omit-intent", "no-mistakes.omit-intent"}, true},
		{"among others", []string{"no-mistakes.skip=review", "no-mistakes.omit-intent", "no-mistakes.pr-base-branch=x"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOmitIntentPushOptions(tc.options)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("parseOmitIntentPushOptions = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConflictingActiveRunOmitIntent(t *testing.T) {
	t.Parallel()
	omitted := &ipc.RunInfo{ID: "run-omit", OmitIntent: true}
	if err := conflictingActiveRunOmitIntent(omitted, true); err != nil {
		t.Fatalf("flag reattaching to an omitting run should reattach: %v", err)
	}
	publishing := &ipc.RunInfo{ID: "run-publish"}
	if err := conflictingActiveRunOmitIntent(publishing, false); err != nil {
		t.Fatalf("no flag should always reattach: %v", err)
	}
	err := conflictingActiveRunOmitIntent(publishing, true)
	if err == nil {
		t.Fatal("expected conflict when --no-publish-intent would be discarded by reattach")
	}
	if !strings.Contains(err.Error(), "run-publish") {
		t.Fatalf("error = %v, want it to name the active run", err)
	}
}
