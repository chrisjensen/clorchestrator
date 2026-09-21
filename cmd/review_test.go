package cmd

import (
	"reflect"
	"testing"

	"github.com/chrisjensen/clorchestrate/internal/config"
)

func TestTaskKeyOf(t *testing.T) {
	cases := []struct {
		name     string
		worktree string
		label    string
		want     string
	}{
		{"strips trailing label", "myrepo-task1-claude", "claude", "myrepo-task1"},
		{"label not a suffix leaves worktree unchanged", "myrepo-task1-claude", "gpt", "myrepo-task1-claude"},
		{"empty label strips trailing dash", "myrepo-task1-", "", "myrepo-task1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := taskKeyOf(c.worktree, c.label); got != c.want {
				t.Errorf("taskKeyOf(%q, %q) = %q, want %q", c.worktree, c.label, got, c.want)
			}
		})
	}
}

func TestFindJSONBounds(t *testing.T) {
	cases := []struct {
		name      string
		s         string
		wantStart int
		wantEnd   int
		wantOK    bool
	}{
		{"simple object", `{"a":1}`, 0, 7, true},
		{"prose before and after", `hello {"a":1} world`, 6, 13, true},
		{"nested braces", `{"a":{"b":1}}`, 0, 13, true},
		{"brace inside string ignored", `{"a":"}"}`, 0, 9, true},
		{"escaped quote inside string", `{"a":"\"}"}`, 0, 11, true},
		{"no object", `no json here`, 0, 0, false},
		{"unterminated object", `{"a":1`, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end, ok := findJSONBounds(c.s)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if start != c.wantStart || end != c.wantEnd {
				t.Errorf("bounds = (%d, %d), want (%d, %d); substr=%q", start, end, c.wantStart, c.wantEnd, c.s[start:end])
			}
		})
	}
}

func TestExtractJSONAndProse(t *testing.T) {
	t.Run("bare json no prose", func(t *testing.T) {
		jsonStr, prose, err := extractJSONAndProse(`{"problem":"x"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if jsonStr != `{"problem":"x"}` {
			t.Errorf("jsonStr = %q", jsonStr)
		}
		if prose != "" {
			t.Errorf("prose = %q, want empty", prose)
		}
	})

	t.Run("json with trailing prose no fence", func(t *testing.T) {
		jsonStr, prose, err := extractJSONAndProse(`{"problem":"x"}` + "\ntrailing notes")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if jsonStr != `{"problem":"x"}` {
			t.Errorf("jsonStr = %q", jsonStr)
		}
		if prose != "trailing notes" {
			t.Errorf("prose = %q", prose)
		}
	})

	t.Run("fenced json with surrounding prose", func(t *testing.T) {
		input := "Here is my evaluation:\n```json\n{\"problem\":\"x\"}\n```\nThanks for reading."
		jsonStr, prose, err := extractJSONAndProse(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if jsonStr != `{"problem":"x"}` {
			t.Errorf("jsonStr = %q", jsonStr)
		}
		if prose != "Here is my evaluation:\n\nThanks for reading." {
			t.Errorf("prose = %q", prose)
		}
	})

	t.Run("no json found", func(t *testing.T) {
		_, _, err := extractJSONAndProse("just some prose, no braces at all")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		_, _, err := extractJSONAndProse(`{"a": }`)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestSplitAspects(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want []string
	}{
		{"comma separated", "Foo, Bar,baz", []string{"foo", "bar", "baz"}},
		{"empty string", "", nil},
		{"blank entries dropped", "foo,, bar , ", []string{"foo", "bar"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitAspects(c.s)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("splitAspects(%q) = %#v, want %#v", c.s, got, c.want)
			}
		})
	}
}

func TestComputeCompleteness(t *testing.T) {
	t.Run("scores each row independently", func(t *testing.T) {
		rows := []tokenTotals{
			{eval: evalJSON{AllAspects: "a,b,c,d", Missing: "a"}},
			{eval: evalJSON{AllAspects: "x,y", Missing: ""}},
		}
		computeCompleteness(rows, []int{0, 1})

		if rows[0].complete != 75 || rows[0].aspectCount != 4 {
			t.Errorf("row0 = complete=%d aspectCount=%d, want 75/4", rows[0].complete, rows[0].aspectCount)
		}
		if rows[1].complete != 100 || rows[1].aspectCount != 2 {
			t.Errorf("row1 = complete=%d aspectCount=%d, want 100/2", rows[1].complete, rows[1].aspectCount)
		}
	})

	t.Run("zero all_aspects leaves row untouched", func(t *testing.T) {
		rows := []tokenTotals{{complete: -1, aspectCount: -1}}
		computeCompleteness(rows, []int{0})
		if rows[0].complete != -1 || rows[0].aspectCount != -1 {
			t.Errorf("row should be untouched, got complete=%d aspectCount=%d", rows[0].complete, rows[0].aspectCount)
		}
	})

	t.Run("missing count cannot exceed all_aspects", func(t *testing.T) {
		rows := []tokenTotals{{eval: evalJSON{AllAspects: "a", Missing: "a,b,c"}}}
		computeCompleteness(rows, []int{0})
		if rows[0].complete != 0 {
			t.Errorf("complete = %d, want clamped to 0", rows[0].complete)
		}
	})
}

func TestExpandHome(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		homeDir string
		want    string
	}{
		{"leading tilde slash expanded", "~/src/repo", "/home/user", "/home/user/src/repo"},
		{"bare tilde expanded", "~", "/home/user", "/home/user"},
		{"absolute path unchanged", "/abs/path", "/home/user", "/abs/path"},
		{"tilde mid-string unchanged", "/foo/~/bar", "/home/user", "/foo/~/bar"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := expandHome(c.path, c.homeDir); got != c.want {
				t.Errorf("expandHome(%q, %q) = %q, want %q", c.path, c.homeDir, got, c.want)
			}
		})
	}
}

func TestClaudeProjectsDir(t *testing.T) {
	got := claudeProjectsDir("/home/user", "/home/user/src/my-repo")
	want := "/home/user/.claude/projects/-home-user-src-my-repo"
	if got != want {
		t.Errorf("claudeProjectsDir = %q, want %q", got, want)
	}
}

func TestIsRetryableAPIError(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"529 overloaded is retryable", "some text\nAPI Error: 529 overloaded\nmore text", true},
		{"auth error is not retryable", "API Error: 401 unauthorized", false},
		{"empty output is not retryable", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryableAPIError(c.output); got != c.want {
				t.Errorf("isRetryableAPIError(%q) = %v, want %v", c.output, got, c.want)
			}
		})
	}
}

func TestEvalResultFile(t *testing.T) {
	if got, want := evalResultFile("myworktree"), "/tmp/clorchestrate-eval-myworktree.out"; got != want {
		t.Errorf("evalResultFile = %q, want %q", got, want)
	}
}

func TestEvalScreenName(t *testing.T) {
	if got, want := evalScreenName("myworktree"), "clorchestrate-eval-myworktree"; got != want {
		t.Errorf("evalScreenName = %q, want %q", got, want)
	}
}

func TestWorktreeDoneLocal(t *testing.T) {
	dir := t.TempDir()

	t.Run("not done without markers", func(t *testing.T) {
		w := discoveredWorktree{dir: dir + "/nope"}
		if worktreeDone("", w, "claude") {
			t.Error("expected false, got true")
		}
	})
}

func TestMarkIncompleteGroups(t *testing.T) {
	t.Run("no missing siblings leaves rows untouched", func(t *testing.T) {
		rows := []tokenTotals{{taskKey: "task1", label: "claude", server: "", parent: "/nowhere"}}
		scanCfg := &config.Config{Commands: []config.Command{{Label: "claude"}}}
		markIncompleteGroups(rows, scanCfg, false)
		if rows[0].skipEval {
			t.Error("expected skipEval=false when there are no sibling labels")
		}
	})
}
