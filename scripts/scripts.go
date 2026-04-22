package scripts

import _ "embed"

//go:embed start-task.sh
var StartTask []byte

//go:embed worktree-checkout.sh
var WorktreeCheckout []byte

type Script struct {
	Name    string
	Content []byte
}

func All() []Script {
	return []Script{
		{Name: "start-task.sh", Content: StartTask},
		{Name: "worktree-checkout.sh", Content: WorktreeCheckout},
	}
}
