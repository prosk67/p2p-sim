# Security audit

The project's current security audit: code review, live probes and automated
scans in one place. Re-run the scans with `scripts/security-audit.sh` (see
§7). The earlier audit, `2026-11-04-security-audit.md`, is kept as history.

- **Last updated:** 2026-11-24 (fixes applied; see the Status column and §5)
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

The 2026-11-16 review found three real exposures. The 2026-11-24 fixes close
or narrow all three:

- **The browser on the same machine (M-1, M-2).** Any website the user visited
  could start runs and, through DNS rebinding, read the API. **Fixed:** both
  servers now check `Host`, refuse cross-site browser requests (GET included)
  and require JSON bodies on the gateway.
- **Any deployment beyond loopback (M-3).** There was no usable
  authentication. **Narrowed:** an optional pool token now protects every node
  endpoint except `/health`. The GUI still cannot send `GATEWAY_TOKEN`, so the
  gateway relies on network placement (loopback or Tailscale).
- **Stale base images (D-2).** **Partly fixed:** the node image now applies
  Debian updates and drops unused build tooling, and the Go toolchain is
  pinned. The web (mock) image just needs a rebuild on a fresh base.

| ID | Severity | Finding | Status |
|----|----------|---------|--------|
| M-1 | Medium | Any website can start runs on the gateway and on nodes (cross-site requests, including GET streams) | **Fixed** 2026-11-24 |
| M-2 | Medium | No `Host` check: DNS rebinding gives a hostile page full API access | **Fixed** 2026-11-24 |
| M-3 | Medium (off loopback) | No usable authentication once ports leave loopback | **Partly fixed**: pool token for nodes; GUI token open |
| M-4 | Medium (off loopback) | Anyone reaching a node can kill or inject records into a running PDES/attack partition | **Fixed** when `POOL_TOKEN` is set; run ids now unguessable |
| L-1 | Low | Live streams can hold every simulation slot indefinitely | **Fixed** 2026-11-24 |
| L-2 | Low | `/api/cluster` fans out to N² + 2N node requests, uncached | **Fixed** (1 s cache) |
| L-3 | Low | `GET /resources` discloses machine size and load | Behind the pool token when set |
| L-4 | Low | Partition sessions from a peer are bounded only by slots | Open (accepted in a trusted pool) |
| D-1 | High (dev server only) | Vite 8.3.2 advisories | **Fixed**: Vite 8.3.8, `npm audit` clean |
| D-2 | Critical/High (images) | Outdated packages in the web and node base images | **Partly fixed** (node image); web image: rebuild |
| D-3 | Low | 173 bandit findings, all LOW and accepted | Accepted |
| D-4 | Info | hadolint: 3 cosmetic Dockerfile findings | **Fixed** (DL3025); DL3066 accepted |
| I-1 | Info | `.gitignore` has no `.env*` rule; base images use floating tags; `node/go.mod` declares `go 1.22` | **Mostly fixed**: `.env*` ignored, `go 1.27`, `golang:1.27.4` pinned; Python/nginx/node tags still float |

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

### M-4 Partition sessions can be tampered with (added 2026-11-24)
The partition endpoints on each node (`/partition/{run}/step`, `/finish`,
`DELETE`, and the `/attack/...` equivalents) did not check who was calling.
Anyone who could reach a node port and knew a run id could:

- delete or finish a partition, which fails the run;
- inject fake packet arrivals into a step, which silently changes the merged
  result. Only runs with the baseline comparison enabled would notice.

Run ids were predictable (prefix, node and timestamp) and are listed by
`/api/runs`.

**Fixed:**

- run ids now end in a random suffix, so they cannot be guessed;
- with `POOL_TOKEN` set, every node endpoint except `GET /health` requires
  the pool token, which peers and the gateway send automatically.

Without a token this stays as exposed as M-3: fine on loopback or behind the
Tailscale access policy, not on an open network.

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

Done on 2026-11-24:

1. [x] M-1, M-2: `Host` allow-list and cross-site refusal in both servers
       (`node/internal/guard`, gateway `guard` middleware); JSON-only POSTs on
       the gateway. Cross-site GETs (`/api/stream`, node `/stream`) are covered.
2. [x] D-1: Vite 8.3.8; `npm audit` reports 0 vulnerabilities.
3. [x] L-1: live streams capped at one hour of real time (`duration/speed`),
       and at most `max_concurrent_tasks - 1` streams per node (minimum one),
       so batches always have a slot. The GUI offers only speeds within the cap.
4. [x] L-2: the gateway caches the cluster view for 1 s.
5. [x] M-4 and the node half of M-3: optional `POOL_TOKEN`; random run ids.
6. [x] D-2 (node image): `apt-get upgrade` at build, setuptools/wheel removed,
       `golang:1.27.4` builder. D-4: exec-form `HEALTHCHECK` (DL3025).
7. [x] I-1: `.env*` ignored, `node/go.mod` raised to `go 1.27`.

Still open:

1. [ ] M-3 (gateway): let the GUI send `GATEWAY_TOKEN` (needs a sign-in step),
       and refuse to start on a non-loopback address with an empty token.
       Until then keep the gateway on loopback or behind `tailscale serve`.
2. [ ] D-2 (web image): rebuild on a fresh `nginx-unprivileged` base and
       re-scan; consider pinning Python, nginx and Node base tags.
3. [ ] L-4: global cap on open partition sessions if nodes are ever exposed.
4. [ ] Re-run `scripts/security-audit.sh` to confirm the image findings
       (not re-scanned after these changes).
5. [ ] Optional: distroless node runtime.

## 6. History
- **2026-11-04:** `2026-11-04-security-audit.md` at `c0c8b7f`.
- **2026-11-16:** this document. It merges a code and runtime review with an
  automated scan pass. The scan pass's generated summary is
  `security-audit-summary-2026-11-16.md`.
- **2026-11-24:** code review of the 2026-11-08 to 2026-11-19 changes found
  M-4 and widened M-1 to GET streams. M-1, M-2, M-4, L-1, L-2, D-1, D-4 fixed;
  M-3, D-2 and I-1 narrowed. Verified live on the Compose stack with a pool
  token: guards return 421/403/415/401 as intended, and an M/M/1 batch and a
  3-partition PDES run (results identical to baseline) completed.

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
