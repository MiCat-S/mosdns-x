import argparse
import hashlib
import tempfile
import unittest
from pathlib import Path

import release


class ReleaseTest(unittest.TestCase):
    def test_version_validation(self):
        self.assertEqual(release.parse_version("26.09.11"), "v26.09.11")
        self.assertEqual(release.parse_version("v26.09.11"), "v26.09.11")
        for invalid in ("", "4.6.0", "26.09.11;touch", "26-09-11", "26.13.40"):
            with self.assertRaises(argparse.ArgumentTypeError):
                release.parse_version(invalid)

    def test_target_name(self):
        self.assertEqual(
            release.target_name((("GOOS", "linux"), ("GOARCH", "amd64"), ("GOAMD64", "v3"))),
            "mosdns-linux-amd64-v3",
        )

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


if __name__ == "__main__":
    unittest.main()
