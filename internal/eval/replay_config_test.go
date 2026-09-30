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
// corpus. ScopingBaseBranch stays empty because it is never serialized, which
// is what makes the replayed steps scope against replayDefaultBranch.
func TestReplayConfigKeepsTheCapturedRepositoryConfig(t *testing.T) {
	dir := writeReplayCaseConfig(t, "pr:\n  base_branch: develop\nignore_patterns:\n  - \"vendor/**\"\n")

	cfg, err := replayConfig(Case{Manifest: Manifest{ID: "case-1", DefaultBranch: "main"}, Dir: dir})
	if err != nil {
		t.Fatalf("replayConfig: %v", err)
	}
	if cfg.PR.BaseBranch != "develop" {
		t.Fatalf("PR.BaseBranch = %q, want the captured PR target replayed as recorded", cfg.PR.BaseBranch)
	}
	if cfg.PR.ScopingBaseBranch != "" {
		t.Fatalf("PR.ScopingBaseBranch = %q, want empty so replay scopes against the pinned default branch", cfg.PR.ScopingBaseBranch)
	}
	if len(cfg.IgnorePatterns) != 1 || cfg.IgnorePatterns[0] != "vendor/**" {
		t.Fatalf("IgnorePatterns = %v, want the captured repo config preserved", cfg.IgnorePatterns)
	}
}
