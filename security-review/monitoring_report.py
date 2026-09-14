"""Read security_health snapshots; missing, stale or malformed data is UNKNOWN."""
import argparse
import datetime as dt
import json
import math
from pathlib import Path
import tempfile
import time

MAX_TAIL_BYTES = 8 * 1024 * 1024
MAX_LINE_BYTES = 1024 * 1024
KEYS = {
    "session_cleanup": "会话清理失败率",
    "db_connections": "控制库连接池占用",
    "rate_limiter": "IP 限速器最高占用",
    "credential_count": "凭证计数不一致用户",
    "transaction_rollback": "MySQL 回滚失败",
}
STATUSES = {"healthy": "正常", "warning": "警告", "critical": "异常",
            "unknown": "数据不足", "not_applicable": "不适用"}
EXIT_CODES = {"healthy": 0, "warning": 1, "critical": 2, "unknown": 3}


def timestamp(value):
    if not isinstance(value, str):
        raise ValueError("timestamp must be a timezone-qualified string")
    parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("timestamp has no timezone")
    return parsed.astimezone(dt.timezone.utc)


def extract_snapshot(line):
    # Production JSON or the actual zap console format with JSON fields.
    if len(line) > MAX_LINE_BYTES:
        return None
    try:
        if line.lstrip().startswith("{"):
            event = json.loads(line)
            if not isinstance(event, dict) or event.get("msg") != "security_health":
                return None
        else:
            prefix, separator, fields = line.partition("{")
            if not separator or "security_health" not in prefix.rstrip().split("\t"):
                return None
            event = json.loads("{" + fields)
        snapshot = event.get("health")
        return snapshot if isinstance(snapshot, dict) else None
    except (ValueError, TypeError, RecursionError):
        return None


def finite_number(value):
    if not isinstance(value, (int, float)) or isinstance(value, bool):
        return False
    try:
        return math.isfinite(value) and value >= 0
    except OverflowError:
        return False


def validate_snapshot(snapshot):
    metrics = snapshot.get("metrics")
    if not isinstance(metrics, list) or len(metrics) != len(KEYS):
        return False
    seen = set()
    for metric in metrics:
        if not isinstance(metric, dict):
            return False
        key, status, value = metric.get("key"), metric.get("status"), metric.get("value")
        if not isinstance(key, str) or key not in KEYS or key in seen or not isinstance(status, str) or status not in STATUSES:
            return False
        seen.add(key)
        if status in {"unknown", "not_applicable"}:
            if value is not None:
                return False
        elif not finite_number(value):
            return False
    return True


def analyze(lines, now, minutes=5):
    latest, latest_at = None, None
    for line in lines:
        snapshot = extract_snapshot(line)
        if snapshot is None:
            continue
        try:
            at = timestamp(snapshot.get("timestamp"))
        except (ValueError, OverflowError):
            continue
        if at > now or now - at > dt.timedelta(minutes=minutes):
            continue
        # Invalid newest data must not be hidden by an older healthy record.
        if latest_at is None or at >= latest_at:
            latest, latest_at = snapshot, at
    if latest is None:
        return {"status": "unknown", "reason": "no_recent_health_snapshot", "score": None, "metrics": []}
    if not validate_snapshot(latest):
        return {"status": "unknown", "reason": "invalid_health_snapshot", "score": None, "metrics": []}
    metrics = [{key: item.get(key) for key in ("key", "status", "value")} for item in latest["metrics"]]
    storage_fresh = latest.get("storage_status") == "healthy"
    try:
        checked = timestamp(latest.get("storage_checked_at"))
        storage_fresh = storage_fresh and dt.timedelta(0) <= now - checked <= dt.timedelta(seconds=120)
    except (ValueError, OverflowError):
        storage_fresh = False
    if not storage_fresh:
        for item in metrics:
            if item["key"] in {"db_connections", "credential_count", "transaction_rollback"}:
                item["status"], item["value"] = "unknown", None
    statuses = {item["status"] for item in metrics}
    complete = storage_fresh and "unknown" not in statuses
    status = "critical" if "critical" in statuses else "warning" if "warning" in statuses else "healthy" if complete else "unknown"
    score = max(0, 100 - sum(25 if m["status"] == "critical" else 10 if m["status"] == "warning" else 0 for m in metrics)) if complete else None
    return {
        "status": status, "score": score, "snapshot_at": latest_at.isoformat(),
        "reason": "" if complete else "incomplete_or_stale_metrics", "metrics": metrics,
    }


def read_report(path, now, minutes):
    # Bound I/O and memory even on multi-GB logs; never read a FIFO/device.
    if not path.is_file():
        raise ValueError("日志路径不是普通文件")
    with path.open("rb") as source:
        size = source.seek(0, 2)
        source.seek(max(0, size - MAX_TAIL_BYTES))
        if size > MAX_TAIL_BYTES:
            source.readline(MAX_LINE_BYTES)
        payload = source.read(MAX_TAIL_BYTES)
    lines = (line.decode("utf-8", errors="replace") for line in payload.splitlines())
    report = analyze(lines, now, minutes)
    report["tail_limited"] = size > MAX_TAIL_BYTES
    report["window_minutes"] = minutes
    return report


def display(report, as_json):
    if as_json:
        print(json.dumps(report, ensure_ascii=False, allow_nan=False))
        return
    print(f"安全监控：{STATUSES[report['status']]}")
    print(f"已观测项评分：{report['score'] if report['score'] is not None else '不可用'}")
    if report.get("snapshot_at"):
        print(f"快照时间：{report['snapshot_at']}")
    if report.get("reason"):
        print(f"数据说明：{report['reason']}")
    for metric in report["metrics"]:
        value = metric["value"] if metric["value"] is not None else "—"
        print(f"  {KEYS[metric['key']]}：{value} · {STATUSES[metric['status']]}")
    print("计数与失败率从进程启动累计；时间窗口仅用于选择新鲜快照。")
    if report.get("tail_limited"):
        print("仅读取日志末尾 8 MiB；缺失数据不代表正常。")


def demo():
    now = dt.datetime.now(dt.timezone.utc)
    report = {
        "timestamp": now.isoformat(), "storage_checked_at": now.isoformat(),
        "storage_status": "healthy",
        "metrics": [{"key": key, "status": "healthy", "value": 0} for key in KEYS],
    }
    report["metrics"][0].update(status="unknown", value=None)
    print("模拟数据演示（不是服务实测）；无清理样本应显示“数据不足”。")
    with tempfile.TemporaryDirectory(prefix="mosdns-monitor-demo-") as directory:
        path = Path(directory) / "sample.jsonl"
        path.write_text(json.dumps({"msg": "security_health", "health": report}) + "\n")
        display(read_report(path, now, 5), False)
    return 0


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("log_file", nargs="?", default="/var/log/mosdns/mosdns.log")
    parser.add_argument("mode", nargs="?", choices=["watch"])
    parser.add_argument("--window", type=int, default=5, help="快照新鲜度窗口，分钟")
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--demo", action="store_true")
    args = parser.parse_args(argv)
    if not 1 <= args.window <= 1440:
        parser.error("--window 必须在 1 到 1440 之间")
    if args.demo:
        return demo()
    try:
        while True:
            try:
                report = read_report(Path(args.log_file), dt.datetime.now(dt.timezone.utc), args.window)
            except (OSError, ValueError):
                report = {"status": "unknown", "reason": "log_unavailable", "score": None, "metrics": []}
            display(report, args.json)
            if args.mode != "watch":
                return EXIT_CODES[report["status"]]
            time.sleep(60)
    except KeyboardInterrupt:
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
