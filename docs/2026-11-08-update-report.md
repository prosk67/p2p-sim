# p2p-sim — Full Update Report

**Date:** 2026-11-08
**Repository:** `p2p-sim` at commit [`993f187`](https://github.com/) — *Make PDES runs visible and faster; declutter the Network lab* (2026-11-08). Working tree clean.
**Project age:** first commit 2026-09-23; 36 commits; merged PRs including #2 (gateway/GUI foundation) and #4 (UI declutter).
**Purpose:** a complete, self-contained statement of the project's current state — architecture, features, verification evidence (re-run today), performance measurements, security posture, known issues, and history — prepared so that a full polished report can be written from this document plus the cited source docs, without re-deriving anything from the code.

---

## 1. Executive summary

p2p-sim is a **distributed discrete-event simulation platform** on a symmetric peer-to-peer pool of three (scalable to more) Go worker nodes, with Python/SimPy simulation kernels and a React GUI served by a coordinating Go gateway.

The platform supports **six run kinds**:

1. **M/M/1 replication batches** — N independent seeded replications fanned out over the pool, aggregated with 95% CIs and checked against closed-form queueing theory (verdict PASS/FAIL), with an optional serial baseline for exact-match and speedup checks.
2. **Network replication batches** — the same replication pattern applied to a packet-level network model (routers, links, flows, failures), with per-flow / per-link / per-router metrics and CIs.
3. **PDES partitioned runs** — *one* network simulation split into k partitions across nodes, advancing in conservative lock-step windows; merged output is **byte-for-byte identical** to a single-process run of the same seed, and reports a measured speedup.
4. **Threat-campaign (attack) runs** — deterministic DDoS / SYN-flood / ransomware-worm / data-exfil campaigns, executable in-browser, on a single worker, or partitioned PDES-style over topology tokens (merged samples match the browser model exactly).
5. **Live M/M/1 streaming** — one replication paced to wall clock, streamed as Server-Sent Events to the browser with charts against theory.
6. **Live network streaming** — one network replication paced to wall clock, streamable from any simulated time point.

**Current state: everything implemented is working and verified.** A full end-to-end health pass was executed on 2026-11-08 (§6): all test suites green (82 Python, 12 Go node packages with race detector, gateway Go suites with race detector, 96 web tests + typecheck), the 4-container Docker cluster healthy, and a live distributed M/M/1 batch PASSED against theory with a 2.6× distributed speedup over the serial baseline. The PDES feature was previously verified end-to-end with a **1.84× speedup at k=3 and byte-for-byte identical results** versus the k=1 baseline, including clean failure handling when a partition node is killed mid-run (§7.2).

The most recent work (2026-11-04 → 2026-11-08) made PDES runs visible live (compute-vs-wait bars, window progress on the network map) and faster, and completed a 10-task **UI declutter** program that consolidated five stat-strip variants, chart primitives and page primitives into shared tested components.

Security posture is strong and was formally audited on 2026-11-04 (§9): no critical/high findings in application code; open items are toolchain/dependency hygiene (Go stdlib patches in Docker builder, Vite dev-server version) and four low-severity findings tied to the documented trusted-peer threat model.

---

## 2. System overview

```
 Browser (React GUI, web/)                #/cluster #/network #/attack #/traffic #/launch #/live #/report
        │  HTTP JSON + SSE, same origin
        ▼
 Gateway (Go, gateway/) ── SQLite run history (modernc.org/sqlite, pure Go), serves the embedded GUI
        │  HTTP to any node (static peer list)
        ▼
 Node pool (Go, node/) — N identical nodes, no permanent coordinator, loopback-published ports
        │  subprocess, JSON over stdin/stdout
        ▼
 Simulators (Python + SimPy, sim/)
     simulate.py (M/M/1)   netsim.py (packet network, PDES planner/worker/merge)
     attacksim.py (threat campaigns, same CLI/planner/worker protocol)
```

**Core principles (design invariants):**

- **Symmetric peers.** Every node runs the same binary and reads the same `config/peers.yaml` (static membership; no discovery or gossip). Whichever node receives a run request coordinates that run, sending work to all peers *including itself* (in-process, not over HTTP).
- **Go orchestrates, Python simulates.** They share only a JSON-on-stdio contract: stdout carries only JSON (one line per message/event), diagnostics go to stderr, exit 0 = success / 1 = failure.
- **Seeds determine everything.** `seed = base_seed + task_index` for replications; per-entity RNG streams (`sha256("{seed}:{kind}:{id}")`) inside scenarios; a deterministic partition planner and canonical cross-partition record order `(arrive, link, flow, seq)`. Results reproduce bit-for-bit anywhere, which is the basis for failover (resubmit same seed elsewhere) and the PDES equality oracle.
- **Run state lives only on the coordinator.** The gateway polls and persists snapshots; five consecutive failed polls (≈5 s) or a 404 marks a run `coordinator_lost`, with resubmit-with-same-seed offered in the GUI.
- **503 means busy, not failure.** Busy refusals never charge a retry attempt; backtrack: failed attempts requeue after up to `max_attempts` = 3, with exponential backoff until `/health` passes.
- **One code path for k=1 and k>1** in PDES/attack runs — the baseline uses the same worker, so partial changes surface as `results_identical: false`.

---

## 3. Component inventory (measured on working tree, 2026-11-08)

| Component | Language | Files | Lines | Role |
|---|---|---|---|---|
| `sim/` | Python (SimPy 4.1.1) | 6 | 3,261 | `simulate.py` M/M/1; `netsim.py` packet network (~1,100 lines) with `--validate/--seed/--stream/--plan/--partition/--merge`; `attacksim.py` campaigns with the same CLI/planner/worker protocol; test suites |
| `node/` | Go | 45 | 7,581 | Peer node: HTTP API, batch managers (M/M/1, network), dispatch (fan-out, retry, busy, health backoff), PDES coordinator + partition host, subprocess runners, SSE streaming |
| `gateway/` | Go | 15 | 3,749 | Public API + embedded GUI, run tracking with `coordinator_lost` detection, SQLite history, cluster snapshot, plan pass-through, SSE proxies |
| `web/` | TypeScript/React/Vite | 9 pages, 14 components | 9,522 | Network Lab, Threat Lab, Cluster, Live traffic, Launch, Live run, Report; mock API for offline dev |
| `config/` | YAML | 3 | — | `peers.yaml` / `peers.4.yaml` (membership), `node.yaml` (shared settings + run defaults) |
| `scripts/` | bash | 3 | — | `demo.sh`, `kill-node-test.sh`, `kill-coordinator-test.sh` |
| `docs/` | markdown | ~15 | — | Architecture briefing, API references, PDES design/implementation/test reports, security audit, assumptions, PRD, planning docs |

**Total:** ≈ 24,000 lines of source across three languages.

---

## 4. Feature-by-feature status

| Area | Status (2026-11-08) | Notes |
|---|---|---|
| M/M/1 batches + theory verdicts + serial baseline | ✅ Complete, verified today | Verdict PASS with CI overlap of theory for all 3 metrics (§6.2) |
| Dispatch: retry, busy handling, health backoff, shared concurrency limit | ✅ Complete | `max_concurrent_tasks` shared by batches, streams and PDES partitions; busy 503 never charges a retry |
| Network lab (topology editor, grid/torus generator, flows incl. step/ramp/on-off, failure events) | ✅ Complete | Routing = shortest path by latency, recomputed on every failure/recovery; 3 sample networks; JSON import/export |
| Network live view (netstream SSE, mid-run start, playback with seeking) | ✅ Complete | Seeking restarts the exact copy on a node from that time point |
| PDES planner (farthest-point seeds + slow-link cluster packing, lookahead, window count) | ✅ Complete | Deterministic; rejects plans > 200,000 windows |
| PDES execution (lock-step windows, cross-partition record exchange, live compute/wait stats) | ✅ Complete, E2E verified | 1.84× at k=3, identical results (§7.2) |
| PDES live visibility (last commit 993f187) | ✅ Complete | Window progress, live link/router utilization, colour-coded partition map |
| Threat Lab (4 campaigns; browser / single-worker / PDES-pooled modes; fallback to local model) | ✅ Complete | Merged samples match the browser model exactly; `parts=1` is the single-worker path |
| Attack live view + report (map, phases, event log, impact vs availability) | ✅ Complete | Shared window/timing components with PDES |
| Gateway run tracking + history (SQLite) + resubmit on `coordinator_lost`/failed | ✅ Complete | History `limit` clamps to 200; tracking resumes after gateway restart |
| UI declutter program (10 tasks, PR #4) | ✅ Complete (2026-11-06) | Shared `StatStrip`, `PagePrimitives`, `AxisFrame`/`LineChart`, `CopyJsonButton`, `PartitionTimingList`; merged run actions; single Cluster health story; report cards simplified |
| Security audit remediation | ⚠️ Partial | Dockerfiles still on unpatched `golang:1.27` builder and Vite 8.3.2 (audit checklist §9.4) |
| Leader election / automatic coordinator failover | ❌ Not built (design decision) | Documented as PRD stretch goal; manual resubmit instead |
| GUI token delivery (`GATEWAY_TOKEN` from browser) | ❌ Not built | Leave token unset when using the GUI (documented) |
| Per-flow drop-reason breakdown in network report | ⚠️ Intentionally omitted | Aggregate link/router drops present (§10.3) |
| 4th-node scaling (`docker-compose.node4.yml` + `peers.4.yaml`) | ✅ Complete | No code changes required; every participant must share the same peer file |

---

## 5. Run kinds and where they live

| Kind | Node entry point | Gateway API | GUI page |
|---|---|---|---|
| `mm1` (default) | `POST /run` | `POST /api/runs {kind:"mm1"}` | Launch → Live → Report |
| `network` | `POST /netrun` | `POST /api/runs {kind:"network"}` | Network Lab ("run on all nodes") |
| `pdes` | `POST /pdesrun` (+ `POST /pdes/plan` preview) | `POST /api/pdes/plan`, `{kind:"pdes"}` | Network Lab ("run partitioned", with partition preview and slowdown/faster hint from `splitOutlook`) |
| `attack` | `POST /attackrun` (+ `POST /attack/plan`) | `POST /api/attack/plan`, `{kind:"attack"}` | Threat Lab |
| live M/M/1 | `GET /stream` (SSE) | `GET /api/stream` (SSE proxy) | Traffic |
| live network | `POST /netstream` (SSE) | `POST /api/netstream` (SSE proxy) | Network Lab ("watch live") |

**Node HTTP surface (port 8000, container-published loopback only):** `/run`, `/netrun`, `/pdesrun`, `/pdes/plan`, `/attackrun`, `/attack/plan`, `/status`, `/report`, `/pdes/status`, `/pdes/report`, `/attack/status`, `/attack/report`, `/task`, `POST/DELETE /pdes/{run}/open|step|finish`, `/attack/{run}/…`, `/stream`, `/netstream`, `/health`, `/peers`.

**Gateway surface (port 8080 in container → 127.0.0.1:8090 on host):** `/healthz`, `/api/config`, `/api/cluster`, `POST/GET /api/runs`, `/api/runs/{id}` (+`/status`, `/report`), `/api/pdes/plan`, `/api/attack/plan`, `/api/stream`, `/api/netstream`; static SPA with fallback.

Run IDs are `base64url(["coordinator","batch_id"])`. Terminal states: `running`, `complete`, `failed`, `coordinator_lost`, `unknown`.

---

## 6. Verification — full pass executed 2026-11-08 (commit `993f187`)

### 6.1 Automated suites (all executed today)

| Suite | Command | Result |
|---|---|---|
| SimPy models (`sim/`) | `../.venv/bin/python -m pytest -q` | **82 passed** in 10.6 s — M/M/1, M/D/1, network (routing, failures, traffic patterns, partition == single-process, planner, worker protocol), attack campaigns, CLI contracts |
| Go node (12 packages, 2 without tests) | `go test -race ./...` (in `golang:1.27` container) | **all packages ok** — api, batch, config, dispatch, netbatch, pdes, peers, sim, stats, worker |
| Go gateway (6 packages with tests) | `go test -race ./...` (in `golang:1.27` container) | **all packages ok** — aggregate, config, nodeclient, runs, server, store |
| Web typecheck | `npm run typecheck` (tsc -b) | ok |
| Web tests | `npm test` (vitest) | **96 passed** (11 files) in 0.34 s |

### 6.2 End-to-end distributed M/M/1 batch (today)

Submitting through `scripts/demo.sh 20` (20 replications, λ = 0.8, μ = 1.0, sim time 10 000, warmup 1 000; node-1 coordinating over node-1..3):

| Metric | Distributed mean (n=20) | 95% CI half-width | Theory | Rel. error |
|---|---|---|---|---|
| Mean wait time | 4.856 (vs 5.0) | ±0.719 | 5.0 | ~2.9% |
| Mean queue length | 4.0022 | ±0.2389 | 4.0 | 0.054% |
| Utilization | 0.8007 | ±0.0081 | 0.8 | 0.086% |

Verdict: **PASS** — all three metrics within tolerance and theory inside every 95% CI. `serial_results_match_distributed: true`. Distributed wall clock **0.885 s** vs serial **2.31 s** → **2.61× speedup** with 3 peers.

### 6.3 Cluster health (today)

`docker compose -f docker-compose.yml -f docker-compose.web.yml up --build -d` rebuilt with the current commit's code: node-1..3 (`127.0.0.1:8081-8083`) and gateway (`127.0.0.1:8090`) **all healthy**; every node reports 3 peers; `GET /api/cluster` shows three healthy nodes, each seeing its peers unanimously (latency ≈ 0.7–0.8 ms).

---

## 7. PDES — design and verified performance

### 7.1 How it works (summary; full detail in `docs/2026-10-26-pdes-implementation.md` and the architecture doc §3–§5)

- **Planner (`partition_plan(sc, k)`):** deterministic, no randomness. Fold hosts into routers → pick k seed routers by farthest-point selection → grow regions breadth-first (smallest region next, lowest-latency frontier link) → also try *slow-link cluster packing* (drop links ≥ threshold, pack connected groups into k bins, accept when largest bin ≤ 1.25× fair share) and prefer it only when lookahead improves — so uniform grids stay unchanged and multi-site networks cut exactly on wide-area links. `lookahead = min over cut links of (1/bandwidth + delay)`; `windows = ceil(duration / lookahead)`.
- **Worker protocol (JSON lines on stdin/stdout):** `open` → repeated `step {until, inbound[]}` → `finish`. Records carry `hops` so the 64-hop limit holds across partitions (fixed 2026-11-08, with a dedicated test); inbound validation enforces `arrive ≥ start + 1/bw + delay` and the lookahead safety argument (a record produced in window `[T0, T0+L)` has `arrive ≥ T0+L`, so it is never in the receiver's past).
- **Ownership:** routers belong to their partition; link direction `a→b` belongs to `a`'s partition; hosts follow their router; flows are generated in the source host's partition, delays recorded in the destination's. Every partition holds the full scenario and applies every failure event, so routing tables stay identical everywhere without messages.
- **Coordinator loop:** plan with `--plan` → open all partitions in parallel → per window, step all partitions in parallel (each with its own `pdes_step_timeout`), route outbound records by `plan.assignment[record.to]`, sort inbound by `(arrive, link, flow, seq)` → collect per-partition `compute_s` / `wait_s` / messages / bytes / events / packet totals and `cut_traffic` per link → `finishWorkers` → `--merge` → if baseline enabled, run k=1 through the same worker and `compareJSON` (gives `results_identical` and `first_difference`, a JSON path); `speedup = baseline_wall / partitioned_wall`. The run stays `running` (phase `baseline`) until the comparison finishes, so a poll never sees a complete run without its comparison.
- **Failure handling:** any open/step/finish error or timeout → `DELETE` to every opened partition → state/phase `failed` with `failed_node` named. No checkpointing/automatic restart; the GUI offers "Rerun on surviving nodes" (same scenario and seed, k = survivors).
- **Partitioned limits** (validation with `partitioned=True`): 2,000 nodes, 6,000 links, 400 flows, 200 failure events — vs normal-mode limits of 60 / 120 / 40 / 60 and 150,000 offered packets.
- **Live visibility (commit `993f187`):** at most every 500 ms the coordinator asks for stats and converts cumulative busy times into per-link and per-router utilization deltas (`live`), drawn on the network map; per-partition compute-vs-wait bars (`partitionBreakdown`) and window progress on the same page for network *and* attack split runs.

### 7.2 Verified end-to-end measurements (2026-10-26 cluster, `docs/2026-10-26-pdes-test-report.md`)

Scenario: 5×5 router grid, 25 hosts, 30 flows at 300 pkt/s each = 9,000 pkt/s offered (540,000 offered packets total, duration 60 s); routers 1,200 pkt/s / buffer 200; links 1,000 pkt/s / 20 ms delay / buffer 100. Plan: parts 3, lookahead 0.021 s, **2,858 windows**, 16 cut links, load 9/8/8.

| Run | Seed | Parts | Result | `results_identical` | Speedup |
|---|---|---|---|---|---|
| 1 | 888 | 3 | complete | **true** | **1.84×** (22.71 s vs baseline 41.83 s) |
| 2 | 888 | 2 | complete | **true** | **1.40×** (29.61 s vs 41.57 s) |
| 3 | 888 | 3 (vs run 2) | complete | k=3 metrics **byte-for-byte equal to k=2** for the same seed | — |
| 4 | 777 | 3 | **failed** mid-run (`docker kill node-2` at window 124/2858) | — | `failed_node: node-2`, abort error precise (`step at 3.066: connect: connection refused`) |
| 5 | 888 | 2 (survivors) | complete | **true** | 1.40× |

Metrics-consistency audit on the merged report: `totals.sent` = Σ per-flow = 541,541; delivered 219,663; dropped 318,654; Σ link drops (7,006) + Σ router drops (311,648) = Σ flow drops; packets in flight at t = duration (3,224) identical to baseline; `series` mean throughput matches totals; per-partition timing fully populated (compute 11.3/15.5/17.4 s at k=3; ≈200k–312k messages; ≈28–44 MB per partition).

**Timing analysis (why the numbers look as they do):** partitioning redistributes work but doesn't remove it — Σ compute ≈ 44.2 s vs baseline 41.8 s (+6% coordination overhead). The wall floor is the busiest partition (17.4 s at k=3), ceiling ≈ 2.4×; the remaining gap is the coordinator's per-window routing overhead. Measured asymptotics across other scenarios: Regional ISP (4 ms cuts, 69,231 windows, ~0.6 packets/window) ran 0.11× (splitting doesn't pay); Three-site WAN ran 0.82× / 1.02× / 1.2× at 8,572 / 4,616 / 2,858 windows. The GUI turns packets-per-window into a slower / about even / faster hint (`splitOutlook` in `web/src/lib/pdes.ts`). Speedup depends on traffic distribution, not only on `parts`.

---

## 8. GUI state

- **Routes (hash-based):** `#/cluster`, `#/network`, `#/attack`, `#/traffic`, `#/launch`, `#/live/{run_id}`, `#/report/{run_id}`.
- **Network Lab:** canvas drawing (`TopologyCanvas.tsx`) or grid/torus generator (`generateGridScenario`, capped by shared `LIMITS`), flows (steady, step/ramp schedules, on/off bursts), failure events; watch live (SSE), run on all nodes, or run partitioned with a colour-coded partition preview; playback with play/pause, time slider, speed and copy picker everywhere the view is time-based (seeking restarts the exact copy from that moment).
- **Threat Lab:** animated attack map, target posture, threat index, campaign phases, event log, offered load vs capacity, impact vs availability; scrub/pause/replay; pick in-browser / one worker / PDES pool; local fallback on remote failure.
- **Cluster:** one health story (merged after the declutter), latency, who-sees-whom; peer matrix collapsed when healthy.
- **Launch:** M/M/1 parameters with live ρ/W/L preview, coordinator picker, "try another node" on 409.
- **Live:** branches by `kind` — M/M/1 progress, network player, PDES windows + compute/wait bars, attack windows/timing; `coordinator_lost`/`failed` offers resubmit with same `base_seed` (`lib/resubmit.ts`).
- **Report:** branches by `report.kind` — M/M/1 verdict + CIs + tasks-per-node + distributed-vs-serial timing; `NetworkReport` (per-flow/links/routers ± CI, charts with failures marked, load heatmap); `PDESReport` (speedup, `results_identical` badge, time breakdown, partition map, replay of the run's seed); `AttackReport` (summary, placement, peak snapshot).
- **Shared primitives (post-declutter):** `StatStrip` (5 variants unified), `PagePrimitives` (`PageHeading`, `SectionTitle`, `PanelTitle`, `Notice`, `EmptyState`, `LoadingState`), `AxisFrame`/`LineChart` (all live/network/attack/report charts migrated), `CopyJsonButton`, `PartitionTimingList`; dead History nav removed; PDES generator moved below the canvas; advanced knobs collapsed.
- **Dev mode:** `npm run dev` runs against a scripted mock API (worker-failure, coordinator-loss, busy, serial-mismatch scenarios on the Cluster screen); `npm run dev:gateway` against a real gateway; a mock-only container exists (`docker-compose.web.yml --profile mock`, port 8091).

---

## 9. Security posture (audit 2026-11-04 at commit `c0c8b7f`, `docs/2026-11-04-security-audit.md`)

**Overall: working condition confirmed, no critical/high application findings; posture "unusually strong for a research/simulation codebase".** Semantic-probe verification at audit time: 144 tests, `go vet` clean, production build clean.

**Verified strengths (selected):**
- No command injection: `os/exec` without shell; task parameters reach Python only as formatted floats/ints; scenario JSON delivered on **stdin, never argv**; subprocess env forwarded only `PATH`, `LANG`, `SIM_LOG_LEVEL`.
- Tight request parsing: `http.MaxBytesReader` (1 MiB default; 4 MiB + metadata for PDES/attack plans) + `DisallowUnknownFields` everywhere; gateway rejects trailing JSON.
- SSRF resistance: peers only from validated `peers.yaml`; outbound clients refuse redirects; run IDs path-escaped.
- Bounded outputs: response caps (1 MiB; 64 MiB partition replies), capped subprocess buffers; scenario size caps; result-integrity echo checks (`task_id`/`seed`).
- Gateway middleware: optional constant-time bearer token on `/api/*` (`crypto/subtle`), strict CSP, `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `no-store`; SPA static handler traversal-safe.
- None of SQL-injection / path-traversal / `innerHTML` / `eval` classes found; frontend validation mirrors server rules; seeds from `crypto.getRandomValues`.
- Container hardening: non-root uid 10001, `cap_drop ALL`, `no-new-privileges`, read-only rootfs, tmpfs `/tmp`, `pids_limit`, `mem_limit` 768 MB nodes / 256 MB gateway, config `:ro`, **all host ports loopback by default**; gateway runtime image is distroless (no shell).

**Findings / open remediation items (as of audit; re-verify before final report):**

| ID | Severity | Finding | Remediation |
|---|---|---|---|
| L-1 | Low | No CSRF/Origin enforcement on state-changing `/api/*` (non-JSON Content-Type accepted) | Reject non-JSON Content-Type and/or check Origin/Sec-Fetch-Site |
| L-2 | Low | Node-to-node API unauthenticated by design | Keep loopback default; add bearer/mTLS if ever exposed |
| L-3 | Low | `GATEWAY_TOKEN` optional; empty disables auth | Warn/fail fast on non-loopback bind with empty token |
| L-4 | Low | Unbounded per-run partition opens until slots exhaust (then 503); idle reaped at 60 s | Optional per-source cap; acceptable within trusted pool |
| Info | — | `.env*` not gitignored; `node/go.mod` `go 1.22` vs 1.27 toolchain | Hygiene |
| **Toolchain** | **High value/low effort** | Go stdlib vulns **reachable** from built binaries (11 gateway / 10 node classes at `golang:1.27.0`; net/http HTTP/2 HPACK race, trailer-memory, desync, Range DoS, crypto/tls handshake issues) — fixed in Go ≥ 1.27.2 | Bump Docker builder images to patched Go; add `govulncheck ./...` to CI |
| **npm** | **1 high (dev-server only)** | Vite 8.3.2 — 6 advisories incl. dev-server command-exec / cross-origin script / fs bypass (production bundle unaffected; dev binds 127.0.0.1) | `npm audit fix --force` → Vite ≥ 8.3.5; relock |
| ✅ | — | Go modules (yaml.v3, modernc sqlite) 0 reachable vulns; `pip-audit` clean (SimPy 4.1.1) | — |

---

## 10. Known issues and limitations (honest list for the report)

1. **Coordinator loss = batch loss.** Run state lives only on the coordinating node; there is no leader election or automatic failover. Recovery = resubmit the same `base_seed` elsewhere (seed ⇒ bit-for-bit reproduction). The gateway marks the run `coordinator_lost` after ~5 s of failed polling and offers resubmit; a returning coordinator can also resume a `coordinator_lost` record.
2. **Static membership.** `config/peers.yaml` shared by every node and the gateway; no discovery/gossip; 4-node scaling is config-only by design.
3. **One coordinated batch per node** (M/M/1 and network share `CoordinatorSlot`; PDES/attack manager has its own one-run check) — multiple coordinators at once are possible but their wall-clock timing figures then interfere.
4. **One local task at a time per node process** (plus shared `max_concurrent_tasks` slots across batches, streams and partitions). Multi-core scale = run more node processes.
5. **GUI generator capped at 60 nodes** while partitioned runs allow 2,000 — no PDES-specific generator size yet (known issue 1 in the architecture doc).
6. **Speedup timing asymmetry:** the partitioned wall includes worker start-up, the k=1 baseline excludes its own start — slightly *understates* speedup.
7. **No auth/TLS** within the pool by design (trusted static peers); mitigations = loopback publishing, container hardening, optional gateway token. GUI cannot send a token yet.
8. **Dependency hygiene (from audit):** Go builder images on unpatched `golang:1.27`; Vite 8.3.2 dev-server advisories; deps pinned by version/tag, not digest.
9. **No workflow congestion control** in the network model (deliberate simplification: FIFO single-server routers, fixed-size packets, no TCP).
10. **`/verify`-type structural validation cannot catch disconnected-host scenarios** (0-delivered runs are statistically legal); mitigated by "delivered > 0" probes in testing practice; suggested cheap fix documented (PDES test report §8.1).
11. **Live traffic page overlaps** with the Network Lab's live view; kept by product decision.
12. **Streaming/batch concurrency shares one slot pool**, so a busy node answers 503 for streams; documented, intended (protects nodes).

Also fixed on 2026-11-08 (architecture doc §12): hop limit restarting at partition boundaries (records now carry `hops`, with a regression test) and status showing `complete` before the baseline finished.

---

## 11. Recent history (newest first, condensed)

| Date | Commit(s) | Work |
|---|---|---|
| 2026-11-08 | `993f187` | Make PDES runs visible and faster; declutter the Network lab; two PDES correctness fixes (hops across partitions, baseline-before-complete status) |
| 2026-11-06 | PR #4 (`29deecf` + tasks 5–10) | UI declutter series completed: shared chart/page/stat primitives, merged run actions, single cluster health story, report-card simplifications, deduped traffic/attack facts |
| 2026-11-04 | `fed8a9e`, `18efc97`, `ca5015c` | Declutter phase 1 + tasks 2–4 (primitives, StatStrip, tested components) |
| 2026-11-04 | `d53f0a0` |Security-audit doc committed |
| 2026-11-03 | `c0c8b7f` | Worker-backed Threat Lab campaigns with PDES execution |
| 2026-10-26 | (working tree era) | PDES feature: design spec, ~1,700-line implementation across 29 files, PDES test report with 1.84× verified speedup |
| 2026-10-25 | `8d27a45` | PDES design spec committed |
| 2026-10-24 → 2026-10-23 | — | Minimal-gateway plan and spec |
| 2026-10-04 … earlier | PR #2 era (`28f2ea6`, `ffb0c9f`) | Gateway + Live/Report screens, Network lab, assumptions/gap docs, first GUI |

---

## 12. Reproduction sheet (everything above can be re-checked)

```bash
# Cluster (3 nodes + gateway/GUI)
docker compose -f docker-compose.yml -f docker-compose.web.yml up --build -d
open http://localhost:8090            # GUI;  :8081-8083 node APIs (loopback)

# Distributed M/M/1 demo batch (used for the numbers in §6.2)
scripts/demo.sh 20                    # verdict PASS, ~2.6x vs serial baseline

# Automated suites (as run today)
cd sim && ../.venv/bin/python -m pytest -q                 # 82 passed
cd node && go test -race ./...                             # all ok (needs Go ≥ 1.27)
cd gateway && go test -race ./...                          # all ok (ran in golang:1.27 container)
cd web && npm run typecheck && npm test                    # ok; 96 tests

# Failure drills
scripts/kill-node-test.sh node-2 100        # worker dies mid-batch → reassignment
scripts/kill-coordinator-test.sh 100        # coordinator dies → resubmit same seed

# PDES E2E (historical procedure): docs/pdes-testing.md + docs/2026-10-26-pdes-test-report.md
```

---

## 13. Source-of-truth documents (for the final report's citations)

| Topic | File |
|---|---|
| Architecture briefing (as implemented) | `docs/architecture.md` |
| PDES design | `docs/superpowers/specs/2026-10-25-pdes-network-design.md` |
| PDES implementation walkthrough | `docs/2026-10-26-pdes-implementation.md` |
| PDES test report (verified speedup + failure drills) | `docs/2026-10-26-pdes-test-report.md`; procedure in `docs/pdes-testing.md` |
| Gateway design/API | `docs/superpowers/plans/2026-10-24-minimal-gateway.md`, `docs/superpowers/specs/2026-10-23-minimal-gateway-design.md`, `docs/gateway-api.md` |
| Node API observations and gaps | `docs/node-api-observed.md`, `docs/node-api-gaps.md` |
| Security audit | `docs/2026-11-04-security-audit.md` |
| UI declutter plan | `docs/superpowers/plans/2026-11-04-ui-declutter.md`, `docs/gui-stage1-2-task-plan.md`, `gui-stage1-2-plan.md`, `gui-roadmap.md` |
| Assumptions and decisions | `docs/assumptions.md` |
| Product requirements | `prd.md` |
| Earlier work summary | `docs/2026-10-26-project-work-summary.md` |
| Interim state snapshot (2026-11-08) | `docs/2026-11-08-project-state.md` |
