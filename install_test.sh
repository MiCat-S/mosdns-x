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
