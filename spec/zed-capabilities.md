# Zed: what the bus can drive, and what to build next

Survey of every external entry point into Zed 1.25.3 (`8dd255c`), checked in the source and on this machine. Each one is judged on whether it helps Claude Desktop orchestrate work. The two parts that have been built are in `launch-zed.md`.

## Entry points

| Entry point | What it does | Works for WSL projects? | Useful? |
|---|---|---|---|
| `zed --wsl u@d <path>[:line[:col]]` | Opens a project or a file at a position | Yes | **Yes**: already used; file positions not used yet |
| `-n` / `-a` / `-e` / hidden `-r` / hidden `--classic` | Window choice | Only `-n` and `-r` affect remote projects | **Yes**: `-r` is used for tabs |
| `zed://agent?prompt=` | New agent thread with the prompt pre-filled, never sent | Goes to the active window | **Yes**: used |
| `--wait` | Waits until the file or window is closed | **No**: `--wsl` paths go through the URL branch, which exits at once | No |
| `--diff a b` | Multi-file diff view | Local Windows paths only (existence checked on Windows) | No |
| `zed -` (stdin) | Opens piped text in a temp file | Windows temp file, local window | Marginal |
| `zed://git/commit/<sha>?repo=` | Opens the commit view | Local repos only | No |
| `zed://git/clone?repo=` | Clones a repo and opens it | Asks for a Windows folder | No (`git clone` in WSL, then `launch-zed`) |
| `zed://skill?data=` | Opens the Skill Creator in install mode | UI the user confirms | No (Claude can write skill files directly) |
| `zed://settings/<path>`, `zed://extension/<id>`, `zed://schemas/…` | Deep links into the UI | n/a | No |
| `--dev-container` | Opens the project in a dev container | n/a | Not now |
| Choosing the agent or model for a thread | none | — | Not possible: the URL takes only `prompt` |
| Sending the prompt | none | — | Not possible, by design |

## Feedback: reading back what happened

Zed records every agent thread in `%LOCALAPPDATA%\Zed\db\0-stable\db.sqlite`, table `sidebar_threads`. Each row has `agent_id` (`claude-acp`, `codex-acp`, …), `session_id`, `title`, `folder_paths`, `remote_connection`, `created_at`, `updated_at` and `archived`.

- A thread that is still waiting for review has `session_id = NULL` and an empty title. The threads `launch-zed` created during testing look like this.
- After the user presses Enter, the agent fills in `session_id`. Zed itself doesn't store the conversation; each agent writes it to its own transcript:
  - `claude-acp` → `~/.claude/projects/<path with / replaced by ->/<session_id>.jsonl` (WSL). It is the usual Claude Code transcript: an assistant entry whose `stop_reason` is `end_turn` and that has nothing after it means the turn is done.
  - `codex-acp` → `~/.codex/sessions/YYYY/MM/DD/rollout-*-<session_id>.jsonl` (WSL). The marker for a finished turn still needs to be confirmed.

So Claude can find out, without guessing, whether the user has sent the prompt yet, whether the agent is still working or has finished, and what the agent's last message was. Which files changed is already covered by `run-wsl-command` (`git status`, `git diff`).

## Choosing the agent

`zed://agent` uses whichever agent the panel has selected (`external_thread(None, …)` → `selected_agent`). When the panel loads, it takes the selection in this order:
1. the agent of the thread being restored;
2. the selection saved for that workspace (`scoped_kv_store`, namespace `agent_panel`, keyed by workspace id);
3. the global `kv_store` key `agent_panel__last_used_external_agent`, which is `{"agent":{"custom":{"name":"codex-acp"}}}` on this machine. That's why the test threads went to Codex.

No setting sets a default agent, and the URL has no agent parameter. The only ways to control it are:
- **(a) Writing Zed's DB before the open.** Set the global key (and, for a workspace that was opened before, its scoped entry) to the agent you want. This only affects panels that haven't loaded yet: a panel that is already open keeps its selection in memory. It means writing to another app's live SQLite DB with no official support. From WSL, that write would go through `/mnt/c`, where file locking can't be trusted, so it would have to come from the Windows `.exe` with a Go SQLite driver. The keys can also change in any Zed update. **Not recommended.**
- **(b) Picking it once in Zed.** Choosing Claude Agent in any panel updates the global key, so every workspace opened from then on (including those `launch-zed` creates) starts on Claude. Workspaces that are already open keep their own selection until you change it there.
- **(c) Reporting it.** `agent-status` returns the agent (`claude` or `codex`) of the newest transcript, so Claude can at least see which agent got the prompt and tell you.

Recommendation: (b) plus (c). Revisit (a) only if Zed adds a supported way, for example an `agent=` URL parameter.

## Proposals

1. **`zed-agent-status`**, a new tool. **Built as `agent-status`**: it reads the agents' transcripts instead of the DB (see `agent-status.md`).
   - Args: `project` (required) and `wait_seconds` (optional: keep polling until the thread is done or the time runs out).
   - It reports, for the newest thread in that folder: the agent, the title, a state (`awaiting review` / `running` / `done`) and the last assistant message (truncated, about 2 KB). It never returns the whole transcript, to save tokens.
   - With this, Desktop can run "launch → wait → read the result" as a loop. Today the loop ends at "launch".
   - **Decision needed — how to read SQLite.**
     - (a) `python3 -I` with the standard-library `sqlite3`, run inside WSL on a copy of the DB and its WAL file. The binary stays small, but WSL then needs Python.
     - (b) `modernc.org/sqlite` (pure Go) inside the `.exe`. There's no runtime dependency, but each binary grows by several MB and pulls in a large set of dependencies.
     - (c) Skip the DB and guess the thread from the newest transcript in that folder after the launch time. This is the simplest, but it can't tell "not sent yet" apart from "sent to some other agent".
     - My recommendation is (a). Python is already in this Ubuntu install, and it keeps the "one small Go binary" property.
2. **`launch-zed` optional `file` arg** (**built without line numbers**, because `:line` triggers a Zed bug for WSL paths; see `launch-zed.md`): a relative path with an optional `:line[:col]`, opened in the same `zed -r --wsl` call. The use is "look at this spot while you review". It adds about 15 schema tokens and no new tool.
3. **`launch-zed` without `task`** (**built**) would only open the project and skip the `zed://agent` call and the 2 s wait. Today it opens an empty thread, which is noise. This makes `launch-zed` the general "open in Zed" tool.

## Not proposed

- Editing Zed's `settings.json` (for example, to change the default agent): it is the user's config, and Zed rewrites it.
- Typing keystrokes or automating the UI to press Enter: that would defeat Zed's review step on purpose.
- Opening diff views or commit views: they only work for local Windows repositories.
