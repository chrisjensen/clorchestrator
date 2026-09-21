package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chrisjensen/clorchestrate/internal/iterm"
)

// runInCurrentTerminal sets the tab color (if any) then runs the remote
// command in the current terminal, inheriting stdin/stdout/stderr.
func runInCurrentTerminal(colorHex, remoteCmd string) error {
	if colorHex != "" {
		iterm.WriteTabColor(os.Stdout, colorHex)
	}
	cmd := exec.Command("sh", "-c", remoteCmd)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func detectMode(handle, branch, issue string) (Mode, error) {
	switch {
	case handle == "" && branch == "" && issue == "":
		return ModeBareSession, nil
	case handle != "" && branch == "" && issue == "":
		return ModeHandleSession, nil
	case handle != "" && branch != "" && issue == "":
		return ModeWorktree, nil
	case handle != "" && branch != "" && issue != "":
		return ModeFullTask, nil
	default:
		return 0, fmt.Errorf("invalid argument combination: handle=%q branch=%q issue=%q", handle, branch, issue)
	}
}

// resolveConfigPath expands a bare name (no path separators) to
// ~/.clorchestrate/{name}.toml. Full and relative paths are returned as-is.
func resolveConfigPath(name string) (string, error) {
	if strings.ContainsRune(name, '/') || strings.HasPrefix(name, ".") {
		return name, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return filepath.Join(home, ".clorchestrate", name+".toml"), nil
}

// findExistingSession returns the full "PID.name" session ID and its screen
// state (e.g. "Detached", "Attached") for a session matching sessionName.
// When server is "" it queries the local screen; otherwise it queries via SSH.
// Returns "","" if none found or the command fails.
func findExistingSession(server, sessionName string) (id, state string) {
	grepCmd := fmt.Sprintf("screen -ls | grep -F '.%s' | head -1", sessionName)
	var out []byte
	var err error
	if server == "" {
		out, err = exec.Command("sh", "-c", grepCmd).Output()
	} else {
		out, err = exec.Command("ssh", server, grepCmd).Output()
	}
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return "", ""
	}
	sessions := parseScreenLs(string(out))
	if len(sessions) == 0 {
		return "", ""
	}
	s := sessions[0]
	// parseScreenLs strips the PID prefix; rebuild "PID.name" for screen -r/-dr.
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) > 0 {
		id = fields[0]
	} else {
		id = s.name
	}
	return id, s.state
}

// killSession terminates the named screen session, then reaps dead session
// sockets via `screen -wipe`. Runs locally when server is "".
func killSession(server, sessionID string) error {
	shellCmd := fmt.Sprintf("screen -S %s -X quit 2>/dev/null; screen -wipe >/dev/null 2>&1; true", sessionID)
	var cmd *exec.Cmd
	if server == "" {
		cmd = exec.Command("sh", "-c", shellCmd)
	} else {
		cmd = exec.Command("ssh", server, shellCmd)
	}
	return cmd.Run()
}

// screenShellCmd returns the command run inside screen's login shell: either
// a bare login shell, or launchCmd followed by one, so callers that need to
// run something before handing control to the interactive shell (cd into a
// worktree, launch claude) can do so without a separate typed-in followup
// step.
func screenShellCmd(launchCmd string) string {
	if launchCmd == "" {
		return "bash -l"
	}
	return fmt.Sprintf("bash -c '%s && exec bash -l'", launchCmd)
}
