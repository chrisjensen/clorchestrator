# clorchestrate

Go CLI that orchestrates Claude coding sessions in screen sessions on remote servers via SSH.

## After making changes

Always rebuild the binary before testing:

```
go build -o clorchestrate .
```

The shell scripts in `scripts/` are embedded in the binary at build time. Without rebuilding, the server will continue receiving the old versions.

## Server-side PATH requirements

Setup runs over plain (non-tty) SSH and uses `bash -lc` for `post_setup_cmd` in
the background. Claude is launched in an interactive login shell inside screen.
Both paths require that login shells on the server have `npm`, `claude`, and
any post-setup tools on `PATH`. If nvm is in `~/.bashrc` only, non-interactive
login shells won't see it — add to `~/.bash_profile`:

```bash
export NVM_DIR="$HOME/.nvm"
[ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh"
export PATH="$HOME/.local/bin:$PATH"
```

Failures show up in `<worktree>/.setup.log` on the server — fix the profile
and re-run.
