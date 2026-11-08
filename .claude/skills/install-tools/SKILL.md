---
name: install-tools
description: Build and install this repo's MCP tools into the user's Claude Desktop (Windows, Microsoft Store build) from WSL - quit the app, run make install, relaunch it and verify the servers start. Use after changing any tool, when adding a tool, or when the user asks to install, reinstall or deploy the tools.
---

# Install the tools into Claude Desktop

Claude Desktop keeps the tool `.exe` files open and rewrites its config while it
runs, so it must be fully closed (tray too) before installing. Run everything
from the repo root in WSL.

## 1. Check before closing anything

- `go test ./...` and `go vet ./...` pass in every changed module (`core`, `tools/<name>`).
- For `google-tasks`: `local.env` at the repo root (untracked) defines
  `GOOGLE_TASKS_CLIENT_ID` and `GOOGLE_TASKS_CLIENT_SECRET`. Without it the build
  warns and the tool answers `NOT_CONFIGURED`. See "Google OAuth client" below.
- For `brightspace` (`external/brightspace.json`): the user must have done the one-time
  login in "Brightspace login" below. Without it the server starts but every call
  answers that no one is signed in. Never run that step yourself: it takes the
  user's Uniandes password and MFA approval.
- Closing Claude Desktop ends the user's open chats' tool servers. Tell the user
  before doing it unless they already asked for the reinstall.

## 2. Quit Claude Desktop

```sh
powershell.exe -NoProfile -Command 'Get-Process claude -ErrorAction SilentlyContinue | Stop-Process -Force; Start-Sleep 3; (Get-Process claude -ErrorAction SilentlyContinue | Measure-Object).Count'
```

Expect `0`. Closing the window isn't enough: the app stays in the tray.

## 3. Install

```sh
make install
```

This builds `bin/*.exe` (`build.sh`), copies them to `C:\Users\<win user>\tools`
and registers each tool in `claude_desktop_config.json`
(`register-claude-desktop.sh`, which finds `%LOCALAPPDATA%\Claude-Data\claude_*`).
`cp` fails (permission denied) if any tool `.exe` is still running. If so, stop those processes (step 2 with their names) and rerun.

Check the registration:

```sh
jq '.mcpServers | keys' "$(ls -d /mnt/c/Users/*/AppData/Local/Claude-Data/claude_*/claude_desktop_config.json | head -1)"
```

## 4. Relaunch and verify

```sh
explorer.exe 'shell:AppsFolder\Claude_pzs8sxrjxfjjc!Claude'
```

(`explorer.exe` may return a non-zero exit code even on success, so verify with the
next step instead. If the package family name ever changes, get it with `powershell.exe -NoProfile -Command '(Get-AppxPackage Claude).PackageFamilyName'`.)

After ~15 s each tool should be running (usually twice, one per app process):

```sh
powershell.exe -NoProfile -Command 'Get-Process | Where-Object { $_.Path -like "$env:USERPROFILE\tools\*" } | Select-Object -ExpandProperty ProcessName'
```

Then check a tool's log for `initialize` and `tools/list` without errors:

```sh
tail -n 20 /mnt/c/Users/<win user>/AppData/Local/Claude/logs/mcp-server-<tool>.log
```

`brightspace` is a Node process, not an `.exe` under `tools`, so it won't appear in that
list; check `mcp-server-brightspace.log` instead. (The first call may be slow while npx
unpacks the package.)

A tool missing from the process list, or an error in its log, means it crashed on
start. Run its `.exe` from WSL with a JSON-RPC `initialize` + `tools/list` on stdin
to see why (see AGENTS.md, "Testing").

## Brightspace login (one-time, the user runs it)

`brightspace` is upstream's `brightspace-mcp-server`, pinned in
`external/brightspace.json` (see `spec/brightspace.md`). Its login takes the user's
Uniandes password and MFA approval, so only the user does it, in a **Windows**
terminal (PowerShell), with the same version as the pin:

```powershell
npx -y brightspace-mcp-server@5.1.41 setup --url https://bloqueneon.uniandes.edu.co
```

- Answer **no** to every "connect to <app>" question. Upstream would register an
  unpinned `@latest` entry; our `make install` registers the pinned one.
- Pick the Microsoft/phone-approval options when asked. If the sign-in page isn't
  recognized, choose the option that opens the page so the user finishes it by hand.
- Then `npx -y brightspace-mcp-server@5.1.41 doctor` should pass.
- To bump the version, edit the pin in `external/brightspace.json` and here, rerun
  `make install`, and rerun the `setup` line so the new version's browser and cache
  are ready.
- The password lives in Windows Credential Manager and the session under
  `%USERPROFILE%\.d2l-session`. `npx -y brightspace-mcp-server@5.1.41 auth --logout`
  clears the session.

## Google OAuth client (one-time, for google-tasks)

The user does this once in the Google Cloud console (about 10 minutes, no billing
account needed). As far as we know, Google offers no command or API to create
this kind of client, so an agent can't do it.

1. console.cloud.google.com → create a project (e.g. `local-agent-mcp-bus`).
2. **APIs & Services → Library → Google Tasks API → Enable.**
3. **Google Auth Platform → Get started:** app name, support email, audience
   **External**, contact email, accept.
4. **Audience → Publish app** ("In production"), so the login doesn't expire. (In
   Testing mode you add yourself as a test user instead, but refresh tokens expire
   after 7 days and the browser login comes back weekly.)
5. **Clients → Create client → Desktop app**. Copy the client ID and secret.
6. Put them in `local.env` at the repo root (untracked; `build.sh` reads it):

   ```sh
   GOOGLE_TASKS_CLIENT_ID=...apps.googleusercontent.com
   GOOGLE_TASKS_CLIENT_SECRET=...
   ```

7. Reinstall (steps 2–4). The first `tasks_*` call opens the Google login in the
   browser. On "Google hasn't verified this app" click **Advanced → Go to …
   (unsafe) → Allow** (once). The token is saved to
   `%APPDATA%\local-agent-mcp-bus\google-tasks.json`; delete that file to log in
   again or switch accounts.

Limits: free, 50,000 requests/day per project. An unverified app is capped at 100
users, which is fine for personal use.
