#!/usr/bin/env bash
set -euo pipefail

repository="ouones/cliproxyapi-plugin"
requested_plugin_dir=""
requested_version="latest"
proc_root="${COMMAND_CODE_PROC_ROOT:-/proc}"
help_requested=0

usage() {
    cat <<'EOF'
Usage: install-online.sh [--plugin-dir PATH] [--version VERSION]

Install the Linux amd64 command-code plugin from a GitHub release.
EOF
}

parse_args() {
    while (( $# > 0 )); do
        case "$1" in
            --plugin-dir)
                (( $# >= 2 )) || return 2
                requested_plugin_dir="$2"
                shift 2
                ;;
            --version)
                (( $# >= 2 )) || return 2
                requested_version="$2"
                shift 2
                ;;
            -h|--help)
                usage
                help_requested=1
                return 0
                ;;
            *)
                usage
                return 2
                ;;
        esac
    done
}

require_linux_amd64() {
    [[ "$(uname -s)" == Linux ]] || return 1
    case "$(uname -m)" in
        x86_64|amd64) ;;
        *) return 1 ;;
    esac
}

require_command() {
    command -v "$1" >/dev/null 2>&1
}

read_plugins_dir() {
    local config_path="$1"

    [[ -f "$config_path" ]] || return 1

    awk '
        function leading_spaces(value) {
            match(value, /^[[:space:]]*/)
            return RLENGTH
        }

        function trim(value) {
            sub(/^[[:space:]]+/, "", value)
            sub(/[[:space:]]+$/, "", value)
            return value
        }

        BEGIN {
            in_plugins = 0
            plugins_indent = -1
            child_indent = -1
            found = 0
            single_quote = sprintf("%c", 39)
        }

        /^[[:space:]]*#/ { next }

        {
            line = $0
            if (line ~ /^[[:space:]]*plugins:[[:space:]]*$/) {
                in_plugins = 1
                plugins_indent = leading_spaces(line)
                child_indent = -1
                next
            }

            if (!in_plugins) {
                next
            }

            if (line !~ /^[[:space:]]*$/) {
                indent = leading_spaces(line)
                if (indent <= plugins_indent) {
                    in_plugins = 0
                }
            }

            if (!in_plugins || line ~ /^[[:space:]]*$/) {
                next
            }

            indent = leading_spaces(line)
            if (child_indent < 0) {
                child_indent = indent
            }
            if (indent != child_indent) {
                next
            }

            value = line
            sub(/^[[:space:]]*dir:[[:space:]]*/, "", value)
            if (value == line) {
                next
            }
            value = trim(value)
            if (value == "" || value == "|" || value == ">" ||
                value ~ /^[\[\{]/ || value ~ /^-/) {
                exit 2
            }

            first = substr(value, 1, 1)
            last = substr(value, length(value), 1)
            if (first == single_quote || first == "\"") {
                if (last != first || length(value) < 2) {
                    exit 2
                }
                value = substr(value, 2, length(value) - 2)
            } else if (last == single_quote || last == "\"") {
                exit 2
            }

            print value
            found = 1
            exit 0
        }

        END {
            if (!found) {
                print "plugins"
            }
        }
    ' "$config_path"
}

config_arg_for_pid() {
    local pid="$1"
    local cmdline="$proc_root/$pid/cmdline"
    local arg

    [[ -r "$cmdline" ]] || return 1
    while IFS= read -r -d '' arg; do
        case "$arg" in
            --config)
                IFS= read -r -d '' arg || return 2
                printf '%s\n' "$arg"
                return 0
                ;;
            --config=*)
                printf '%s\n' "${arg#--config=}"
                return 0
                ;;
        esac
    done < "$cmdline"
}

native_candidate_for_pid() {
    local pid="$1"
    local proc_dir="$proc_root/$pid"
    local executable cwd config_path plugin_dir

    [[ -d "$proc_dir" ]] || return 1
    executable="$(readlink -f -- "$proc_dir/exe" 2>/dev/null)" || return 1
    case "$(basename -- "$executable")" in
        cli-proxy-api|cliproxyapi) ;;
        *) return 1 ;;
    esac

    cwd="$(readlink -f -- "$proc_dir/cwd" 2>/dev/null)" || return 1
    config_path="$(config_arg_for_pid "$pid")" || return 1
    [[ -n "$config_path" ]] || config_path="config.yaml"
    if [[ "$config_path" != /* ]]; then
        config_path="$cwd/$config_path"
    fi

    plugin_dir="$(read_plugins_dir "$config_path")" || return 1
    if [[ "$plugin_dir" != /* ]]; then
        plugin_dir="$cwd/$plugin_dir"
    fi
    readlink -m -- "$plugin_dir"
}

discover_native_candidates() {
    local proc_entry pid candidate

    for proc_entry in "$proc_root"/*; do
        [[ -d "$proc_entry" ]] || continue
        pid="${proc_entry##*/}"
        [[ "$pid" =~ ^[0-9]+$ ]] || continue
        if candidate="$(native_candidate_for_pid "$pid")"; then
            printf '%s\n' "$candidate"
        fi
    done
}

select_single_candidate() {
    local candidate
    local -a candidates=()

    while IFS= read -r candidate; do
        [[ -n "$candidate" ]] || continue
        candidates+=("$candidate")
    done

    if (( ${#candidates[@]} == 0 )); then
        printf '%s\n' 'Unable to detect a unique CLIProxyAPI plugin directory; use --plugin-dir PATH.' >&2
        return 1
    fi
    if (( ${#candidates[@]} > 1 )); then
        printf '%s\n' 'Multiple CLIProxyAPI plugin directories detected:' >&2
        printf '  %s\n' "${candidates[@]}" >&2
        return 1
    fi
    printf '%s\n' "${candidates[0]}"
}

detect_plugin_dir() {
    if [[ -n "$requested_plugin_dir" ]]; then
        readlink -m -- "$requested_plugin_dir"
        return 0
    fi

    discover_native_candidates | sort -u | select_single_candidate
}

main() {
    parse_args "$@"
    (( help_requested == 1 )) && return 0
    require_linux_amd64 || {
        printf '%s\n' 'This installer supports Linux amd64 only.' >&2
        return 1
    }
    detect_plugin_dir >/dev/null
    printf '%s\n' 'Installer download support is not configured.' >&2
    return 1
}

if [[ "${COMMAND_CODE_INSTALLER_LIBRARY:-0}" != 1 ]]; then
    main "$@"
fi
