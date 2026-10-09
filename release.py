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
CHECKSUM_FILE = "SHA256SUMS"
# A single-target build (-i) writes its checksum under a different name so the
# upload list in docs/releasing.md, which is read from SHA256SUMS, cannot pick
# up a one-archive build by accident.
PARTIAL_CHECKSUM_FILE = "SHA256SUMS.partial"
# Files this script writes into release/. --clean deletes exactly these names
# (plus mosdns-*.zip) and never removes the directory or any other file in it.
KNOWN_ARTIFACT_NAMES = (CHECKSUM_FILE, PARTIAL_CHECKSUM_FILE, "config.yaml")
ARCHIVE_GLOB = PROJECT_NAME + "-*.zip"
VERSION_RE = re.compile(r"(?:v)?(\d{2}\.\d{2}\.\d{2})(?:\.([1-9]\d*))?\Z")
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
    (Path("examples/control-production-mysql.yaml"), "examples/control-production-mysql.yaml"),
    (Path("examples/Caddyfile"), "examples/Caddyfile"),
    (Path("examples/mosdns.service"), "examples/mosdns.service"),
    (Path("examples/ttl_big_domains.txt"), "examples/ttl_big_domains.txt"),
)


def parse_version(value: str) -> str:
    match = VERSION_RE.fullmatch(value)
    if not match:
        raise argparse.ArgumentTypeError("version must be a date version such as 26.09.11 or 26.09.11.1")
    try:
        datetime.datetime.strptime(match.group(1), "%y.%m.%d")
    except ValueError as exc:
        raise argparse.ArgumentTypeError("version must contain a valid calendar date") from exc
    revision = f".{match.group(2)}" if match.group(2) else ""
    return "v" + match.group(1) + revision


def target_name(target) -> str:
    return PROJECT_NAME + "-" + "-".join(value for _, value in target)


def linker_version(version: str) -> str:
    return version.removeprefix("v")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_checksums(release_dir: Path, archives, name: str = CHECKSUM_FILE) -> Path:
    lines = [f"{sha256_file(path)}  {path.name}\n" for path in sorted(archives, key=lambda p: p.name)]
    path = release_dir / name
    path.write_text("".join(lines), encoding="utf-8")
    return path


def stale_artifacts(release_dir: Path):
    """Return the files a previous run of this script left in release_dir."""
    if not release_dir.is_dir():
        return []
    found = {path for path in release_dir.glob(ARCHIVE_GLOB)}
    found.update(release_dir / name for name in KNOWN_ARTIFACT_NAMES)
    return sorted((path for path in found if path.is_file() or path.is_symlink()), key=lambda p: p.name)


class StaleArtifactsError(Exception):
    pass


def prepare_release_dir(release_dir: Path, clean: bool) -> None:
    """Create release_dir, refusing to mix in artefacts from an earlier run.

    With clean=True only the known artefact names are unlinked; the directory
    itself and any unrelated file in it are left alone.
    """
    stale = stale_artifacts(release_dir)
    if stale and not clean:
        listing = "".join(f"\n  {path}" for path in stale)
        raise StaleArtifactsError(
            f"{release_dir} already contains artefacts from a previous run:{listing}\n"
            "Refusing to mix them into this build. Re-run with --clean to delete "
            "exactly these files, or move them away first."
        )
    for path in stale:
        LOGGER.info("--clean: removing %s", path)
        path.unlink()
    release_dir.mkdir(exist_ok=True)


def partial_build_warning(archive_name: str) -> str:
    bar = "!" * 72
    return (
        f"\n{bar}\n"
        f"PARTIAL BUILD (-i): only {archive_name} is built.\n"
        "This is NOT a release. Do not upload the release/ directory to GitHub Releases.\n"
        f"Its checksum is written to {PARTIAL_CHECKSUM_FILE}, not {CHECKSUM_FILE}.\n"
        f"{bar}"
    )


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
        f"-X github.com/pmkol/mosdns-x/constant.Version={linker_version(version)} "
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
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, type=parse_version)
    parser.add_argument("-upx", action="store_true")
    parser.add_argument(
        "-i",
        type=int,
        choices=range(len(TARGETS)),
        help=f"build only this target index (partial build, never a release; checksum goes to {PARTIAL_CHECKSUM_FILE})",
    )
    parser.add_argument(
        "--clean",
        action="store_true",
        help=f"delete {ARCHIVE_GLOB}, {', '.join(KNOWN_ARTIFACT_NAMES)} left in release/ by a previous run before building",
    )
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
    try:
        prepare_release_dir(release_dir, args.clean)
    except StaleArtifactsError as exc:
        LOGGER.error("%s", exc)
        return 1
    partial = args.i is not None
    checksum_path = release_dir / (PARTIAL_CHECKSUM_FILE if partial else CHECKSUM_FILE)
    warning = partial_build_warning(target_name(TARGETS[args.i]) + ".zip") if partial else ""
    if partial:
        LOGGER.warning("%s", warning)

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
    targets = (TARGETS[args.i],) if partial else TARGETS
    archives = []
    try:
        for target in targets:
            archives.append(build_archive(repo, release_dir, target, args.version, commit, build_time, args.upx, args.headless))
    except Exception:
        checksum_path.unlink(missing_ok=True)
        LOGGER.exception("release build failed")
        return 1
    write_checksums(release_dir, archives, checksum_path.name)
    if partial:
        LOGGER.warning("%s", warning)
    return 0


if __name__ == "__main__":
    sys.exit(main())
