package config

import (
	"fmt"
	"hash/fnv"
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
}

func Parse(path string) (*Config, error) {
	cfg := &Config{DefaultBase: "main"}
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}
