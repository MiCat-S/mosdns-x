#!/usr/bin/env bash
# Read-only monitor. No service changes, database writes, cron or remote alerts.
set -euo pipefail
command -v python3 >/dev/null 2>&1 || { printf '%s\n' '需要 Python 3。' >&2; exit 3; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec python3 "$script_dir/monitoring_report.py" "$@"
