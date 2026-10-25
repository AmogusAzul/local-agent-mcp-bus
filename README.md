# local-agent-mcp-bus

Small, compiled stdio MCP servers that let Claude Desktop (or any MCP client) drive local automation in WSL — Claude Desktop plans, cheap local binaries act. No runtime dependencies, no networking, narrow tools for low token overhead.

## Layout

- `core/` — shared Go library that owns the MCP plumbing (stdio transport, schemas, responses).
- `tools/<name>/` — one independent Go module per tool, importing `core` through a local `replace`.
- `spec/` — short specs agreed before each phase is built.

## Tools

- `launch-claude-code` — opens a Windows Terminal tab (WSL `Ubuntu` profile) and starts Claude Code in `project`, with an optional `task` and raw CLI `args` (e.g. `--model opus`, `--continue`).
- `run-wsl-command` — runs a bash `command` in WSL and returns its output; killed after `timeout_seconds` (default 60).

Both run `bash -lc`, so your login `PATH` (e.g. `~/.local/bin`) applies.

## Build and install

```sh
make build-windows   # cross-compiles tools/* to bin/*.exe
make install         # copies them to C:\Users\<you>\tools and registers them in Claude Desktop
```

`make install` runs `register-claude-desktop.sh` (needs `jq`), which adds an `mcpServers` entry per tool to `claude_desktop_config.json`. It finds the file in `%LOCALAPPDATA%\Claude-Data\claude_*\` for the Microsoft Store build, or in `%APPDATA%\Claude\` otherwise. Quit Claude Desktop fully before installing, because it rewrites that file while running, and reopen it afterwards. Server logs are in `%LOCALAPPDATA%\Claude\logs\mcp-server-<tool>.log`.

## Adding a tool

Create `tools/<name>/` with a `go.mod` that points to `../../core` through a `replace` directive, and a `main.go` that calls `core.Serve(...)` with a `core.Tool`. Then run `make build-windows`.
