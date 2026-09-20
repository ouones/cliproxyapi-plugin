#!/usr/bin/env bash
set -euo pipefail
export MSYS=winsymlinks:nativestrict

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
export COMMAND_CODE_INSTALLER_LIBRARY=1
# shellcheck source=../scripts/install-online.sh
# shellcheck disable=SC1091
source "$repo_root/scripts/install-online.sh"

failures=0
assert_eq() {
    local want="$1" got="$2" name="$3"
    if [[ "$want" != "$got" ]]; then
        printf 'FAIL %s: want <%s>, got <%s>\n' "$name" "$want" "$got" >&2
        failures=$((failures + 1))
    fi
}
assert_fails() {
    local name="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        printf 'FAIL %s: unexpectedly succeeded\n' "$name" >&2
        failures=$((failures + 1))
    fi
}

requested_plugin_dir=""
requested_version="latest"
parse_args --plugin-dir /opt/CLIProxyAPI/plugins --version v0.1.0
assert_eq /opt/CLIProxyAPI/plugins "$requested_plugin_dir" explicit-dir
assert_eq v0.1.0 "$requested_version" explicit-version
assert_eq /opt/CLIProxyAPI/plugins \
    "$(printf '%s\n' /opt/CLIProxyAPI/plugins | select_single_candidate)" \
    single-candidate
assert_fails zero-candidates select_single_candidate </dev/null
assert_fails multiple-candidates select_single_candidate \
    < <(printf '%s\n' /opt/a/plugins /opt/b/plugins)

test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT
fake_proc="$test_root/proc"
instance_one="$test_root/instance-one"
mkdir -p "$fake_proc/101" "$instance_one/conf"
printf '%s' '' > "$test_root/cli-proxy-api"
ln -s "$test_root/cli-proxy-api" "$fake_proc/101/exe"
ln -s "$instance_one" "$fake_proc/101/cwd"
printf 'cli-proxy-api\0--config\0conf/config.yaml\0' > "$fake_proc/101/cmdline"
cat > "$instance_one/conf/config.yaml" <<'YAML'
plugins:
  enabled: true
  dir: "runtime-plugins"
YAML

proc_root="$fake_proc"
assert_eq "$instance_one/runtime-plugins" \
    "$(native_candidate_for_pid 101)" native-relative-config-and-dir
assert_eq runtime-plugins "$(read_plugins_dir "$instance_one/conf/config.yaml")" \
    double-quoted-plugin-dir

mkdir -p "$fake_proc/105"
ln -s "$test_root/cli-proxy-api" "$fake_proc/105/exe"
ln -s "$instance_one" "$fake_proc/105/cwd"
printf 'cli-proxy-api\0-config\0conf/config.yaml\0' > "$fake_proc/105/cmdline"
assert_eq "$instance_one/runtime-plugins" \
    "$(native_candidate_for_pid 105)" native-single-dash-config
rm -rf -- "$fake_proc/105"

printf '%s\n' 'plugins: {dir: runtime-plugins}' > "$test_root/complex-config.yaml"
assert_fails complex-plugins-value \
    read_plugins_dir "$test_root/complex-config.yaml"

cat > "$instance_one/conf/config.yaml" <<'YAML'
plugins:
  enabled: true
YAML
assert_eq "$instance_one/plugins" \
    "$(native_candidate_for_pid 101)" default-plugin-dir

mkdir -p "$fake_proc/103"
ln -s "$test_root/unrelated" "$fake_proc/103/exe"
ln -s "$instance_one" "$fake_proc/103/cwd"
printf 'unrelated\0' > "$fake_proc/103/cmdline"
assert_eq "$instance_one/plugins" \
    "$(discover_native_candidates)" unrelated-exe-ignored

instance_two="$test_root/instance-two"
mkdir -p "$fake_proc/104" "$instance_two"
ln -s "$test_root/cli-proxy-api" "$fake_proc/104/exe"
ln -s "$instance_two" "$fake_proc/104/cwd"
printf 'cli-proxy-api\0' > "$fake_proc/104/cmdline"
touch "$instance_two/config.yaml"
assert_eq "$instance_two/plugins" \
    "$(native_candidate_for_pid 104)" no-config-uses-default-config
assert_fails multiple-native-candidates select_single_candidate \
    < <(discover_native_candidates)
rm -rf -- "$fake_proc/104"

mkdir -p "$fake_proc/102"
ln -s "$test_root/cli-proxy-api" "$fake_proc/102/exe"
ln -s "$instance_one" "$fake_proc/102/cwd"
printf 'cli-proxy-api\0' > "$fake_proc/102/cmdline"
assert_eq "$instance_one/plugins" \
    "$(printf '%s\n' "$(discover_native_candidates)" | sort -u | select_single_candidate)" \
    duplicate-native-candidates-deduplicated

fake_bin="$test_root/fake-bin"
docker_state="$test_root/docker-state"
mkdir -p "$fake_bin" "$docker_state"
cat > "$fake_bin/docker" <<'DOCKER'
#!/usr/bin/env bash
set -euo pipefail

state_dir="${FAKE_DOCKER_STATE:?}"
if [[ "$1" == ps && "$2" == --format ]]; then
    cat "$state_dir/ids"
    exit 0
fi

if [[ "$1" == inspect && "$2" == --format ]]; then
    format="$3"
    container_id="$4"
    case "$format" in
        '{{.Config.Image}}') cat "$state_dir/$container_id.image" ;;
        '{{.Path}}') cat "$state_dir/$container_id.path" ;;
        *'.Mounts'*) cat "$state_dir/$container_id.mounts" ;;
        *) exit 2 ;;
    esac
    exit 0
fi

exit 2
DOCKER
chmod +x "$fake_bin/docker"
printf '%s\n' cpa-exact cpa-parent cpa-no-plugin unrelated > "$docker_state/ids"
printf '%s\n' 'ghcr.io/ouones/cliproxyapi:latest' > "$docker_state/cpa-exact.image"
printf '%s\n' '/entrypoint' > "$docker_state/cpa-exact.path"
{
    printf '%s|%s\n' /srv/cpa-parent /CLIProxyAPI
    printf '%s|%s\n' /srv/cpa/plugins /CLIProxyAPI/plugins
} > "$docker_state/cpa-exact.mounts"
printf '%s\n' nginx > "$docker_state/cpa-parent.image"
printf '%s\n' '/usr/local/bin/cli-proxy-api' > "$docker_state/cpa-parent.path"
printf '%s|%s\n' /srv/cpa-parent /CLIProxyAPI > "$docker_state/cpa-parent.mounts"
printf '%s\n' CLIProxyAPI > "$docker_state/cpa-no-plugin.image"
printf '%s\n' /entrypoint > "$docker_state/cpa-no-plugin.path"
printf '%s|%s\n' /srv/unrelated /data > "$docker_state/cpa-no-plugin.mounts"
printf '%s\n' nginx > "$docker_state/unrelated.image"
printf '%s\n' /bin/nginx > "$docker_state/unrelated.path"
printf '%s|%s\n' /srv/nginx /data > "$docker_state/unrelated.mounts"

export FAKE_DOCKER_STATE="$docker_state"
export PATH="$fake_bin:$PATH"
requested_plugin_dir=""
proc_root="$test_root/missing-proc"
assert_eq /srv/cpa/plugins \
    "$(host_path_for_container_path cpa-exact /CLIProxyAPI/plugins)" \
    exact-mount-wins-over-parent
assert_eq /srv/cpa-parent/plugins \
    "$(host_path_for_container_path cpa-parent /CLIProxyAPI/plugins)" \
    parent-mount-adds-relative-path
assert_fails missing-plugin-mount \
    host_path_for_container_path cpa-no-plugin /CLIProxyAPI/plugins
assert_eq $'/srv/cpa-parent/plugins\n/srv/cpa/plugins' \
    "$(discover_docker_candidates | sort -u)" \
    docker-candidates-filtered-and-normalized
assert_fails mixed-native-and-docker-candidates detect_plugin_dir

requested_version=latest
assert_eq \
    'https://github.com/ouones/cliproxyapi-plugin/releases/latest/download' \
    "$(release_base_url)" latest-release-url
requested_version=v0.1.0
assert_eq \
    'https://github.com/ouones/cliproxyapi-plugin/releases/download/v0.1.0' \
    "$(release_base_url)" fixed-release-url
requested_version=not-a-version
assert_fails invalid-release-version release_base_url
requested_version=latest

release_dir="$test_root/release"
release_source_dir="$release_dir"
download_dir="$test_root/download"
deployment_root="$test_root/deployment"
plugin_dir="$deployment_root/plugins"
mkdir -p "$release_dir" "$download_dir" "$deployment_root"
printf '%s' 'new plugin bytes' > "$release_dir/command-code-linux-amd64.so"
(
    cd "$release_dir"
    sha256sum command-code-linux-amd64.so > command-code-linux-amd64.so.sha256
)
download_file() {
    local url="$1" destination="$2"
    cp "$release_source_dir/${url##*/}" "$destination"
}

download_and_verify "$download_dir"
install_verified_artifact \
    "$download_dir/command-code-linux-amd64.so" "$plugin_dir"
assert_eq 'new plugin bytes' "$(<"$plugin_dir/command-code.so")" \
    verified-artifact-installed
assert_eq 755 "$(stat -c '%a' "$plugin_dir/command-code.so")" \
    installed-mode
assert_eq 0 \
    "$(find "$plugin_dir" -maxdepth 1 -type f -name '.command-code.so.*' | wc -l | tr -d ' ')" \
    install-temp-removed

main_plugin_root="$test_root/main-deployment"
mkdir -p "$main_plugin_root"
unset download_dir
(
    main --plugin-dir "$main_plugin_root/plugins" >/dev/null
)
assert_eq 'new plugin bytes' "$(<"$main_plugin_root/plugins/command-code.so")" \
    main-installs-explicit-directory

same_download_dir="$test_root/download-same"
mkdir -p "$same_download_dir"
download_and_verify "$same_download_dir"
install_verified_artifact \
    "$same_download_dir/command-code-linux-amd64.so" "$plugin_dir"
assert_eq 0 \
    "$(find "$plugin_dir" -maxdepth 1 -type f -name 'command-code.so.backup-*' | wc -l | tr -d ' ')" \
    same-hash-does-not-backup

printf '%s' 'updated plugin bytes' > "$release_dir/command-code-linux-amd64.so"
(
    cd "$release_dir"
    sha256sum command-code-linux-amd64.so > command-code-linux-amd64.so.sha256
)
requested_version=v0.1.0
updated_download_dir="$test_root/download-updated"
mkdir -p "$updated_download_dir"
download_and_verify "$updated_download_dir"
install_verified_artifact \
    "$updated_download_dir/command-code-linux-amd64.so" "$plugin_dir"
assert_eq 'updated plugin bytes' "$(<"$plugin_dir/command-code.so")" \
    changed-artifact-installed
assert_eq 1 \
    "$(find "$plugin_dir" -maxdepth 1 -type f -name 'command-code.so.backup-*' | wc -l | tr -d ' ')" \
    changed-artifact-backed-up
backup_path="$(find "$plugin_dir" -maxdepth 1 -type f -name 'command-code.so.backup-*' | head -n 1)"
assert_eq 'new plugin bytes' "$(<"$backup_path")" backup-preserves-old-artifact
if [[ "$(basename -- "$backup_path")" =~ ^command-code\.so\.backup-[0-9]{8}T[0-9]{6}Z(\.[0-9]+)?$ ]]; then
    :
else
    printf 'FAIL backup-timestamp-format: got <%s>\n' "$(basename -- "$backup_path")" >&2
    failures=$((failures + 1))
fi

bad_release_dir="$test_root/release-bad"
bad_download_dir="$test_root/download-bad"
mkdir -p "$bad_release_dir" "$bad_download_dir"
printf '%s' 'bad plugin bytes' > "$bad_release_dir/command-code-linux-amd64.so"
printf '%s  %s\n' \
    0000000000000000000000000000000000000000000000000000000000000000 \
    command-code-linux-amd64.so > "$bad_release_dir/command-code-linux-amd64.so.sha256"
release_source_dir="$bad_release_dir"
assert_fails checksum-mismatch download_and_verify "$bad_download_dir"
assert_eq 'updated plugin bytes' "$(<"$plugin_dir/command-code.so")" \
    checksum-failure-keeps-installed-artifact
assert_eq 1 \
    "$(find "$plugin_dir" -maxdepth 1 -type f -name 'command-code.so.backup-*' | wc -l | tr -d ' ')" \
    checksum-failure-keeps-backups
printf '%s  %s\n' \
    0000000000000000000000000000000000000000000000000000000000000000 \
    ../command-code-linux-amd64.so > "$bad_release_dir/command-code-linux-amd64.so.sha256"
assert_fails checksum-path-traversal download_and_verify "$bad_download_dir"

requested_plugin_dir="$test_root/explicit"
# shellcheck disable=SC2034
proc_root="$test_root/missing-proc"
assert_eq "$test_root/explicit" "$(detect_plugin_dir)" explicit-dir-skips-proc

(( failures == 0 )) || exit 1
printf '%s\n' 'all install-online tests passed'
