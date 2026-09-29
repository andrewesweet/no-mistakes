package eval

import (
	"os"
	"path/filepath"
	"testing"
)

// A replayed case carries the captured repo config verbatim, but the isolated
// eval gate only pins refs/remotes/origin/<DefaultBranch>. A captured
// pr.base_branch must therefore not survive into the replay config: the
// scoping steps would resolve their base against a ref that does not exist in
// the replay worktree and score the candidate against a different diff.
func TestReplayConfigDropsTheCapturedPRBaseBranch(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "global.yaml"), []byte("agent: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoYAML := "pr:\n  base_branch: develop\nignore_patterns:\n  - \"vendor/**\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "repo-config.yaml"), []byte(repoYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := replayConfig(Case{Dir: dir})
	if err != nil {
		t.Fatalf("replayConfig: %v", err)
	}
	if cfg.PR.BaseBranch != "" {
		t.Fatalf("PR.BaseBranch = %q, want empty so replay scopes against the pinned default branch", cfg.PR.BaseBranch)
	}
	if len(cfg.IgnorePatterns) != 1 || cfg.IgnorePatterns[0] != "vendor/**" {
		t.Fatalf("IgnorePatterns = %v, want the captured repo config preserved", cfg.IgnorePatterns)
	}
}
