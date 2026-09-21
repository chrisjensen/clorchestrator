package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/conffile"
)

// writeRemoteFile writes content to path on the server (or locally when server
// is ""). desc is used only for error messages.
func writeRemoteFile(server, path, content, desc string) error {
	if server == "" {
		return os.WriteFile(path, []byte(content), 0644)
	}
	cmd := exec.Command("ssh", server, fmt.Sprintf("cat > %s", path))
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write %s on %s: %w", desc, server, err)
	}
	return nil
}

func writeTaskConf(server, handle string, tc conffile.TaskConf) error {
	return writeRemoteFile(server, fmt.Sprintf("/tmp/task-%s.conf", handle), conffile.Render(tc), "task conf")
}

func writePromptFile(server, handle, prompt string) error {
	return writeRemoteFile(server, fmt.Sprintf("/tmp/task-%s.prompt.md", handle), prompt, "prompt file")
}

// writeTaskMD stages the hive task.md body; the checkout script copies it into
// the run dir as task.md (read by the hive-coordinate skill during its
// merge/review steps).
func writeTaskMD(server, handle, body string) error {
	return writeRemoteFile(server, fmt.Sprintf("/tmp/task-%s.md", handle), body, "task.md")
}

// runSetup runs ~/bin/worktree-checkout.sh <handle>. When server is set it
// runs over plain (non-tty) SSH; otherwise it runs locally. Output streams
// live to the local terminal so the user sees progress and any failures
// immediately. Returns non-nil error if the script exits non-zero.
func runSetup(server, handle string) error {
	var cmd *exec.Cmd
	if server == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		cmd = exec.Command(filepath.Join(home, "bin", "worktree-checkout.sh"), handle)
	} else {
		cmd = exec.Command("ssh", server, fmt.Sprintf("~/bin/worktree-checkout.sh %s", handle))
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
