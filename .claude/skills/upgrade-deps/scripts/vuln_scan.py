#!/usr/bin/env python3
"""Scan Puls for known vulnerabilities: govulncheck for Go, OSV for the smoke test's pip pins.

Run from the repository root:  python .claude/skills/upgrade-deps/scripts/vuln_scan.py

govulncheck also checks the Go standard library, but it reports the stdlib of whichever Go runs it,
and a plain `go run golang.org/x/vuln/cmd/govulncheck@latest` can silently switch to another Go (one
scan here once checked go1.26 while CI built with go1.25.0). So this builds govulncheck and scans
with exactly the toolchain go.mod pins. The Docker image is FROM scratch with one static binary, so
this covers everything the image ships, which Dependabot alerts don't (they never see the stdlib).

Each finding is reported at its most precise level: CALLED (our code reaches the vulnerable function),
IMPORTED (a vulnerable package is imported, the function isn't reached) or REQUIRED (only the module
is in go.mod). Exit code 1 when any finding has a fixed version; advisories without a fix are listed
but don't fail the scan.
"""

import json
import os
import re
import subprocess
import sys
import tempfile
import urllib.request
from pathlib import Path

LEVELS = ["REQUIRED", "IMPORTED", "CALLED"]


def json_stream(text):
    dec, i, objs = json.JSONDecoder(), 0, []
    while True:
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            return objs
        obj, i = dec.raw_decode(text, i)
        objs.append(obj)


def ascii(s, n=90):
    return s.encode("ascii", "replace").decode()[:n]


def govulncheck():
    mod = json.loads(subprocess.run(["go", "mod", "edit", "-json"], capture_output=True, encoding="utf-8", errors="replace", check=True).stdout)
    toolchain = mod.get("Toolchain") or "go" + mod["Go"]
    bindir = tempfile.mkdtemp(prefix="govulncheck-")
    # +auto: build with go.mod's toolchain, or newer if govulncheck needs it; never older, or it can't
    # type-check our code ("package requires newer Go version").
    built = subprocess.run(["go", "install", "golang.org/x/vuln/cmd/govulncheck@latest"], capture_output=True,
                           text=True, env=dict(os.environ, GOBIN=bindir, GOTOOLCHAIN=toolchain + "+auto"))
    if built.returncode != 0:
        sys.exit(f"go install govulncheck failed:\n{built.stderr[-2000:]}")
    exe = Path(bindir) / ("govulncheck.exe" if os.name == "nt" else "govulncheck")
    # GOTOOLCHAIN alone isn't enough: some of govulncheck's own `go` calls end up on whatever Go is
    # first on PATH, which then type-checks the newer stdlib at too old a language version.
    goroot = subprocess.run(["go", "env", "GOROOT"], capture_output=True, encoding="utf-8", errors="replace", check=True,
                            env=dict(os.environ, GOTOOLCHAIN=toolchain)).stdout.strip()
    path = os.pathsep.join([os.path.join(goroot, "bin"), os.environ.get("PATH", "")])
    out = subprocess.run([str(exe), "-json", "./..."], capture_output=True, encoding="utf-8", errors="replace",
                         env=dict(os.environ, GOTOOLCHAIN=toolchain, PATH=path))
    if out.returncode != 0:
        sys.exit(f"govulncheck failed:\n{out.stderr[-2000:]}")
    return toolchain, json_stream(out.stdout)


def go_findings(messages):
    summaries = {m["osv"]["id"]: m["osv"].get("summary", "") for m in messages if "osv" in m}
    scanned = next((m["config"].get("go_version") for m in messages if "config" in m), None)
    by_id = {}
    for m in messages:
        f = m.get("finding")
        if not f:
            continue
        frame = f["trace"][0]
        level = 2 if frame.get("function") else 1 if frame.get("package") else 0
        prev = by_id.get(f["osv"])
        if prev is None or level > prev["level"]:
            by_id[f["osv"]] = {"level": level, "module": frame["module"], "version": frame.get("version", "?"),
                               "fixed": f.get("fixed_version", ""), "summary": summaries.get(f["osv"], "")}
    return scanned, by_id


def pip_hits():
    reqs = Path("smoke_test/requirements.txt")
    pins = re.findall(r"^([A-Za-z0-9_.\-]+)==([^\s#]+)", reqs.read_text(encoding="utf-8"), re.M) if reqs.is_file() else []
    if not pins:
        return pins, []
    queries = [{"package": {"ecosystem": "PyPI", "name": n}, "version": v} for n, v in pins]
    req = urllib.request.Request("https://api.osv.dev/v1/querybatch", data=json.dumps({"queries": queries}).encode(),
                                 headers={"Content-Type": "application/json"})
    results = json.load(urllib.request.urlopen(req, timeout=60))["results"]
    return pins, [(pins[i], [v["id"] for v in r["vulns"]]) for i, r in enumerate(results) if r.get("vulns")]


def main():
    toolchain, messages = govulncheck()
    scanned, findings = go_findings(messages)
    print(f"govulncheck: go.mod toolchain {toolchain}, scanned stdlib {scanned}")
    fixable = 0
    for vid, f in sorted(findings.items(), key=lambda kv: (-kv[1]["level"], kv[0])):
        fixable += bool(f["fixed"])
        hint = "  (bump the go.mod toolchain line)" if f["module"] == "stdlib" and f["fixed"] else ""
        print(f"  {LEVELS[f['level']]:8} {vid} {f['module']}@{f['version']} "
              f"{'fixed in ' + f['fixed'] if f['fixed'] else 'NO FIX'} - {ascii(f['summary'])}{hint}")
    counts = {lvl: sum(f["level"] == i for f in findings.values()) for i, lvl in enumerate(LEVELS)}

    pins, hits = pip_hits()
    print(f"OSV (smoke_test/requirements.txt): {len(pins)} pins checked")
    for (name, version), ids in hits:
        print(f"  VULNERABLE {name}=={version}")
        for vid in ids:
            vuln = json.load(urllib.request.urlopen(f"https://api.osv.dev/v1/vulns/{vid}", timeout=30))
            fixed = sorted({e["fixed"] for a in vuln.get("affected", []) if a["package"]["name"].lower() == name.lower()
                            for r in a.get("ranges", []) for e in r.get("events", []) if "fixed" in e})
            print(f"    {vid} fixed in {', '.join(fixed) or '?'} - {ascii(vuln.get('summary', ''))}")

    print(f"go findings: called {counts['CALLED']}, imported {counts['IMPORTED']}, required {counts['REQUIRED']} "
          f"(fixable {fixable}); vulnerable pip pins: {len(hits)}")
    return 1 if fixable or hits else 0


if __name__ == "__main__":
    sys.exit(main())
