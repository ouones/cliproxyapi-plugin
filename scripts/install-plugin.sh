#!/usr/bin/env bash
set -euo pipefail

artifact=""
plugin_dir=""

while (( $# > 0 )); do
    case "$1" in
        --artifact)
            if (( $# < 2 )); then
                printf '%s\n' '--artifact requires a path' >&2
                exit 2
            fi
            artifact="$2"
            shift 2
            ;;
        --plugin-dir)
            if (( $# < 2 )); then
                printf '%s\n' '--plugin-dir requires a path' >&2
                exit 2
            fi
            plugin_dir="$2"
            shift 2
            ;;
        *)
            printf 'usage: %s --artifact PATH --plugin-dir PATH\n' "$0" >&2
            exit 2
            ;;
    esac
done

if [[ -z "$artifact" || -z "$plugin_dir" ]]; then
    printf 'usage: %s --artifact PATH --plugin-dir PATH\n' "$0" >&2
    exit 2
fi

if [[ ! -f "$artifact" ]]; then
    printf 'artifact does not exist: %s\n' "$artifact" >&2
    exit 1
fi

case "$artifact" in
    *.so) extension=".so" ;;
    *.dll) extension=".dll" ;;
    *.dylib) extension=".dylib" ;;
    *)
        printf 'unsupported plugin artifact extension: %s\n' "$artifact" >&2
        exit 1
        ;;
esac

mkdir -p "$plugin_dir"
destination_path="$plugin_dir/command-code$extension"
cp -f -- "$artifact" "$destination_path"
printf '%s\n' "$destination_path"
