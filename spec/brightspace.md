# `brightspace`: read and submit work in Brightspace (D2L) from an MCP client

## Problem

An MCP client such as Claude Desktop can't see or act on a student's Brightspace
(D2L): courses, due dates, grades, announcements, course files, assignment
submissions. The motivating deployment is Uniandes' Bloque Neón
(`https://bloqueneon.uniandes.edu.co`), where:

- The university doesn't register third-party API apps, so the official OAuth route
  isn't available.
- The web session cookies `d2lSessionVal` and `d2lSecureSessionVal` are session-only
  and stop working after about 3 hours, so anything built on a pasted cookie or token
  breaks within the day.
- Logins go through SAML to Microsoft Entra, with MFA.

The tool should work without registering an app, without handling the user's
password, and without asking for a login every few hours. It should follow the rest of
this repo: a standalone Go binary, few narrow tools, compact output.

## Findings

Checked against Bloque Neón on 2026-11-07, on a Windows PC with the university work
account signed in.

- **Where the single sign-on comes from.** When Bloque Neón redirects through SAML to
  `login.microsoftonline.com`, Edge answers with the signed-in work account and Entra
  issues the login. No password or MFA prompt.
- **A brand-new, empty browser profile gets it too.** A fresh Edge profile with no
  saved credentials, started headless (`--headless=new`), reached `/d2l/home` in 5.4 s
  and held both `d2l*` cookies. A visible fresh profile did the same.
- **Cookie lifetimes.** Both `d2l*` cookies are session-only. Entra's
  `ESTSAUTHPERSISTENT` cookie lasts 2160 h (90 days).
- **The API accepts that session.** `POST /d2l/lp/auth/oauth2/token` with body
  `scope=*:*:*` and header `x-csrf-token` (the value of `localStorage["XSRF.Token"]`,
  also present in the `/d2l/home` HTML) returned 200. The bearer token it returned
  worked on `/d2l/api/lp/1.43/users/whoami` (200). The reply has no `expires_in`.
- **Not established:** whether that first login came from the Windows work account
  (a device-bound token) or from the persistent cookie alone. That decides what happens
  on a PC without the work account (see Open questions).
- API versions can be discovered at `/d2l/api/versions/` instead of being hard-coded.

## Alternatives considered

- **The existing Node server
  [RohanMuppa/brightspace-mcp-server](https://github.com/RohanMuppa/brightspace-mcp-server)**
  (v5.1.41, MIT, SLSA provenance). It works, but its login is a saved password plus MFA
  in a headless Chromium it downloads (~150 MB, Node 22 required), and it can't use
  the Windows work-account sign-in. It exposes 28 tools: `tools/list` is about 41 KB
  (roughly 10k tokens), descriptions are long and replies are raw JSON. That conflicts
  with this repo's token economy and no-runtime-dependency goals.
- **Replaying the Entra login over plain HTTP.** Entra's page runs JavaScript and
  handles MFA (number matching, device checks), so this is fragile and would require
  the tool to handle the password.
- **Reusing the Windows work account's device token without a browser.** That is token
  extraction rather than signing in, and is out of bounds.

## Decisions

- **A Go tool `brightspace`, one `.exe`, no Node.** Same shape as `overleaf`: narrow
  tools, compact text output, truncated long fields, dates as `Mon 12 Nov 14:00` in the
  local zone, HTML stripped.
- **A browser performs the login; the tool never sees credentials.**
  - It uses a Chromium-based browser already on the machine: Edge first (it ships with
    Windows 10 and 11), then Chrome, Chromium and Brave, found by their usual paths per
    OS. If none is found: `BROWSER_NOT_FOUND`. Nothing is downloaded.
  - **It never touches the user's own browser profile or session.** The browser is
    started with `--user-data-dir` pointing to a directory the tool owns,
    `os.UserConfigDir()/local-agent-mcp-bus/brightspace/profile`, and
    `--remote-debugging-port=0`. The tool reads the `d2l*` cookies and the XSRF token
    over CDP (the Chrome DevTools Protocol, a local WebSocket), then closes the browser.
  - **Silent first.** The first attempt is headless on that profile: open `/d2l/home`
    and wait for that URL. Where the PC has a work account signed in, Entra answers by
    itself.
- **Not signed in: the same pattern as `google-tasks`.** A call with no cookies, a
  failed headless attempt, or a rejected token after a re-login starts a login:
  - A **visible** browser window opens on the same profile at the site's login. The user
    signs in by hand, including MFA, and ticks "Stay signed in" so Entra's 90-day
    cookie stays in the profile. Later headless attempts are then silent again, even
    without a work account on the PC.
  - The login lives in a goroutine with no time limit. A call waits at most **30 s** for
    it, then returns `LOGIN_PENDING`, which stays under the usual ~60 s MCP client
    timeout. Later calls reuse the pending login without opening another window, and
    each waits up to 30 s again. Closing the window or restarting the server drops it,
    and the next call opens a new one.
  - When the window reaches `/d2l/home`, the tool takes the cookies, closes the window
    and the call proceeds. There is no separate login tool: any call starts the login.
- **Cookies and token are cached and renewed on demand.** The bearer token is minted
  over plain HTTP from the cookies. The cache `{cookies, token, minted_at}` lives in
  `os.UserConfigDir()/local-agent-mcp-bus/brightspace.json` with mode 0600 (the same
  place and permissions as `google-tasks.json`). The token is reused for up to 50 min.
  On a 401, or with an empty cache, the browser login runs again (about 5 s) and the
  call is retried once.
- **Reads and one write, with a confirmation step.** Stage 1 reads courses, due items,
  grades, announcements and content, downloads files, and submits assignment files.
  Everything else is a later stage (below).
- **Submitting is two steps.** `brightspace_submit` without `confirm` only previews:
  nothing is sent. With `confirm="true"` it uploads. The tool description tells the
  model to show the preview to the user and wait for approval first. The preview and
  the checks below exist because a submission should be treated as irreversible: the
  student API doesn't offer a way to retract one.
  - The preview shows the course, the assignment folder, its due and end dates,
    attempts used and allowed, accepted file types and size limit, and each local file
    with its size.
  - It refuses, with a reason, when the folder is closed, no attempts remain, a file is
    missing, over the size limit or of a type the folder rejects, or the folder
    doesn't take file submissions.
  - It reads only the paths it is given, never a directory, and never reads its own
    cache or profile.
  - After uploading it reads the folder's submissions back and reports the submission
    id, time and files, so the result is confirmed from the server.
- **Bulk downloads are a background job.** `brightspace_sync` mirrors a course's files
  (or a module's) into a local directory. Large courses take longer than the MCP
  timeout, so it uses the same pattern as the login:
  - A call starts a job and waits up to 30 s. If it finishes, it returns the summary;
    otherwise it returns `JOB <id> running: 120/870 files, 340 MB`. A later call with
    `job=<id>` waits up to 30 s again for progress. The job lives in a goroutine until
    it finishes or the server exits.
  - It keeps the module/topic folder structure, sanitizes names, runs up to 4 downloads
    at once and writes `.brightspace-sync.json` in the destination with each file's
    size and last-modified time. Rerunning skips files that haven't changed, so an
    interrupted run is resumed by running it again. It never deletes local files.
  - Replies stay small: counts, bytes, the destination, and the first 5 failures. Never
    a file listing. The manifest holds the full detail.
  - The destination is resolved on the machine the server runs on. A UNC path such as
    `\\wsl.localhost\<distro>\...` reaches a WSL directory.
- **API versions** are discovered once per session from `/d2l/api/versions/` and cached.
- **Errors** follow `google-tasks`: `CODE: message. Hint: ...` with the codes
  `LOGIN_PENDING` ("The login window is open. This request can't run until you sign in
  there. Hint: sign in, then retry."), `LOGIN_BROWSER` (the window couldn't open; the
  login stays pending), `BROWSER_NOT_FOUND`, `NOT_FOUND`, `CLOSED`, `NO_ATTEMPTS`,
  `FILE_REJECTED`, `API_ERROR`.
- **Portable and shareable.** The only site-specific piece is the school URL, injected
  at build time like Google's client: `BRIGHTSPACE_URL`, from the environment or the
  untracked `local.env`, through `-ldflags -X`. It defaults to
  `https://bloqueneon.uniandes.edu.co`. Another school with the same Entra and D2L setup
  works by rebuilding. Browser discovery and launch are behind `_windows.go` and
  `_other.go` files, so nothing in the design is Windows-only. The first target is the
  Windows `.exe`.
- **Remove the earlier approach.** Delete `external/brightspace.json`, the
  `external/*.json` merge in `register-claude-desktop.sh`, the Brightspace section of
  the `install-tools` skill and the matching line in `spec/claude-desktop-config.md`.
  Nothing else uses `external/*.json`.

## Interface

| Tool | Does | Args |
|---|---|---|
| `brightspace_courses` | Courses the user is enrolled in now: id, name, code. | `all` (`true` includes past and inactive) |
| `brightspace_due` | Upcoming due items across courses, soonest first. | `days` (default 14), `course` |
| `brightspace_grades` | Grades for one course, or a one-line summary per course. | `course` |
| `brightspace_announcements` | Recent announcements, text only. | `course`, `days` (default 14) |
| `brightspace_content` | A course's module and topic tree, or one topic's text. | `course` (req), `topic` |
| `brightspace_download` | Saves one course file locally and returns the path. | `course`, `topic` (req), `dest` |
| `brightspace_sync` | Mirrors a course's files into a directory as a background job. | `course` (req), `dest`, `module`, `types`, `max_mb`, `job` |
| `brightspace_submit` | Previews, or with `confirm="true"` submits, files to an assignment folder. | `course`, `assignment`, `files` (req), `comment`, `confirm` |

- `course` and `assignment` accept an id or a name fragment. `files` and `types` are
  comma-separated. Arguments are strings; booleans are `"true"` or `"false"`.
- `brightspace_sync` with `job` set ignores the other arguments and reports that job.
- The Valence endpoints (for example `enrollments/myenrollments`, `dueItems`,
  `grades/values/myGradeValues`, `news`, `content/root`, `dropbox/folders` and
  `dropbox/folders/{id}/submissions/mysubmissions/`) and their exact versions and
  response shapes are fixed during implementation and recorded here.

## Check

- Unit tests (run on Linux), using injected fakes for the HTTP client, the browser
  launcher and the clock:
  - response parsing, version discovery, course and assignment matching, text
    formatting, cache and expiry;
  - the 401 → login → retry path, and the pending login across calls;
  - `submit`: the preview sends nothing; each refusal case; the multipart body; the
    read-back after upload; no upload without `confirm="true"`;
  - `sync`: a job past 30 s returns `running`; a resume skips unchanged files; names
    are sanitized; a failed file doesn't stop the job; nothing local is deleted.
- Against the real site with no cache and no profile: a call either opens the login
  window or, with a work account signed in, logs in by itself in about 5 s, and lists
  the current semester. A second call is instant.
- Login pending: the first call returns `LOGIN_PENDING` after 30 s if the user is slow;
  a retry reuses the same window; finishing the login lets the next call through.
- Deleting only the `d2l*` entries from the cache: the next call recovers by itself.
- After the real 3-hour expiry a call succeeds without user action.
- After one visible login, a later call on the same profile is headless and silent.
- `tools/list` is under 4 KB and a typical `brightspace_due` reply under 1 KB.
- No password, cookie or token value appears in logs, errors or stdout.
- The user's own browser profile and cookies are never opened or modified.
- `submit` against an assignment folder where extra attempts are harmless: the preview
  matches the folder's rules, the upload appears on the site, and the read-back
  matches.
- `sync` on a large course: the first call returns `running`, the job finishes, a
  second run transfers nothing, and an interrupted run resumes.

## Notice

This tool uses the same endpoints the Brightspace web app uses, including its internal
token endpoint, and signs in through the user's own browser session. It isn't an
official integration, and an institution may restrict it. The write action
(`brightspace_submit`) acts as the user, so use it at your own risk and review each
preview.

## Later stages

Saved for later, not part of stage 1.

- **Groups.** List a course's group categories and groups, show which can still be
  joined, and join or leave a self-enrollment group, with the same preview and
  `confirm` step as `brightspace_submit`. Likely endpoints (to be verified):
  `/d2l/api/lp/{v}/{orgUnitId}/groupcategories/` and
  `.../groupcategories/{id}/groups/{groupId}/enrollments/`. Creating or editing groups
  stays out, since that is instructor-side.
- **Discussions.** Read forums and topics, then post and reply, with the same
  confirmation.
- **Change digest.** What is new since the last call (announcements, grades, content,
  due items), kept as a small local snapshot, so a daily check costs a few lines.
- **Feedback and rubrics.** Instructor feedback and the rubric on a submitted
  assignment, and the rubric before submitting.
- **Quizzes.** Upcoming quizzes, attempts left and scores (taking a quiz stays out of
  scope).
- **Syllabus and search.** The syllabus text and a keyword search across a course's
  content.
- **Text extraction.** Turn a downloaded PDF or document into plain text.
- **Instructor tools** (rosters, grading) stay out unless the project grows a
  maintainer who needs them.

## Out of scope

- Taking quizzes, and anything instructor-side.
- Syncing due dates into Google Tasks: the client can chain the two tools itself.
- Deleting or retracting submissions and deleting local files.

## Open questions

- **A PC without the work account signed in.** The design relies on Entra's 90-day
  persistent cookie after one visible login. Whether an institution's Conditional
  Access allows that can only be tested on such a PC or in a profile with no Windows
  single sign-on. If it doesn't, that PC logs in again whenever the cookie lapses,
  like any browser.
- **Submission folder variants.** Folders can require online text instead of a file,
  or have no attempt limit. Stage 1 handles file submissions only, and the preview
  reports other folder types as unsupported. Does text submission need to join stage 1?

## Implementation plan

- `tools/brightspace/`:
  - `main.go`: the tools.
  - `session.go`: cache, token mint, retry, pending login.
  - `browser_windows.go` and `browser_other.go`: find a Chromium, start it, read cookies
    over CDP.
  - `api.go`: versions, calls, parsing.
  - `submit.go`: the preview and the multipart upload.
  - `sync.go`: the background job and the manifest.
  - `format.go`: compact text.
  - Tests for each.
  - `go.mod` with the usual `replace` and Go 1.25. The only extra dependency is a
    WebSocket client for CDP (`github.com/coder/websocket`), or the standard library
    if that stays small enough.
- Remove the earlier approach (see Decisions).
- `build.sh`: inject `BRIGHTSPACE_URL`, with the default.
- README: add a tool entry.
