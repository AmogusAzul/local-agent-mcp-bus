# `launch-claude-code`: preflight launch and model selection

Two follow-ups from testing v0.2.0 live from Claude Desktop. Both affect only `launch-claude-code`.

## Symptom

- **Blind success.** The tool returns `Launched …` as soon as `wt` opens the tab. If Claude Code then fails inside the tab (a bad project path, `claude` missing), Desktop never learns about it.
- **Lost quotes.** `wt` re-joins its arguments without escaping embedded `"`. The task `fix the "login" bug` arrives as `fix the login bug`, and unbalanced quotes can garble the script.
- **No model choice.** The tool never passes `--model`, so sessions run on whatever Claude Code is configured with. That default (Opus today) suits large orchestration work, but small tasks don't need its cost. Desktop can only pass `--model` inside raw `args`, and nothing tells it which values are valid.

Launching itself works: a probe through the installed `.exe` printed `2.1.310 (Claude Code)` in the new tab. The "command not found" seen earlier was a leftover from the build before `bash -lc`.

## Cause

- `wt` is fire-and-forget, and it re-parses the command line it receives.
- There is no model argument and no guidance on choosing a model.

## Decisions

- Implement both parts now.
- No default model in the tool. When `model` is omitted, no `--model` is passed and the Claude Code CLI's own default applies (currently `opus` in the user's config). The tool only overrides the model when asked to.
- No stronger push toward cheaper models; the guidance in the argument description is enough for now.
- If `model` is set and `args` also contains `--model`, return an error instead of guessing which one wins.

## Fix

1. **Preflight over the headless path.** Before opening the tab, the tool makes one `wsl -e bash -lc` call, the same way `run-wsl-command` does, with the project passed as a positional argument and a 30-second limit. That call:
   - fails with a clear error if `command -v claude` finds nothing or the project is not a directory;
   - writes the launch script, received on stdin, to a `mktemp` file and prints its path. The script deletes itself and then runs `cd '<project>' && exec claude [--model '<model>'] <args> ['<task>']`.
2. **The tab gets only plain words:** `wt -w 0 new-tab --profile Ubuntu -- wsl -e bash -l <tmpfile>`. The `;` → `\;` escaping is dropped.
   - This costs about 0.5 s extra per launch and adds no new arguments.
3. **New `model` argument**, optional. `--model` is passed only when it is set. The argument description carries the selection guidance:
   > Model for the session. Omit to use the Claude Code default. Use `opus` for large or open-ended work: orchestration, multi-step features, refactors. Use `sonnet` for a focused, well-defined task. Use `haiku` for trivial mechanical edits. Also accepts a full model ID.
   - This adds about 50 schema tokens to each Desktop conversation. Routing small tasks to Sonnet or Haiku saves far more than that.
4. **Other text updates:**
   - The `args` description no longer uses `--model` in its example.
   - The README Tools section mentions `model`, and that omitting it uses the Claude Code default.

## Check

- A Go test runs the generated script under bash, with a stub `claude` on `PATH` that prints its arguments. A task containing `"`, `'`, `;` and `$` arrives unchanged, the script deletes itself, and `cd` lands in the project.
- Through the `.exe`, with no tab opened:
  - a missing project directory returns an error;
  - `model` together with `--model` inside `args` returns an error;
  - `tools/list` shows `model` together with the guidance text.
- A real launch from Desktop with a quoted task is checked by hand.

## Out of scope

- Watching the session after the tab opens. The preflight only catches failures that happen before launch.
- A default model in the tool or set at install time.
