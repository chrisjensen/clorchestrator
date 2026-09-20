package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner abstracts subprocess execution so tests can inject fakes.
type Runner interface {
	// Run executes a command with the given stdin (may be nil) and returns
	// combined stdout/stderr.
	Run(name string, args []string, stdin []byte) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(name string, args []string, stdin []byte) ([]byte, error) {
	cmd := exec.Command(name, args...)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	return cmd.CombinedOutput()
}

// DefaultRunner is the real subprocess runner.
var DefaultRunner Runner = execRunner{}

type Script struct {
	Name    string // basename on the server, e.g. "start-task.sh"
	Content []byte // local script bytes (from //go:embed)
	Dir     string // target dir on the server; defaults to "~/bin" when empty
}

// dir returns the target directory for the script, defaulting to ~/bin.
func (s Script) dir() string {
	if s.Dir != "" {
		return s.Dir
	}
	return "~/bin"
}

// SyncScripts ensures each script in ~/bin/ on the server matches the
// provided content. Uploads via scp (through a local temp file) and chmods
// +x any scripts that are missing or whose remote hash differs.
// Output is written to the provided writer for user feedback.
func SyncScripts(server string, scripts []Script, r Runner, log func(format string, a ...any)) error {
	if r == nil {
		r = DefaultRunner
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	for _, s := range scripts {
		remotePath := s.dir() + "/" + s.Name
		wantHash := sha256Hex(s.Content)
		gotHash, err := remoteHash(r, server, remotePath)
		if err != nil {
			return fmt.Errorf("hash check for %s: %w", s.Name, err)
		}
		target := server
		if target == "" {
			target = "local"
		}
		if gotHash == wantHash {
			log("Script %s up to date on %s\n", s.Name, target)
			continue
		}
		log("Syncing %s to %s (current: %s, want: %s)\n", s.Name, target, shortHash(gotHash), shortHash(wantHash))
		if err := ensureRemoteDir(r, server, s.dir()); err != nil {
			return err
		}
		if err := uploadScript(r, server, s, remotePath); err != nil {
			return fmt.Errorf("upload %s: %w", s.Name, err)
		}
	}
	return nil
}

// remoteHash returns the sha256 hex of the script at remotePath, or "" if
// missing. When server is "" it reads the local file directly.
func remoteHash(r Runner, server, remotePath string) (string, error) {
	if server == "" {
		localPath := expandHome(remotePath)
		content, err := os.ReadFile(localPath)
		if os.IsNotExist(err) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read local script %s: %w", localPath, err)
		}
		return sha256Hex(content), nil
	}
	// shasum -a 256 works on macOS and Linux (sha256sum is Linux-only).
	cmd := fmt.Sprintf(`if [ -f %s ]; then shasum -a 256 %s 2>/dev/null | awk '{print $1}'; else echo MISSING; fi`, remotePath, remotePath)
	out, err := r.Run("ssh", []string{server, cmd}, nil)
	if err != nil {
		return "", fmt.Errorf("ssh %s: %w: %s", server, err, string(out))
	}
	// SSH may emit warnings on stderr (e.g. port-forwarding failures) which
	// CombinedOutput mixes in. Scan for the sha256 line explicitly.
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "MISSING" {
			return "", nil
		}
		if len(line) == 64 && isHex(line) {
			return line, nil
		}
	}
	return "", nil
}

func ensureRemoteDir(r Runner, server, dir string) error {
	if server == "" {
		return os.MkdirAll(expandHome(dir), 0755)
	}
	out, err := r.Run("ssh", []string{server, fmt.Sprintf("mkdir -p %s", dir)}, nil)
	if err != nil {
		return fmt.Errorf("mkdir %s on %s: %w: %s", dir, server, err, string(out))
	}
	return nil
}

func uploadScript(r Runner, server string, s Script, remotePath string) error {
	if server == "" {
		localPath := expandHome(remotePath)
		if err := os.WriteFile(localPath, s.Content, 0755); err != nil {
			return fmt.Errorf("write local script %s: %w", localPath, err)
		}
		return nil
	}

	tmp, err := os.CreateTemp("", "clorchestrate-*-"+s.Name)
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(s.Content); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	// scp to remote. Remote path may contain ~ which scp expands via shell.
	scpTarget := fmt.Sprintf("%s:%s", server, remotePath)
	out, err := r.Run("scp", []string{tmp.Name(), scpTarget}, nil)
	if err != nil {
		return fmt.Errorf("scp: %w: %s", err, string(out))
	}

	out, err = r.Run("ssh", []string{server, fmt.Sprintf("chmod +x %s", remotePath)}, nil)
	if err != nil {
		return fmt.Errorf("chmod: %w: %s", err, string(out))
	}
	return nil
}

// expandHome replaces a leading ~/ with the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func shortHash(h string) string {
	if len(h) == 0 {
		return "MISSING"
	}
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// LocalScriptsDir returns the path where local copies of the scripts are
// stored for reference / manual editing. The binary is the source of truth
// (via //go:embed), but this directory mirrors them so a user can inspect
// what would be synced.
func LocalScriptsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".clorchestrate", "bin"), nil
}
