#!/usr/bin/env python3
"""Turn goreleaser's dist/ binaries into launcher packages. It never publishes.

    python3 scripts/pack.py npm  <version>   -> npm/out/*.tgz   (npm pack)
    python3 scripts/pack.py pypi <version>   -> pypi/out/*.whl  (one wheel per platform)

npm uses the esbuild pattern: one @meetstream/cli-<os>-<arch> package per platform,
and the @meetstream/cli launcher lists them all as optionalDependencies.
PyPI uses the ruff pattern: platform wheels that put the binary in scripts/, so
`pipx install meetstream-cli` and `uvx --from meetstream-cli meetstream` work without Go.
(The PyPI name `meetstream` belongs to another project; the command is still `meetstream`.)
"""

from __future__ import annotations

import base64
import hashlib
import json
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DIST = ROOT / "dist"
# npm --provenance requires repository.url to match the repo that built the package.
REPOSITORY = {"type": "git", "url": "git+https://github.com/meetstream-ai/cli.git"}

# goreleaser (os, arch) -> npm (platform, cpu), wheel platform tag
TARGETS = {
    ("darwin", "arm64"): (("darwin", "arm64"), "macosx_11_0_arm64"),
    ("darwin", "amd64"): (("darwin", "x64"), "macosx_10_12_x86_64"),
    ("linux", "arm64"): (("linux", "arm64"), "manylinux_2_17_aarch64.manylinux2014_aarch64.musllinux_1_1_aarch64"),
    ("linux", "amd64"): (("linux", "x64"), "manylinux_2_17_x86_64.manylinux2014_x86_64.musllinux_1_1_x86_64"),
    ("windows", "arm64"): (("win32", "arm64"), "win_arm64"),
    ("windows", "amd64"): (("win32", "x64"), "win_amd64"),
}


def binary(goos: str, goarch: str) -> Path:
    exe = "meetstream.exe" if goos == "windows" else "meetstream"
    hits = sorted(DIST.glob(f"meetstream_{goos}_{goarch}*/{exe}"))
    if not hits:
        sys.exit(f"no binary for {goos}/{goarch} in dist/; run: goreleaser release --snapshot --clean")
    return hits[0]


def pack_npm(version: str) -> None:
    out = ROOT / "npm" / "out"
    shutil.rmtree(out, ignore_errors=True)
    out.mkdir(parents=True)
    for (goos, goarch), ((plat, cpu), _) in TARGETS.items():
        src = binary(goos, goarch)
        d = out / f"cli-{plat}-{cpu}"
        (d / "bin").mkdir(parents=True)
        shutil.copy2(src, d / "bin" / src.name)
        (d / "bin" / src.name).chmod(0o755)  # npm pack keeps the exec bit only if it is set
        (d / "package.json").write_text(json.dumps({
            "name": f"@meetstream/cli-{plat}-{cpu}", "version": version,
            "description": f"meetstream binary for {plat}-{cpu}. Install @meetstream/cli instead.",
            "license": "Apache-2.0", "repository": REPOSITORY, "os": [plat], "cpu": [cpu], "files": ["bin"],
            "preferUnplugged": True,
        }, indent=2) + "\n")
        subprocess.run(["npm", "pack", "--silent", "--pack-destination", str(out)], cwd=d, check=True)
    launcher = out / "cli"
    shutil.copytree(ROOT / "npm" / "meetstream", launcher)
    shutil.copy2(ROOT / "README.md", launcher / "README.md")
    pj = json.loads((launcher / "package.json").read_text())
    pj["version"] = version
    pj["optionalDependencies"] = {k: version for k in pj["optionalDependencies"]}
    (launcher / "package.json").write_text(json.dumps(pj, indent=2) + "\n")
    subprocess.run(["npm", "pack", "--silent", "--pack-destination", str(out)], cwd=launcher, check=True)
    for t in sorted(out.glob("*.tgz")):
        print(t.relative_to(ROOT))


def _record_line(name: str, data: bytes) -> str:
    digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
    return f"{name},sha256={digest},{len(data)}"


def pack_pypi(version: str) -> None:
    out = ROOT / "pypi" / "out"
    shutil.rmtree(out, ignore_errors=True)
    out.mkdir(parents=True)
    pyver = version.replace("-dev", ".dev0").replace("-", "+", 1) if "-" in version else version
    readme = (ROOT / "README.md").read_text()
    for (goos, goarch), (_, tag) in TARGETS.items():
        src = binary(goos, goarch)
        dist_info = f"meetstream_cli-{pyver}.dist-info"
        files = {
            f"meetstream_cli-{pyver}.data/scripts/{src.name}": src.read_bytes(),
            f"{dist_info}/METADATA": (
                "Metadata-Version: 2.1\nName: meetstream-cli\n"
                f"Version: {pyver}\n"
                "Summary: MeetStream CLI: meeting bots, transcripts and your own agent in the call\n"
                "License: Apache-2.0\nHome-page: https://docs.meetstream.ai\n"
                "Description-Content-Type: text/markdown\n\n" + readme
            ).encode(),
            f"{dist_info}/WHEEL": (
                f"Wheel-Version: 1.0\nGenerator: meetstream-pack\nRoot-Is-Purelib: false\n"
                + "".join(f"Tag: py3-none-{t}\n" for t in tag.split("."))
            ).encode(),
        }
        record = [_record_line(n, d) for n, d in files.items()] + [f"{dist_info}/RECORD,,"]
        files[f"{dist_info}/RECORD"] = ("\n".join(record) + "\n").encode()
        whl = out / f"meetstream_cli-{pyver}-py3-none-{tag}.whl"
        with zipfile.ZipFile(whl, "w", zipfile.ZIP_DEFLATED) as z:
            for name, data in files.items():
                info = zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0))
                info.external_attr = (0o100755 if "/scripts/" in name else 0o100644) << 16  # S_IFREG so pip keeps the exec bit
                z.writestr(info, data, zipfile.ZIP_DEFLATED)
        print(whl.relative_to(ROOT))


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[1] not in {"npm", "pypi"}:
        sys.exit(__doc__)
    {"npm": pack_npm, "pypi": pack_pypi}[sys.argv[1]](sys.argv[2])
