package cmd

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/chrisjensen/clorchestrate/internal/iterm"
)

const reconnectUsage = `usage:
  clorchestrate reconnect [<config>] [--force]

Without <config>: reconnect all sessions for every config in ~/.clorchestrate/
that has a 'server' field set.
With <config>: reconnect only sessions belonging to that config.

By default only Detached sessions are reattached (via 'screen -r'), so
sessions already open in another tab are left alone. Pass --force to
forcibly reattach any matching session (via 'screen -dr'), detaching any
currently-connected client.
`

type screenSession struct {
	name  string
	state string
}

type configEntry struct {
	path string
	cfg  *config.Config
}

func RunReconnect(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("reconnect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	force := fs.Bool("force", false, "forcibly reattach matching sessions using 'screen -dr'")
	fs.Usage = func() { fmt.Fprint(stderr, reconnectUsage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	positional := fs.Args()
	if len(positional) > 1 {
		fs.Usage()
		return fmt.Errorf("too many arguments")
	}

	var entries []configEntry
	if len(positional) == 1 {
		configPath, err := resolveConfigPath(positional[0])
		if err != nil {
			return err
		}
		cfg, err := config.Parse(configPath)
		if err != nil {
			return err
		}
		entries = []configEntry{{path: configPath, cfg: cfg}}
	} else {
		var err error
		entries, err = loadAllConfigs()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Fprintln(stderr, "no configs found in ~/.clorchestrate/")
			return nil
		}
	}

	// Group by server to SSH once per unique server.
	byServer := map[string][]configEntry{}
	for _, e := range entries {
		if e.cfg.Server == "" {
			continue
		}
		byServer[e.cfg.Server] = append(byServer[e.cfg.Server], e)
	}

	for server, serverEntries := range byServer {
		sessions, err := listScreenSessions(server)
		if err != nil {
			fmt.Fprintf(stderr, "warning: could not list sessions on %s: %v\n", server, err)
			continue
		}

		for _, e := range serverEntries {
			configID, err := config.ConfigID(e.path)
			if err != nil {
				fmt.Fprintf(stderr, "warning: could not determine config ID for %s: %v\n", e.path, err)
				continue
			}
			prefix := configID + "_"
			tabColor := config.ResolveTabColor(e.cfg.ITermTabColor, e.path)

			screenAction := "screen -r"
			if *force {
				screenAction = "screen -dr"
			}
			for _, sess := range sessions {
				if !strings.HasPrefix(sess.name, prefix) {
					continue
				}
				if !*force && sess.state != "Detached" {
					fmt.Fprintf(stderr, "skipping %s (%s) — pass --force to reattach\n", sess.name, sess.state)
					continue
				}
				reconnectCmd := fmt.Sprintf(`ssh -t %s "%s %s"`, server, screenAction, sess.name)
				if err := iterm.OpenTab(iterm.TabOptions{
					TabColorHex: tabColor,
					RemoteCmd:   reconnectCmd,
				}); err != nil {
					fmt.Fprintf(stderr, "warning: could not open tab for session %s: %v\n", sess.name, err)
				}
			}
		}
	}

	return nil
}

func loadAllConfigs() ([]configEntry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".clorchestrate")
	dirEntries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config dir: %w", err)
	}

	var result []configEntry
	for _, e := range dirEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		cfg, err := config.Parse(path)
		if err != nil {
			continue
		}
		result = append(result, configEntry{path: path, cfg: cfg})
	}
	return result, nil
}

func listScreenSessions(server string) ([]screenSession, error) {
	// screen -ls exits non-zero even when sessions exist; ignore exit code.
	out, _ := exec.Command("ssh", server, "screen -ls").CombinedOutput()
	return parseScreenLs(string(out)), nil
}

// parseScreenLs parses the output of `screen -ls` into a list of sessions.
// Lines containing sessions look like:
//
//	\t<pid>.<name>\t(<state>)
func parseScreenLs(output string) []screenSession {
	var sessions []screenSession
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		dotIdx := strings.Index(trimmed, ".")
		if dotIdx == -1 {
			continue
		}
		pid := trimmed[:dotIdx]
		if pid == "" || !allDigits(pid) {
			continue
		}
		rest := trimmed[dotIdx+1:]
		parenIdx := strings.LastIndex(rest, "(")
		if parenIdx == -1 {
			continue
		}
		name := strings.TrimSpace(rest[:parenIdx])
		state := strings.TrimSuffix(strings.TrimSpace(rest[parenIdx+1:]), ")")
		if name == "" {
			continue
		}
		sessions = append(sessions, screenSession{name: name, state: state})
	}
	return sessions
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
