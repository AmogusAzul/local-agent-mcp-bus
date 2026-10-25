# local-agent-mcp-bus

Small, compiled stdio MCP servers that let Claude Desktop (or any MCP client) drive local automation in WSL — Claude Desktop plans, cheap local binaries act. No runtime dependencies, no networking, narrow tools for low token overhead.

## Layout

- `core/` — shared Go library that owns the MCP plumbing (stdio transport, schemas, responses).
- `tools/<name>/` — one independent Go module per tool, importing `core` through a local `replace`.
- `spec/` — short specs agreed before each phase is built.

## Tools

- `launch-claude-code` — opens a Windows Terminal tab (WSL `Ubuntu` profile) and starts Claude Code in `project` with `task`.
- `run-wsl-command` — runs a bash `command` in WSL and returns its output.

## Build and install

```sh
make build-windows   # cross-compiles tools/* to bin/*.exe
make install         # copies them to C:\Users\<you>\tools (override WIN_USER / INSTALL_DIR if needed)
```

Then add one entry per tool to `%APPDATA%\Claude\claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "launch-claude-code": { "command": "C:\\Users\\<you>\\tools\\launch-claude-code.exe" },
    "run-wsl-command": { "command": "C:\\Users\\<you>\\tools\\run-wsl-command.exe" }
  }
}
```

## Adding a tool

Create `tools/<name>/` with a `go.mod` that points to `../../core` through a `replace` directive, and a `main.go` that calls `core.Serve(...)` with a `core.Tool`. Then run `make build-windows`.
