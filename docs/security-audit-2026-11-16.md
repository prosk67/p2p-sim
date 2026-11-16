# Security Audit — 2026-11-16

Full security audit of the p2p-sim project. Re-run at any time with
`scripts/security-audit.sh` (raw JSON results land in
`/tmp/opencode/security-audit/results/`, override with `SECURITY_AUDIT_OUT=...`).
A compact machine-generated summary is written by `--report` to
`docs/security-audit-summary-<date>.md`.

## Tools used

| Tool | Purpose | Version |
|---|---|---|
| trivy | dependency + image CVEs, Dockerfile config checks | 0.76.2 |
| pip-audit | Python venv dependency audit | 2.10.1 |
| npm audit | web dependency audit | npm 26.12.1 |
| bandit | Python SAST | 1.9.4 |
| semgrep (p/golang, p/typescript, p/python) | Go + TS + Python SAST | latest container |
| gitleaks | secrets, full git history | latest container |
| hadolint | Dockerfile lint | latest container |

## Executive summary

| Area | Result | Action needed |
|---|---|---|
| Secrets (working tree + full history) | ✅ Clean | none |
| gateway Go deps | ✅ Clean | none |
| node Go deps | ✅ Clean | none |
| sim Python deps (venv + requirements) | ✅ Clean | none |
| web npm deps | ⚠️ 6 findings in `vite` (1 HIGH, 4 MEDIUM, 1 LOW) | **bump vite 8.3.2 → 8.3.3** |
| Python SAST (bandit) | ⚠️ 173 findings, all LOW | accept (see below) |
| Go/TS/Python SAST (semgrep, 267 rules / 105 files) | ✅ Clean | none |
| Dockerfile config checks (trivy, 27 per file) | ✅ Clean | none |
| Dockerfile lint (hadolint) | ⚠️ 3 findings (2 warnings, 1 info) | cosmetic; fix when convenient |
| gateway image (distroless) | ✅ 5 UNKNOWN only | none — best baseline |
| node image (python:3.11-slim) | ⚠️ 225 findings (53 HIGH), 209 without an available fix | accept most; apt-upgrade the 16 fixable |
| web image (nginx-unprivileged:1.29-alpine) | ❌ 238 findings, 1 CRITICAL, 56 HIGH — **all fixable** | **pull newer base + rebuild** |
| Compose hardening | ✅ Strong (manual review) | none |

Priority order: vite bump → web base image refresh → node apt upgrade.

## Findings and dispositions

### 1. web npm deps — vite 8.3.2 (fix: bump to 8.3.3)

| CVE | Severity | Fixed in |
|---|---|---|
| CVE-2026-112942 | HIGH | 8.3.3 / 8.2.4 / 8.1.6 / 7.3.7 / 6.4.4 |
| CVE-2026-112943, -112944, -112946, -112947 | MEDIUM | 8.3.3 / 8.2.4 |
| CVE-2026-112945 | LOW | 8.3.3 / 8.2.4 |

npm audit flags the same set via the launch-editor chain. Fixes are available in
8.3.3, a patch-level bump. **Action: `npm install vite@8.3.3` in `web/`, rebuild.**

### 2. web image — nginx-unprivileged:1.29-alpine (fix: refresh base image)

238 findings, all with fixes available in newer Alpine packages. Highlights:

- 1 CRITICAL: `brotli-libs` 1.2.0 → 1.2.1 (CVE-2026-111497)
- 56 HIGH: `curl`/`libcurl` 8.17.0-r1 (9 CVEs), `libcrypto3`/`libssl3` 3.5.6
  (OpenSSL, 2 CVEs), `libexpat`, `libuuid`, `libxml2`, `libxslt`, `musl`,
  `pcre2`, `tiff`, `zlib`

The runtime image only serves static files from `web/dist` via nginx, and the
container binds to loopback by default, which limits exposure — but the fix is
cheap: **`docker pull nginxinc/nginx-unprivileged:1.29-alpine` (or move to
1.30-alpine) and rebuild.** Add a periodic base-image refresh to the routine.

### 3. node image — python:3.11-slim (partially fixable, mostly accept)

225 findings: 16 fixable, 209 without an upstream fix. The HIGH set is dominated
by Debian's util-linux/bsdutils family (CVE-2026-76642, CVE-2026-78408/09/10 —
no fix released yet), ncurses (CVE-2025-69720), libsqlite3, systemd-libs, and
perl-base. Also 2 HIGH in pip-bundled `wheel` and `jaraco.context`.

Context: the node runtime is heavily sandboxed (read-only rootfs, no caps, no
new privileges, pids/memory limits, loopback-only binding), and the
vulnerabilities sit in packages largely unused at runtime. Disposition:

- **Accept** the unfixed Debian CVEs (209) — no patch exists; container
  hardening bounds the risk.
- **Accept** `wheel`/`jaraco.context` — pip build tooling present in the base
  image, not invoked by the node.
- **Fix** the 16 fixable ones via `apt-get upgrade` at build time, or refresh
  the python:3.11-slim base.
- **Consider** switching the runtime base to Alpine Python or distroless to
  shrink the surface long-term (the gateway's distroless image is the model to
  follow).

### 4. Python SAST — bandit, 173 findings, all LOW — accept

Breakdown: ~150 × B101 (`assert` in pytest test files — expected),
B311 `random` in the simulator core (statistical simulation, not security
sensitive — the PRNG must stay non-cryptographic by design), B404/B603
`subprocess` use in tests (fixed argument lists, no untrusted input). No
MEDIUM/HIGH findings. Disposition: **accept all**; recommend adding
`bandit -r sim --skip B101` to routine runs so real findings stand out
(`# nosec` or a bandit.ini to silence the accepted ones).

### 5. hadolint — 3 cosmetic findings — fix when convenient

- `node/Dockerfile:29` DL3025 (warning): CMD/ENTRYPOINT should use JSON
  (exec-form) notation.
- `web/Dockerfile:24` DL3025 (warning): same.
- `web/Dockerfile:17` DL3066 (info): non-numeric user id — acceptable, the
  nginx-unprivileged image provides the `nginx` user.

Exec-form also brings PID 1 signal handling into play; change deliberately.

### 6. Compose configuration — manual review (trivy does not scan compose)

Already strong:

- Shared `x-hardening` anchor: `read_only`, `cap_drop: ALL`,
  `no-new-privileges:true`, tmpfs `/tmp`, `pids_limit`, `mem_limit` everywhere
  (node-4 override repeats it manually — consistent).
- All host ports bound to `${NODE_BIND:-127.0.0.1}` / `${GUI_BIND:-127.0.0.1}`.
- Config volumes mounted `:ro`.

Notes:

- `GATEWAY_TOKEN` defaults to empty → the API is unauthenticated. This is a
  documented trade-off (the GUI cannot send a token yet). The gateway enforces
  it correctly when set, using `subtle.ConstantTimeCompare` on the Bearer
  token. Disposition: **accept for loopback use**; revisit before any
  non-loopback deployment.
- The API is unauthenticated **between nodes as well** (any node can accept
  `POST /run`); fine for a simulation on a trusted network, worth an explicit
  note in the README security section.

### 7. Secrets — clean

gitleaks scanned the working tree and the entire git history (~3.16 MB): no
leaks. Note for the future: `.gitignore` does not yet cover `.env` files; if
one is ever added for compose overrides, add `.env` to `.gitignore`.

## Remediation checklist

1. [ ] `web/`: bump `vite` to 8.3.3, rebuild bundle.
2. [ ] Refresh `nginxinc/nginx-unprivileged:1.29-alpine` base (or 1.30), rebuild web image, re-run trivy image scan to confirm the CRITICAL/HIGH set is gone.
3. [ ] `node/Dockerfile`: add `apt-get upgrade` step or refresh python:3.11-slim to clear the 16 fixable CVEs.
4. [ ] Optional: switch node runtime to a smaller base (distroless-style) to drop the unfixed-Debian noise.
5. [ ] Optional: hadolint DL3025 exec-form fixes in node/web Dockerfiles.
6. [ ] Add `.env` to `.gitignore` before ever committing one.
7. [ ] Document in README: unauthenticated node API + `GATEWAY_TOKEN` semantics and loopback-only default binding.

## Re-running the audit

```sh
scripts/security-audit.sh            # full: everything incl. image builds
scripts/security-audit.sh --quick    # skip image build/scan
scripts/security-audit.sh --report   # also write a compact summary to docs/
```

Tool setup (one-time): `pip install bandit pip-audit` into `.venv`; trivy is
installed at `.venv/bin/trivy`; gitleaks/hadolint/semgrep run via Docker images.
