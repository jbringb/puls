#!/usr/bin/env python3
"""Report available upgrades and modernc lockstep problems for Puls.

Run from the repository root:  python .claude/skills/upgrade-deps/scripts/check_versions.py

Covers the Go toolchain (go.mod `go` and `toolchain` lines), every module required in go.mod, GitHub
Actions in .github/workflows, Docker images, and the smoke test's Python version and pip pins. Exit
code 1 means the modernc lockstep rule is broken in the *current* go.mod (see LOCKSTEP in SKILL.md);
available upgrades alone never fail the script.
"""

import json
import re
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

GOPROXY = "https://proxy.golang.org"
# modernc.org/sqlite is machine-translated C that is only tested against the exact libc/memory/mathutil
# versions in its own go.mod, and `go get -u` moves those past it.
LOCKSTEP_PARENT = "modernc.org/sqlite"

_cache = {}


def fetch(url):
    if url not in _cache:
        req = urllib.request.Request(url, headers={"User-Agent": "puls-upgrade-deps"})
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                _cache[url] = r.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            if e.code != 404:
                raise RuntimeError(f"{url}: HTTP {e.code}") from e
            _cache[url] = None
    return _cache[url]


def go(*args):
    out = subprocess.run(["go", *args], capture_output=True, encoding="utf-8", errors="replace")
    if out.returncode != 0:
        sys.exit(f"go {' '.join(args)} failed:\n{out.stderr[-2000:]}")
    return out.stdout


def json_stream(text):
    dec, i, objs = json.JSONDecoder(), 0, []
    while True:
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            return objs
        obj, i = dec.raw_decode(text, i)
        objs.append(obj)


def vkey(v):
    return [int(p) if p.isdigit() else -1 for p in re.split(r"[.\-]", v.lstrip("v"))]


def prefix(v, n):
    return ".".join(v.split(".")[:n])


def lockstep_wants(sqlite_version):
    gomod = fetch(f"{GOPROXY}/{LOCKSTEP_PARENT}/@v/{sqlite_version}.mod") or ""
    return dict(re.findall(r"^\s*(modernc\.org/[\w./-]+) (v\S+)", gomod, re.M))


def tag_exists(repo, tag):
    refs = json.loads(fetch(f"https://api.github.com/repos/{repo}/git/matching-refs/tags/{tag}") or "[]")
    return any(r["ref"] == f"refs/tags/{tag}" or r["ref"].startswith(f"refs/tags/{tag}.") for r in refs)


def docker_tag_exists(image, tag):
    repo = image if "/" in image else f"library/{image}"
    return fetch(f"https://hub.docker.com/v2/repositories/{repo}/tags/{tag}") is not None


def main():
    root = Path.cwd()
    mod = json.loads(go("mod", "edit", "-json"))
    requires = {r["Path"]: r for r in mod.get("Require", [])}
    problems = []

    print("== Go toolchain (go.mod)")
    current = (mod.get("Toolchain") or "go" + mod["Go"])[2:]
    stable = sorted({r["version"][2:] for r in json.loads(fetch("https://go.dev/dl/?mode=json")) if r.get("stable")},
                    key=vkey)
    same_minor = [v for v in stable if prefix(v, 2) == prefix(current, 2)]
    print(f"  go directive {mod['Go']}, toolchain go{current}  (supported releases: {', '.join(stable)})")
    if not same_minor:
        print(f"  go{prefix(current, 2)} is NO LONGER SUPPORTED: move to go{stable[-1]}")
    elif vkey(same_minor[-1]) > vkey(current):
        print(f"  patch go{same_minor[-1]} available")
    else:
        print("  toolchain is the latest patch")
    if same_minor and prefix(stable[-1], 2) != prefix(current, 2):
        print(f"  new Go release go{stable[-1]} available")

    sqlite = requires.get(LOCKSTEP_PARENT, {}).get("Version")
    wants = lockstep_wants(sqlite) if sqlite else {}

    print("== Go modules (go.mod)")
    outdated, uptodate, sqlite_update = [], 0, None
    for m in json_stream(go("list", "-m", "-u", "-json", "all")):
        req = requires.get(m["Path"])
        if not req:
            continue
        update = m.get("Update", {}).get("Version")
        if m["Path"] == LOCKSTEP_PARENT:
            sqlite_update = update
        if not update:
            uptodate += 1
            continue
        tags = (" (indirect)" if req.get("Indirect") else "") + ("  [lockstep: see below]" if m["Path"] in wants else "")
        outdated.append((bool(req.get("Indirect")), f"  {m['Path']:58} {m['Version']:36} -> {update}{tags}"))
    for _, line in sorted(outdated):
        print(line)
    print(f"  {uptodate} more up to date")

    if sqlite:
        print(f"== modernc lockstep (go.mod must match what {LOCKSTEP_PARENT} {sqlite} requires)")
        for path, want in sorted(wants.items()):
            have = requires.get(path, {}).get("Version")
            if have is None:
                continue
            ok = have == want
            print(f"  [{'OK' if ok else 'MISMATCH'}] {path} {have} {'==' if ok else '!='} {want}")
            if not ok:
                problems.append(f"{path} {have} != {want} (required by {LOCKSTEP_PARENT} {sqlite})")
        if sqlite_update:
            cand = lockstep_wants(sqlite_update)
            pins = ", ".join(f"{p} {v}" for p, v in sorted(cand.items()) if p in requires)
            print(f"  candidate {LOCKSTEP_PARENT} {sqlite_update}: set {pins}")

    print("== GitHub Actions")
    uses = set()
    for wf in sorted((root / ".github/workflows").glob("*.y*ml")):
        uses |= set(re.findall(r"uses:\s*([\w.-]+/[\w.-]+)(?:/[\w./-]+)?@v(\d+)", wf.read_text(encoding="utf-8")))
    for repo, major in sorted(uses):
        latest = int(major)
        while tag_exists(repo, f"v{latest + 1}"):
            latest += 1
        print(f"  {repo:45} v{major:<6} {'up to date' if latest == int(major) else f'MAJOR v{latest}'}")

    print("== Docker images (tags float, so patches arrive on rebuild; a newer tag is a deliberate change)")
    files = [root / "Dockerfile", root / "docker-compose.yml", *sorted((root / ".github/workflows").glob("*.y*ml"))]
    images = set()
    for f in files:
        if f.is_file():
            images |= set(re.findall(r"(?:FROM\s+|image:\s*)([a-z][a-z0-9./-]*):(\d+)(?:\.(\d+))?(-[\w.-]+)?",
                                     f.read_text(encoding="utf-8")))
    for image, major, minor, suffix in sorted(images):
        def tag(n):
            return f"{major}.{n}{suffix}" if minor else f"{n}{suffix}"
        start = int(minor or major)
        n = start
        while docker_tag_exists(image, tag(n + 1)):
            n += 1
        print(f"  {image + ':' + tag(start):35} {'newer ' + image + ':' + tag(n) if n > start else 'no newer tag'}")

    print("== Smoke test (Python in smoke.yml, pins in smoke_test/requirements.txt)")
    py = re.search(r"python-version:\s*\"?([\d.]+)", (root / ".github/workflows/smoke.yml").read_text(encoding="utf-8"))
    manifest = json.loads(fetch("https://raw.githubusercontent.com/actions/python-versions/main/versions-manifest.json"))
    newest_py = sorted({prefix(r["version"], 2) for r in manifest if r.get("stable")}, key=vkey)[-1]
    if py:
        print(f"  {'python':45} {py.group(1):12} {'up to date' if vkey(newest_py) <= vkey(py.group(1)) else 'newer ' + newest_py}")
    reqs = root / "smoke_test/requirements.txt"
    pins = re.findall(r"^([A-Za-z0-9_.\-]+)==([^\s#]+)", reqs.read_text(encoding="utf-8"), re.M) if reqs.is_file() else []
    cp = "cp" + newest_py.replace(".", "")
    for name, cur in pins:
        info = json.loads(fetch(f"https://pypi.org/pypi/{name}/json"))
        latest = info["info"]["version"]
        wheel = any(cp in u["filename"] or "py3-none-any" in u["filename"] for u in info["urls"])
        print(f"  {name:45} {cur:12} {'up to date' if latest == cur else 'latest ' + latest}"
              f"  (python {newest_py} wheel: {'yes' if wheel else 'NO'})")

    if problems:
        print("\nLOCKSTEP PROBLEMS:")
        for p in problems:
            print(f"  - {p}")
        return 1
    print("\nLockstep OK.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
