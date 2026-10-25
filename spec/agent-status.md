# `agent-status`: read back what an agent did in a project

## Problem

`launch-zed` and `launch-claude-code` are fire-and-forget. Desktop can't tell whether the user has sent the pre-filled prompt yet, whether the agent is still working, or what it answered. Without that, Desktop can't close the loop.

## Decisions

- **Read the agents' transcripts, not Zed's DB.** Zed doesn't store the conversation; each agent writes its own transcript inside WSL (`zed-capabilities.md`, Feedback). Reading the transcripts means no SQLite, no Python and no dependence on Zed's internal schema. It also covers sessions started with `launch-claude-code`, which is why the name doesn't mention Zed.
  - Claude (Claude Code CLI or Zed's `claude-acp`): `~/.claude/projects/<project with every non-alphanumeric character turned into ->/*.jsonl`. The newest one by modification time is the current session.
  - Codex (`codex-acp`): `~/.codex/sessions/**/rollout-*.jsonl` whose first line (`session_meta`) has `"cwd":"<project>"`. Only files changed in the last 7 days are checked, to keep the scan cheap.
  - When both exist, the one modified most recently wins.
- **Cost: return at once by default.** A call with no `wait_seconds` returns a snapshot right away and blocks nothing. With `wait_seconds`, the tool polls inside WSL every 2 s. Waiting costs no tokens, only wall time. It returns as soon as **the next turn finishes**: the newest transcript is done, and either it is a different file than at the start, or it has changed since, or it wasn't done at the start. "Next turn" is the agent's whole answer to one prompt, including every tool call, not just its first message.
  - The wait is capped at 50 s, because MCP clients usually give up on a request at 60 s. If the turn is still running after that, the reply says `running` and Desktop can call again.
- **Small output.** Each call returns only:
  - agent, state and last update (age);
  - the latest prompt, cut to 300 characters;
  - the last assistant text, cut to 2000 characters.
  The whole transcript never goes back to Desktop.
- **States.**
  - `done`: the turn ended. For Claude, the last user/assistant entry is an assistant message with `stop_reason: end_turn`. For Codex, the last `task_started`/`task_complete` event is `task_complete`.
  - `running`: anything else.
  - No transcript: the reply says so. That usually means the prompt is still waiting for review in Zed, or that it went to an agent this tool can't read.
- **Reading.** One `wsl -e sh -c` call picks the transcript and prints its agent and modification time. It then prints the last 20 user lines that aren't tool results, because in a long turn the prompt can be well before the tail (seen: about 580 lines back), and then the last 500 lines. Go parses those lines with `encoding/json`. Within those lines, the latest real prompt is the last user text that isn't a tool result, and isn't a Codex message injected with a leading `<`.

## Interface

- `project` (required): an absolute WSL path.
- `wait_seconds` (optional, at most 50): wait for the next turn to finish.

## Also in this change: `launch-zed` without `task`

When `task` is empty, `launch-zed` only opens the project: it skips the window wait, the 2 s pause and the `zed://agent` call. It replies `Opened <project> in Zed.`

## Check

- Go tests: parsing a Claude transcript (done, and running with a pending `tool_use`), parsing a Codex transcript (done, and running), skipping injected Codex prompts, the truncation limits, and the `wait_seconds` limits.
- Through the `.exe`, against real transcripts:
  - a snapshot for `entre-notas` (Claude) and for `case-atlas` (Codex);
  - an empty project gives "no transcript";
  - `wait_seconds` returns once a turn finishes.

## Out of scope

- Telling Zed's unsent draft apart from "sent to an agent we can't read". Only Zed's DB knows that.
- Zed's native agent threads (`threads.db`). They aren't used here.
