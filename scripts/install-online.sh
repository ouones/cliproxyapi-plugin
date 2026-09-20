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
            invalid = 0
            single_quote = sprintf("%c", 39)
        }

        /^[[:space:]]*#/ { next }

        {
            line = $0
            if (line ~ /^[[:space:]]*plugins:[[:space:]]*($|#)/) {
                in_plugins = 1
                plugins_indent = leading_spaces(line)
                child_indent = -1
                next
            }
            if (line ~ /^[[:space:]]*plugins:/) {
                invalid = 1
                exit 2
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
                invalid = 1
                exit 2
            }

            first = substr(value, 1, 1)
            last = substr(value, length(value), 1)
            if (first == single_quote || first == "\"") {
                if (last != first || length(value) < 2) {
                    invalid = 1
                    exit 2
                }
                value = substr(value, 2, length(value) - 2)
            } else if (last == single_quote || last == "\"") {
                invalid = 1
                exit 2
            }

            print value
            found = 1
            exit 0
        }

        END {
            if (!found && !invalid) {
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
            --config|-config)
                IFS= read -r -d '' arg || return 2
                printf '%s\n' "$arg"
                return 0
                ;;
            --config=*|-config=*)
                printf '%s\n' "${arg#*=}"
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

docker_available() {
    command -v docker >/dev/null 2>&1
}

is_cliproxyapi_container() {
    local container_id="$1"
    local image path identity

    image="$(docker inspect --format '{{.Config.Image}}' "$container_id" 2>/dev/null)" || image=""
    path="$(docker inspect --format '{{.Path}}' "$container_id" 2>/dev/null)" || path=""
    identity="${image,,} ${path,,}"
    [[ "$identity" == *cliproxyapi* || "$identity" == *cli-proxy-api* ]]
}

host_path_for_container_path() {
    local container_id="$1"
    local container_path="$2"
    local mount_format='{{range .Mounts}}{{.Source}}|{{.Destination}}{{"\n"}}{{end}}'
    local source destination relative candidate
    local best_source="" best_relative="" best_length=-1

    while IFS='|' read -r source destination; do
        [[ -n "$source" && -n "$destination" ]] || continue
        [[ "$source" == /* ]] || continue
        [[ "$source" == /var/lib/docker/overlay2/* ]] && continue
        destination="${destination%/}"

        if [[ "$container_path" == "$destination" ]]; then
            relative=""
        elif [[ "$container_path" == "$destination/"* ]]; then
            relative="${container_path#"$destination/"}"
        else
            continue
        fi

        if (( ${#destination} > best_length )); then
            best_source="$source"
            best_relative="$relative"
            best_length=${#destination}
        fi
    done < <(docker inspect --format "$mount_format" "$container_id" 2>/dev/null || true)

    [[ -n "$best_source" ]] || return 1
    candidate="$best_source"
    [[ -n "$best_relative" ]] && candidate="$candidate/$best_relative"
    readlink -m -- "$candidate"
}

discover_docker_candidates() {
    local container_id candidate

    docker_available || return 0
    while IFS= read -r container_id; do
        [[ -n "$container_id" ]] || continue
        if is_cliproxyapi_container "$container_id" &&
            candidate="$(host_path_for_container_path "$container_id" /CLIProxyAPI/plugins)"; then
            printf '%s\n' "$candidate"
        fi
    done < <(docker ps --format '{{.ID}}' 2>/dev/null || true)
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

    {
        discover_native_candidates
        discover_docker_candidates
    } | sort -u | select_single_candidate
}

release_base_url() {
    case "$requested_version" in
        latest)
            printf 'https://github.com/%s/releases/latest/download\n' "$repository"
            ;;
        v[0-9]*.[0-9]*.[0-9]*)
            if [[ "$requested_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
                printf 'https://github.com/%s/releases/download/%s\n' \
                    "$repository" "$requested_version"
            else
                printf '%s\n' 'Invalid version; expected vX.Y.Z.' >&2
                return 1
            fi
            ;;
        *)
            printf '%s\n' 'Invalid version; expected latest or vX.Y.Z.' >&2
            return 1
            ;;
    esac
}

download_file() {
    curl -fsSL "$1" -o "$2"
}

validate_checksum_file() {
    local checksum_file="$1"

    awk '
        BEGIN { entries = 0; invalid = 0 }
        /^[[:space:]]*$/ { invalid = 1; next }
        {
            entries++
            if (NF != 2 || $2 != "command-code-linux-amd64.so" ||
                $2 ~ /\// || $2 ~ /\.\./) {
                invalid = 1
            }
        }
        END {
            exit (entries == 1 && invalid == 0) ? 0 : 1
        }
    ' "$checksum_file"
}

download_and_verify() {
    local download_dir="$1"
    local base_url checksum_file

    [[ -d "$download_dir" ]] || return 1
    base_url="$(release_base_url)" || return 1
    download_file "$base_url/command-code-linux-amd64.so" \
        "$download_dir/command-code-linux-amd64.so"
    download_file "$base_url/command-code-linux-amd64.so.sha256" \
        "$download_dir/command-code-linux-amd64.so.sha256"

    checksum_file="$download_dir/command-code-linux-amd64.so.sha256"
    (
        cd "$download_dir" || exit 1
        validate_checksum_file "$checksum_file" || exit 1
        sha256sum --check --strict command-code-linux-amd64.so.sha256
    )
}

install_verified_artifact() (
    local source="$1"
    local plugin_dir="$2"
    local target parent source_hash target_hash
    local temp_path=""
    local backup_base backup_path suffix

    cleanup_temp() {
        # shellcheck disable=SC2317
        if [[ -n "$temp_path" ]]; then
            # shellcheck disable=SC2317
            rm -f -- "$temp_path"
        fi
    }

    trap cleanup_temp EXIT
    [[ -f "$source" ]] || exit 1
    parent="$(dirname -- "$plugin_dir")"
    [[ -d "$parent" ]] || exit 1
    mkdir -p -- "$plugin_dir"
    target="$plugin_dir/command-code.so"

    source_hash="$(sha256sum "$source" | awk '{print $1}')"
    if [[ -f "$target" ]]; then
        target_hash="$(sha256sum "$target" | awk '{print $1}')"
        [[ "$source_hash" == "$target_hash" ]] && exit 0
    fi

    temp_path="$(mktemp "$plugin_dir/.command-code.so.XXXXXX")"
    install -m 0755 -- "$source" "$temp_path"

    if [[ -e "$target" || -L "$target" ]]; then
        backup_base="$plugin_dir/command-code.so.backup-$(date -u +%Y%m%dT%H%M%SZ)"
        backup_path="$backup_base"
        suffix=0
        while [[ -e "$backup_path" || -L "$backup_path" ]]; do
            suffix=$((suffix + 1))
            backup_path="$backup_base.$suffix"
        done
        cp -p -- "$target" "$backup_path"
    fi

    mv -f -- "$temp_path" "$target"
    temp_path=""
)

main() {
    parse_args "$@"
    (( help_requested == 1 )) && return 0
    require_linux_amd64 || {
        printf '%s\n' 'This installer supports Linux amd64 only.' >&2
        return 1
    }
    require_command curl || {
        printf '%s\n' 'Missing required command: curl.' >&2
        return 1
    }
    require_command sha256sum || {
        printf '%s\n' 'Missing required command: sha256sum.' >&2
        return 1
    }
    require_command install || {
        printf '%s\n' 'Missing required command: install.' >&2
        return 1
    }
    local plugin_dir download_dir
    plugin_dir="$(detect_plugin_dir)"
    download_dir="$(mktemp -d)"
    trap 'rm -rf -- "$download_dir"' EXIT
    download_and_verify "$download_dir"
    install_verified_artifact \
        "$download_dir/command-code-linux-amd64.so" "$plugin_dir"
    printf '%s\n' 'Installation complete. Restart CLIProxyAPI to load command-code.'
}

if [[ "${COMMAND_CODE_INSTALLER_LIBRARY:-0}" != 1 ]]; then
    main "$@"
fi
