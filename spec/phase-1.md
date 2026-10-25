# Phase 1: core library and WSL tools

## Goal

Claude Desktop on Windows gets two narrow MCP tools to drive WSL: one opens a Claude Code session in a new terminal tab, the other runs a bash command. Both are built on a shared `core` library, so later tools only have to write their own logic.

## Decisions

- Module path is `github.com/AmogusAzul/local-agent-mcp-bus/...`. Each tool depends on `core` through `replace => ../../core`.
- MCP SDK is `github.com/mark3labs/mcp-go`. Transport is stdio only, so stdout carries only protocol messages and logs go to stderr.
- Binaries are cross-compiled with `GOOS=windows GOARCH=amd64`.
- The WSL distro and the Windows Terminal profile are both named `Ubuntu`.
- The Windows user (`sarfx`) is not the WSL `$USER`, so `make install` looks up `%USERNAME%` through `cmd.exe` instead of using `$USER`.

## What changes

- **`core/server.go`**
  - Tool authors implement `Tool{Name, Description, Args, Execute}` and call `Serve(name, version, tools...)`.
  - `core` turns `Args` (string arguments, each optionally required) into input schemas and rejects calls that are missing a required argument.
  - When `Execute` returns an error, the caller gets an MCP error result; otherwise it gets a text result.
  - `RunCommand(ctx, name, args...)` returns combined output, and a non-zero exit becomes an error that includes the output.
- **`tools/launch-claude-code`**
  - Arguments: `project` and `task`, both required.
  - Runs `wt -w 0 new-tab --profile "Ubuntu" -- wsl -e bash -c "cd '<project>' && claude '<task>'"`.
  - Single quotes are escaped as `'\''` in both `task` and `project`.
  - Returns once the tab has launched.
- **`tools/run-wsl-command`**
  - Argument: `command`, required.
  - Runs `wsl -e bash -c <command>` and returns the output.
- **`build.sh`** loops over `tools/*/` and builds each one into `bin/<tool>.exe`.
- **`Makefile`**
  - `build-windows` runs `build.sh`.
  - `install` copies `bin/*.exe` to `/mnt/c/Users/$(WIN_USER)/tools/`; both `WIN_USER` and `INSTALL_DIR` can be overridden.
- **`README.md`** gets build, install and Claude Desktop config instructions.

## How we'll know it works

- `go vet` passes in every module, and `make build-windows` produces both `.exe` files.
- Piping JSON-RPC (`initialize`, `tools/list`, `tools/call`) into each `.exe` from WSL lists the tools with their schemas and keeps stdout limited to protocol messages.
- For `run-wsl-command`:
  - A command's output comes back.
  - A non-zero exit comes back as an error that includes the output.
  - A missing argument is rejected.
- `launch-claude-code` is checked by hand from Claude Desktop, because calling it opens a live session.

## Out of scope

- A `spawn-mcp-server` meta-tool, domain tool packages, a shared config file, and a coordinator layer.
- Editing the Claude Desktop config automatically.
