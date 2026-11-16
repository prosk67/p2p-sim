# Worker pool resource usage in the web UI

**Why.** While a run is in flight there is no way to see, from the browser,
which nodes are working and which have headroom. `docker stats` shows it, but
only in a terminal on the machine that runs the containers.

**Goal.** The Cluster page shows, per node and refreshed with the existing
3-second poll: simulation slots in use, CPU, memory, network I/O and process
count — the `docker stats` columns plus the node's own slot limit.

**Non-goals.** Stored history, alerts, per-run attribution, GPU or disk I/O.

## Approaches

1. **Each node reports its own usage (chosen).** The node reads its container's
   cgroup v2 counters — the same source `docker stats` reads — and serves them
   on `GET /resources`. Works in Compose and on real machines, counts the
   Python simulator subprocesses, needs no extra privileges.
2. **Gateway calls the Docker API.** Exact `docker stats` output, but the
   gateway would need the Docker socket (root-equivalent) and it only works
   when every node is a container on the gateway's own host.
3. **Prometheus + Grafana.** A separate tool, not the web UI.

## Design

- **Node** — `internal/resources`: a sampler takes a reading every 2 s and
  turns counter deltas into rates. `scope` is `container` when cgroup v2
  files are present (`cpu.stat`, `memory.current` minus `inactive_file`,
  `memory.max`, `cpu.max`, `pids.current`), else `host` (`/proc/stat`,
  `/proc/meminfo`), so the UI can label whole-machine figures. Network
  rates come from `/proc/net/dev` (all interfaces except `lo`). Slots come
  live from `worker.Executor`.
- **Gateway** — `/api/cluster` nodes gain `resources` (the node's body passed
  through, or `null`). A node that fails `/resources` stays healthy.
- **Web** — each Cluster node row gets a resources line: slots, CPU (with a
  sparkline of the last ~2 minutes, kept in the browser only), memory,
  network, processes. The summary strip gains pool-wide busy slots and CPU.
