# `google-tasks`: read and change the user's Google Tasks

## Problem

Desktop can plan the user's day but can't touch their to-do list. The user wants an
agent to fully manage Google Tasks (lists, tasks, subtasks, order, due dates, done or
open) without any setup commands. When something is impossible in Google Tasks, the
tool should say so with a fixed error instead of half-doing it.

## Decisions

- **Official Tasks API v1 over HTTPS.** Base URL `https://tasks.googleapis.com/tasks/v1`,
  scope `https://www.googleapis.com/auth/tasks`, plain `net/http` plus
  `golang.org/x/oauth2`. Not the private web API that browser cookies would unlock:
  it's undocumented and fragile. So these stay impossible: recurring tasks, due times,
  stars and reminders. They get explicit errors (see Errors).
  - This is the first tool here that uses the network. The README's "no networking"
    line gets an exception.
- **Login without commands.** OAuth installed-app loopback flow with PKCE, started by
  the server itself:
  - The server keeps the refresh token in `os.UserConfigDir()/local-agent-mcp-bus/google-tasks.json`
    (`%APPDATA%\…` for the Windows `.exe`), with mode 0600.
  - A call with no token, or one Google rejects with `invalid_grant`, starts a login:
    a listener on `127.0.0.1:<random port>`, and the consent URL opened with
    `rundll32 url.dll,FileProtocolHandler <url>`. The URL is passed as an argument, with
    no `cmd`, so `&` needs no escaping.
  - **The login has no time limit.** It lives in a goroutine until it finishes or the
    server exits. A call waits for it for at most **30 s**, then returns
    `LOGIN_PENDING`. That stays under the usual 60 s MCP client timeout.
  - While a login is pending, later calls reuse it, without a new tab, and each waits
    up to 30 s again. If the server restarts, the pending login is lost, and the next
    call opens a fresh tab.
  - The listener answers the browser with a short "Logged in, you can close this tab"
    page (or the error), saves the token and ends.
- **OAuth client built in at compile time.** It's a Google Cloud "Desktop app" client,
  set to **In production** because in Testing mode tokens expire after 7 days.
  `make build-windows` injects its ID and secret with `-ldflags -X` from
  `GOOGLE_TASKS_CLIENT_ID` and `GOOGLE_TASKS_CLIENT_SECRET`, so they never land in
  git. Google says desktop secrets aren't confidential, so this is fine. A binary built
  without them returns `NOT_CONFIGURED`.
- **Seven tools in one server**, named `tasks_*`: one per job keeps descriptions short
  and outputs specific.
- **References by name or id, exact match only.**
  - `list` is a list title (case-insensitive) or id. If it's omitted, the tool uses
    `@default`.
  - `task` is a task id or an exact title (case-insensitive) within that list.
  - More than one match gives `AMBIGUOUS` with the candidates. There's no fuzzy
    matching, to avoid editing the wrong task. To search, use `query` in `tasks_list`.
- **Subtasks: one level, enforced by the tool.** Google's apps only show one level. A
  `parent` that is itself a subtask gives `UNSUPPORTED_DEPTH`.
- **Requests for impossible features fail loudly.** MCP ignores arguments a tool
  doesn't declare, so "star it" would silently drop. `core.Tool` gets an optional
  `Reject map[string]string` (argument name → error text). The handler checks
  `req.GetArguments()` for those keys before running. `tasks_create` and
  `tasks_update` reject `star`, `starred`, `repeat`, `recurrence`, `reminder` and
  `time`. These names aren't in the schema, so they cost no tokens. They catch an
  agent that invents them. The rest of the cover comes from the descriptions, which
  state the limits.
- **Compact text output.** Every listing starts with `Today: YYYY-MM-DD (Day)`, using
  the Windows local date, so the agent can resolve "tomorrow". Errors are MCP tool
  errors, with the text `CODE: message. Hint: …`.
- **No batching.** Bulk changes are repeated calls.

## Interface

Every argument is a string, because `core` only has string args. "Default list"
means `@default`.

| Tool | Description (as the agent sees it, shortened) | Args |
|---|---|---|
| `tasks_lists` | List your task lists (id, open count), or create, rename, delete or clear completed tasks from one. | `action` (`create`\|`rename`\|`delete`\|`clear`, omit to list), `list`, `title` |
| `tasks_list` | Show a list's tasks as a tree in app order. Open tasks only, unless `completed="true"`, which includes tasks completed in the apps (`showHidden`). | `list`, `completed`, `due_before`, `due_after` (inclusive dates), `query` (title or notes text, filtered by the tool), `limit` (default 50) |
| `tasks_get` | Everything about one task: notes, parent, subtasks, origin links, `webViewLink`. | `task`*, `list` |
| `tasks_create` | Create a task, optionally with notes, a due **date** (no time), a parent (one level) and a position (`after`, top by default). Returns its id. | `title`*, `list`, `notes`, `due`, `parent`, `after` |
| `tasks_update` | Change only the fields passed. `status` is `done`\|`open`, and `due`/`notes` take `none` to clear. Moving is done with `tasks_move`. | `task`*, `list`, `title`, `notes`, `due`, `status` |
| `tasks_move` | Nest under a task (`parent`, or `none` for the top level), reorder (`after`, or `top`), or send to another list (`to_list`). Needs at least one of these. | `task`*, `list`, `parent`, `after`, `to_list` |
| `tasks_delete` | Delete a task and its subtasks. Tasks assigned from Docs or Chat are deleted at their origin too, so they need `confirm="true"`. | `task`*, `list`, `confirm` |

\* required.

Output examples:

```
Today: 2026-11-01 (Sun) · My Tasks · 3 open
[ ] Pay rent                 due 2026-11-03   id:X1a…
[ ] Thesis draft                              id:Q9c…
    [ ] Write intro          due 2026-11-05   id:Rk2…
    notes: start from the outline
```

```
Created "Write intro" (id:Rk2…) in My Tasks under "Thesis draft", due 2026-11-05.
Updated "Thesis draft": status open → done. It still has 1 open subtask.
```

API mapping, for reference:
- `status done` → patch `status=completed`.
- `due` → `YYYY-MM-DDT00:00:00.000Z`.
- `tasks_move` → `tasks.move` with `parent`, `previous` and `destinationTasklist`.
- `clear` → `tasks.clear`.
- `tasks_list` pages with `maxResults=100`, rebuilds the tree from `parent`, and
  sorts siblings by `position`.

## Errors

| Code | When |
|---|---|
| `NOT_CONFIGURED` | Binary built without an OAuth client. |
| `LOGIN_PENDING` | Not logged in after this call's 30 s wait. Text: "The Google login page was opened in your browser. This request can't run until you log in. Hint: log in there, then retry." |
| `LOGIN_BROWSER` | The browser couldn't be opened. Text gives the URL to open by hand; the login stays pending. |
| `LOGIN_DENIED` | User clicked Cancel. The next call starts a new login. |
| `LOGIN_SCOPE` | Consent given without the Tasks scope. |
| `UNSUPPORTED_DUE_TIME` | `due` that isn't exactly `YYYY-MM-DD` but parses as a date with a time. |
| `UNSUPPORTED_RECURRENCE` | `repeat`/`recurrence` args, or Google refusing to move a repeating task to another list. |
| `UNSUPPORTED_STAR` | `star`/`starred` args. |
| `UNSUPPORTED_REMINDER` | `reminder`/`time` args. |
| `UNSUPPORTED_ASSIGNED` | Notes, nesting or moving on a task with `assignmentInfo`. The hint includes `linkToTask`. |
| `UNSUPPORTED_DEPTH` | `parent` is a subtask. The hint names its top-level parent. |
| `INVALID_ARG` | Bad date, empty or too long title or notes, unknown `action`/`status`, `tasks_move` with no target, or a missing arg the chosen `action` needs. |
| `LIST_NOT_FOUND` | No list matches. The text lists the existing ones. |
| `TASK_NOT_FOUND` | No task matches in the list. |
| `AMBIGUOUS` | More than one match. The text lists each with its id and due date. |
| `NOT_ALLOWED` | Deleting the default list, or deleting an assigned task without `confirm`. |
| `LIMIT_REACHED` | Google's 20k-per-list or 100k-total task limit. |
| `CONFLICT` | 412 on an `If-Match` write, after one automatic retry. |
| `RATE_LIMITED` | 429 or quota errors. |
| `GOOGLE_ERROR` | Anything else: the HTTP status plus Google's message. |

## Check

- Go tests, with an `httptest` server standing in for Google:
  - name and id resolution, including `AMBIGUOUS` and not-found;
  - tree building and ordering from `parent` + `position`;
  - date parsing: a date passes, a date with a time gives `UNSUPPORTED_DUE_TIME`, junk
    gives `INVALID_ARG`;
  - every `Reject` argument;
  - `UNSUPPORTED_DEPTH` and `UNSUPPORTED_ASSIGNED`;
  - Google status codes mapped to error codes.
- Login logic with a fake browser opener and an `httptest` token endpoint:
  - a call returns `LOGIN_PENDING` after 30 s (made shorter in tests) while the login
    is still pending;
  - a second call doesn't open another tab;
  - a finished login saves the token and the call goes ahead;
  - `invalid_grant` starts a new login.
- Through the `.exe` in Claude Desktop, on the real account:
  - the first call opens the browser, and logging in within 30 s finishes the call;
  - a slow login gives `LOGIN_PENDING`, and the retry works;
  - create, nest, reorder, move to another list, complete, reopen, clear and delete,
    each confirmed in the Tasks web app;
  - asking for "remind me at 3pm" or a repeat gives the matching error.
- To confirm against the real API during the build, since these are guesses for now:
  - deleting a parent deletes its subtasks;
  - subtasks follow a parent moved with `destinationTasklist`;
  - the exact error Google returns for moving a repeating task to another list;
  - binding `127.0.0.1` doesn't trigger a Windows Firewall prompt.

## Out of scope

- Recurring tasks, due times, stars, reminders: the private web API would be needed.
- Push or sync: no webhooks exist, and this tool only answers calls.
- Several Google accounts. One token file means one account. A
  `tasks_lists action=logout` could come later if needed.

## Implementation plan

- `core/server.go`: add `Reject map[string]string` to `Tool` and check it in `handler`.
- `tools/google-tasks/`:
  - `go.mod` with the `core` replace and `golang.org/x/oauth2`;
  - `main.go`: the tool definitions;
  - `api.go`: a thin REST client and the mapping from Google errors to codes;
  - `auth.go`: token file, pending-login goroutine, loopback listener, browser opener;
  - `resolve.go`: name and id lookup;
  - `format.go`: tree and text output;
  - `main_test.go`.
- `Makefile`: pass `-ldflags "-X main.clientID=$(GOOGLE_TASKS_CLIENT_ID) -X main.clientSecret=$(GOOGLE_TASKS_CLIENT_SECRET)"` for this tool.
- `README.md`: a tool entry, the one-time Google Cloud setup, and the networking
  exception. I'll propose the wording before applying it.
