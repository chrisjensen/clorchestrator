package cmd

import (
	"strings"
	"testing"

	"github.com/chrisjensen/clorchestrate/internal/config"
	"github.com/chrisjensen/clorchestrate/prompts"
)

func TestDetectMode(t *testing.T) {
	cases := []struct {
		name    string
		handle  string
		branch  string
		issue   string
		want    Mode
		wantErr bool
	}{
		{"bare", "", "", "", ModeBareSession, false},
		{"worktree", "h", "b", "", ModeWorktree, false},
		{"full", "h", "b", "42", ModeFullTask, false},
		{"handle only", "h", "", "", ModeHandleSession, false},
		{"invalid issue only", "", "", "42", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := detectMode(c.handle, c.branch, c.issue)
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr=%v", err, c.wantErr)
			}
			if !c.wantErr && m != c.want {
				t.Errorf("got %v, want %v", m, c.want)
			}
		})
	}
}

func TestBuildRemoteCmd_FullTaskFresh(t *testing.T) {
	cfg := &config.Config{Server: "myserver"}
	got := buildRemoteCmd(cfg, ModeFullTask, "my-handle", "mycon_my-handle", "", "~/src/extractor-branch-x", false)
	if !strings.Contains(got, "ssh -t myserver") {
		t.Errorf("missing ssh -t: %s", got)
	}
	if !strings.Contains(got, "screen -S mycon_my-handle bash -l") {
		t.Errorf("expected plain screen+bash -l: %s", got)
	}
	if strings.Contains(got, "start-task.sh") {
		t.Errorf("should not reference start-task.sh: %s", got)
	}
}

func TestBuildRemoteCmd_FullTaskReattach(t *testing.T) {
	cfg := &config.Config{Server: "myserver"}
	got := buildRemoteCmd(cfg, ModeFullTask, "my-handle", "mycon_my-handle", "12345.mycon_my-handle", "~/src/extractor-branch-x", false)
	if !strings.Contains(got, "screen -r 12345.mycon_my-handle") {
		t.Errorf("expected screen -r for reattach: %s", got)
	}
	if strings.Contains(got, "screen -dr") {
		t.Errorf("reattach should not force-detach with -dr: %s", got)
	}
}

func TestBuildRemoteCmd_BareSession(t *testing.T) {
	cfg := &config.Config{Server: "myserver", RemoteRepo: "~/src/repo"}
	got := buildRemoteCmd(cfg, ModeBareSession, "", "", "", "", false)
	if !strings.Contains(got, "cd ~/src/repo") {
		t.Errorf("missing cd: %s", got)
	}
	if strings.Contains(got, "screen") {
		t.Errorf("bare session should not use screen: %s", got)
	}
}

func TestBuildRemoteCmd_HandleSession(t *testing.T) {
	cfg := &config.Config{Server: "myserver", RemoteRepo: "~/src/repo"}
	got := buildRemoteCmd(cfg, ModeHandleSession, "my-handle", "mycon_my-handle", "", "", false)
	if !strings.Contains(got, "ssh -t myserver") {
		t.Errorf("missing ssh -t: %s", got)
	}
	if !strings.Contains(got, "screen -S mycon_my-handle") {
		t.Errorf("missing screen -S with config prefix: %s", got)
	}
	if !strings.Contains(got, "cd ~/src/repo") {
		t.Errorf("missing cd to remote repo: %s", got)
	}
}

func TestBuildRemoteCmd_NoClaude(t *testing.T) {
	wd := "~/src/extractor-branch-x"
	cases := []struct {
		name string
		mode Mode
	}{
		{"ModeFullTask", ModeFullTask},
		{"ModeWorktree", ModeWorktree},
	}
	for _, c := range cases {
		t.Run("remote/"+c.name, func(t *testing.T) {
			cfg := &config.Config{Server: "myserver"}
			got := buildRemoteCmd(cfg, c.mode, "h", "mycon_h", "", wd, true)
			want := `ssh -t myserver "screen -S mycon_h bash -c 'cd ~/src/extractor-branch-x && exec bash -l'"`
			if got != want {
				t.Errorf("got %q\nwant %q", got, want)
			}
		})
		t.Run("local/"+c.name, func(t *testing.T) {
			cfg := &config.Config{Server: ""}
			got := buildRemoteCmd(cfg, c.mode, "h", "mycon_h", "", wd, true)
			want := "screen -S mycon_h bash -c 'cd ~/src/extractor-branch-x && exec bash -l'"
			if got != want {
				t.Errorf("got %q\nwant %q", got, want)
			}
		})
	}

	// Reattach still wins over noClaude — an existing detached session
	// already has whatever state the user wants to resume.
	t.Run("reattach overrides noClaude", func(t *testing.T) {
		cfg := &config.Config{Server: "myserver"}
		got := buildRemoteCmd(cfg, ModeFullTask, "h", "mycon_h", "999.mycon_h", wd, true)
		if !strings.Contains(got, "screen -r 999.mycon_h") {
			t.Errorf("expected screen -r for reattach: %s", got)
		}
	})
}

func TestBuildFollowupCmd(t *testing.T) {
	wd := "~/src/extractor-branch-x"
	defaultCmd := "headclaude --model opus"
	if got := buildFollowupCmd(ModeFullTask, "h", wd, "", false, true, true, defaultCmd); !strings.Contains(got, "cd ~/src/extractor-branch-x && headclaude --model opus --permission-mode plan \"$(cat /tmp/task-h.prompt.md)\"") {
		t.Errorf("ModeFullTask fresh: got %q", got)
	}
	if got := buildFollowupCmd(ModeWorktree, "h", wd, "", false, false, true, defaultCmd); got != "cd ~/src/extractor-branch-x && headclaude --model opus" {
		t.Errorf("ModeWorktree no prompt: got %q", got)
	}
	if got := buildFollowupCmd(ModeWorktree, "h", wd, "", false, true, true, defaultCmd); !strings.Contains(got, "cd ~/src/extractor-branch-x && headclaude --model opus --permission-mode plan \"$(cat /tmp/task-h.prompt.md)\"") {
		t.Errorf("ModeWorktree with prompt: got %q", got)
	}
	// Hive sessions pass forcePlan=false: the skill prompt runs without plan mode.
	if got := buildFollowupCmd(ModeWorktree, "h", wd, "", false, true, false, defaultCmd); got != `cd ~/src/extractor-branch-x && headclaude --model opus "$(cat /tmp/task-h.prompt.md)"` {
		t.Errorf("hive no-plan with prompt: got %q", got)
	}
	if got := buildFollowupCmd(ModeFullTask, "h", wd, "123.x", false, true, true, defaultCmd); got != "" {
		t.Errorf("reattach should suppress followup: got %q", got)
	}
	if got := buildFollowupCmd(ModeHandleSession, "h", wd, "", false, false, true, defaultCmd); got != "" {
		t.Errorf("ModeHandleSession should have no followup: got %q", got)
	}
	if got := buildFollowupCmd(ModeFullTask, "h", wd, "", true, true, true, defaultCmd); got != "" {
		t.Errorf("noClaude ModeFullTask should suppress followup: got %q", got)
	}
	if got := buildFollowupCmd(ModeWorktree, "h", wd, "", true, false, true, defaultCmd); got != "" {
		t.Errorf("noClaude ModeWorktree should suppress followup: got %q", got)
	}
	templateCmd := "kopencode --agent plan --prompt {prompt}"
	if got := buildFollowupCmd(ModeFullTask, "h", wd, "", false, true, true, templateCmd); got != `cd ~/src/extractor-branch-x && kopencode --agent plan --prompt "$(cat /tmp/task-h.prompt.md)"` {
		t.Errorf("template with prompt: got %q", got)
	}
	if got := buildFollowupCmd(ModeFullTask, "h", wd, "", false, false, true, templateCmd); got != "cd ~/src/extractor-branch-x && kopencode --agent plan --prompt {prompt}" {
		t.Errorf("template no prompt: got %q", got)
	}
}

func TestWorktreePath(t *testing.T) {
	// Mirrors scripts/worktree-checkout.sh: $(dirname REMOTE_REPO)/<prefix>-<sanitized-branch>
	// prefix defaults to repo basename when empty.
	cases := []struct {
		repo, branch, prefix, want string
	}{
		{"~/src/ncoderz/extractor", "9802-foo", "extractor", "~/src/ncoderz/extractor-9802-foo"},
		{"~/src/ncoderz/extractor/", "9802-foo", "extractor", "~/src/ncoderz/extractor-9802-foo"},
		{"~/src/ncoderz/extractor", "feature/abc", "extractor", "~/src/ncoderz/extractor-feature-abc"},
		{"~/src/ncoderz/bitmark-parser-generator", "feature/abc", "parser", "~/src/ncoderz/parser-feature-abc"},
		{"~/src/ncoderz/bitmark-parser-generator", "feature/abc", "", "~/src/ncoderz/bitmark-parser-generator-feature-abc"},
		{"/home/chris/src/repo", "x/y/z", "myprefix", "/home/chris/src/myprefix-x-y-z"},
	}
	for _, c := range cases {
		if got := worktreePath(c.repo, c.branch, c.prefix); got != c.want {
			t.Errorf("worktreePath(%q, %q, %q) = %q, want %q", c.repo, c.branch, c.prefix, got, c.want)
		}
	}
}

func TestParseScreenLs(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		want   []screenSession
	}{
		{
			name: "detached without timestamp",
			input: "\t12345.mycon_handle\t(Detached)\n",
			want:  []screenSession{{name: "mycon_handle", state: "Detached"}},
		},
		{
			name: "detached with timestamp",
			input: "\t12345.mycon_handle\t(04/26/26 12:28:46)\t(Detached)\n",
			want:  []screenSession{{name: "mycon_handle", state: "Detached"}},
		},
		{
			name: "attached with timestamp",
			input: "\t99999.extra_extract-may\t(04/26/26 12:28:46)\t(Attached)\n",
			want:  []screenSession{{name: "extra_extract-may", state: "Attached"}},
		},
		{
			name:  "no sessions",
			input: "No Sockets found in /tmp/screens/S-user.\n",
			want:  nil,
		},
		{
			name: "multiple sessions",
			input: "\t111.cfg_a\t(01/01/26 10:00:00)\t(Detached)\n\t222.cfg_b\t(Attached)\n",
			want: []screenSession{
				{name: "cfg_a", state: "Detached"},
				{name: "cfg_b", state: "Attached"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseScreenLs(c.input)
			if len(got) != len(c.want) {
				t.Fatalf("got %v sessions, want %v: %+v", len(got), len(c.want), got)
			}
			for i, s := range got {
				if s.name != c.want[i].name || s.state != c.want[i].state {
					t.Errorf("[%d] got {%q %q}, want {%q %q}", i, s.name, s.state, c.want[i].name, c.want[i].state)
				}
			}
		})
	}
}

func TestBuildPrompt(t *testing.T) {
	got, err := prompts.Prompt(prompts.PromptData{
		IssueNum:        "42",
		IssueRepo:       "org/repo",
		IssueURL:        "https://github.com/org/repo/issues/42",
		PlanningContext: "ctx-here",
		ExtraContext:    "extra-here",
	})
	if err != nil {
		t.Fatalf("prompts.Issue: %v", err)
	}
	for _, want := range []string{
		"issue #42 in org/repo",
		"https://github.com/org/repo/issues/42",
		"ctx-here",
		"extra-here",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}
