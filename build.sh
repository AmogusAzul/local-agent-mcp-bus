#!/usr/bin/env bash
# Cross-compiles every tool under tools/ into bin/<tool>.exe for Windows.
# -trimpath and -buildvcs=false keep a tool's binary identical across builds
# when its code is unchanged, so `make install` can leave it untouched.
# google-tasks gets its OAuth client from GOOGLE_TASKS_CLIENT_ID and
# GOOGLE_TASKS_CLIENT_SECRET, taken from the environment or from local.env.
# brightspace takes an optional BRIGHTSPACE_URL (the school's site) the same way.
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
	if [ "$name" = brightspace ] && [ -n "${BRIGHTSPACE_URL:-}" ]; then
		ldflags="-X main.siteURL=${BRIGHTSPACE_URL}"
	fi
	(cd "$dir" && GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "../../bin/$name.exe" .)
done
