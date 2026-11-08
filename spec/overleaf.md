# `overleaf`: create, edit and compile Overleaf projects from Desktop

From the Google Task "Add Overleaf connection to local-agent-mcp-bus". The task asks
for a **free** Overleaf account, full CRUD from project creation onward, and
rendering and previewing the output. Desktop must manage it with this server alone:
no `run-wsl-command`, Claude Code or other tools. All work happens on the Windows
side.

## Problem

Desktop can write LaTeX but can't put it in Overleaf, compile it there, or read back
the errors. Findings (2026-11-04 research, checked live 2026-11-07):

- **There's no public API.** Git, GitHub and Dropbox sync are premium-only. The free
  compile timeout is short (10–20 s).
- **The only route on a free plan is the web app's own endpoints**, with the session
  cookie `overleaf_session2` and an `X-Csrf-Token` from `<meta name="ol-csrfToken">`.
  This is what `pyoverleaf` (used by the earlier `ol.py` skill) does:
  - list: `GET /project` embeds `ol-prefetchedProjectsBlob` (`{totalSize, projects:
    [{id, name, lastUpdated, accessLevel, trashed, archived, …}]}`) and
    `ol-usersEmail`;
  - create: `POST /project/new` `{projectName, template:"none"}`;
  - read: `GET /project/:id/download/zip`;
  - project tree with entity ids: Socket.IO 0.9. `GET /socket.io/1/?projectId=…`
    gives `sid:…`. Polling `GET /socket.io/1/xhr-polling/<sid>?projectId=…` returns
    `1::`, then `5:::{"name":"joinProjectResponse","args":[{"project":{"rootFolder":
    [{_id, docs, fileRefs, folders}]}}]}`. xhr-polling works on overleaf.com, so no
    websocket client is needed. The `GCLB` cookie from the handshake must be kept
    (sticky routing);
  - write: `POST /project/:id/upload?folder_id=<id>` (multipart `qqfile`, `name`,
    `relativePath=null`). Uploading over an existing doc replaces its content in
    place (same id, diffed into history), so the last write wins;
  - folders: `POST /project/:id/folder` `{parent_folder_id, name}`;
  - delete: `DELETE /project/:id/{doc|file|folder}/:entity_id`;
  - compile: `POST /project/:id/compile` returns `{status, outputFiles:[{path,url}],
    clsiServerId}`; `output.pdf` and `output.log` are fetched from `url` with
    `?clsiserverid=`.
- **Login:** password login is behind reCAPTCHA. A cookie copied from the browser
  works for any sign-in (Google, Uniandes SSO, password). It rolls, and expires after
  inactivity.
- **Terms:** Overleaf's Acceptable Use Policy bans access "by any means other than
  our publicly supported interfaces". The user accepts the risk (account flagged or
  rate-limited at worst). The tool keeps traffic at human scale.

## Decisions

- **One server, `overleaf.exe`, with twelve `overleaf_*` tools.** The seven jobs
  (projects, create, pull, push, status, delete a file, compile), plus login and
  logout, plus read, write and edit so Desktop needs no other tool to change files.
- **Mirror on the Windows side.** Each project is mirrored in
  `%TEMP%\lamb-overleaf\<slug>-<last 6 of id>`, with `.overleaf.json` (account,
  project id and name, SHA-256 per file). Overleaf is the source of truth; the
  mirror is disposable.
  - `project` everywhere is a name (exact, case-insensitive) or an id. File tools find
    the mirror locally; a project not mirrored yet is pulled first (one zip).
  - `pull` refuses to overwrite unpushed changes (`LOCAL_CHANGES`) unless
    `force="true"`. It removes mirrored files that are gone remotely.
  - `push` uploads changed and new files (all, or the ones in `paths`). First it
    downloads the zip: if any of them changed remotely since the last pull, it stops
    with `REMOTE_CHANGED`, unless `force="true"`. Missing folders are created.
    Local deletions aren't propagated; `overleaf_delete_file` deletes on both sides.
  - Paths are relative, `/`-separated, and can't leave the mirror or touch
    `.overleaf*`.
- **Compile remotely** on Overleaf (what the user sees there). It compiles what's on
  Overleaf, so it warns when the mirror has unpushed changes. It returns the status,
  the first 10 errors as `file:line: message` (or `! message` when the log has no
  line), the PDF's Windows path and the project link. The PDF and log are saved in
  `<mirror>\.overleaf-output\`. `open="true"` opens the PDF in the default Windows
  viewer; `attach="true"` returns it in the result as an embedded
  `application/pdf` resource, for the Desktop chat.
- **Reading files:** text as text (optional `lines="a-b"`, long files truncated at
  100 KB with a note), PNG/JPEG/GIF/WebP as image content, PDF as an embedded
  resource, other binaries as a size line. `path` omitted lists the files.
  - `core` gains an optional `Tool.Run` returning text plus binary blobs; `Execute`
    is unchanged for the other tools.
- **Accounts.** `overleaf_login cookie=…` checks the cookie against `GET /project`,
  stores it under the account's email in
  `os.UserConfigDir()\local-agent-mcp-bus\overleaf.json` (0600) and makes it active.
  `overleaf_login account=<email>` switches; with no args it lists accounts. A mirror
  remembers its account, so `push` uses the right one. `overleaf_logout` forgets an
  account (default: the active one). Without stored accounts, `OVERLEAF_SESSION`
  from the environment is used.
  - A rolled `overleaf_session2` in any response is saved back, so use keeps the
    session alive.
  - The cookie never appears in output or logs.
- **Low traffic:** one zip per pull or push, one project join per push or delete, no
  polling apart from the socket join, no compile retries, an honest User-Agent.
- Errors follow `google-tasks`: `CODE: message. Hint: …`.

| Code | When |
|---|---|
| `NOT_LOGGED_IN` | no stored account and no `OVERLEAF_SESSION` |
| `AUTH_EXPIRED` | Overleaf redirects to login; paste a fresh `overleaf_session2` |
| `NOT_FOUND` / `AMBIGUOUS` | project, account or file reference |
| `INVALID_ARG` | bad path, missing text, `old` not found exactly once |
| `LOCAL_CHANGES` | pull over unpushed edits |
| `REMOTE_CHANGED` | push over remote edits |
| `OVERLEAF_ERROR` | any other HTTP failure (status shown) |

## Interface

Every argument is a string.

| Tool | Description | Args |
|---|---|---|
| `overleaf_login` | Save a browser `overleaf_session2` cookie, switch account, or list accounts. | `cookie`, `account` |
| `overleaf_logout` | Forget an account's cookie. | `account` |
| `overleaf_projects` | List projects: name, id, last updated, local mirror. | |
| `overleaf_create` | Create a blank project and mirror it. | `name` (req) |
| `overleaf_pull` | Download a project into its local mirror. | `project` (req), `force` |
| `overleaf_read` | Read a mirrored file, or list files. | `project` (req), `path`, `lines` |
| `overleaf_write` | Create or overwrite a mirrored file. | `project`, `path`, `content` (req) |
| `overleaf_edit` | Replace one exact occurrence of text in a mirrored file. | `project`, `path`, `old`, `new` (req; `new` may be empty) |
| `overleaf_status` | Local changes since the last pull/push (M/A/D). | `project` (req) |
| `overleaf_push` | Upload changed and new files to Overleaf. | `project` (req), `paths`, `force` |
| `overleaf_delete_file` | Delete a file or folder on Overleaf and locally. | `project`, `path` (req) |
| `overleaf_compile` | Compile on Overleaf; errors, PDF path. | `project` (req), `open`, `attach` |

## Check

- Go tests against fixtures: project-list meta parsing, socket.io frame parsing and
  tree flattening, zip-to-manifest, the M/A/D diff, path validation, the log error
  parser, the logged-out redirect, account store round trip, `edit` uniqueness.
- Live, through the `.exe`, on a throwaway project `lamb-mcp-test`:
  login → create → read → edit `main.tex` → push → compile shows the change; a LaTeX
  error comes back as `file:line`; a new file in a new folder pushes; delete_file;
  `REMOTE_CHANGED` when the remote changed since the pull; then trash the project in
  the browser.

## Out of scope

- Live collaborative editing (OT), comments, tracked changes, history.
- Renaming, trashing or deleting projects, and renaming files (do it in the browser).
- Premium Git or GitHub sync. PNG previews of PDF pages.

## Implementation plan

- `core/server.go`: `Tool.Run func(ctx, args) (Result, error)` with
  `Result{Text string; Blobs []Blob}`; images become image content, other blobs
  embedded resources.
- `tools/overleaf/` (stdlib only): `main.go` (tools), `client.go` (cookies, CSRF,
  REST, compile), `socket.go` (join), `accounts.go`, `mirror.go` (zip, manifest,
  diff, paths), `latexlog.go`, `errors.go`, `main_test.go`.
- README: a tool entry and the ToS note, proposed to the user.
