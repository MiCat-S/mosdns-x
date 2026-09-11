#!/usr/bin/env bash
set -Eeuo pipefail

test_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
MOSDNS_INSTALLER_LIB_ONLY=1 source "$test_dir/install.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_eq() {
  local expected=$1
  local actual=$2
  local message=$3
  [[ $actual == "$expected" ]] || fail "$message: expected ${expected@Q}, got ${actual@Q}"
}

assert_fails() {
  local message=$1
  shift
  if ("$@" >/dev/null 2>&1); then
    fail "$message"
  fi
}

assert_eq mosdns-linux-amd64.zip "$(asset_for_arch x86_64)" 'x86_64 mapping'
assert_eq mosdns-linux-arm64.zip "$(asset_for_arch aarch64)" 'aarch64 mapping'
assert_eq mosdns-linux-arm-5.zip "$(asset_for_arch armv5tel)" 'ARM v5 mapping'
assert_eq mosdns-linux-arm-6.zip "$(asset_for_arch armv6l)" 'ARM v6 mapping'
assert_eq mosdns-linux-arm-7.zip "$(asset_for_arch armv7l)" 'ARM v7 mapping'
assert_eq mosdns-linux-mipsle-softfloat.zip "$(asset_for_arch mipsel)" 'MIPS LE mapping'
assert_eq mosdns-linux-mips64le-hardfloat.zip "$(asset_for_arch mips64el)" 'MIPS64 LE mapping'
assert_eq mosdns-linux-ppc64le.zip "$(asset_for_arch ppc64le)" 'ppc64le mapping'
assert_eq mosdns-linux-amd64.zip "$(bash "$test_dir/install.sh" --print-asset x86_64)" '--print-asset CLI'

validate_version v26.09.11
if (validate_version 26.09.11 >/dev/null 2>&1); then
  fail 'version without v prefix was accepted'
fi
if (validate_version 'v26.13.01' >/dev/null 2>&1); then
  fail 'invalid month was accepted'
fi

trace_is_china <<'EOF'
fl=29f202
loc=CN
tls=TLSv1.3
EOF
printf 'loc=CN\r\n' | trace_is_china
assert_fails 'loc=cn was treated as China' trace_is_china <<< 'loc=cn'
assert_fails 'embedded loc=CN was treated as China' trace_is_china <<< 'note=loc=CN'
assert_fails 'loc=CN with a suffix was treated as China' trace_is_china <<< 'loc=CN-extra'
assert_fails 'loc=US was treated as China' trace_is_china <<< 'loc=US'
assert_fails 'missing loc was treated as China' trace_is_china <<< 'warp=off'

detect_china_ip() { return 0; }
assert_eq "$DEFAULT_GITHUB_PROXY" "$(choose_github_proxy auto)" 'China IP default proxy'
detect_china_ip() { return 1; }
assert_eq '' "$(choose_github_proxy auto)" 'non-China or failed detection direct download'
assert_eq '' "$(choose_github_proxy disabled)" 'disabled proxy'
assert_eq 'https://mirror.example/path/' "$(choose_github_proxy custom 'https://mirror.example/path///')" 'custom proxy normalization'
assert_eq 'https://gh-proxy.com/https://github.com/MiCat-S/mosdns-x/releases/download/v26.09.11/mosdns-linux-amd64.zip' \
  "$(github_download_url 'https://gh-proxy.com/' 'https://github.com/MiCat-S/mosdns-x/releases/download/v26.09.11/mosdns-linux-amd64.zip')" \
  'proxy URL composition'
assert_eq 'https://github.com/MiCat-S/mosdns-x/releases/download/v26.09.11/SHA256SUMS' \
  "$(github_download_url '' 'https://github.com/MiCat-S/mosdns-x/releases/download/v26.09.11/SHA256SUMS')" \
  'direct URL composition'
assert_fails 'HTTP custom proxy was accepted' normalize_github_proxy 'http://mirror.example/'
assert_fails 'custom proxy with whitespace was accepted' normalize_github_proxy $'https://mirror.example/\nmalicious'
assert_fails 'custom proxy with query was accepted' normalize_github_proxy 'https://mirror.example/?target=x'
assert_fails 'mutually exclusive proxy options were accepted' bash "$test_dir/install.sh" \
  --print-asset x86_64 --no-github-proxy --github-proxy https://mirror.example/
assert_fails 'duplicate custom proxy options were accepted' bash "$test_dir/install.sh" \
  --print-asset x86_64 --github-proxy https://one.example/ --github-proxy https://two.example/
assert_fails 'missing custom proxy value was accepted' bash "$test_dir/install.sh" --github-proxy
assert_eq mosdns-linux-amd64.zip \
  "$(bash "$test_dir/install.sh" --print-asset x86_64 --no-github-proxy)" \
  'valid disabled proxy CLI option'
assert_eq mosdns-linux-amd64.zip \
  "$(bash "$test_dir/install.sh" --print-asset x86_64 --github-proxy https://mirror.example///)" \
  'valid custom proxy CLI option'

valid_generated_unit='[Unit]
Description=A DNS forwarder
ConditionFileIsExecutable=/usr/local/bin/mosdns

[Service]
ExecStart=/usr/local/bin/mosdns start --as-service -d /etc/mosdns -c /etc/mosdns/config.yaml'
unit_uses_installed_binary <<< "$valid_generated_unit" ||
  fail 'generated unit using installed binary was rejected'

valid_custom_unit='[Service]
ExecStart=/usr/local/bin/mosdns start -c /etc/mosdns/config.yaml'
unit_uses_installed_binary <<< "$valid_custom_unit" ||
  fail 'valid unit without executable condition was rejected'

wrong_condition_unit='[Unit]
ConditionFileIsExecutable=/etc/mosdns/mosdns
[Service]
ExecStart=/usr/local/bin/mosdns start --as-service -d /etc/mosdns -c /etc/mosdns/config.yaml'
assert_fails 'unit with stale executable condition was accepted' \
  unit_uses_installed_binary <<< "$wrong_condition_unit"

wrong_exec_start_unit='[Unit]
ConditionFileIsExecutable=/etc/mosdns/mosdns
[Service]
ExecStart=/etc/mosdns/mosdns start --as-service -d /etc/mosdns -c /etc/mosdns/config.yaml'
assert_fails 'unit using stale executable path was accepted' \
  unit_uses_installed_binary <<< "$wrong_exec_start_unit"

reset_exec_start_unit='[Service]
ExecStart=/etc/mosdns/mosdns start --as-service
ExecStart=
ExecStart=/usr/local/bin/mosdns start -c /etc/mosdns/config.yaml'
unit_uses_installed_binary <<< "$reset_exec_start_unit" ||
  fail 'systemd ExecStart reset was not honored'

assert_fails 'unit without ExecStart was accepted' \
  unit_uses_installed_binary <<< '[Unit]
Description=A DNS forwarder'

checksum_dir=$(mktemp -d "${TMPDIR:-/tmp}/mosdns-x-installer-test.XXXXXXXX")
trap 'rm -rf -- "$checksum_dir"' EXIT
asset=mosdns-linux-amd64.zip
printf 'verified archive\n' > "$checksum_dir/$asset"
hash=$(sha256sum "$checksum_dir/$asset")
hash=${hash%% *}
printf '%s  %s\n' "$hash" "$asset" > "$checksum_dir/SHA256SUMS"
verify_checksum "$checksum_dir" "$asset" >/dev/null

printf '%s  %s\n%s  %s\n' "$hash" "$asset" "$hash" "$asset" > "$checksum_dir/SHA256SUMS"
if (select_checksum_line "$checksum_dir/SHA256SUMS" "$asset" >/dev/null 2>&1); then
  fail 'duplicate checksum lines were accepted'
fi

printf 'install.sh helper tests passed\n'
