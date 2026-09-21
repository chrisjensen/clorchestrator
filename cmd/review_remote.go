package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// remoteFileExists returns true if the file exists (may be empty).
func remoteFileExists(server, path string) bool {
	if server == "" {
		_, err := os.Stat(path)
		return err == nil
	}
	return exec.Command("ssh", server, fmt.Sprintf("test -f %s", path)).Run() == nil
}

// remoteDirExists returns true if path exists and is a directory.
func remoteDirExists(server, path string) bool {
	if server == "" {
		info, err := os.Stat(path)
		return err == nil && info.IsDir()
	}
	return exec.Command("ssh", server, fmt.Sprintf("test -d %s", path)).Run() == nil
}

// remoteFileNonEmpty returns true if the file exists and has non-zero size.
func remoteFileNonEmpty(server, path string) bool {
	if server == "" {
		info, err := os.Stat(path)
		return err == nil && info.Size() > 0
	}
	return exec.Command("ssh", server, fmt.Sprintf("test -s %s", path)).Run() == nil
}

// screenSessionRunning returns true if a screen session with the given name exists on the server.
func screenSessionRunning(server, name string) bool {
	if server == "" {
		return false
	}
	return exec.Command("ssh", server, fmt.Sprintf("screen -ls | grep -qF '.%s'", name)).Run() == nil
}

// readRemoteFile reads a file from the server (or locally when server is "").
func readRemoteFile(server, path string) (string, error) {
	var out []byte
	var err error
	if server == "" {
		out, err = os.ReadFile(path)
	} else {
		out, err = exec.Command("ssh", server, fmt.Sprintf("cat %s", path)).Output()
	}
	if err != nil {
		return "", fmt.Errorf("read result file %s: %w", path, err)
	}
	return string(out), nil
}

// remoteHomeDir returns the home directory on the given server (empty = local).
func remoteHomeDir(server string) (string, error) {
	var out []byte
	var err error
	if server == "" {
		home, e := os.UserHomeDir()
		return home, e
	}
	out, err = exec.Command("ssh", server, "echo $HOME").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// expandHome replaces a leading "~" with homeDir.
func expandHome(path, homeDir string) string {
	if strings.HasPrefix(path, "~/") {
		return homeDir + path[1:]
	}
	if path == "~" {
		return homeDir
	}
	return path
}

// remoteGlob runs a shell glob on the server and returns matching paths.
func remoteGlob(server, pattern string) ([]string, error) {
	shellCmd := fmt.Sprintf("ls -d %s 2>/dev/null || true", pattern)
	var out []byte
	var err error
	if server == "" {
		out, err = exec.Command("sh", "-c", shellCmd).Output()
	} else {
		out, err = exec.Command("ssh", server, shellCmd).Output()
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}
