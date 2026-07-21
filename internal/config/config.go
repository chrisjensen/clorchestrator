package config

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// defaultTabColors is the palette used when iterm_tab_color is unset.
var defaultTabColors = []string{
	"#4a9eff",
	"#ff6b6b",
	"#51cf66",
	"#fcc419",
	"#cc5de8",
	"#ff922b",
	"#20c997",
	"#f06595",
}

// ResolveTabColor returns the effective tab color given the raw config value and
// the config file path. "none"/"no"/"false" suppresses the color; an empty value
// selects a deterministic default from the palette based on the config filename.
func ResolveTabColor(rawValue, configPath string) string {
	switch strings.ToLower(rawValue) {
	case "none", "no", "false":
		return ""
	}
	if rawValue != "" {
		return rawValue
	}
	name := filepath.Base(configPath)
	h := fnv.New32a()
	h.Write([]byte(name))
	return defaultTabColors[h.Sum32()%uint32(len(defaultTabColors))]
}

// Command describes a named command for launching a Claude session.
// The entry with Default=true (or the first entry if none is marked) is used
// for all normal sessions. All entries are available for --benchmark runs.
type Command struct {
	Label   string `toml:"label"`
	Cmd     string `toml:"cmd"`
	Default bool   `toml:"default"`
}

// Package describes a library or package within a project that may have its own repo,
// setup command, and optionally a persistent process (start_cmd).
type Package struct {
	Name               string `toml:"name"`
	RemoteRepo         string `toml:"remote_repo"`
	IssueRepo          string `toml:"issue_repo"`
	BranchRepo         string `toml:"branch_repo"`
	DefaultBase        string `toml:"default_base"`
	PostSetupCmd       string `toml:"post_setup_cmd"`
	StartCmd           string `toml:"start_cmd"`
	WorktreePrefix     string `toml:"worktree_prefix"`
	ITermTabColor      string `toml:"iterm_tab_color"`      // color for claude/work sessions on this package
	PackageRunnerColor string `toml:"package_runner_color"` // color for the start_cmd runner session
}

type Config struct {
	Server           string    `toml:"server"`
	RemoteRepo       string    `toml:"remote_repo"`
	IssueRepo        string    `toml:"issue_repo"`
	BranchRepo       string    `toml:"branch_repo"`
	DefaultBase      string    `toml:"default_base"`
	BranchNameFormat string    `toml:"branch_name_format"`
	PostSetupCmd     string    `toml:"post_setup_cmd"`
	WorktreePrefix   string    `toml:"worktree_prefix"`
	ITermTabColor    string    `toml:"iterm_tab_color"`
	PlanningContext  string    `toml:"planning_context"`
	DispatchMode     string    `toml:"dispatch_mode"`
	ID               string    `toml:"id"`
	Packages         []Package `toml:"package"`
	Commands         []Command `toml:"command"`
}

// DefaultClaudeCmd returns the command used to launch Claude in normal
// (non-benchmark) sessions. Returns the first command with Default=true, then
// the first command in the list, then the built-in fallback.
func (c *Config) DefaultClaudeCmd() string {
	for _, cmd := range c.Commands {
		if cmd.Default {
			return cmd.Cmd
		}
	}
	if len(c.Commands) > 0 {
		return c.Commands[0].Cmd
	}
	return "headclaude --model opus"
}

// CommandByLabel returns the Cmd for the named command, or an error if no
// command with that label exists in the config.
func (c *Config) CommandByLabel(label string) (string, error) {
	for _, cmd := range c.Commands {
		if cmd.Label == label {
			return cmd.Cmd, nil
		}
	}
	return "", fmt.Errorf("no command with label %q defined in config", label)
}

// ResolvePackage returns a copy of the config with the named package's fields
// overlaid. Fields set on the package override the global value; unset fields
// fall back to the global value. Returns the config unchanged when name is
// empty (backward-compatible with configs that have no [[package]] blocks).
func (c *Config) ResolvePackage(name string) (*Config, error) {
	if name == "" {
		return c, nil
	}
	for _, pkg := range c.Packages {
		if pkg.Name == name {
			resolved := *c
			if pkg.RemoteRepo != "" {
				resolved.RemoteRepo = pkg.RemoteRepo
			}
			if pkg.IssueRepo != "" {
				resolved.IssueRepo = pkg.IssueRepo
			}
			if pkg.BranchRepo != "" {
				resolved.BranchRepo = pkg.BranchRepo
			}
			if pkg.DefaultBase != "" {
				resolved.DefaultBase = pkg.DefaultBase
			}
			if pkg.PostSetupCmd != "" {
				resolved.PostSetupCmd = pkg.PostSetupCmd
			}
			if pkg.ITermTabColor != "" {
				resolved.ITermTabColor = pkg.ITermTabColor
			}
			if pkg.WorktreePrefix != "" {
				resolved.WorktreePrefix = pkg.WorktreePrefix
			}
			return &resolved, nil
		}
	}
	return nil, fmt.Errorf("package %q not found in config", name)
}

// ConfigID returns the effective identifier for the config at configPath.
// If the config's "id" field is set, that value is returned. Otherwise the
// identifier is the shortest prefix of the filename stem (≥5 chars) that is
// unique among all .toml files in ~/.clorchestrate/.
func ConfigID(configPath string) (string, error) {
	cfg, err := Parse(configPath)
	if err != nil {
		return "", err
	}
	if cfg.ID != "" {
		return cfg.ID, nil
	}

	stem := strings.TrimSuffix(filepath.Base(configPath), ".toml")

	allStems := configDirStems()

	minLen := 5
	if len(stem) < minLen {
		minLen = len(stem)
	}
	for n := minLen; n <= len(stem); n++ {
		prefix := stem[:n]
		conflict := false
		for _, other := range allStems {
			if other == stem {
				continue
			}
			if strings.HasPrefix(other, prefix) {
				conflict = true
				break
			}
		}
		if !conflict {
			return prefix, nil
		}
	}
	return stem, nil
}

// configDirStems returns the filename stems (without .toml extension) of all
// .toml files in ~/.clorchestrate/. Returns an empty slice if the directory
// does not exist or cannot be read.
func configDirStems() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(home, ".clorchestrate"))
	if err != nil {
		return nil
	}
	var stems []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
			stems = append(stems, strings.TrimSuffix(e.Name(), ".toml"))
		}
	}
	return stems
}

func Parse(path string) (*Config, error) {
	cfg := &Config{DefaultBase: "main"}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}
