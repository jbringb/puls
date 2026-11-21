---
name: upgrade-deps
description: Check Puls's dependencies for new versions and known vulnerabilities (CVEs), upgrade them with the modernc lockstep rule, verify, and ship. Use for any Go toolchain, Go module, GitHub Actions, Docker image or smoke-test (Python/pip) upgrade or security check — and as the monthly scheduled upgrade run. Dependabot version-update PRs are turned off; this skill replaces them.
---

# Upgrade dependencies (security first)

Two scripts do the checking; run them from the repo root (`python` or `python3`):

- `scripts/check_versions.py` — newer versions of the Go toolchain, every module in `go.mod`, GitHub Actions, Docker images, the smoke test's Python and its pip pins, plus the **lockstep check** below. Exit 1 = lockstep broken.
- `scripts/vuln_scan.py` — govulncheck (Go modules **and** the standard library, with reachability) plus OSV for `smoke_test/requirements.txt`. Exit 1 = something with a fix available is vulnerable.

GitHub's Dependabot alerts never see the Go standard library, which is where the most urgent fixes land (a `net/http` crash only needs a toolchain patch). The image is `FROM scratch` with one static binary, so **`vuln_scan.py` covers everything that ships**; an empty alerts page proves nothing. The script pins govulncheck to go.mod's toolchain on purpose: a plain `go run golang.org/x/vuln/cmd/govulncheck@latest` can silently scan a different Go's stdlib.

## Lockstep rule (why the obvious "bump to latest" is wrong here)

`modernc.org/sqlite` is machine-translated C, tested only against the exact `modernc.org/libc`, `modernc.org/memory` and `modernc.org/mathutil` versions in its own `go.mod`. `go get -u ./...` always moves libc and memory past those, into a combination nobody tested. After every `go get -u`, set them back to the versions `check_versions.py` prints for the chosen sqlite (`go get modernc.org/libc@<v> modernc.org/memory@<v>`), then `go mod tidy`.

## Steps

1. **Start clean.** `git checkout main && git pull`. Unattended (scheduled) run: create branch `fix-deps-<yyyy-mm-dd>`; never push to `main`.
2. **Baseline.** Run both scripts; keep the output for the report.
3. **Pick the target set:**
   - **Security fixes always**, reachable or not (unreachable ones are cheap and keep scanners quiet). A stdlib finding means bumping the `toolchain` line: `go get toolchain@go<version>`. A module finding means `go get <module>@<fixed or latest>`. NO FIX means report it.
   - **Go toolchain:** the latest patch of the current minor, always. **New Go release** (e.g. 1.28): adopt it. Go only supports the two newest releases, so never fall more than one behind. Run `go get go@1.N.0 toolchain@go1.N.x`, change the Dockerfile to `golang:1.N-alpine` and "Go 1.N+" in `README.md` and `CLAUDE.md`, and read that release's section of https://go.dev/doc/godebug for changed defaults. CI follows automatically (`setup-go` reads the `toolchain` line).
   - **Modules:** `go get -u -t ./...`, then the lockstep fix above, then `go mod tidy`. A module that shows up as newly *added* is new supply chain: find out who pulls it (`go mod why -m <module>`) and say so in the report. Module majors (`/v2` paths) never arrive this way; report them if you notice one.
   - **GitHub Actions:** bump `uses: …@vN` to the major shown; skim the release notes for removed inputs first. Keep `go-version-file: go.mod`.
   - **Docker images:** tags float, so patches arrive on rebuild. `golang:1.N-alpine` moves with the Go minor (above). **Postgres majors are report-only** (`postgres:17-alpine` in `ci.yml` and `docker-compose.yml`): 18+ images moved the data directory, so the compose volume mount has to change, and Render's managed Postgres is upgraded separately.
   - **Smoke test:** bump the pins in `smoke_test/requirements.txt` to latest. Move `python-version` in `.github/workflows/smoke.yml` to the newest stable minor only when `check_versions.py` shows a wheel for it.
4. **Apply.** `go mod tidy` must leave `go.mod`/`go.sum` stable. Re-run `check_versions.py` until it says `Lockstep OK.`.
5. **Verify — all must pass:**
   - `check_versions.py` → `Lockstep OK.`; `vuln_scan.py` → `fixable 0`, `vulnerable pip pins: 0`.
   - `go build ./... && go vet ./...`
   - Tests **with Postgres** (Docker): start `docker run -d --rm --name puls-pgtest -e POSTGRES_PASSWORD=test -e POSTGRES_DB=puls_test -p 55432:5432 postgres:17-alpine`, wait for `docker exec puls-pgtest pg_isready -U postgres`, run `TEST_DATABASE_URL=postgres://postgres:test@localhost:55432/puls_test go test -count=1 ./...`, then `docker stop puls-pgtest`. Without Docker, `go test ./...` skips the Postgres tests by itself; say so. `-race` needs cgo; CI runs it.
   - The agent cross-compiles for CI's matrix: `CGO_ENABLED=0 GOOS=<linux|windows|darwin> GOARCH=<amd64|arm64> go build -o /dev/null ./cmd/puls-agent` (all 6).
   - **Image + smoke test** (Docker, free, about a minute): `docker build -t puls:deps-check .`; run it with `-e PULS_JWT_SECRET=<32+ chars> -e PULS_ADMIN_SECRET=<16+ chars, different> -p 58080:8080`; wait for `curl -sf localhost:58080/readyz`; `docker exec <container> /puls-server healthcheck` must exit 0 (in Git Bash, set `MSYS_NO_PATHCONV=1` or the path gets rewritten). Then run the smoke test with the Python version from `smoke.yml`: `BASE_URL=http://localhost:58080 ADMIN_SECRET=<same> SKIP_COMPOSE=1 uv run --no-project --python <3.x> --with-requirements smoke_test/requirements.txt python smoke_test/smoke.py` (or a venv + `pip install -r smoke_test/requirements.txt`). All 13 steps must pass. Stop the container and `docker rmi` the image. If you can't run it, write "smoke test not run" in the report.
   - If a bump breaks the build and the fix isn't obvious, fall back to the patch-only set (toolchain patch + security fixes) and report the blocker.
6. **Docs.** Only when the Go minor changed: "Go 1.N+" in `README.md` and the first line of `CLAUDE.md`.
7. **Ship.** One commit with a one-line Conventional Commits message (e.g. `chore(deps): bump Go toolchain to 1.27.5 and update modules`). No body and **no attribution trailers** (no `Co-Authored-By`, no "Generated with" line), in commits and PR bodies alike.
   - Interactive: commit and push only when the user asks.
   - Unattended: push the branch and open a PR titled with the commit subject. Body: what changed (old → new), the before/after scan results, what was verified (and what wasn't, e.g. smoke test), and **report-only items** (Postgres majors, NO FIX advisories, blockers). If nothing needs changing and nothing is vulnerable, don't open a PR — just report that.

## Report (always)

Plain-language summary: what was vulnerable and how it was fixed, what was upgraded, what was deliberately left alone and why (lockstep pins, Postgres majors), what was verified, and anything that could bite later.
