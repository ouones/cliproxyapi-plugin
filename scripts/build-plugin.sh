#!/usr/bin/env bash
set -euo pipefail

if (( $# > 2 )); then
    printf 'usage: %s [windows|linux|darwin] [amd64|arm64]\n' "$0" >&2
    exit 2
fi

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
goos="$(go env GOOS)"
goarch="$(go env GOARCH)"

if (( $# >= 1 )); then
    goos="$1"
fi
if (( $# == 2 )); then
    goarch="$2"
fi

case "$goos" in
    windows) extension=".dll" ;;
    linux) extension=".so" ;;
    darwin) extension=".dylib" ;;
    *)
        printf 'unsupported GOOS: %s\n' "$goos" >&2
        exit 2
        ;;
esac

case "$goarch" in
    amd64|arm64) ;;
    *)
        printf 'unsupported GOARCH: %s\n' "$goarch" >&2
        exit 2
        ;;
esac

output_directory="$repo_root/dist/$goos/$goarch"
output_path="$output_directory/command-code$extension"
header_path="$output_directory/command-code.h"
mkdir -p "$output_directory"

(
    cd "$repo_root"
    CGO_ENABLED=1 GOOS="$goos" GOARCH="$goarch" \
        go build -trimpath -buildmode=c-shared -o "$output_path" .
)

rm -f "$header_path"
printf '%s\n' "$output_path"
