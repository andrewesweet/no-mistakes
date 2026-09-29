package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeReplayCaseConfig(t *testing.T, repoYAML string) string {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "global.yaml"), []byte("agent: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "repo-config.yaml"), []byte(repoYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A case captured before capture refused stacked runs records a base derived
// from the DEFAULT branch while its review saw the narrower parent-branch diff,
// so replaying it would score the candidate against gold labels for a different
// change. Such a case fails loudly instead.
func TestReplayConfigRefusesACaseScopedToAnotherBaseBranch(t *testing.T) {
	dir := writeReplayCaseConfig(t, "pr:\n  base_branch: develop\n")

	_, err := replayConfig(Case{Manifest: Manifest{ID: "case-1", DefaultBranch: "main"}, Dir: dir})
	if err == nil {
		t.Fatal("replayConfig error = nil, want refusal for a non-default captured base branch")
	}
	if !strings.Contains(err.Error(), "develop") || !strings.Contains(err.Error(), "main") {
		t.Fatalf("replayConfig error = %q, want both the captured base branch and the default named", err)
	}
}

// Ordinary path: a captured base branch equal to the repository default (or
// absent) replays, and the rest of the captured repo config survives.
func TestReplayConfigKeepsACaseOnTheRepositoryDefaultBranch(t *testing.T) {
	for _, tc := range []struct{ name, repoYAML, defaultBranch string }{
		{name: "no captured base branch", repoYAML: "ignore_patterns:\n  - \"vendor/**\"\n", defaultBranch: "main"},
		{name: "captured base branch is the default", repoYAML: "pr:\n  base_branch: develop\nignore_patterns:\n  - \"vendor/**\"\n", defaultBranch: "develop"},
		{name: "unset manifest default branch means main", repoYAML: "pr:\n  base_branch: main\nignore_patterns:\n  - \"vendor/**\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeReplayCaseConfig(t, tc.repoYAML)

			cfg, err := replayConfig(Case{Manifest: Manifest{ID: "case-1", DefaultBranch: tc.defaultBranch}, Dir: dir})
			if err != nil {
				t.Fatalf("replayConfig: %v", err)
			}
			if len(cfg.IgnorePatterns) != 1 || cfg.IgnorePatterns[0] != "vendor/**" {
				t.Fatalf("IgnorePatterns = %v, want the captured repo config preserved", cfg.IgnorePatterns)
			}
		})
	}
}
