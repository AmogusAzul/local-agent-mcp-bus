# Login shell, command timeout, and Claude Code arguments

## Symptom

From Claude Desktop, `run-wsl-command` with `which claude || find /home -name claude ...` took about 2.5 minutes. The Desktop log `mcp-server-run-wsl-command.log` shows the call at 03:14:42 and the result at 03:17:08. The model also had to fall back to `run-wsl-command` whenever it wanted to start Claude Code with any flags.

## Cause

- `wsl -e bash -c` starts a non-login, non-interactive shell that never reads `~/.profile`, so `~/.local/bin` (where `claude` lives) is missing from `PATH`.
- Because of that, `which claude` failed and the fallback `find /home` walked every `node_modules` directory and cache. That took 50 seconds when run directly.
- `launch-claude-code` uses the same kind of shell, so its `claude '<task>'` would fail with "command not found".
- Checked: `wsl -e bash -lc 'command -v claude'` prints `/home/amogus-azul/.local/bin/claude`.
- `launch-claude-code` only accepts `project` and `task`, so flags such as `--model`, `--permission-mode` or `--resume` can't be passed.

## Fix

- **Both tools run `bash -lc`** (a login shell), so the user's `PATH` and profile apply.
- **`run-wsl-command`: new optional `timeout_seconds` argument**, defaulting to 60.
  - The command runs as `wsl -e timeout -k 5 <n> bash -lc <command>`, so it is killed inside Linux; killing only `wsl.exe` may not stop the Linux process.
  - Exit code 124 becomes the error "timed out after Ns", followed by any partial output.
  - A Go context deadline of `n + 15s` is a backstop in case `wsl.exe` itself hangs.
- **`launch-claude-code`: new optional `args` argument**, holding raw Claude Code CLI flags (for example `--model opus --permission-mode plan`, or `--resume`).
  - It is inserted unquoted before the prompt, so it can carry several flags. This means bash interprets it, which is the same trust level as `run-wsl-command`.
  - `task` becomes optional, so `args: "--continue"` alone works. The resulting script is `cd '<project>' && claude <args> ['<task>']`.

## Check

- Locally, via JSON-RPC against the `.exe` files:
  - `command -v claude` returns the `~/.local/bin` path.
  - `sleep 5` with `timeout_seconds: "2"` returns the timeout error in about 2 seconds.
  - A normal command still returns its output.
  - `tools/list` shows the new optional arguments.
- After `make install` and restarting Claude Desktop, both servers connect in `mcp.log`.
- The launch itself is checked by hand from Desktop.

## Out of scope

- Structured flag arguments (separate `model`, `permission_mode` and so on); raw `args` covers them.
