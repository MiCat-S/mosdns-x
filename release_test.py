import argparse
import hashlib
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import release


class ReleaseTest(unittest.TestCase):
    def test_version_validation(self):
        self.assertEqual(release.parse_version("26.09.11"), "v26.09.11")
        self.assertEqual(release.parse_version("v26.09.11"), "v26.09.11")
        self.assertEqual(release.parse_version("26.09.11.1"), "v26.09.11.1")
        self.assertEqual(release.parse_version("v26.09.11.12"), "v26.09.11.12")
        for invalid in ("", "4.6.0", "26.09.11.0", "26.09.11.01", "26.09.11;touch", "26-09-11", "26.13.40"):
            with self.assertRaises(argparse.ArgumentTypeError):
                release.parse_version(invalid)

    def test_target_name(self):
        self.assertEqual(
            release.target_name((("GOOS", "linux"), ("GOARCH", "amd64"), ("GOAMD64", "v3"))),
            "mosdns-linux-amd64-v3",
        )

    def test_linker_version_avoids_double_prefix(self):
        self.assertEqual(release.linker_version("v26.09.11.1"), "26.09.11.1")

    def test_checksums_only_include_selected_archives_sorted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            selected = [root / "z.zip", root / "a.zip"]
            for path, data in zip(selected, (b"z", b"a")):
                path.write_bytes(data)
            (root / "old.zip").write_bytes(b"old")
            release.write_checksums(root, selected)
            expected = (
                f"{hashlib.sha256(b'a').hexdigest()}  a.zip\n"
                f"{hashlib.sha256(b'z').hexdigest()}  z.zip\n"
            )
            self.assertEqual((root / "SHA256SUMS").read_text(), expected)


    def test_refuses_stale_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stale = root / "mosdns-linux-amd64.zip"
            stale.write_bytes(b"old")
            with self.assertRaises(release.StaleArtifactsError) as caught:
                release.prepare_release_dir(root, clean=False)
            self.assertIn(str(stale), str(caught.exception))
            self.assertIn("--clean", str(caught.exception))
            self.assertTrue(stale.exists())

    def test_main_refuses_stale_archive_before_building(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "SHA256SUMS").write_text("old\n")
            with mock.patch.object(release, "RELEASE_DIR", root), \
                    mock.patch.object(release.subprocess, "run") as run, \
                    self.assertLogs(release.LOGGER, "ERROR") as logs:
                self.assertEqual(release.main(["--version", "26.09.30", "--headless"]), 1)
            run.assert_not_called()
            self.assertIn("SHA256SUMS", "\n".join(logs.output))
            self.assertTrue((root / "SHA256SUMS").exists())

    def test_clean_removes_only_known_artifacts(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            known = [
                root / "mosdns-linux-amd64.zip",
                root / "mosdns-windows-amd64.zip",
                root / "SHA256SUMS",
                root / "SHA256SUMS.partial",
                root / "config.yaml",
            ]
            unrelated = [root / "notes.txt", root / "other.zip", root / "mosdns-linux-amd64.zip.bak"]
            for path in known + unrelated:
                path.write_bytes(b"x")
            release.prepare_release_dir(root, clean=True)
            for path in known:
                self.assertFalse(path.exists(), path.name)
            for path in unrelated:
                self.assertTrue(path.exists(), path.name)
            self.assertTrue(root.is_dir())

    def test_empty_or_missing_release_dir_is_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "release"
            release.prepare_release_dir(root, clean=False)
            self.assertTrue(root.is_dir())
            (root / "unrelated.txt").write_text("keep")
            release.prepare_release_dir(root, clean=False)

    def test_partial_build_warns_and_writes_partial_checksums(self):
        def fake_build(repo, release_dir, target, *args):
            archive = release_dir / f"{release.target_name(target)}.zip"
            archive.write_bytes(b"archive")
            return archive

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            completed = subprocess.CompletedProcess([], 0, stdout="0" * 40 + "\n")
            with mock.patch.object(release, "RELEASE_DIR", root), \
                    mock.patch.object(release.subprocess, "run", return_value=completed), \
                    mock.patch.object(release, "build_archive", side_effect=fake_build), \
                    self.assertLogs(release.LOGGER, "WARNING") as logs:
                self.assertEqual(release.main(["--version", "26.09.30", "-i", "2", "--headless"]), 0)
            warnings = [line for line in logs.output if line.startswith("WARNING")]
            self.assertEqual(len(warnings), 2)
            for line in warnings:
                self.assertIn("PARTIAL BUILD (-i): only mosdns-linux-amd64.zip is built.", line)
                self.assertIn("This is NOT a release. Do not upload", line)
            self.assertFalse((root / "SHA256SUMS").exists())
            self.assertEqual(
                (root / "SHA256SUMS.partial").read_text(),
                f"{hashlib.sha256(b'archive').hexdigest()}  mosdns-linux-amd64.zip\n",
            )


if __name__ == "__main__":
    unittest.main()
