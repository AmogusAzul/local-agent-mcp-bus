#!/usr/bin/env bash
# Cross-compiles every tool under tools/ into bin/<tool>.exe for Windows.
# google-tasks gets its OAuth client from GOOGLE_TASKS_CLIENT_ID and
# GOOGLE_TASKS_CLIENT_SECRET, taken from the environment or from local.env.
set -euo pipefail
cd "$(dirname "$0")"
[ -f local.env ] && . ./local.env
mkdir -p bin
for dir in tools/*/; do
	name=$(basename "$dir")
	echo "building $name"
	ldflags=
	if [ "$name" = google-tasks ]; then
		[ -n "${GOOGLE_TASKS_CLIENT_ID:-}" ] || echo "warning: GOOGLE_TASKS_CLIENT_ID unset; google-tasks will answer NOT_CONFIGURED" >&2
		ldflags="-X main.clientID=${GOOGLE_TASKS_CLIENT_ID:-} -X main.clientSecret=${GOOGLE_TASKS_CLIENT_SECRET:-}"
	fi
	(cd "$dir" && GOOS=windows GOARCH=amd64 go build -ldflags "$ldflags" -o "../../bin/$name.exe" .)
done
