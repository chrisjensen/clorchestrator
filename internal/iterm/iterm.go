package iterm

import (
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
)

type TabOptions struct {
	TabColorHex string // "#RRGGBB" or "RRGGBB", may be empty
	RemoteCmd   string // the shell command the new tab will run
}

// WriteTabColor emits iTerm2 escape sequences to w to set the current
// terminal tab's background color. Does nothing if hex is empty or invalid.
func WriteTabColor(w io.Writer, hex string) {
	r, g, b, ok := hexToRGB(hex)
	if !ok {
		return
	}
	fmt.Fprintf(w, "\033]6;1;bg;red;brightness;%d\a", r)
	fmt.Fprintf(w, "\033]6;1;bg;green;brightness;%d\a", g)
	fmt.Fprintf(w, "\033]6;1;bg;blue;brightness;%d\a", b)
}

// OpenTab creates a new iTerm2 tab and runs RemoteCmd in it, optionally
// setting the tab's background color via iTerm2 escape sequences.
func OpenTab(opts TabOptions) error {
	script := BuildAppleScript(opts)
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, string(out))
	}
	return nil
}

func BuildAppleScript(opts TabOptions) string {
	colorCmd := ""
	if opts.TabColorHex != "" {
		if esc, ok := tabColorEscape(opts.TabColorHex); ok {
			colorCmd = esc + "; "
		}
	}
	// Escape double quotes and backslashes in the write text arg for AppleScript.
	inner := colorCmd + opts.RemoteCmd
	return fmt.Sprintf(`tell application "iTerm2"
  tell current window
    create tab with default profile
    tell current session of current tab
      write text "%s"
    end tell
  end tell
end tell`, appleScriptEscape(inner))
}

// tabColorEscape returns a `printf '\033]...'` command that sets the iTerm2
// tab background color. Empty-ok=false for invalid hex.
func tabColorEscape(hex string) (string, bool) {
	r, g, b, ok := hexToRGB(hex)
	if !ok {
		return "", false
	}
	return fmt.Sprintf(`printf '\033]6;1;bg;red;brightness;%d\a\033]6;1;bg;green;brightness;%d\a\033]6;1;bg;blue;brightness;%d\a'`, r, g, b), true
}

var hexRE = regexp.MustCompile(`^#?([0-9A-Fa-f]{6})$`)

func hexToRGB(hex string) (r, g, b uint8, ok bool) {
	m := hexRE.FindStringSubmatch(hex)
	if m == nil {
		return 0, 0, 0, false
	}
	h := m[1]
	rv, _ := strconv.ParseUint(h[0:2], 16, 8)
	gv, _ := strconv.ParseUint(h[2:4], 16, 8)
	bv, _ := strconv.ParseUint(h[4:6], 16, 8)
	return uint8(rv), uint8(gv), uint8(bv), true
}

// appleScriptEscape escapes a string for safe inclusion in an AppleScript
// double-quoted string literal. Both backslashes and double quotes must be
// doubled/escaped.
func appleScriptEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' || c == '"' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return string(out)
}
