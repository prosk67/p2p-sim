# Security audit

The project's current security audit: code review, live probes and automated
scans in one place. Re-run the scans with `scripts/security-audit.sh` (see
§7). The earlier audit, `2026-11-04-security-audit.md`, is kept as history.

- **Last updated:** 2026-11-16
- **Scope:** whole repository at `61939e9` (including the pool resource-usage
  change: node `GET /resources`, `resources` in `/api/cluster`)
- **Method:**
  - manual review of the HTTP surface, subprocess handling, storage and
    container config;
  - live probes against the Compose stack on loopback;
  - automated scans with the tools below.

| Tool | Purpose | Version used |
|---|---|---|
| trivy | dependency and image CVEs, Dockerfile config checks | 0.76.2 |
| pip-audit | Python dependency audit | 2.10.1 |
| npm audit | web dependency audit | npm 26.12.1 |
| bandit | Python SAST | 1.9.4 |
| semgrep (`p/golang`, `p/typescript`, `p/python`) | Go, TypeScript, Python SAST | container |
| gitleaks | secrets, full git history | container |
| hadolint | Dockerfile lint | container |

`govulncheck` was not run. Trivy found no vulnerable Go modules, but it
does not check which standard-library functions the code actually calls.

## 1. Summary

There is no critical issue in the application code. Nothing is exploitable
from the network in the default loopback-only setup.

There are three real exposures:

- **The browser on the same machine (M-1, M-2).** Any website the user visits
  can start runs and, through DNS rebinding, read the API.
- **Any deployment beyond loopback (M-3).** The system has no usable
  authentication.
- **Stale base images (D-2).** The web image has one critical and 56 high CVEs,
  all fixed by a rebuild.

| ID | Severity | Finding |
|----|----------|---------|
| M-1 | Medium | Any website can start runs on the gateway and on nodes (cross-site POST) |
| M-2 | Medium | No `Host` check: DNS rebinding gives a hostile page full API access |
| M-3 | Medium (off loopback) | No usable authentication once ports leave loopback |
| L-1 | Low | Live streams can hold every simulation slot indefinitely |
| L-2 | Low | `/api/cluster` fans out to N² + 2N node requests, uncached |
| L-3 | Low | `GET /resources` discloses machine size and load |
| L-4 | Low | Partition sessions from a peer are bounded only by slots |
| D-1 | High (dev server only) | Vite 8.3.2 advisories |
| D-2 | Critical/High (images) | Outdated packages in the web and node base images |
| D-3 | Low | 173 bandit findings, all LOW and accepted |
| D-4 | Info | hadolint: 3 cosmetic Dockerfile findings |
| I-1 | Info | `.gitignore` has no `.env*` rule; base images use floating tags; `node/go.mod` declares `go 1.22` |

| Area | Result |
|---|---|
| Secrets (working tree and full history) | ✅ none (gitleaks) |
| Go dependencies (gateway, node) | ✅ clean (trivy) |
| Python dependencies | ✅ clean (trivy, pip-audit) |
| SAST (semgrep, 267 rules, 105 files) | ✅ 0 findings |
| Dockerfile config (trivy) | ✅ 0 misconfigurations |
| Gateway image (distroless) | ✅ 5 UNKNOWN only |
| Compose and container hardening | ✅ strong |

## 2. Application and runtime findings

### M-1 Cross-site requests can start runs
Neither the gateway nor the nodes check `Content-Type`, `Origin` or
`Sec-Fetch-Site` on requests that change state. A "simple" cross-origin
request therefore needs no CORS preflight. Verified:

```
POST localhost:8090/api/runs  Content-Type: text/plain  Origin: http://evil.example  -> 202
POST localhost:8082/run       Content-Type: text/plain  Origin: http://evil.example  -> 202
```

A page open in any browser on the host can do the following. It cannot read
the responses.

- Start batches of up to 100,000 replications.
- Hold the coordinator slot, so the user's own runs get 409.
- Fill the run history.

The nodes are affected too, because Compose publishes them on
`127.0.0.1:8081-8083`.

**Fix:** on every non-GET route in `gateway/internal/server` and the node
handlers, require `Content-Type: application/json`. Also reject requests
whose `Sec-Fetch-Site` is `cross-site`, or whose `Origin` is not the
server's own.

### M-2 DNS rebinding
Both servers answer any `Host` header (`Host: evil.example:8090` gets 200 on
`/api/cluster`). A page that rebinds its own domain to `127.0.0.1` becomes
same-origin with the gateway. It can then also *read* everything: run
history, reports, cluster layout and resource usage. The token, when set, is
the only barrier, and the GUI cannot use it (M-3).

**Fix:** in the gateway and node middleware, allow only known `Host` values
(`localhost`, `127.0.0.1`, `[::1]`, plus configured names). Answer 421
otherwise.

### M-3 No usable authentication beyond loopback
- **Nodes:** node-to-node and client-to-node traffic is unauthenticated by
  design. Anyone who can reach a node port can start work, open and delete
  partition sessions, and read `/resources`.
- **Gateway:** `GATEWAY_TOKEN` defaults to empty. It is checked correctly when
  set (constant-time Bearer compare), but the GUI cannot send it. A gateway
  that serves the GUI therefore runs without auth.

This is fine on one machine with loopback binds. On real machines the LAN
becomes the trust boundary. Before setting `NODE_BIND` or `GUI_BIND` to
anything else:

- add a shared peer token (or mTLS) between nodes;
- let the GUI send the gateway token;
- refuse to start on a non-loopback address with an empty token.

Until then, keep the ports on a VPN or firewalled to the cluster machines,
and document this in the README security section.

### L-1 Live streams can hold simulation slots indefinitely
`GET /stream` and `POST /netstream` each hold one slot for the life of the
stream. `speed` only has to be positive, so `duration=3600&speed=0.000001`
holds a slot for decades (the timeout in `sim/stream.go` is
`duration/speed + 15s`). Two such requests per node (default
`max_concurrent_tasks: 2`) leave batches nowhere to run.

**Fix:** bound the wall-clock length (for example `duration/speed <= 1h`),
and cap concurrent streams below the slot count.

### L-2 Cluster probe amplification
One unauthenticated `GET /api/cluster` makes the gateway call `/health`,
`/peers` and `/resources` on each of N nodes. Each `/peers` call then makes
that node health-check all N peers. Nothing is cached or rate-limited.

**Fix:** cache the snapshot in the gateway for 1–2 s.

### L-3 Resource usage is readable without auth
`GET /resources` returns core count, memory size and use, process count and
network rates. Outside a container these describe the whole machine.

- **Low risk:** it is read-only and has no identifiers, paths or process
  names. It fits the trusted-pool model, but it is reconnaissance data and
  falls under M-3 when ports are exposed.
- **Input handling:** the sampler reads fixed paths under `/sys/fs/cgroup` and
  `/proc` only, and no request input reaches the filesystem.
- **Gateway:** the gateway caps the pass-through body at 64 KiB and forwards
  only JSON objects.

### L-4 Partition sessions
A peer can open partition sessions until slots run out. Idle sessions are
reaped after 60 s and messages are capped at 8 MiB. This is acceptable inside
the trusted pool. Add a global cap if nodes are ever exposed.

## 3. Dependency and image findings

### D-1 Vite 8.3.2 (web dev dependency)
Trivy and `npm audit` report the same six advisories:

| CVE | Severity |
|---|---|
| CVE-2026-112942 | HIGH |
| CVE-2026-112943, -112944, -112946, -112947 | MEDIUM |
| CVE-2026-112945 | LOW |

They are dev-server issues: `launch-editor` command execution, files served
outside `server.fs`, and cross-origin script execution.

- **Exposure:** the production bundle the gateway embeds is not affected, and
  the dev server binds `127.0.0.1`.
- **Fix:** bump `vite` in `web/`. Trivy lists 8.3.3 as the first fixed
  release; `npm audit fix` offers 8.3.8.

### D-2 Base-image packages
- **Web image (`nginxinc/nginx-unprivileged:1.29-alpine`):** 238 findings,
  including 1 CRITICAL and 56 HIGH, and **all have fixes**.
  - CRITICAL: `brotli-libs` 1.2.0 → 1.2.1 (CVE-2026-111497).
  - HIGH: `curl`/`libcurl` (9 CVEs), OpenSSL `libcrypto3`/`libssl3`,
    `libexpat`, `libuuid`, `libxml2`, `libxslt`, `musl`, `pcre2`, `tiff`,
    `zlib`.
  - The image only serves static files on loopback, which limits exposure.
  - **Fix:** pull a fresh base (or move to 1.30-alpine), rebuild, and re-scan.
- **Node image (`python:3.11-slim`):** 225 findings (53 HIGH). Only 16 have
  fixes.
  - The HIGH set is mostly Debian `util-linux`/`bsdutils`, `ncurses`,
    `libsqlite3`, `systemd-libs` and `perl-base`, plus pip-bundled `wheel`
    and `jaraco.context`. These packages are largely unused at runtime, and
    the container hardening bounds the risk.
  - **Fix:** refresh the base, or run `apt-get upgrade` at build time, to clear
    the 16 fixable ones.
  - **Accept:** the rest.
  - **Longer term:** a distroless-style runtime (as the gateway already uses)
    would remove this noise.
- **Gateway image (distroless):** 5 UNKNOWN only. Its binaries were built
  with go1.27.3.

### D-3 bandit: 173 findings, all LOW (accepted)
- About 150 are B101, `assert` in pytest files.
- B311 is `random` in the simulator core. It must stay non-cryptographic.
- B404/B603 are `subprocess` calls in tests with fixed argument lists.

There are no MEDIUM or HIGH findings. The script runs bandit with
`--skip B101`, so real findings stand out.

### D-4 hadolint: 3 cosmetic findings
- DL3025 (shell-form `CMD`) in `node/Dockerfile` and `web/Dockerfile`.
  Switching to exec form changes PID 1 signal handling, so change it
  deliberately.
- DL3066 (non-numeric user) in `web/Dockerfile`. This is fine: the base image
  provides the user.

## 4. Confirmed still good
- **No shell anywhere.** Every subprocess is `exec.Command(python, script, …)`
  with numeric argv, and scenarios go on stdin.
- **No dynamic code.** There is no `eval`, `exec`, `pickle` or `yaml.load` in
  `sim/`, and no `innerHTML`, `dangerouslySetInnerHTML` or `eval` in
  `web/src`.
- **Bounded input.** Request bodies and node responses are size-capped,
  unknown JSON fields are rejected, and SQL uses placeholders only.
- **Headers.** The gateway sends a strict CSP, `X-Frame-Options: DENY`,
  `nosniff` and `Referrer-Policy: no-referrer`, and sends no CORS headers.
- **Fixed targets.** Run and stream targets are looked up in the configured
  peer list, and outbound clients refuse redirects. The gateway cannot be
  pointed at arbitrary hosts.
- **No secrets** in the tree or in git history (gitleaks).
  `web/.env.gateway` holds only `VITE_USE_MOCK=false`.
- **Containers:**
  - read-only root and all capabilities dropped;
  - `no-new-privileges` and non-root users;
  - PID and memory limits;
  - loopback-only published ports and `:ro` config mounts.

## 5. Remediation checklist

1. [ ] M-1 and M-2: one small middleware in each server (JSON content type,
       same-site check, `Host` allow-list).
2. [ ] D-1: bump `vite` in `web/`, rebuild.
3. [ ] D-2: refresh the web base image; refresh or `apt-get upgrade` the node
       base; re-scan.
4. [ ] L-1: wall-clock cap on live streams. L-2: cache the cluster snapshot.
5. [ ] M-3, before any deployment off loopback: peer token, GUI-sent gateway
       token, fail fast on an empty token; document it in the README.
6. [ ] I-1: add `.env*` (except `.env.gateway`) to `.gitignore`; pin base
       images; raise `node/go.mod` to `go 1.27`.
7. [ ] Optional: hadolint DL3025 exec-form fixes; distroless node runtime.

## 6. History
- **2026-11-04:** `2026-11-04-security-audit.md` at `c0c8b7f`.
- **2026-11-16:** this document. It merges a code and runtime review with an
  automated scan pass. The scan pass's generated summary is
  `security-audit-summary-2026-11-16.md`.

## 7. Re-running the scans

```sh
scripts/security-audit.sh            # everything, including image builds and scans
scripts/security-audit.sh --quick    # skip image builds and scans
scripts/security-audit.sh --report   # also write docs/security-audit-summary-<date>.md
```

Raw JSON results go to `results/security-audit/` (git-ignored). Override the
location with `SECURITY_AUDIT_OUT`.

**Setup:** install `trivy`, `bandit` and `pip-audit` on PATH or in `.venv/`.
gitleaks, hadolint and semgrep run as pinned Docker images, which you can
override with `GITLEAKS_IMAGE`, `HADOLINT_IMAGE` and `SEMGREP_IMAGE`.

The scans cover dependencies, images and code patterns only. They cannot find
issues like M-1 to L-4, so re-check those by hand when the HTTP surface
changes.
