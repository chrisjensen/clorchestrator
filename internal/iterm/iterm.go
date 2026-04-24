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
	FollowupCmd string // optional; typed once a shell prompt is detected after RemoteCmd
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
	firstLine := fmt.Sprintf(`      write text "%s"`, appleScriptEscape(colorCmd+opts.RemoteCmd))

	extra := ""
	if opts.FollowupCmd != "" {
		// Poll contents of the tab until something is waiting for input:
		//   1. Content has grown (SSH produced output — banner, MOTD, or prompt)
		//   2. Content has been stable for ~300ms (nothing printing right now)
		// Together these mean "there's a new prompt and we're idle." We
		// deliberately don't match specific prompt characters because PS1
		// styles vary and iTerm's "contents" can trim trailing whitespace.
		// Poll is 50ms for fast response; hard cap ~20s.
		extra = fmt.Sprintf(`
      set priorContent to (contents as text)
      set priorLen to (length of priorContent)
      set lastContent to priorContent
      set stableCount to 0
      set readyFound to false
      repeat 400 times
        delay 0.05
        set cs to (contents as text)
        if ((length of cs) > priorLen + 5) then
          if cs is equal to lastContent then
            set stableCount to stableCount + 1
            if stableCount is greater than or equal to 6 then
              set readyFound to true
              exit repeat
            end if
          else
            set stableCount to 0
            set lastContent to cs
          end if
        end if
      end repeat
      if not readyFound then
        delay 1
      end if
      write text "%s"`, appleScriptEscape(opts.FollowupCmd))
	}

	return fmt.Sprintf(`tell application "iTerm2"
  tell current window
    create tab with default profile
    tell current session of current tab
%s%s
    end tell
  end tell
end tell`, firstLine, extra)
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
