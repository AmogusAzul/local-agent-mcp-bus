# Register tools in Claude Desktop's config

## Goal

`make install` also registers every tool in Claude Desktop's `claude_desktop_config.json`, so nobody has to edit that file by hand.

## Decisions

- **Bash + `jq`, not Python.**
  - Pure bash can't safely parse JSON.
  - `jq` is installed and makes the edit a single expression.
  - Python (standard library only) remains the fallback if `jq` turns out to be a problem.
- **Idempotent upsert.** For each `tools/<name>/` the script sets `mcpServers[<name>] = {command: "<win install dir>\\<name>.exe"}`. Other servers and other top-level keys are left untouched. Running it again doesn't change the file.
- **Non-Go servers.** Each `external/<name>.json` holds one `mcpServers` entry as is (no secrets) and is upserted under `<name>`, after the Go tools. See `spec/brightspace.md`.
- **Config location.**
  - Checked on this machine: Claude Desktop is the Microsoft Store (MSIX) build, and it keeps its user data outside the package, in `%LOCALAPPDATA%\Claude-Data\claude_<id>\`. That directory's README says so, and its `claude_desktop_config.json` holds the live preferences.
  - A config written to the old redirected path (`Packages\Claude_*\LocalCache\Roaming\Claude`) is ignored; that folder has a `.user-data-left…` marker.
  - The real `%APPDATA%\Claude` does not exist.
  - The script uses `Claude-Data\claude_*` when it exists and falls back to `%APPDATA%\Claude` otherwise.
  - An optional second argument overrides the path.
- **Edit while the app is closed.** The app rewrites this file while it runs and can overwrite our entries, so the README says to quit Claude Desktop fully (tray icon too) before running `make install`.
- **Missing or empty config.** The script creates the file as `{}` and then adds the entries.
- **Safe write.** The new JSON goes to a temporary file next to the config, which then replaces it with `mv`.

## What changes

- New `register-claude-desktop.sh` in the project root. Usage: `register-claude-desktop.sh <install-dir> [config-file]`.
- The `Makefile` `install` target gets one more line: `./register-claude-desktop.sh $(INSTALL_DIR)`.
- `README.md`: the step that says to add entries to the config by hand becomes "`make install` registers them; restart Claude Desktop".

## How we'll know it works

- Against a temporary config that contains another server and a top-level key:
  - Both tools are added.
  - The other server and the top-level key are left as they were.
  - A second run leaves the file byte-identical.
- With a path that doesn't exist yet, the file is created with just our entries.
- `make install` against the real machine, followed by a Claude Desktop restart, shows both tools.

## Out of scope

- Removing entries for tools that no longer exist.
- Non-Windows Claude Desktop installs.
