# AGENTS.md

Guide for coding agents working in this repo. `README.md` is the user-facing
overview. This file is how to work here.

## What this is

Small, compiled **stdio MCP servers** (Go, cross-compiled to Windows `.exe`) that
let Claude Desktop on Windows drive local automation in WSL. Claude Desktop plans,
and the cheap local binaries act. Design values:

- **Token economy:** narrow, well-named tools, short descriptions, compact text
  output (not raw JSON), truncated long fields.
- **No runtime dependencies:** a single `.exe` per tool.
- **Local only:** `google-tasks` (HTTPS to Google) and `overleaf` (overleaf.com)
  are the exceptions that use the network.

## Layout

- `core/server.go`: the shared MCP plumbing over `github.com/mark3labs/mcp-go`.
  - Tools supply `core.Tool{Name, Description, Args, Reject, Execute}` and call
    `core.Serve(name, version, tools...)`.
  - Args are **strings only** (`core.Arg{Name, Description, Required}`). Booleans
    are `"true"`/`"false"`.
  - `Reject` maps argument names the tool doesn't declare to an error, so requests
    for unsupported features fail loudly.
  - An error from `Execute` becomes an MCP tool error (`isError`). Tools that
    return images or files set `Run` instead, returning `core.Result{Text, Blobs}`.
  - `core.RunCommand` runs a process and returns its combined output.
- `tools/<name>/`: one independent Go module per tool, with
  `replace github.com/AmogusAzul/local-agent-mcp-bus/core => ../../core`.
  - `launch-claude-code`: Windows Terminal tab + Claude Code in a WSL project.
  - `run-wsl-command`: runs `bash -lc` in WSL with a timeout.
  - `launch-zed`: opens a WSL project in Zed, with an optional pre-filled agent
    prompt.
  - `agent-status`: reads back the latest Claude/Codex turn from transcripts.
  - `google-tasks`: seven `tasks_*` tools over the Google Tasks API, with a
    browser OAuth login the server starts on its own.
  - `overleaf`: twelve `overleaf_*` tools over Overleaf's web endpoints (free
    plan, browser session cookie), with a local copy per project under
    `%TEMP%\lamb-overleaf\` and compiling on Overleaf.
- `spec/`: one short spec per change, agreed before building. Read the relevant
  one before touching a tool. They record findings and decisions the code doesn't
  explain.
- `build.sh` builds every `tools/*/` into `bin/<name>.exe`
  (`GOOS=windows GOARCH=amd64`). It sources the untracked `local.env` for build
  secrets.
- `register-claude-desktop.sh` upserts `mcpServers` entries in Claude Desktop's
  config (needs `jq`).
- `Makefile`: `build-windows`, `install` (build + copy to
  `C:\Users\<win user>\tools` + register).
- `references/`: local, git-excluded notes. Never commit them.

## Workflow

- **Spec first** for anything non-trivial. Write `spec/<topic>.md` in the house
  style (Problem, Decisions, Interface, Check, Out of scope, Implementation plan),
  get the user's reaction, and commit it on its own. Then implement.
- **Commits:** short imperative subjects, one logical change each, usually in
  pairs: `Add spec for <x>`, then `Add <x> tool` or the behaviour change. A body
  only when the subject isn't enough. No trailers. Keep history clean, with
  minimal unrelated changes.
- **README changes:** propose the wording to the user and apply only after they
  approve.
- **When the build differs from the spec**, update the spec in the same commit as
  the code.

## Build, test, install

- Go: the modules say `go 1.25.5`, and the local toolchain gets it through
  `GOTOOLCHAIN=auto`. Keep new modules and dependencies on 1.25. For example,
  `golang.org/x/oauth2` is pinned to v0.36.0 because v0.37 needs Go 1.26.
- Per module: `go vet ./...` and `go test ./...`. Tests must run on Linux, so put
  Windows-only code behind `_windows.go`/`_other.go` files (see `launch-zed`) or
  behind injectable functions.
- `make build-windows` builds all the `.exe` files.
- **Installing into Claude Desktop:** use the repo skill
  `.claude/skills/install-tools/SKILL.md` (quit the app, `make install`, relaunch,
  verify).

## Testing a built `.exe`

The `.exe` runs from WSL through interop. Pipe JSON-RPC lines into it:

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"<tool>","arguments":{}}}' \
  | timeout 20 ./bin/<tool>.exe
```

Stdout must carry only protocol messages. Logs go to stderr.

## Machine facts

- Windows user `sarfx`, which differs from the WSL user. That's why the Makefile
  asks `cmd.exe` for `%USERNAME%`. The WSL distro and Windows Terminal profile are
  both `Ubuntu`.
- Claude Desktop is the **Microsoft Store (MSIX)** build, package family
  `Claude_pzs8sxrjxfjjc`, app id `Claude`.
  - Live config: `%LOCALAPPDATA%\Claude-Data\claude_<id>\claude_desktop_config.json`.
    The old `Packages\...\LocalCache` path is ignored.
  - Server logs: `%LOCALAPPDATA%\Claude\logs\mcp-server-<tool>.log`.
  - The app rewrites its config and locks the tool `.exe` files while it runs, so
    quit it fully (tray too) before installing.
- `google-tasks`:
  - It needs `GOOGLE_TASKS_CLIENT_ID`/`GOOGLE_TASKS_CLIENT_SECRET` in `local.env`
    (a Google Cloud "Desktop app" OAuth client, consent screen "In production").
  - Its token is saved to `%APPDATA%\local-agent-mcp-bus\google-tasks.json`.
  - Without the client, the tool answers `NOT_CONFIGURED`.

## Conventions in tool code

- Tool errors are actionable text. `google-tasks` uses fixed codes:
  `CODE: message. Hint: …` (table in `spec/google-tasks.md`).
- WSL commands run through `wsl -e sh -c '<script>' _ <args...>`, or `bash -lc`
  where the login `PATH` matters. Values are passed as positional arguments
  rather than spliced into the script.
- Respect the ~60 s MCP client timeout. Waits are capped (`agent-status` 50 s,
  `google-tasks` login 30 s per call).
