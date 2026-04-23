package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParse_AllKeys(t *testing.T) {
	path := writeTemp(t, `
server = "myserver"
remote_repo = "~/src/myrepo"
issue_repo = "org/issues"
branch_repo = "org/branches"
default_base = "develop"
branch_name_format = "feature/{issue}-{handle}"
post_setup_cmd = "npm run build"
iterm_tab_color = "#f4b6cf"
planning_context = "some context"
`)
	cfg, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "myserver" {
		t.Errorf("Server = %q, want myserver", cfg.Server)
	}
	if cfg.RemoteRepo != "~/src/myrepo" {
		t.Errorf("RemoteRepo = %q", cfg.RemoteRepo)
	}
	if cfg.IssueRepo != "org/issues" {
		t.Errorf("IssueRepo = %q", cfg.IssueRepo)
	}
	if cfg.BranchRepo != "org/branches" {
		t.Errorf("BranchRepo = %q", cfg.BranchRepo)
	}
	if cfg.DefaultBase != "develop" {
		t.Errorf("DefaultBase = %q", cfg.DefaultBase)
	}
	if cfg.BranchNameFormat != "feature/{issue}-{handle}" {
		t.Errorf("BranchNameFormat = %q", cfg.BranchNameFormat)
	}
	if cfg.PostSetupCmd != "npm run build" {
		t.Errorf("PostSetupCmd = %q", cfg.PostSetupCmd)
	}
	if cfg.ITermTabColor != "#f4b6cf" {
		t.Errorf("ITermTabColor = %q", cfg.ITermTabColor)
	}
	if cfg.PlanningContext != "some context" {
		t.Errorf("PlanningContext = %q", cfg.PlanningContext)
	}
}

func TestParse_MultilineValue(t *testing.T) {
	path := writeTemp(t, `
server = "myserver"
planning_context = """
line one

line two"""
`)
	cfg, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "myserver" {
		t.Errorf("Server = %q, want myserver", cfg.Server)
	}
	want := "line one\n\nline two"
	if cfg.PlanningContext != want {
		t.Errorf("PlanningContext = %q, want %q", cfg.PlanningContext, want)
	}
}

func TestResolveTabColor(t *testing.T) {
	for _, suppress := range []string{"none", "no", "false", "None", "NO", "FALSE"} {
		if got := ResolveTabColor(suppress, "/any/path.toml"); got != "" {
			t.Errorf("ResolveTabColor(%q, ...) = %q, want empty", suppress, got)
		}
	}

	if got := ResolveTabColor("#aabbcc", "/any/path.toml"); got != "#aabbcc" {
		t.Errorf("ResolveTabColor passthrough = %q, want #aabbcc", got)
	}

	// Deterministic: same path always yields the same color.
	c1 := ResolveTabColor("", "/some/project.toml")
	c2 := ResolveTabColor("", "/some/project.toml")
	if c1 != c2 {
		t.Errorf("ResolveTabColor not deterministic: %q != %q", c1, c2)
	}
	if c1 == "" {
		t.Error("ResolveTabColor returned empty for unset value")
	}

	// Different filenames should (in practice) produce different colors.
	cA := ResolveTabColor("", "/a/alpha.toml")
	cB := ResolveTabColor("", "/b/beta.toml")
	if cA == cB {
		t.Logf("ResolveTabColor: alpha.toml and beta.toml both mapped to %q (hash collision, not a bug)", cA)
	}
}

func TestParse_DefaultsAndComments(t *testing.T) {
	path := writeTemp(t, `
# A comment
server = "myserver"

# Another comment
`)
	cfg, err := Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultBase != "main" {
		t.Errorf("DefaultBase = %q, want main", cfg.DefaultBase)
	}
	if cfg.Server != "myserver" {
		t.Errorf("Server = %q", cfg.Server)
	}
}
