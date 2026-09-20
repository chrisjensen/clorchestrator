package scripts

import _ "embed"

//go:embed worktree-checkout.sh
var WorktreeCheckout []byte

//go:embed hive-signal.sh
var HiveSignal []byte

type Script struct {
	Name    string
	Content []byte
	// Dir is the target directory on the server ("" means ~/bin). hive-signal.sh
	// goes to ~/.local/bin so hive skills can invoke it bare (that dir is on the
	// login-shell PATH per the project's server PATH requirements).
	Dir string
}

func All() []Script {
	return []Script{
		{Name: "worktree-checkout.sh", Content: WorktreeCheckout},
		{Name: "hive-signal.sh", Content: HiveSignal, Dir: "~/.local/bin"},
	}
}
