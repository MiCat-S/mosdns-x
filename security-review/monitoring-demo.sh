#!/usr/bin/env bash
# Synthetic data in a private temporary directory, removed on exit.
set -euo pipefail
command -v python3 >/dev/null 2>&1 || { printf '%s\n' '需要 Python 3。' >&2; exit 3; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec python3 "$script_dir/monitoring_report.py" --demo
