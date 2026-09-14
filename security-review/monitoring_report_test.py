import copy
import datetime as dt
import json
from pathlib import Path
import tempfile
import unittest

import monitoring_report as monitor


class ReportTests(unittest.TestCase):
    def setUp(self):
        self.now = dt.datetime(2026, 9, 14, 12, tzinfo=dt.timezone.utc)
        self.sample = {
            "timestamp": self.now.isoformat(), "storage_checked_at": self.now.isoformat(),
            "storage_status": "healthy", "overall_score": 100,
            "metrics": [{"key": key, "value": 0, "status": "healthy"} for key in monitor.KEYS],
        }

    def line(self, value=None):
        return json.dumps({"msg": "security_health", "health": value or self.sample})

    def test_missing_and_old_data_are_unknown(self):
        old = copy.deepcopy(self.sample)
        old["timestamp"] = "2020-01-01T00:00:00Z"
        for lines in ([], ["garbage"], [self.line(old)], ['{"msg":"session_cleanup_success"}']):
            report = monitor.analyze(lines, self.now)
            self.assertEqual(report["status"], "unknown")
            self.assertIsNone(report["score"])

    def test_timezones_future_and_staleness(self):
        self.sample["timestamp"] = "2026-09-14T20:00:00+08:00"
        self.assertEqual(monitor.analyze([self.line()], self.now)["status"], "healthy")
        self.sample["timestamp"] = "2026-09-15T00:00:00Z"
        self.assertEqual(monitor.analyze([self.line()], self.now)["status"], "unknown")
        self.sample["timestamp"] = self.now.isoformat()
        self.sample["storage_checked_at"] = (self.now - dt.timedelta(seconds=121)).isoformat()
        self.assertIsNone(monitor.analyze([self.line()], self.now)["score"])

    def test_actual_zap_console_and_json(self):
        console = "2026-09-14T12:00:00Z\tinfo\tsecurity_health\t" + json.dumps({"health": self.sample})
        for line in (console, self.line()):
            self.assertEqual(monitor.analyze([line], self.now)["status"], "healthy")
        self.assertIsNone(monitor.extract_snapshot(console.replace("\tsecurity_health\t", "\tother\t")))

    def test_unknown_is_not_a_healthy_zero(self):
        self.sample["metrics"][0].update(status="unknown", value=None)
        report = monitor.analyze([self.line()], self.now)
        self.assertEqual(report["status"], "unknown")
        self.assertIsNone(report["score"])
        self.sample["metrics"][0].update(status="not_applicable")
        self.assertEqual(monitor.analyze([self.line()], self.now)["status"], "healthy")

    def test_no_samples_and_historical_errors_do_not_block_health(self):
        self.sample["runtime_status"] = "healthy"
        self.sample["storage_age_seconds"] = 0
        self.sample["metrics"][0].update(status="no_samples", value=None)
        self.sample["metrics"][4].update(status="info", value=1)
        report = monitor.analyze([self.line()], self.now)
        self.assertEqual(report["status"], "healthy")
        self.assertEqual(report["score"], 100)
        self.sample["metrics"][0].update(status="info", value=100)
        self.assertEqual(monitor.analyze([self.line()], self.now)["score"], 100)

    def test_scan_failure_preserves_runtime_metrics_and_applicability(self):
        self.sample.update(storage_status="unknown", runtime_status="healthy", storage_age_seconds=0)
        self.sample["metrics"][0].update(status="no_samples", value=None)
        self.sample["metrics"][1].update(status="healthy", value=20)
        self.sample["metrics"][3].update(status="not_applicable", value=None)
        self.sample["metrics"][4].update(status="info", value=2)
        report = monitor.analyze([self.line()], self.now)
        self.assertEqual(report["status"], "unknown")
        self.assertIsNone(report["score"])
        self.assertEqual(report["metrics"][1]["value"], 20)
        self.assertEqual(report["metrics"][3]["status"], "not_applicable")
        self.assertEqual(report["metrics"][4]["value"], 2)

    def test_monotonic_scan_age_survives_wall_adjustment_but_event_still_expires(self):
        self.sample.update(runtime_status="healthy", storage_age_seconds=30)
        self.sample["storage_checked_at"] = (self.now + dt.timedelta(hours=1)).isoformat()
        self.assertEqual(monitor.analyze([self.line()], self.now)["status"], "healthy")
        self.assertEqual(monitor.analyze([self.line()], self.now + dt.timedelta(seconds=91))["status"], "unknown")
        self.sample["storage_age_seconds"] = None
        self.assertEqual(monitor.analyze([self.line()], self.now)["status"], "unknown")

    def test_critical_wins_but_incomplete_has_no_score(self):
        self.sample["metrics"][0].update(status="critical", value=20)
        self.sample["metrics"][1].update(status="unknown", value=None)
        report = monitor.analyze([self.line()], self.now)
        self.assertEqual(report["status"], "critical")
        self.assertIsNone(report["score"])

    def test_invalid_newest_record_not_hidden_by_old_healthy_record(self):
        old = copy.deepcopy(self.sample)
        old["timestamp"] = (self.now - dt.timedelta(seconds=10)).isoformat()
        self.sample["metrics"] = []
        report = monitor.analyze([self.line(old), self.line()], self.now)
        self.assertEqual(report["status"], "unknown")
        for bad in (float("nan"), float("inf"), -1, True, "0", 10**400):
            invalid = copy.deepcopy(old)
            invalid["metrics"][0]["value"] = bad
            self.assertFalse(monitor.validate_snapshot(invalid))

    def test_extreme_input_is_unknown_not_a_crash(self):
        self.sample["metrics"][0]["value"] = 10**400
        self.assertEqual(monitor.analyze([self.line()], self.now)["status"], "unknown")
        nested = '{"msg":"security_health","health":' + "[" * 2000 + "0" + "]" * 2000 + "}"
        self.assertIsNone(monitor.extract_snapshot(nested))
        self.sample["timestamp"] = "0001-01-01T00:00:00+23:00"
        self.assertEqual(monitor.analyze([self.line()], self.now)["status"], "unknown")

    def test_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "health.log"
            path.write_text(self.line() + "\n")
            self.assertEqual(monitor.read_report(path, self.now, 5)["status"], "healthy")
            path.write_text("")
            self.assertEqual(monitor.read_report(path, self.now, 5)["status"], "unknown")


if __name__ == "__main__":
    unittest.main()
