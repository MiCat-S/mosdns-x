#!/usr/bin/env python3
"""Build local mosdns release archives."""

import argparse
import datetime
import hashlib
import logging
import os
from pathlib import Path
import re
import subprocess
import sys
import zipfile

PROJECT_NAME = "mosdns"
RELEASE_DIR = Path("release")
VERSION_RE = re.compile(r"(?:v)?(\d{2}\.\d{2}\.\d{2})\Z")
LOGGER = logging.getLogger(__name__)

# More information: https://go.dev/doc/install/source
TARGETS = (
    (("GOOS", "darwin"), ("GOARCH", "amd64")),
    (("GOOS", "darwin"), ("GOARCH", "arm64")),
    (("GOOS", "linux"), ("GOARCH", "amd64")),
    (("GOOS", "linux"), ("GOARCH", "amd64"), ("GOAMD64", "v3")),
    (("GOOS", "linux"), ("GOARCH", "arm"), ("GOARM", "5")),
    (("GOOS", "linux"), ("GOARCH", "arm"), ("GOARM", "6")),
    (("GOOS", "linux"), ("GOARCH", "arm"), ("GOARM", "7")),
    (("GOOS", "linux"), ("GOARCH", "arm64")),
    (("GOOS", "linux"), ("GOARCH", "mipsle"), ("GOMIPS", "softfloat")),
    (("GOOS", "linux"), ("GOARCH", "mips64le"), ("GOMIPS64", "hardfloat")),
    (("GOOS", "linux"), ("GOARCH", "ppc64le")),
    (("GOOS", "freebsd"), ("GOARCH", "amd64")),
    (("GOOS", "windows"), ("GOARCH", "amd64")),
)
ARCHIVE_FILES = (
    (Path("README.md"), "README.md"),
    (Path("LICENSE"), "LICENSE"),
    (Path("examples/control-production-proxy.yaml"), "examples/control-production-proxy.yaml"),
    (Path("examples/Caddyfile"), "examples/Caddyfile"),
    (Path("examples/mosdns.service"), "examples/mosdns.service"),
)


def parse_version(value: str) -> str:
    match = VERSION_RE.fullmatch(value)
    if not match:
        raise argparse.ArgumentTypeError("version must be a date version such as 26.09.11")
    try:
        datetime.datetime.strptime(match.group(1), "%y.%m.%d")
    except ValueError as exc:
        raise argparse.ArgumentTypeError("version must contain a valid calendar date") from exc
    return "v" + match.group(1)


def target_name(target) -> str:
    return PROJECT_NAME + "-" + "-".join(value for _, value in target)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_checksums(release_dir: Path, archives) -> None:
    lines = [f"{sha256_file(path)}  {path.name}\n" for path in sorted(archives, key=lambda p: p.name)]
    (release_dir / "SHA256SUMS").write_text("".join(lines), encoding="utf-8")


def build_archive(repo: Path, release_dir: Path, target, version: str, commit: str,
                  build_time: str, use_upx: bool, headless: bool) -> Path:
    name = target_name(target)
    archive = release_dir / f"{name}.zip"
    archive.unlink(missing_ok=True)
    env = os.environ.copy()
    env["CGO_ENABLED"] = "0"
    env.update(target)
    binary = release_dir / (PROJECT_NAME + (".exe" if env["GOOS"] == "windows" else ""))
    command = ["go", "build", "-mod=readonly"]
    if not headless:
        command.extend(["-tags", "ui"])
    ldflags = (
        "-s -w -buildid= "
        f"-X github.com/pmkol/mosdns-x/constant.Version={version} "
        f"-X github.com/pmkol/mosdns-x/constant.BuildTime={build_time}"
    )
    command.extend(["-ldflags", ldflags, "-trimpath", "-o", str(binary), str(repo)])
    LOGGER.info("building %s", archive.name)
    try:
        subprocess.run(command, cwd=release_dir, env=env, check=True)
        if use_upx:
            try:
                subprocess.run(["upx", "-9", "-q", str(binary)], check=True,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            except (OSError, subprocess.CalledProcessError) as exc:
                LOGGER.error("upx failed for %s: %s", archive.name, exc)
        with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED, compresslevel=5) as output:
            output.write(binary, binary.name)
            output.write(release_dir / "config.yaml", "config.yaml")
            for source, destination in ARCHIVE_FILES:
                output.write(repo / source, destination)
            output.writestr(
                "BUILD-INFO.txt",
                f"version={version}\ncommit={commit}\nbuild_time_utc={build_time}\n",
            )
    finally:
        binary.unlink(missing_ok=True)
    return archive


def parse_args(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True, type=parse_version)
    parser.add_argument("-upx", action="store_true")
    parser.add_argument("-i", type=int, choices=range(len(TARGETS)))
    parser.add_argument(
        "--headless",
        action="store_true",
        help="skip the frontend build and create binaries without embedded UI assets",
    )
    return parser.parse_args(argv)


def main(argv=None) -> int:
    args = parse_args(argv)
    logging.basicConfig(level=logging.INFO)
    repo = Path(__file__).resolve().parent
    release_dir = repo / RELEASE_DIR
    release_dir.mkdir(exist_ok=True)
    checksum_path = release_dir / "SHA256SUMS"
    checksum_path.unlink(missing_ok=True)

    commit = subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=repo, check=True, text=True, capture_output=True
    ).stdout.strip()
    build_time = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    LOGGER.info("using version %s at commit %s", args.version, commit)

    if not args.headless:
        subprocess.run(["npm", "ci"], cwd=repo / "web", check=True)
        subprocess.run(["npm", "run", "build"], cwd=repo / "web", check=True)

    subprocess.run(
        ["go", "run", "../", "config", "gen", "config.yaml"],
        cwd=release_dir,
        check=True,
    )
    targets = (TARGETS[args.i],) if args.i is not None else TARGETS
    archives = []
    try:
        for target in targets:
            archives.append(build_archive(repo, release_dir, target, args.version, commit, build_time, args.upx, args.headless))
    except Exception:
        checksum_path.unlink(missing_ok=True)
        LOGGER.exception("release build failed")
        return 1
    write_checksums(release_dir, archives)
    return 0


if __name__ == "__main__":
    sys.exit(main())
