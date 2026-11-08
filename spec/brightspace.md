# `brightspace`: reach Uniandes Brightspace from Desktop

From the Google Task "local-agent-mcp-bus: add brightspace-mcp-server" (due
2026-11-08). Goal from the task: reach Brightspace without the university having to
do anything.

## Problem

Desktop can't see courses, due dates, grades or announcements in Bloque Neón
(`https://bloqueneon.uniandes.edu.co`). Uniandes won't register an API app, so the
official D2L API with OAuth is out. The task names
[RohanMuppa/brightspace-mcp-server](https://github.com/RohanMuppa/brightspace-mcp-server),
a Node MCP server that needs no help from the school.

Findings (researched 2026-11-04, re-checked against npm and the package source on
2026-11-07; nothing has been run):

- **Uniandes login:** `/d2l/login` goes to SAML, then to `login.microsoftonline.com`
  (Microsoft Entra). The repo doesn't name Uniandes. An unknown school falls back to
  its Purdue flow, which handles Entra and number-match MFA. Its start step is
  Purdue-specific, so it's unproven here.
- **How it signs in:** headless Playwright Chromium does the real SSO login and keeps
  the D2L session cookies. It mints a bearer token from them and renews it without the
  browser.
  - The password goes in the OS keychain (Windows Credential Manager).
  - The session goes in AES-GCM files under `~/.d2l-session`.
  - `setup` is a wizard; there are also `auth`, `doctor` and `http`.
  - If the browser login fails, there's a manual visible window (`D2L_HEADLESS=false`)
    or a pasted `D2L_SESSION_COOKIE`.
- **Tools:** 28, from `get_my_courses` and `get_upcoming_due_dates` to
  `get_video_transcript` and `download_file`. None writes to Brightspace. A few change
  local state: `sign_in` (starts a login, which can open a browser window), `sign_out`
  (deletes the saved session) and the download tools (write files).
  - Every tool returns raw `JSON.stringify` output, and the descriptions are long.
    That's the opposite of this repo's token economy.
- **Runtime:** Node ≥ 22 and a Chromium download of about 150 MB at install. Windows
  has Node v24.19.0; WSL has only v18.
  - In WSL, the keychain (no Secret Service) and the visible login are problems.
- **Risk:**
  - One maintainer, with a release almost every day (5.1.41 on 2026-11-06). Every
    release would run with the user's Uniandes SSO password.
  - The docs say `npx …@latest`, and that is what its `setup` writes into client
    configs, so each client restart fetches whatever is newest. That is the real
    exposure. The server itself doesn't install updates: with the check on, it looks
    at the npm registry, shows a notice and deletes old npx cache copies of the
    package (`update-checker.js`). Only a user-run `auth` re-executes through
    `@latest` (`self-update.js`). `D2L_NO_UPDATE_CHECK` turns all of that off.
  - Verified: MIT licence; 5.1.41 carries npm SLSA provenance; the only network
    hosts in the code are the school, `login.microsoftonline.com`-style SSO pages,
    the npm registry, and YouTube (transcripts). No telemetry found.
  - Release notes: there are no GitHub releases after v3.9.7, so the commit log is
    the only record. Nothing between 5.1.38 and 5.1.41 touches auth beyond a new
    Memorial University sign-in flow (`mun-sso.js`) and clearer error messages for
    `get_syllabus` and `get_assignment_rubric`. Earlier hardening exists (a cap on
    ZIP inflation in attachment text extraction, stricter course-file link checks).
- **Its `setup` registers itself in Claude Desktop, Cursor, Codex and others** and,
  in the wizard, asks per app. For Desktop it writes
  `%APPDATA%\Claude\claude_desktop_config.json`, which the MSIX build ignores (see
  `claude-desktop-config.md`); other apps get an `@latest` entry. Its flags:
  `--url`, `--username`, `--replace`, `--prepare` (registers every app it finds
  without asking), `--sign-in`. There is no flag to skip registration, so the wizard
  answers must be "no".
- **Settings:** `setup` saves the URL and username to `~/.brightspace-mcp/config.json`
  and the password to Credential Manager; `D2L_BASE_URL` overrides the saved URL.
  The saved session is keyed by URL and username, so `setup` and the server must run
  as the same Windows user.

## Decisions

- **Use it as is, not reimplemented in Go.** The SSO, MFA and token-minting work is
  the hard part, and it's already done. A Go rewrite isn't worth it until we know
  what's used.
- **Run it natively on Windows**, not through WSL: Node 24 is there, Credential
  Manager works, and Chromium runs without WSLg.
- **Pinned, no update notices.** Pin the exact version `brightspace-mcp-server@5.1.41`
  (checked: it exists, it is the latest on npm, and its diff against 5.1.38 is
  benign) and set `D2L_NO_UPDATE_CHECK=1`. Bumping it is a deliberate edit in this
  repo. Run `setup` with the same pinned version, so the npx cache and the Chromium
  download are already there when Desktop starts the server (the first launch would
  otherwise download them inside the ~60 s MCP client timeout).
- **Registered by our script, not by its wizard.** Run `setup` once from a Windows
  terminal for the URL, credentials and MFA, and decline its client registration.
  `register-claude-desktop.sh` gets a second source: `external/*.json`, one file per
  non-Go server holding its `mcpServers` entry. They're merged with the same jq upsert,
  keyed by file name. The command is `cmd /c npx`, the form upstream documents for
  Windows (a bare `npx.cmd` may not spawn). `external/brightspace.json`:

  ```json
  { "command": "cmd",
    "args": ["/c", "npx", "-y", "brightspace-mcp-server@5.1.41"],
    "env": { "D2L_BASE_URL": "https://bloqueneon.uniandes.edu.co",
             "D2L_NO_UPDATE_CHECK": "1", "D2L_CURRENT_ONLY": "true" } }
  ```

  It holds no secrets, so it's committed.
- **Token cost is accepted for now and measured.** Count the `tools/list` size and a
  typical `get_upcoming_due_dates` reply.
  - If either is heavy, a follow-up spec adds a thin Go proxy (`brightspace`) that
    passes through 5–6 tools with short descriptions and compacts the JSON to text.
  - That proxy can be added later without changing the login setup.

## Interface

None of ours. Desktop sees the upstream tools under the server name `brightspace`.

## Check

- Checked 2026-11-07: the pinned version and the three env vars exist
  (`D2L_BASE_URL`, `D2L_NO_UPDATE_CHECK`, `D2L_CURRENT_ONLY`); the last takes
  `true`/`false`, and `D2L_NO_UPDATE_CHECK` any non-empty value.
- `register-claude-desktop.sh` against a temporary config: the entry is added, other
  servers and keys stay, a second run changes nothing.
- `cmd /c npx -y brightspace-mcp-server@5.1.41 setup --url https://bloqueneon.uniandes.edu.co`
  on Windows (the user runs it): the Uniandes SSO login succeeds headless (or else
  with `D2L_HEADLESS=false`, or the manual window), and MFA approval works.
- `doctor` passes.
- Through Desktop after `make install`:
  - `get_my_courses` lists the current semester;
  - `get_upcoming_due_dates` matches Bloque Neón;
  - a second chat reuses the session without a login.
- No `claude_desktop_config.json` appears under `%APPDATA%\Claude`.
- Record the `tools/list` and reply sizes in this spec.

## Out of scope

- Writing to Brightspace (submissions, posts). Upstream is read-only anyway.
- Syncing due dates into Google Tasks. Desktop can chain the two tools itself.
- Instructor tools (`get_roster`, `get_dropbox_*`).

## Open questions

- Is the user OK storing their Uniandes SSO password in Windows Credential Manager
  for this? The alternative is the cookie mode, which needs re-pasting whenever the
  session expires.
- Is the 5–6-tool proxy worth doing now instead of measuring first?
- Does the Purdue-style flow cope with Uniandes? Only the user's `setup` can tell.
  If not, the fallback is the manual window, which then needs `D2L_HEADLESS=false`
  on every re-login, or a Uniandes flow contributed upstream.

## Implementation plan

- `external/brightspace.json` as above.
- `register-claude-desktop.sh`: merge `external/*.json` after the Go tools, and a line
  in `spec/claude-desktop-config.md`.
- `.claude/skills/install-tools/SKILL.md`: a one-time `setup` step for Brightspace.
- README: tool entry and the pinned-version note. I'll propose the wording.
