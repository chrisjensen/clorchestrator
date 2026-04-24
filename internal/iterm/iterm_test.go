package iterm

import (
	"strings"
	"testing"
)

func TestHexToRGB_Valid(t *testing.T) {
	r, g, b, ok := hexToRGB("#f4b6cf")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if r != 0xf4 || g != 0xb6 || b != 0xcf {
		t.Errorf("got r=%x g=%x b=%x", r, g, b)
	}
}

func TestHexToRGB_NoHash(t *testing.T) {
	r, g, b, ok := hexToRGB("ff8800")
	if !ok || r != 0xff || g != 0x88 || b != 0x00 {
		t.Errorf("got ok=%v r=%x g=%x b=%x", ok, r, g, b)
	}
}

func TestHexToRGB_Invalid(t *testing.T) {
	if _, _, _, ok := hexToRGB("notahex"); ok {
		t.Error("expected ok=false for invalid hex")
	}
	if _, _, _, ok := hexToRGB("#12345"); ok {
		t.Error("expected ok=false for 5-char hex")
	}
}

func TestBuildAppleScript_WithColor(t *testing.T) {
	got := BuildAppleScript(TabOptions{
		TabColorHex: "#f4b6cf",
		RemoteCmd:   "ssh myserver",
	})
	if !strings.Contains(got, "iTerm2") {
		t.Errorf("missing iTerm2:\n%s", got)
	}
	if !strings.Contains(got, "create tab with default profile") {
		t.Errorf("missing create tab:\n%s", got)
	}
	if !strings.Contains(got, "printf") {
		t.Errorf("missing color printf:\n%s", got)
	}
	if !strings.Contains(got, "ssh myserver") {
		t.Errorf("missing remote command:\n%s", got)
	}
}

func TestBuildAppleScript_NoColor(t *testing.T) {
	got := BuildAppleScript(TabOptions{
		RemoteCmd: "ssh myserver",
	})
	if strings.Contains(got, "printf") {
		t.Errorf("should not contain printf color cmd:\n%s", got)
	}
	if !strings.Contains(got, "ssh myserver") {
		t.Errorf("missing remote cmd:\n%s", got)
	}
}

func TestBuildAppleScript_WithFollowup(t *testing.T) {
	got := BuildAppleScript(TabOptions{
		RemoteCmd:   "ssh myserver",
		FollowupCmd: `claude "hi"`,
	})
	if !strings.Contains(got, "ssh myserver") {
		t.Errorf("missing remote cmd:\n%s", got)
	}
	if !strings.Contains(got, "priorContent") {
		t.Errorf("expected contents-polling loop:\n%s", got)
	}
	if !strings.Contains(got, "stableCount") {
		t.Errorf("expected stability-based polling:\n%s", got)
	}
	if !strings.Contains(got, `claude \"hi\"`) {
		t.Errorf("missing escaped followup:\n%s", got)
	}
}

func TestBuildAppleScript_NoFollowup(t *testing.T) {
	got := BuildAppleScript(TabOptions{RemoteCmd: "ssh myserver"})
	if strings.Contains(got, "priorContent") {
		t.Errorf("should not contain polling loop when FollowupCmd empty:\n%s", got)
	}
}

func TestAppleScriptEscape(t *testing.T) {
	got := appleScriptEscape(`say "hello" \n`)
	want := `say \"hello\" \\n`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
