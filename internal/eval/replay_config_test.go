package eval

import (
	"os"
	"path/filepath"
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

// The captured repo config replays as recorded, including a pr.base_branch: it
// is the PR target, not the scoping base, so replay neither honors nor refuses
// it. Capture is the guard that keeps a non-default-scoped run out of the
// corpus.
func TestReplayConfigKeepsTheCapturedRepositoryConfig(t *testing.T) {
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
