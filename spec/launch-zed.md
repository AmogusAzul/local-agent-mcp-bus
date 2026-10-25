# `launch-zed`: open a project in Zed with a pre-filled agent prompt

## Problem

`launch-claude-code` needs a terminal tab and a full Claude Code session. Sometimes it is enough to open the project in Zed with the task already typed into the Agent Panel, so the user can read it and send it with one keypress.

## Constraints

- **No CLI flag for the agent.** `zed --help` (1.25.3) only opens files and projects. A PR adding `zed --agent` (zed-industries/zed#62060) was closed by the Zed team, so that route is not available.
- **`zed://agent?prompt=…` is the only entry point.** It opens a new thread in the Agent Panel and puts the prompt in the message editor, under a "Review Before Sending" callout. It never sends the prompt. Any web page can open a `zed://` link, so Zed treats the caller as untrusted.
- **So the tool is one keypress short of automated, on purpose.** The human reads the prompt and presses Enter. The tool does not try to get around this, for example by injecting keystrokes.

## Findings

These were checked against the Zed source at the installed commit (`8dd255c`) and with live runs on this machine.

- **How to trigger the URL.** `zed.exe` (the CLI) treats any argument that starts with `zed://` as a URL and passes it to the running Zed over its own IPC. The registered `zed://` handler (`Zed.exe "%1"`) does the same. So the tool runs `zed <url>` directly. It does not use `cmd /c start`, which would need escaping for `&`, `%` and `^`, and it does not use PowerShell, which adds about a second to start.
- **Encoding.** Zed reads the query with `form_urlencoded` and uses only `prompt`. Go's `url.Values.Encode()` produces exactly that format. Zed then cleans up the prompt: it changes `\r\n` to `\n`, keeps at most two blank lines in a row, and drops control and bidi characters. A prompt that is empty after this opens the panel with no text. Bash quoting (`quote()`) does not apply here.
- **Only `prompt` is accepted.** The URL has no parameter for a project, a thread or an agent. The prompt goes into whichever window Zed has active (otherwise the first workspace window), and it opens a new thread with the agent last selected in that window.
- **The project needs its own call, and that call has to come first.** One open request holds a single "kind". When it is `AgentPanel`, Zed ignores any paths in the same request. So the tool makes two calls: `zed --wsl <user>@<distro> <path>`, and then `zed zed://agent?…`.
- **Timing matters.** The CLI returns as soon as Zed accepts the request, before the WSL project has connected. When the URL was sent about 0.1 s after the new window appeared, the agent thread opened but the prompt was lost. When it was sent 2 s after the window took focus, the prompt arrived intact. A cold open took about 2.5 s until the window took focus.
- **Which window.** The user's setting `"cli_default_open_behavior": "new_window"` gives every launch its own window. `-e/--existing` would open the project as a sidebar workspace of the current window, but only for local projects: `open_remote_project` reuses a window only when `requesting_window` is set, and only the hidden `-r` flag sets it. `-r` picks the first window that already holds a workspace at the same WSL location. With AI enabled, Zed keeps that window's previous workspaces in the sidebar instead of replacing them (checked in the workspace DB: three projects shared one `window_id`). Running `-r` again on a project that is already open does not duplicate it. If no WSL window exists, Zed opens a new one.
- **Connection identity.** Zed treats `--wsl Ubuntu` and `--wsl amogus-azul@Ubuntu` as different locations. A project you opened from Zed's UI without a user can therefore appear twice in the sidebar. Most existing workspaces and threads use the explicit user, so the tool sends `user@distro`.
- **No line positions.** In Zed 1.25.3 on Windows, a WSL path with `:line` or `:line:col` (with `-r`, `-a`, and with or without the project in the same call) becomes a mangled `project\file` path, with a Windows backslash, and Zed adds it as an empty new project root, sometimes in another window. So `file` refuses any `:N` suffix, and the preflight checks that the file exists.
- **No path translation.** `--wsl` takes the Linux path (the CLI runs `realpath` inside the distro). The agent URL carries no path. A UNC path would open the project as a local Windows folder, with language servers and agent tools running on the Windows side. That is not what we want.

## Approach

`launch-zed` takes `project` (required, an absolute WSL path, created if missing), `file` (optional, relative to the project) and `task` (optional).

1. **Preflight.** Run `wsl -e sh -c` with the project as a positional argument and a 30-second limit. It rejects relative paths. Without that check, `wsl` would resolve a relative path against the Windows working directory and create folders there. If the project is missing, it runs `mkdir -p` and prints `created`, so agents can start new projects. It then prints `$(id -un)@$WSL_DISTRO_NAME`. That value becomes the `--wsl` target, so neither the user nor the distro is hardcoded.
2. Run `zed -r --wsl <target> <project>`. This opens the project as a new tab in the sidebar of an existing WSL window, or focuses it if it is already open there.
   If both `file` and `task` are empty, stop here and return `Opened <project> in Zed.`
3. **Wait for the window.** Every 100 ms, read the title of the foreground window through `user32` (a Windows-only file, no PowerShell). Stop when the title is the project's base name, either alone or followed by ` - `. If that doesn't happen within 30 s, return an error and do not send the prompt to a window that might be the wrong one.
4. Wait 2 s so the remote project can finish connecting.
   If `file` is set, run `zed -a --wsl <target> <project>/<file>`. `-a` opens the file in the workspace that holds it. With `-r`, the file would instead become a separate workspace of its own.
5. Run `zed "zed://agent?" + url.Values{"prompt": {task}}.Encode()`.
6. Return `Opened` (or `Created and opened`) `<project> in Zed; the prompt is pre-filled and the user must review it and press Enter to send it.` That way the calling model knows nothing has run yet.

A launch takes about 3 to 5 s. Every step after the first call is a Windows call, like the `wt` call in `launch-claude-code`.

## Open questions (resolved or accepted)

- *Windows trigger:* resolved. Call the `zed` CLI with the URL.
- *URL encoding:* resolved. Use `url.Values`. Windows limits a command line to about 32 K characters, and percent-encoding can triple the size of non-ASCII text. A prompt longer than that makes the tool return an error. That is acceptable for task prompts.
- *Separate project open, and ordering:* resolved. Open the project first, then wait for its window to take focus, then wait 2 s.
- *Targeting parameters:* there are none besides `prompt`.
- *Path translation:* not needed. Use `--wsl` with the Linux path.
- *Accepted risk:* if the user switches focus to another Zed window during the 2 s wait, the prompt goes to that window.
- *Accepted risk:* `-r` is hidden from `--help` and could change in a Zed update. If it does, the fallback is the default behavior, which opens a new window.
- *Accepted risk:* a typo in `project` creates a stray directory. The reply says `Created`, so the calling model can notice.
- *Accepted risk:* the 2 s wait comes from measurements. A much slower remote connection could still lose the prompt. Zed shows no sign that the connection is ready.

## Implementation plan

- `tools/launch-zed/`: `go.mod` with the `core` replace, `main.go` (preflight, the two `zed` calls, the agent URL builder), `foreground_windows.go` (user32 title lookup), `foreground_other.go` (a stub so `go test` runs on Linux), and `main_test.go`.
- Reuse `core.RunCommand` for the `zed` calls. Nothing is shared with `launch-claude-code` beyond `core`, because the two tools quote and invoke things in different ways.
- Tests:
  - the URL survives a round trip through `url.ParseQuery` for spaces, `"`, `'`, `&`, `#`, `%`, `+`, newlines and non-ASCII text;
  - the title matcher accepts `name` and `name - file` but rejects `name-other`.
- Through the `.exe`: a missing project returns an error, and a real launch is checked with a screenshot of the Zed window.
- README: add the tool to the Tools list, and add a Claude Desktop config example.

## Out of scope

- Sending the prompt automatically.
- Choosing the agent or model. The URL can't do either.
- Native Windows projects (non-WSL).
