package cmd

import (
	"fmt"
	"regexp"
)

// safeNameRE is the charset allowed in user-supplied handles and branch
// names. These values are interpolated into ssh/screen command strings
// (session names, /tmp/task-<slug>.* paths, worktree-checkout.sh args)
// without shell escaping, so anything outside [A-Za-z0-9._-] is rejected at
// entry. benchmarkLabel/commandLabel need no validation: they are always
// config-defined [[command]] labels resolved via ResolveCommand.
var safeNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)

// validateSafeName rejects kind ("handle"/"branch") containing characters
// outside [A-Za-z0-9._-]. Empty allowed (both are optional).
func validateSafeName(kind, value string) error {
	if safeNameRE.MatchString(value) {
		return nil
	}
	return fmt.Errorf("invalid %s %q: only letters, digits, '.', '_' and '-' allowed (no spaces or shell characters)", kind, value)
}
