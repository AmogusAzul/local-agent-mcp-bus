#!/usr/bin/env bash
# Cross-compiles every tool under tools/ into bin/<tool>.exe for Windows.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p bin
for dir in tools/*/; do
	name=$(basename "$dir")
	echo "building $name"
	(cd "$dir" && GOOS=windows GOARCH=amd64 go build -o "../../bin/$name.exe" .)
done
