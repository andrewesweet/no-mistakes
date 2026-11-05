//go:build unix

package cli

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParentCommandLineExcludesArguments(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "read release", "Authorization: Bearer diagnostic-secret")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	command := parentCommandLine(cmd.Process.Pid)
	if !strings.Contains(command, "sh") {
		t.Fatalf("command=%q, want executable name", command)
	}
	if strings.Contains(command, "Authorization") || strings.Contains(command, "diagnostic-secret") || strings.Contains(command, "read release") {
		t.Fatalf("process arguments leaked: %q", command)
	}
}
