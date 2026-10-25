#!/usr/bin/env bash
# Adds (or refreshes) one mcpServers entry per tool in Claude Desktop's config.
# Usage: register-claude-desktop.sh <install-dir> [config-file]
# Idempotent: entries are keyed by tool name. Requires jq.
set -euo pipefail
cd "$(dirname "$0")"

install_dir=${1:?usage: $0 <install-dir> [config-file]}
config=${2:-}

if [ -z "$config" ]; then
	win_env() { wslpath "$(cmd.exe /c "echo %$1%" 2>/dev/null | tr -d '\r')"; }
	# The Microsoft Store build keeps its user data in %LOCALAPPDATA%\Claude-Data\claude_<id>.
	store=$(ls -d "$(win_env LOCALAPPDATA)"/Claude-Data/claude_* 2>/dev/null | head -1 || true)
	config=${store:-$(win_env APPDATA)/Claude}/claude_desktop_config.json
fi

mkdir -p "$(dirname "$config")"
[ -s "$config" ] || echo '{}' > "$config"

win_dir=$(wslpath -w "$install_dir")
json=$(cat "$config")
for dir in tools/*/; do
	name=$(basename "$dir")
	json=$(jq --arg name "$name" --arg cmd "$win_dir\\$name.exe" \
		'.mcpServers[$name] = {command: $cmd}' <<<"$json")
done
tmp=$(mktemp "$config.XXXXXX")
printf '%s\n' "$json" > "$tmp"
mv "$tmp" "$config"
echo "registered tools in $config"
