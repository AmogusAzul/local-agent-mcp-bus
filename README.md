# local-agent-mcp-bus

Small, compiled stdio MCP servers that let Claude Desktop (or any MCP client) drive local automation in WSL — Claude Desktop plans, cheap local binaries act. No runtime dependencies, local by default (`google-tasks` and `overleaf` use the network), narrow tools for low token overhead.

## Layout

- `core/` — shared Go library that owns the MCP plumbing (stdio transport, schemas, responses).
- `tools/<name>/` — one independent Go module per tool, importing `core` through a local `replace`.
- `spec/` — short specs agreed before each phase is built.

## Tools

- `launch-claude-code` — opens a Windows Terminal tab (WSL `Ubuntu` profile) and starts Claude Code in `project`, with an optional `task`, `model` (omit to use the Claude Code default) and raw CLI `args` (e.g. `--continue`). It checks `claude` and the project headlessly first, so failures come back to the caller.
- `run-wsl-command` — runs a bash `command` in WSL and returns its output; killed after `timeout_seconds` (default 60).
- `launch-zed` — opens `project` in Zed (Windows app, WSL remote) as a tab of an existing WSL window, creating the directory if missing, and pre-fills an optional `task` in a new Agent Panel thread via `zed://agent?prompt=`. Zed never auto-sends it: the user reviews and presses Enter. Needs `zed` (Zed's CLI) on the Windows `PATH`. An optional `file` (relative, no `:line`, which Zed 1.25.3 mangles for WSL) is opened inside the project. Takes ~3–5 s; without `file` or `task` it only opens the project. See `spec/launch-zed.md`.
- `agent-status` — reports the latest Claude or Codex turn in `project` (from Zed or `launch-claude-code`): agent, `done`/`running`, the prompt and the final reply, truncated. `wait_seconds` (max 50) waits for the next turn to finish.
- `overleaf` — creates, edits and compiles Overleaf projects (free plan) through the web app's own endpoints, with the session cookie from the browser (`overleaf_login`). Files are edited in a local copy under `%TEMP%\lamb-overleaf\` and pushed; compiling happens on Overleaf. This goes against Overleaf's terms, so use it at your own risk. See `spec/overleaf.md`.

`launch-claude-code` and `run-wsl-command` run `bash -lc`, so your login `PATH` (e.g. `~/.local/bin`) applies.

## Build and install

```sh
make build-windows   # cross-compiles tools/* to bin/*.exe
make install         # copies them to C:\Users\<you>\tools and registers them in Claude Desktop
```

`make install` runs `register-claude-desktop.sh` (needs `jq`), which adds an `mcpServers` entry per tool to `claude_desktop_config.json`. It finds the file in `%LOCALAPPDATA%\Claude-Data\claude_*\` for the Microsoft Store build, or in `%APPDATA%\Claude\` otherwise. Quit Claude Desktop fully before installing, because it rewrites that file while running, and reopen it afterwards. Server logs are in `%LOCALAPPDATA%\Claude\logs\mcp-server-<tool>.log`. Each entry looks like:

```json
"launch-zed": { "command": "C:\\Users\\<you>\\tools\\launch-zed.exe" }
```

## Adding a tool

Create `tools/<name>/` with a `go.mod` that points to `../../core` through a `replace` directive, and a `main.go` that calls `core.Serve(...)` with a `core.Tool`. Then run `make build-windows`.
