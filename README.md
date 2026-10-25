# local-agent-mcp-bus

Small, compiled stdio MCP servers that let Claude Desktop (or any MCP client) drive local automation in WSL — Claude Desktop plans, cheap local binaries act. No runtime dependencies, no networking, narrow tools for low token overhead.

## Layout

- `core/` — shared Go library that owns the MCP plumbing (stdio transport, schemas, responses).
- `tools/<name>/` — one independent Go module per tool, importing `core` through a local `replace`.
- `spec/` — short specs agreed before each phase is built.

Planned first tools: `launch-claude-code` (opens Claude Code in a new Windows Terminal WSL tab) and `run-wsl-command` (runs a bash command in WSL).
