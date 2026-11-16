# p2p-sim — Project State Update Report

- **Date:** 2026-11-08
- **Repo:** `p2p-sim` (working tree clean)
- **Latest commit:** `993f187` — *Make PDES runs visible and faster; declutter the Network lab* (2026-11-08)
- **Purpose of this document:** a factual snapshot of current implementation state, verified capabilities, known gaps and recent work, prepared as an input to a fuller polished report.

---

## 1. What the project is

A distributed queueing / discrete-event simulation platform on a symmetric peer-to-peer node pool (Go services + Python/SimPy simulation kernels + React/Vite GUI served by a Go gateway). Headline features:

- **M/M/1 batch replication farm** — any node can coordinate; tasks fan out to a static peer list, seeds make results reproducible and re-runnable anywhere; report compares against closed-form theory with 95% CIs and a serial baseline cross-check.
- **Network lab** — GUI topology editor (hosts, routers, links, flows: steady / stepped / ramped / on-off, failure schedules), shortest-path routing recomputed on every failure/recovery, independent per-flow / per-router random streams from one seed.
- **PDES execution** — conservative window-based simulation splitting one scenario's entities across partitions, with deterministic placement, cross-partition record exchange, and a "merge must reproduce serial" oracle (both metrics match and wall-clock speedup reported).
- **Threat Lab** — deterministic DDoS / SYN flood / ransomware-worm / exfil campaign theater; in-browser model, single remote worker, or PDES-pooled execution (with local fallback).
- **GUI** — Network Lab, Threat Lab, live-traffic view, cluster view, launch / live-batch / report pages, with playback (seeking restarts the exact copy from that time point); sqlite-backed run history upstream at the gateway.

## 2. Verified working right now (end-to-end smoke test, 2026-11-08)

Ran `scripts/demo.sh 20` (20 replications, λ=0.8, μ=1.0, sim 10000, warmup 1000), node-1 coordinating over node-1..3, distributed run succeeded:

- mean wait 4.86 vs theory 5.0, mean queue length 4.00 vs 4.00, utilization 0.80 vs 0.80 — all three metrics **within 95% CI** of theory, verdict **PASS**, `serial_results_match_distributed: true`.
- distributed wall 0.885 s vs serial 2.31 s → **2.6× speedup** (3 workers).
- Live traffic / netstream / PDES into check-by-deployment.

## 3. Repository layout / current surface

```
node/        Go worker+coordinator: api, batch, config, dispatch, netbatch, peers, sim, stats, task, worker
gateway/     Go gateway: config, nodeclient, aggregate, store, runs, server; serves webui (React build)
web/         React+Vite GUI: Cluster, Launch, Live batch, Report, Network Lab, Threat Lab, playback
sim/         simulate.py (M/M/1), netsim.py (networks, plan/partition/merge), attacksim.py (campaigns)
config/      peers.yaml, peers.4.yaml, node.yaml
scripts/     demo.sh, kill-node-test.sh, kill-coordinator-test.sh
docs/        architecture, API docs, PDES spec/report, security audit, declutter plans
```

Docker images (built and tagged): `p2p-sim-node:latest`, `p2p-sim-gateway:latest` (web too for mock profile). Runtime verified with `docker compose -f docker-compose.yml -f docker-compose.web.yml up --build -d`: 3 nodes at `127.0.0.1:8081-8083`, gateway/GUI at `:8090`, all healthy.

## 4. Tests and quality gates

- `sim/` pytest: M/M/1, M/D/1, network (routing, failures, traffic patterns), attack campaigns, CLI contracts.
- `node/` and `gateway/` Go tests with `-race`: config, client, cluster, dispatch/retry/busy, worker, routing, store, HTTP.
- `web/` `npm run typecheck && npm test`: validation, stats, mock run timeline.
- CI/local-test evidence should be re-checked before writing the final report (this document does not assert pass/fail on the latest commit).
- Security audit exists at `docs/2026-11-04-security-audit.md`; container hardening in place (non-root, cap_drop ALL, no-new-privileges, read-only rootfs, pids/mem limits, loopback-only port publishing).

## 5. Recent work (what changed most recently)

- **2026-11-08** — PDES runs made visible and faster; Network Lab UI declutter.
- **2026-11-06** — "UI declutter" series completed (10 sub-tasks): shared `StatStrip`, `PagePrimitives`, chart primitives (`AxisFrame`, `LineChart`), `CopyJsonButton`, `PartitionTimingList`, merged network run actions, single Cluster health story, PDES generator moved below canvas, simplified report cards, deduped traffic/attack facts.
- **2026-11-04** — security audit; live PDES batch progress page.
- **2026-11-03** — worker-backed Threat Lab campaigns incl. PDES execution.

## 6. Known limitations / open issues (state these honestly in the report)

- **Coordinator loss** = batch loss; recovery is manual resubmission with the same `base_seed` (no leader election — PRD stretch goal).
- **One batch per coordinating node** at a time (M/M/1 and network shares combined); nodes can still each coordinate separately, but cross-timing interferes.
- **One local task at a time per node process**; horizontal scale = more node processes.
- Membership is **static** (`config/peers.yaml`), no discovery/gossip/failover.
- No auth/TLS (trusted-pool design assumption; mitigated by loopback publishing).
- Supply-chain: deps pinned but not hash/digest-pinned.
- Live traffic / netstream / PDES / streaming rate limits (from README §API) apply.

## 7. Architecture summary (one-liner you can reuse)

> Every node runs the same Go binary and does two jobs — worker and coordinator — so there is no permanent coordinator; batches fan out replications across peers with retries / busy-handling / health backoff, and seeds make every replication reproducible and reorderable; simulations are Python/SimPy subprocesses talking a one-line JSON contract; the gateway probes the pool, submits batches, previews them to completion and keeps history, and the React GUI covers cluster, launch, live traffic, network lab, threat lab and report views.

## 8. Suggested outline for the polished report (derived from this state)

1. Abstract & problem statement (why distributed / why PDES / why symmetric pool).
2. Architecture — Go orchestration + SimPy kernels + gateway/GUI; diagram (README §IDEA covers).
3. Distributed features — static membership, fan-out with retry/busy/backoff, seed-based reproducibility, serial oracle.
4. PDES — deterministic placement, lookahead windows, merge-equals-serial oracle, speedup vs serial baseline.
5. Simulation capabilities — M/M/1, M/D/1, network model incl. failures/routing/traffic patterns, threat campaign model, deterministic streams.
6. Verification — unit/race tests, demo smoke evidence (Section 2 numbers), theory CI checks, serial-match.
7. GUI & usability — nodes/batches/lab pages, declutter results, playback, sample networks, JSON import/export.
8. Deployment & security — hardening details, loopback defaults, sha-pinning policy (and the limitation).
9. Limitations & future work — Section 6 list + leader election, dynamic membership, digest pinning.
10. Reproducibility — seed scheme, exact serial vs distributed match, demo scripts.

## 9. Data points useful for a metrics-oriented report

- 20-replication sample: distributed 0.885 s vs serial 2.31 s → speedup 2.61; 3 peers; 20 tasks + 20-bit serial check.
- CI: queue-length mean 4.0022 ± 0.2389 (95%) vs theory 4.0; wait 4.86 vs 5.0; util 0.8007 ± 0.0081 vs 0.8. Relative errors ≈ 0.05–0.09%.
- Defaults baseline from `config/node.yaml` / built-ins: replications 100, λ 0.8, μ 1.0, sim 10000, warmup 1000, tolerance 10%, max_attempts 3.
- API limits (state these if harder numbers matter): ≤60 nodes, ≤120 links, ≤40 flows, ≤60 failure events, ≤150k offered packets/replication, ≤150 pkt/s stream, ≤500 topology tokens, ≤3600 sim seconds, ≤200k attack samples.

## 10. Where to look for detail (source-of-truth docs)

| Topic | File |
|---|---|
| Architecture & design rationale | `docs/architecture.md`, README §Design notes |
| Node API reference | `docs/node-api-observed.md`, README §API |
| Gateway API reference | `docs/gateway-api.md` |
| PDES design & test report | `docs/superpowers/specs/2026-10-25-pdes-network-design.md`, `docs/2026-10-26-pdes-implementation.md`, `docs/2026-10-26-pdes-test-report.md` |
| Gateway design | `docs/superpowers/specs/2026-10-23-minimal-gateway-design.md`, `docs/superpowers/plans/2026-10-24-minimal-gateway.md` |
| UI declutter plan & status | `docs/superpowers/plans/2026-11-04-ui-declutter.md`, `docs/gui-stage1-2-task-plan.md` |
| Security audit | `docs/2026-11-04-security-audit.md` |
| Work log / earlier summary | `docs/2026-10-26-project-work-summary.md` |
| Assumptions & PRD | `docs/assumptions.md`, `prd.md` |
