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

type Config struct {
	Server           string `toml:"server"`
	RemoteRepo       string `toml:"remote_repo"`
	IssueRepo        string `toml:"issue_repo"`
	BranchRepo       string `toml:"branch_repo"`
	DefaultBase      string `toml:"default_base"`
	BranchNameFormat string `toml:"branch_name_format"`
	PostSetupCmd     string `toml:"post_setup_cmd"`
	ITermTabColor    string `toml:"iterm_tab_color"`
	PlanningContext  string `toml:"planning_context"`
	DispatchMode     string `toml:"dispatch_mode"`
	ID               string `toml:"id"`
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
