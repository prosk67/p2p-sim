# Security audit — 2026-11-16

Scope: whole repository at `993f187` plus the pool resource-usage change
(`GET /resources`, `resources` in `/api/cluster`). Follow-up to
`2026-11-04-security-audit.md`; findings there that still hold are restated
here, not repeated in full.

Method: manual review of the HTTP surface, subprocess handling, storage and
container config; live probes against the Compose stack on loopback;
`npm audit`. **Not run:** `govulncheck` and `pip-audit` (fetching them was
blocked in this session), so Go and Python dependency status is unverified
beyond what is noted in §3.

## 1. Summary

No critical issue and nothing exploitable from the network in the default
loopback-only setup. The real exposure is the **browser on the same machine**
(M-1, M-2) and **any deployment beyond loopback**, where the system has no
usable authentication (M-3).

| ID | Severity | Finding | Status |
|----|----------|---------|--------|
| M-1 | Medium | Any website can start runs on the gateway and on nodes (cross-site POST) | open (was L-1, gateway only) |
| M-2 | Medium | No `Host` check: DNS rebinding gives a hostile page full API access | new |
| M-3 | Medium (off loopback) | No usable auth once ports leave loopback | open (was L-2/L-3) |
| L-1 | Low | Live streams can pin every simulation slot indefinitely | new |
| L-2 | Low | `/api/cluster` fans out to N² + 2N node requests, uncached | new |
| L-3 | Low | `GET /resources` discloses machine size and load | new (this change) |
| L-4 | Low | Unbounded partition sessions from a peer | open (was L-4) |
| D-1 | High (dev only) | Vite 8.3.2 dev-server advisories | open |
| D-2 | Info | Floating base-image tags; `node/go.mod` says `go 1.22` | open |
| I-1 | Info | `.gitignore` has no `.env*` rule | open |

## 2. Findings

### M-1 Cross-site requests can start runs
Neither the gateway nor the nodes check `Content-Type`, `Origin` or
`Sec-Fetch-Site` on state-changing requests, so a "simple" cross-origin
request needs no CORS preflight. Verified:

```
POST localhost:8090/api/runs  Content-Type: text/plain  Origin: http://evil.example  -> 202
POST localhost:8082/run       Content-Type: text/plain  Origin: http://evil.example  -> 202
```

Any page open in a browser on the host can start batches (up to 100,000
replications), occupy the coordinator slot so the user's own runs get 409,
and fill run history. It cannot read responses. The nodes are affected too
because Compose publishes them on `127.0.0.1:8081-8083`.

Fix: on every non-GET route in `gateway/internal/server` and the node
handlers, require `Content-Type: application/json` and reject requests whose
`Sec-Fetch-Site` is `cross-site` (or whose `Origin` is not the server's own).

### M-2 DNS rebinding
Both servers answer any `Host` header (`Host: evil.example:8090` -> 200 on
`/api/cluster`). A page that rebinds its own domain to `127.0.0.1` becomes
same-origin with the gateway, so it can also *read* everything: run history,
reports, cluster layout, resource usage. The token (when set) is the only
barrier, and the GUI cannot use it (M-3).

Fix: allow-list `Host` (`localhost`, `127.0.0.1`, `[::1]`, plus configured
names) in the gateway and node middleware; answer 421 otherwise.

### M-3 No usable authentication beyond loopback
- Node-to-node and client-to-node traffic is unauthenticated by design. Anyone
  who can reach a node port can start work, open and delete partition
  sessions, and read `/resources`.
- `GATEWAY_TOKEN` protects `/api/*`, but the GUI cannot send it, so a
  gateway that serves the GUI runs without auth.

Fine on one machine with loopback binds. For the planned real-machine
deployment it means the LAN is the trust boundary. Before setting
`NODE_BIND`/`GUI_BIND` to anything else: add a shared peer token (or mTLS)
between nodes, let the GUI send the gateway token, and refuse to start on a
non-loopback address with an empty token. Until then, keep the ports on a
VPN or firewalled to the cluster machines.

### L-1 Live streams can pin simulation slots
`GET /stream` and `POST /netstream` each hold one slot for the life of the
stream. `speed` only has to be positive, so `duration=3600&speed=0.000001`
holds a slot for decades (`sim/stream.go` timeout is `duration/speed + 15s`).
Two such requests per node (default `max_concurrent_tasks: 2`) leave batches
nowhere to run. Fix: bound wall-clock length (e.g. `duration/speed <= 1h`)
and cap concurrent streams below the slot count.

### L-2 Cluster probe amplification
One unauthenticated `GET /api/cluster` makes the gateway call `/health`,
`/peers` and `/resources` on each of N nodes, and each `/peers` makes that
node health-check all N peers. No caching or rate limit. Fix: cache the
snapshot for 1–2 s in the gateway.

### L-3 Resource usage is readable without auth
`GET /resources` (new) returns core count, memory size and use, process
count and network rates; outside a container these describe the whole
machine. Read-only and no identifiers, paths or process names, so it is
consistent with the trusted-pool model, but it is reconnaissance data and
falls under M-3 when ports are exposed. The sampler reads fixed paths under
`/sys/fs/cgroup` and `/proc` only; no request input reaches the filesystem.
The gateway caps the pass-through body at 64 KiB and only forwards JSON
objects.

### L-4, D-2, I-1
Unchanged from the previous audit: partition sessions are bounded only by
slots and a 60 s idle reaper; Dockerfiles use floating tags (`golang:1.27`,
`python:3.11-slim`, `node:22-alpine`) and `node/go.mod` declares `go 1.22`;
`.gitignore` does not exclude `.env*`.

## 3. Dependencies

- **npm:** `npm audit` reports one high-severity package, `vite 8.3.2` (six
  advisories, all dev-server: `launch-editor` command execution, files served
  outside `server.fs`, cross-origin script execution). The production bundle
  the gateway embeds is not affected, and the dev server binds `127.0.0.1`.
  Fix is now `vite 8.3.8`.
- **Go:** the images currently built contain binaries compiled with
  **go1.27.3** (`go version` on the extracted binaries), which satisfies the
  previous audit's ">= 1.27.2". Whether newer stdlib advisories apply is
  unverified — run `govulncheck ./...` in `node/` and `gateway/`.
- **Python:** `simpy==4.1.1` only; `pip-audit` not re-run.

## 4. Confirmed still good

- No shell anywhere: every subprocess is `exec.Command(python, script, …)`
  with numeric argv; scenarios go on stdin.
- No `eval`/`exec`/`pickle`/`yaml.load` in `sim/`; no `innerHTML`,
  `dangerouslySetInnerHTML` or `eval` in `web/src`.
- Request bodies and node responses are size-capped; unknown JSON fields are
  rejected; SQL uses placeholders only.
- Gateway sends a strict CSP, `X-Frame-Options: DENY`, `nosniff`,
  `Referrer-Policy: no-referrer`, and no CORS headers.
- Stream and run targets are looked up in the configured peer list, so the
  gateway cannot be pointed at arbitrary hosts.
- No secrets in the tree or in git history (`web/.env.gateway` holds only
  `VITE_USE_MOCK=false`).
- Containers: read-only root, all capabilities dropped,
  `no-new-privileges`, non-root users, PID and memory limits, loopback-only
  published ports.

## 5. Suggested order

1. M-1 and M-2 together (one small middleware in each server).
2. Bump Vite to 8.3.8.
3. L-1 (stream wall-clock cap), L-2 (cache the cluster snapshot).
4. M-3 before any deployment that leaves loopback.
5. Run `govulncheck` and `pip-audit`; pin base images; add `.env*` to `.gitignore`.
