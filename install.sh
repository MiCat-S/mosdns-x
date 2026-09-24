#!/usr/bin/env bash
set -Eeuo pipefail

readonly DEFAULT_VERSION="v26.09.24"
readonly RELEASE_REPOSITORY="MiCat-S/mosdns-x"
readonly DEFAULT_GITHUB_PROXY="https://gh-proxy.com/"
readonly CLOUDFLARE_TRACE_URL="https://www.cloudflare.com/cdn-cgi/trace"
readonly BINARY_PATH="/usr/local/bin/mosdns"
readonly CONFIG_DIR="/etc/mosdns"
readonly CONFIG_PATH="${CONFIG_DIR}/config.yaml"
SYSTEMCTL_BIN="${SYSTEMCTL_BIN:-systemctl}"
SYSTEMD_DROPIN_DIR="${SYSTEMD_DROPIN_DIR:-/etc/systemd/system/mosdns.service.d}"
readonly CONTROL_STATUS_DISABLED=10
readonly CONTROL_STATUS_UNINITIALIZED=11
readonly CONTROL_STATUS_STORAGE_ERROR=12
die() {
  printf '错误：%s\n' "$*" >&2
  exit 1
}
usage() {
  cat <<'EOF'
用法：
  install.sh [--version vYY.MM.DD[.N]] [--no-github-proxy | --github-proxy PREFIX]
  install.sh --print-asset [ARCH]
  install.sh --help

从 MiCat-S/mosdns-x GitHub Release 下载并校验 Mosdns-x，安装二进制和
缺失的默认配置，然后安装或重启 mosdns systemd 服务。

选项：
  --version TAG          安装指定 Release；默认 v26.09.24
  --no-github-proxy     禁用中国 IP 自动使用的 GitHub 下载代理
  --github-proxy PREFIX 指定并强制使用 HTTPS GitHub 下载代理前缀
  --print-asset ARCH    输出 ARCH 对应的 Release 资产；省略 ARCH 时使用 uname -m
  -h, --help            显示帮助

示例：
  curl -fsSL https://raw.githubusercontent.com/MiCat-S/mosdns-x/main/install.sh | sudo bash
  sudo bash install.sh --version v26.09.24
  sudo bash install.sh --github-proxy https://gh-proxy.com/
EOF
}
validate_version() {
  local version=$1
  [[ $version =~ ^v[0-9]{2}\.(0[1-9]|1[0-2])\.(0[1-9]|[12][0-9]|3[01])(\.[1-9][0-9]*)?$ ]] ||
    die "无效 Release 版本 ${version@Q}；格式必须为 vYY.MM.DD 或 vYY.MM.DD.N"
}
asset_for_arch() {
  local arch=$1
  case "$arch" in
    x86_64 | amd64) printf '%s\n' 'mosdns-linux-amd64.zip' ;;
    aarch64 | arm64) printf '%s\n' 'mosdns-linux-arm64.zip' ;;
    armv5l | armv5tel) printf '%s\n' 'mosdns-linux-arm-5.zip' ;;
    armv6l | armv6) printf '%s\n' 'mosdns-linux-arm-6.zip' ;;
    armv7l | armv7 | armv8l) printf '%s\n' 'mosdns-linux-arm-7.zip' ;;
    mipsel | mipsle) printf '%s\n' 'mosdns-linux-mipsle-softfloat.zip' ;;
    mips64el | mips64le) printf '%s\n' 'mosdns-linux-mips64le-hardfloat.zip' ;;
    ppc64le | powerpc64le) printf '%s\n' 'mosdns-linux-ppc64le.zip' ;;
    *) die "不支持的 Linux 架构 ${arch@Q}" ;;
  esac
}
require_command() {
  command -v "$1" >/dev/null 2>&1 || die "缺少必需命令：$1"
}
trace_is_china() {
  local line
  while IFS= read -r line || [[ -n $line ]]; do
    line=${line%$'\r'}
    [[ $line == 'loc=CN' ]] && return 0
  done
  return 1
}
detect_china_ip() {
  local trace
  trace=$(curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 3 --max-time 5 "$CLOUDFLARE_TRACE_URL" 2>/dev/null) || return 1
  trace_is_china <<< "$trace"
}
normalize_github_proxy() {
  local prefix=$1
  [[ $prefix == https://* ]] || die 'GitHub 代理前缀必须使用 https://'
  [[ $prefix != *[$'\t\r\n ']* ]] || die 'GitHub 代理前缀不能包含空白字符'
  [[ $prefix != *['?'#]* ]] || die 'GitHub 代理前缀不能包含查询串或片段'
  [[ $prefix =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?(/[-A-Za-z0-9._~!$\&\'()*+,\;=:@%/]*)?$ ]] ||
    die "无效 GitHub 代理前缀 ${prefix@Q}"
  while [[ $prefix == */ ]]; do
    prefix=${prefix%/}
  done
  printf '%s/\n' "$prefix"
}
choose_github_proxy() {
  local mode=$1
  local custom_prefix=${2:-}
  case "$mode" in
    auto)
      if detect_china_ip; then
        printf '%s\n' "$DEFAULT_GITHUB_PROXY"
      fi
      ;;
    disabled) ;;
    custom) normalize_github_proxy "$custom_prefix" ;;
    *) die "未知 GitHub 代理模式 ${mode@Q}" ;;
  esac
}
github_download_url() {
  local prefix=$1
  local source_url=$2
  printf '%s%s\n' "$prefix" "$source_url"
}
select_checksum_line() {
  local sums_file=$1
  local asset=$2
  local line name
  local -a matches=()
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line =~ ^[[:xdigit:]]{64}[[:space:]][[:space:]]([^[:space:]]+)$ ]]; then
      name=${BASH_REMATCH[1]}
      [[ $name == "$asset" ]] && matches+=("$line")
    fi
  done < "$sums_file"
  ((${#matches[@]} == 1)) ||
    die "SHA256SUMS 中必须且只能有一条 ${asset@Q} 校验记录，实际为 ${#matches[@]} 条"
  printf '%s\n' "${matches[0]}"
}
# download_checksums fetches SHA256SUMS directly from GitHub even when the
# archive itself came through a proxy. Taking both from the same proxy would
# reduce the check to transport corruption only: whoever serves a tampered
# archive from that hop can serve matching checksums alongside it. A direct
# SHA256SUMS keeps the proxy honest about the bytes it delivered.
download_checksums() {
  local download_dir=$1
  local github_proxy=$2
  local release_base=$3
  if curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 10 --max-time 60 \
    --output "$download_dir/SHA256SUMS" "$release_base/SHA256SUMS"; then
    [[ -n $github_proxy ]] && printf 'SHA256SUMS：直连 GitHub 获取，独立于下载代理。\n'
    return 0
  fi
  [[ -n $github_proxy ]] || die '无法下载 SHA256SUMS'
  printf '警告：无法直连 GitHub 获取 SHA256SUMS，改用代理 %s。\n' "$github_proxy" >&2
  printf '警告：校验和与安装包来自同一跳，仅能发现传输损坏，无法独立证明发布件未被篡改。\n' >&2
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --output "$download_dir/SHA256SUMS" "$(github_download_url "$github_proxy" "$release_base/SHA256SUMS")" ||
    die '无法下载 SHA256SUMS'
}

verify_checksum() {
  local download_dir=$1
  local asset=$2
  local checksum_line
  checksum_line=$(select_checksum_line "$download_dir/SHA256SUMS" "$asset")
  (cd "$download_dir" && printf '%s\n' "$checksum_line" | sha256sum -c -)
}
extract_release_files() {
  local archive=$1
  local destination=$2
  local member
  local mosdns_count=0
  local config_count=0
  unzip -Z1 "$archive" > "$destination/archive-members" || die "无法读取 Release 压缩包"
  while IFS= read -r member || [[ -n $member ]]; do
    [[ $member == mosdns ]] && ((mosdns_count += 1))
    [[ $member == config.yaml ]] && ((config_count += 1))
  done < "$destination/archive-members"
  ((mosdns_count == 1)) || die "Release 压缩包必须且只能包含一个顶层 mosdns 文件"
  ((config_count == 1)) || die "Release 压缩包必须且只能包含一个顶层 config.yaml 文件"
  unzip -p "$archive" mosdns > "$destination/mosdns" || die "解压 mosdns 失败"
  unzip -p "$archive" config.yaml > "$destination/config.yaml" || die "解压 config.yaml 失败"
  [[ -s $destination/mosdns ]] || die "Release 中的 mosdns 为空"
  [[ -s $destination/config.yaml ]] || die "Release 中的 config.yaml 为空"
  local magic=''
  IFS= read -r -n 4 magic < "$destination/mosdns" || true
  [[ $magic == $'\177ELF' ]] || die "Release 中的 mosdns 不是 Linux ELF 可执行文件"
}
unit_uses_installed_binary() {
  local line value
  local exec_start_seen=false
  local exec_start_matches=true
  local executable_condition_matches=true
  while IFS= read -r line || [[ -n $line ]]; do
    line=${line%$'\r'}
    if [[ $line =~ ^[[:space:]]*ExecStart=[[:space:]]*(.*)$ ]]; then
      value=${BASH_REMATCH[1]}
      if [[ -z $value ]]; then
        exec_start_seen=false
        exec_start_matches=true
      else
        exec_start_seen=true
        [[ $value == "$BINARY_PATH" || $value == "$BINARY_PATH "* ]] || exec_start_matches=false
      fi
    elif [[ $line =~ ^[[:space:]]*ConditionFileIsExecutable=[[:space:]]*(.*)$ ]]; then
      value=${BASH_REMATCH[1]}
      if [[ -z $value ]]; then
        executable_condition_matches=true
      elif [[ $value != "$BINARY_PATH" ]]; then
        executable_condition_matches=false
      fi
    fi
  done
  [[ $exec_start_seen == true && $exec_start_matches == true && $executable_condition_matches == true ]]
}
run_systemctl() {
  "$SYSTEMCTL_BIN" "$@"
}
# write_service_dropin orders mosdns after the database and network, and
# shortens the restart delay. The unit that "mosdns service install" writes
# has neither: at boot mosdns could start before a local MySQL, fail to open
# its control store, and then wait the unit's fixed 120 seconds before the
# next try, leaving DNS down that long. After= naming a unit that does not
# exist is ignored, so one file serves bbolt hosts and every MySQL unit name.
write_service_dropin() {
  local dir=$SYSTEMD_DROPIN_DIR
  local path="$dir/10-mosdns-x.conf"
  local staged
  install -d -m 0755 "$dir"
  staged=$(mktemp "$dir/.10-mosdns-x.conf.XXXXXX") || die "无法写入 $dir"
  cat > "$staged" <<'DROPIN'
# Installed by mosdns-x install.sh.
[Unit]
After=network-online.target mysqld.service mysql.service mariadb.service
Wants=network-online.target

[Service]
RestartSec=5
DROPIN
  chmod 0644 "$staged"
  mv -f -- "$staged" "$path"
}
control_storage_status() {
  local binary=$1
  local config=$2
  local output status=0
  if output=$("$binary" control status --config "$config" 2>&1); then
    status=0
  else
    status=$?
  fi
  case "${status}:${output}" in
    '0:ready') printf '%s\n' ready ;;
    "${CONTROL_STATUS_DISABLED}:disabled") printf '%s\n' disabled ;;
    "${CONTROL_STATUS_UNINITIALIZED}:uninitialized") printf '%s\n' uninitialized ;;
    "${CONTROL_STATUS_STORAGE_ERROR}:storage_error") printf '%s\n' storage_error ;;
    *) return 1 ;;
  esac
}
has_interactive_tty() {
  [[ -r /dev/tty && -w /dev/tty ]]
}
print_manual_control_initialization() {
  local binary=$1
  local config=$2
  cat >&2 <<EOF
控制面板尚未初始化，服务没有启动。请在可交互终端执行：
read -r -s -p '管理员密码: ' MOSDNS_ADMIN_PASSWORD
printf '\n'
printf '%s\n' "\$MOSDNS_ADMIN_PASSWORD" | "$binary" control init-admin --config "$config" --username admin
unset MOSDNS_ADMIN_PASSWORD
EOF
}
ensure_control_initialized() {
  local binary=$1
  local config=$2
  local state username password password_confirmation
  state=$(control_storage_status "$binary" "$config") || die '无法检查控制存储状态；请检查配置和存储连接后重试'
  case "$state" in
    disabled)
      printf '控制模式未启用，跳过管理员初始化。\n'
      return 0
      ;;
    ready)
      printf '控制面板管理员已初始化。\n'
      return 0
      ;;
    storage_error) die '控制存储不可用；服务没有启动' ;;
    uninitialized) ;;
    *) die '控制存储返回了未知状态；服务没有启动' ;;
  esac
  if ! has_interactive_tty; then
    print_manual_control_initialization "$binary" "$config"
    die '控制面板需要先初始化管理员'
  fi
  printf '控制面板尚未初始化。\n' >/dev/tty
  printf '管理员用户名 [admin]: ' >/dev/tty
  IFS= read -r username </dev/tty || die '无法读取管理员用户名'
  username=${username:-admin}
  printf '管理员密码: ' >/dev/tty
  IFS= read -r -s password </dev/tty || die '无法读取管理员密码'
  printf '\n确认管理员密码: ' >/dev/tty
  IFS= read -r -s password_confirmation </dev/tty || die '无法读取管理员密码确认'
  printf '\n' >/dev/tty
  if [[ $password != "$password_confirmation" ]]; then
    unset password password_confirmation
    die '两次管理员密码不一致；服务没有启动'
  fi
  unset password_confirmation
  if ! printf '%s\n' "$password" | "$binary" control init-admin --config "$config" --username "$username"; then
    unset password
    die '管理员初始化失败；服务没有启动'
  fi
  unset password
  state=$(control_storage_status "$binary" "$config") || die '无法确认管理员初始化结果；服务没有启动'
  [[ $state == ready ]] || die '管理员初始化后控制存储仍未就绪；服务没有启动'
  printf '控制面板管理员已初始化。\n'
}
start_mosdns_service() {
  local unit_exists=$1
  local binary=${2:-$BINARY_PATH}
  local config=${3:-$CONFIG_PATH}
  if [[ $unit_exists == true ]] && run_systemctl is-active --quiet mosdns.service; then
    run_systemctl stop mosdns.service || die '停止正在运行的 mosdns.service 失败'
  fi
  ensure_control_initialized "$binary" "$config"
  run_systemctl enable mosdns.service
  if [[ $unit_exists == true ]]; then
    run_systemctl restart mosdns.service
  else
    run_systemctl start mosdns.service
  fi
  run_systemctl is-active --quiet mosdns.service || die 'mosdns.service 未进入 active 状态'
}
cleanup() {
  local status=$?
  if [[ -n ${staged_binary:-} && ( -e ${staged_binary:-} || -L ${staged_binary:-} ) ]]; then
    rm -f -- "$staged_binary"
  fi
  if [[ -n ${install_temp_dir:-} && -d ${install_temp_dir:-} && $install_temp_dir != / ]]; then
    rm -rf -- "$install_temp_dir"
  fi
  unset staged_binary
  unset install_temp_dir
  return "$status"
}
main() {
  local version=$DEFAULT_VERSION
  local print_asset=false
  local requested_arch=''
  local github_proxy_mode=auto
  local github_proxy_arg=''
  while (($#)); do
    case "$1" in
      --version)
        (($# >= 2)) || die '--version 缺少参数'
        version=$2
        shift 2
        ;;
      --no-github-proxy)
        [[ $github_proxy_mode == auto ]] ||
          die '--no-github-proxy 与 --github-proxy 互斥，且每项只能指定一次'
        github_proxy_mode=disabled
        shift
        ;;
      --github-proxy)
        (($# >= 2)) || die '--github-proxy 缺少参数'
        [[ $github_proxy_mode == auto ]] ||
          die '--no-github-proxy 与 --github-proxy 互斥，且每项只能指定一次'
        github_proxy_mode=custom
        github_proxy_arg=$2
        shift 2
        ;;
      --print-asset)
        print_asset=true
        if (($# >= 2)) && [[ $2 != -* ]]; then
          requested_arch=$2
          shift 2
        else
          shift
        fi
        ;;
      -h | --help)
        usage
        return 0
        ;;
      --)
        shift
        (($# == 0)) || die "不接受位置参数：$*"
        ;;
      *) die "未知参数 ${1@Q}；使用 --help 查看用法" ;;
    esac
  done
  validate_version "$version"
  if [[ $github_proxy_mode == custom ]]; then
    github_proxy_arg=$(normalize_github_proxy "$github_proxy_arg")
  fi
  if [[ $print_asset == true ]]; then
    [[ -n $requested_arch ]] || requested_arch=$(uname -m)
    asset_for_arch "$requested_arch"
    return 0
  fi
  for command_name in curl unzip sha256sum install uname mktemp "$SYSTEMCTL_BIN" chmod mv rm; do
    require_command "$command_name"
  done
  [[ $(uname -s) == Linux ]] || die '本安装器只支持 Linux'
  ((EUID == 0)) || die '安装二进制和 systemd 服务需要 root；请使用 sudo 运行'
  [[ -d /run/systemd/system ]] || die '当前系统未运行 systemd'
  [[ ( ! -e $BINARY_PATH && ! -L $BINARY_PATH ) || ( -f $BINARY_PATH && ! -L $BINARY_PATH ) ]] ||
    die "$BINARY_PATH 已存在且不是普通文件，拒绝覆盖"
  [[ ( ! -e $CONFIG_PATH && ! -L $CONFIG_PATH ) || -f $CONFIG_PATH ]] ||
    die "$CONFIG_PATH 已存在且不是普通文件，拒绝使用"
  local arch asset release_base github_proxy asset_url
  arch=$(uname -m)
  asset=$(asset_for_arch "$arch")
  release_base="https://github.com/${RELEASE_REPOSITORY}/releases/download/${version}"
  github_proxy=$(choose_github_proxy "$github_proxy_mode" "$github_proxy_arg")
  if [[ -n $github_proxy ]]; then
    if [[ $github_proxy_mode == auto ]]; then
      printf 'GitHub 下载：检测到中国 IP，使用代理 %s\n' "$github_proxy"
    else
      printf 'GitHub 下载：使用指定代理 %s\n' "$github_proxy"
    fi
  elif [[ $github_proxy_mode == disabled ]]; then
    printf 'GitHub 下载：代理已禁用，使用直连。\n'
  else
    printf 'GitHub 下载：未检测到中国 IP，使用直连。\n'
  fi
  asset_url=$(github_download_url "$github_proxy" "$release_base/$asset")
  install_temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/mosdns-x-install.XXXXXXXX") || die '无法创建临时目录'
  chmod 0700 "$install_temp_dir"
  trap cleanup EXIT
  trap 'exit 130' INT TERM HUP
  printf '下载 Mosdns-x %s（%s）...\n' "$version" "$asset"
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --output "$install_temp_dir/$asset" "$asset_url"
  download_checksums "$install_temp_dir" "$github_proxy" "$release_base"
  verify_checksum "$install_temp_dir" "$asset" || die 'Release SHA-256 校验失败'
  extract_release_files "$install_temp_dir/$asset" "$install_temp_dir"
  chmod 0755 "$install_temp_dir/mosdns"
  local version_output
  version_output=$("$install_temp_dir/mosdns" version) || die '下载的 mosdns 无法在当前机器运行'
  [[ $version_output == "version: ${version}, build time: "* ]] ||
    die "下载的 mosdns 版本与 ${version} 不一致：${version_output}"
  if [[ ! -d /usr/local/bin ]]; then
    install -d -o root -g root -m 0755 /usr/local/bin
  fi
  staged_binary="${BINARY_PATH}.new.$$"
  install -o root -g root -m 0755 "$install_temp_dir/mosdns" "$staged_binary"
  mv -f -- "$staged_binary" "$BINARY_PATH"
  staged_binary=''
  if [[ ! -d $CONFIG_DIR ]]; then
    install -d -o root -g root -m 0755 "$CONFIG_DIR"
  fi
  if [[ -e $CONFIG_PATH || -L $CONFIG_PATH ]]; then
    printf '保留现有配置：%s\n' "$CONFIG_PATH"
  else
    install -o root -g root -m 0644 "$install_temp_dir/config.yaml" "$CONFIG_PATH"
    printf '已安装默认配置：%s\n' "$CONFIG_PATH"
  fi
  local unit_exists=false
  local unit_text=''
  if unit_text=$(run_systemctl cat mosdns.service 2>/dev/null); then
    unit_exists=true
    if ! unit_uses_installed_binary <<< "$unit_text"; then
      printf '检测到 mosdns.service 未使用 %s，正在重新安装服务。\n' "$BINARY_PATH"
      "$BINARY_PATH" service uninstall || die '移除旧 mosdns.service 失败'
      "$BINARY_PATH" service install -d "$CONFIG_DIR" -c "$CONFIG_PATH" ||
        die '重新安装 mosdns.service 失败'
    fi
  else
    "$BINARY_PATH" service install -d "$CONFIG_DIR" -c "$CONFIG_PATH" ||
      die 'mosdns service install 失败'
  fi
  unit_text=$(run_systemctl cat mosdns.service 2>/dev/null) || die '无法读取 mosdns.service'
  unit_uses_installed_binary <<< "$unit_text" ||
    die "mosdns.service 未使用 $BINARY_PATH"
  write_service_dropin
  run_systemctl daemon-reload || die 'systemctl daemon-reload 失败'
  start_mosdns_service "$unit_exists"
  printf 'Mosdns-x %s 已安装到 %s，mosdns.service 正在运行。\n' "$version" "$BINARY_PATH"
}
if [[ ${MOSDNS_INSTALLER_LIB_ONLY:-0} != 1 ]]; then
  main "$@"
fi
