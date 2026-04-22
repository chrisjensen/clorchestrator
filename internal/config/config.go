package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Server           string
	RemoteRepo       string
	IssueRepo        string
	BranchRepo       string
	DefaultBase      string
	BranchNameFormat string
	PostSetupCmd     string
	ITermTabColor    string
	PlanningContext  string
	DispatchMode     string
}

func Parse(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()

	cfg := &Config{DefaultBase: "main"}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		value := unquote(strings.TrimSpace(line[eq+1:]))

		switch key {
		case "DISPATCH_SERVER":
			cfg.Server = value
		case "DISPATCH_REMOTE_REPO":
			cfg.RemoteRepo = value
		case "DISPATCH_ISSUE_REPO":
			cfg.IssueRepo = value
		case "DISPATCH_BRANCH_REPO":
			cfg.BranchRepo = value
		case "DISPATCH_DEFAULT_BASE":
			if value != "" {
				cfg.DefaultBase = value
			}
		case "DISPATCH_BRANCH_NAME_FORMAT":
			cfg.BranchNameFormat = value
		case "DISPATCH_POST_SETUP_CMD":
			cfg.PostSetupCmd = value
		case "DISPATCH_ITERM_TAB_COLOR":
			cfg.ITermTabColor = value
		case "DISPATCH_PLANNING_CONTEXT":
			cfg.PlanningContext = value
		case "DISPATCH_MODE":
			cfg.DispatchMode = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return cfg, nil
}

func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
