package scripts

import _ "embed"

//go:embed worktree-checkout.sh
var WorktreeCheckout []byte

type Script struct {
	Name    string
	Content []byte
}

func All() []Script {
	return []Script{
		{Name: "worktree-checkout.sh", Content: WorktreeCheckout},
	}
}
