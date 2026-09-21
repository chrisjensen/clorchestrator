package cmd

import (
	"os"
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
	got := buildRemoteCmd(cfg, ModeFullTask, "my-handle", "mycon_my-handle", "", "~/src/extractor-branch-x", "")
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
	got := buildRemoteCmd(cfg, ModeFullTask, "my-handle", "mycon_my-handle", "12345.mycon_my-handle", "~/src/extractor-branch-x", "")
	if !strings.Contains(got, "screen -r 12345.mycon_my-handle") {
		t.Errorf("expected screen -r for reattach: %s", got)
	}
	if strings.Contains(got, "screen -dr") {
		t.Errorf("reattach should not force-detach with -dr: %s", got)
	}
}

func TestBuildRemoteCmd_BareSession(t *testing.T) {
	cfg := &config.Config{Server: "myserver", RemoteRepo: "~/src/repo"}
	got := buildRemoteCmd(cfg, ModeBareSession, "", "", "", "", "")
	if !strings.Contains(got, "cd ~/src/repo") {
		t.Errorf("missing cd: %s", got)
	}
	if strings.Contains(got, "screen") {
		t.Errorf("bare session should not use screen: %s", got)
	}
}

func TestBuildRemoteCmd_HandleSession(t *testing.T) {
	cfg := &config.Config{Server: "myserver", RemoteRepo: "~/src/repo"}
	got := buildRemoteCmd(cfg, ModeHandleSession, "my-handle", "mycon_my-handle", "", "", "")
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
			got := buildRemoteCmd(cfg, c.mode, "h", "mycon_h", "", wd, "cd "+wd)
			want := `ssh -t myserver "screen -S mycon_h bash -c 'cd ~/src/extractor-branch-x && exec bash -l'"`
			if got != want {
				t.Errorf("got %q\nwant %q", got, want)
			}
		})
		t.Run("local/"+c.name, func(t *testing.T) {
			cfg := &config.Config{Server: ""}
			got := buildRemoteCmd(cfg, c.mode, "h", "mycon_h", "", wd, "cd "+wd)
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
		got := buildRemoteCmd(cfg, ModeFullTask, "h", "mycon_h", "999.mycon_h", wd, "cd "+wd)
		if !strings.Contains(got, "screen -r 999.mycon_h") {
			t.Errorf("expected screen -r for reattach: %s", got)
		}
	})
}

// TestBuildRemoteCmd_LaunchCmdWithPromptSubstitution guards against
// regressing the current-terminal (no --tab) launch: the followup command
// embeds a "$(cat ...)" prompt substitution, which must reach the remote
// bash -c unevaluated by any shell along the way (ssh -t's local sh -c, and
// the remote's own shell) so it only runs once, on the remote host.
func TestBuildRemoteCmd_LaunchCmdWithPromptSubstitution(t *testing.T) {
	wd := "~/src/extractor-branch-x"
	launchCmd := `cd ` + wd + ` && headclaude --permission-mode plan "$(cat /tmp/task-h.prompt.md)"`

	cfg := &config.Config{Server: "myserver"}
	got := buildRemoteCmd(cfg, ModeFullTask, "h", "mycon_h", "", wd, launchCmd)
	want := `ssh -t myserver "screen -S mycon_h bash -c 'cd ~/src/extractor-branch-x && headclaude --permission-mode plan \"\$(cat /tmp/task-h.prompt.md)\" && exec bash -l'"`
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}

	// Local (no server) has no local-shell-then-ssh double layer, so the
	// prompt substitution is embedded verbatim inside the single-quoted
	// bash -c argument.
	localCfg := &config.Config{Server: ""}
	gotLocal := buildRemoteCmd(localCfg, ModeFullTask, "h", "mycon_h", "", wd, launchCmd)
	wantLocal := `screen -S mycon_h bash -c 'cd ~/src/extractor-branch-x && headclaude --permission-mode plan "$(cat /tmp/task-h.prompt.md)" && exec bash -l'`
	if gotLocal != wantLocal {
		t.Errorf("got %q\nwant %q", gotLocal, wantLocal)
	}
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

func TestHiveLaunchPrompt(t *testing.T) {
	if got := hiveLaunchPrompt(hiveRoleWorker, "Implement issue #42.", ""); got != "Implement issue #42.\n\nUse the hive-worker skill to implement this task." {
		t.Errorf("worker with task body: got %q", got)
	}
	if got := hiveLaunchPrompt(hiveRoleWorker, "", ""); got != "Use the hive-worker skill to implement this task." {
		t.Errorf("worker with no task body: got %q", got)
	}
	if got := hiveLaunchPrompt(hiveRoleCoordinator, "Implement issue #42.", "main"); got != "/hive-coordinate main" {
		t.Errorf("coordinator: got %q", got)
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
		name  string
		input string
		want  []screenSession
	}{
		{
			name:  "detached without timestamp",
			input: "\t12345.mycon_handle\t(Detached)\n",
			want:  []screenSession{{name: "mycon_handle", state: "Detached"}},
		},
		{
			name:  "detached with timestamp",
			input: "\t12345.mycon_handle\t(04/26/26 12:28:46)\t(Detached)\n",
			want:  []screenSession{{name: "mycon_handle", state: "Detached"}},
		},
		{
			name:  "attached with timestamp",
			input: "\t99999.extra_extract-may\t(04/26/26 12:28:46)\t(Attached)\n",
			want:  []screenSession{{name: "extra_extract-may", state: "Attached"}},
		},
		{
			name:  "no sessions",
			input: "No Sockets found in /tmp/screens/S-user.\n",
			want:  nil,
		},
		{
			name:  "multiple sessions",
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

func TestValidateSafeName(t *testing.T) {
	for _, s := range []string{"Foo-bar_1.2", "9802-foo", ""} {
		if err := validateSafeName("handle", s); err != nil {
			t.Errorf("validateSafeName(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range []string{"has space", "a$b", "a`b", `a"b`, "feat/x", "a;b"} {
		err := validateSafeName("branch", s)
		if err == nil {
			t.Errorf("validateSafeName(%q) = nil, want error", s)
			continue
		}
		if !strings.Contains(err.Error(), "invalid branch") {
			t.Errorf("error for %q missing kind: %v", s, err)
		}
	}
}

func TestPlanSession(t *testing.T) {
	cfg := &config.Config{RemoteRepo: "~/src/extractor", WorktreePrefix: "extractor"}
	cases := []struct {
		name      string
		handle    string
		branch    string
		issueNum  string
		opts      openOptions
		session   string
		runKey    string
		slug      string
		worktree  string
		worktreeB string
	}{
		{
			name: "bare handle", handle: "h", branch: "b",
			session: "mycon_h", runKey: "mycon_h", slug: "h",
			worktree: "~/src/extractor-b", worktreeB: "extractor-b",
		},
		{
			name: "pkg prefix override", handle: "h", branch: "b", opts: openOptions{pkg: "mypkg"},
			session: "mypkg_h", runKey: "mypkg_h", slug: "h",
			worktree: "~/src/extractor-b", worktreeB: "extractor-b",
		},
		{
			name: "issue suffix", handle: "h", branch: "b", issueNum: "42",
			session: "mycon_h_42", runKey: "mycon_h_42", slug: "h",
			worktree: "~/src/extractor-b", worktreeB: "extractor-b",
		},
		{
			name: "benchmark label", handle: "h", branch: "b", opts: openOptions{benchmarkLabel: "fast"},
			session: "mycon_h_fast", runKey: "mycon_h", slug: "h-fast",
			worktree: "~/src/extractor-b", worktreeB: "extractor-b",
		},
		{
			name: "hive worker", handle: "h", branch: "b",
			opts:    openOptions{benchmarkLabel: "fast", hiveRole: hiveRoleWorker, runDir: "/tmp/run"},
			session: "mycon_h_fast", runKey: "mycon_h", slug: "h-fast",
			worktree: "/tmp/run/fast", worktreeB: "fast",
		},
		{
			name: "hive coordinator", handle: "h", branch: "b",
			opts:    openOptions{benchmarkLabel: "fast", hiveRole: hiveRoleCoordinator, runDir: "/tmp/run"},
			session: "mycon_h_fast_coordinator", runKey: "mycon_h", slug: "h-fast-coordinator",
			worktree: "/tmp/run", worktreeB: "run",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planSession(cfg, "mycon", c.handle, c.branch, c.issueNum, c.opts)
			want := openSessionPlan{
				sessionName:  c.session,
				runKey:       c.runKey,
				slug:         c.slug,
				worktreeDir:  c.worktree,
				worktreeBase: c.worktreeB,
			}
			if got != want {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestBuildTaskConf(t *testing.T) {
	cfg := &config.Config{RemoteRepo: "~/src/r", WorktreePrefix: "p", PostSetupCmd: "echo hi", IssueRepo: "org/repo"}

	tc := buildTaskConf(cfg, ModeWorktree, openOptions{}, "b", "", "~/src/r-b")
	if tc.Branch != "b" || tc.Issue != "" || tc.RunDir != "" || tc.WorktreeDir != "" {
		t.Errorf("worktree only: got %+v", tc)
	}

	tc = buildTaskConf(cfg, ModeFullTask, openOptions{}, "b", "42", "~/src/r-b")
	if tc.Issue != "42" || tc.IssueRepo != "org/repo" {
		t.Errorf("full task: got %+v", tc)
	}

	tc = buildTaskConf(cfg, ModeWorktree, openOptions{hiveRole: hiveRoleWorker, runDir: "/tmp/run"}, "b", "", "/tmp/run/fast")
	if tc.RunDir != "/tmp/run" || tc.WorktreeDir != "/tmp/run/fast" || tc.Branch != "b" {
		t.Errorf("hive worker: got %+v", tc)
	}

	tc = buildTaskConf(cfg, ModeWorktree, openOptions{hiveRole: hiveRoleCoordinator, runDir: "/tmp/run"}, "b", "", "/tmp/run")
	if tc.RunDir != "/tmp/run" || tc.WorktreeDir != "" || tc.Branch != "" {
		t.Errorf("hive coordinator: got %+v", tc)
	}
}

func TestBuildTaskBody(t *testing.T) {
	cfg := &config.Config{IssueRepo: "org/repo", PlanningContext: "ctx-here"}

	body, err := buildTaskBody(cfg, ModeFullTask, openOptions{extraContext: "extra-here"}, "42")
	if err != nil {
		t.Fatalf("full task: %v", err)
	}
	for _, want := range []string{"https://github.com/org/repo/issues/42", "extra-here"} {
		if !strings.Contains(body, want) {
			t.Errorf("full task body missing %q:\n%s", want, body)
		}
	}

	body, err = buildTaskBody(cfg, ModeWorktree, openOptions{extraContext: "extra-here"}, "")
	if err != nil {
		t.Fatalf("worktree with context: %v", err)
	}
	if !strings.Contains(body, "extra-here") {
		t.Errorf("worktree body missing extra context:\n%s", body)
	}

	if body, err = buildTaskBody(cfg, ModeWorktree, openOptions{}, ""); err != nil || body != "" {
		t.Errorf("worktree without context: got (%q, %v), want empty", body, err)
	}
}

func TestSessionPromptFile_BenchmarkDoneMarker(t *testing.T) {
	slug := "open-test-promptfile"
	t.Cleanup(func() { os.Remove("/tmp/task-" + slug + ".prompt.md") })

	cfg := &config.Config{} // server "" => local write, so the prompt file is inspectable
	wrote, err := sessionPromptFile(cfg, openOptions{benchmarkLabel: "fast"}, slug, "body-here")
	if err != nil {
		t.Fatalf("sessionPromptFile: %v", err)
	}
	if !wrote {
		t.Fatal("wrote = false, want true")
	}
	content, err := os.ReadFile("/tmp/task-" + slug + ".prompt.md")
	if err != nil {
		t.Fatalf("read prompt file: %v", err)
	}
	for _, want := range []string{"body-here", ".clorchestrate-done"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("prompt file missing %q:\n%s", want, content)
		}
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
