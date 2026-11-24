import type { ClusterNode, NodeResources } from "../api/types";

/** CPU samples kept per node: 40 polls at 3 s is about two minutes. */
export const HISTORY_LENGTH = 40;

export type LoadTone = "good" | "warn" | "bad";

/** Colour band for a 0–100 load figure. */
export function loadTone(percent: number): LoadTone {
  if (percent >= 90) return "bad";
  if (percent >= 70) return "warn";
  return "good";
}

export function clampPercent(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.min(100, Math.max(0, value));
}

const UNITS = ["B", "KB", "MB", "GB", "TB"];

/** Binary units, like `docker stats` (which labels them MiB/GiB). */
export function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes) || bytes < 0) return "--";
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit === 0 || value >= 100 ? 0 : value >= 10 ? 1 : 2;
  return `${value.toFixed(digits)} ${UNITS[unit]}`;
}

export function formatRate(bytesPerSec: number | null | undefined): string {
  const text = formatBytes(bytesPerSec);
  return text === "--" ? text : `${text}/s`;
}

export interface PoolUsage {
  /** Nodes that reported usage. */
  reporting: number;
  /** Distinct machines among them (nodes without a host id count as one each). */
  machines: number;
  slotsInUse: number;
  slotsTotal: number;
  coresUsed: number;
  cores: number;
  /** Null until at least one node has a CPU rate. */
  cpuPercent: number | null;
}

/**
 * Pool-wide totals over the nodes that report usage, counted per machine so
 * nodes sharing one host are not double counted:
 *
 * - containers on one machine: their cores used add up, and capacity is the
 *   machine's cores (or the sum of their CPU limits, if smaller);
 * - nodes reporting whole-machine ("host") figures on one machine all see the
 *   same load, so it is taken once.
 *
 * The pool figure is total cores used over total capacity, not an average of
 * per-node percentages, which would be wrong for machines of different sizes.
 */
export function poolUsage(nodes: ClusterNode[]): PoolUsage {
  const reports = nodes.flatMap((node) => (node.healthy && node.resources ? [node.resources] : []));
  const usage: PoolUsage = {
    reporting: reports.length, machines: 0, slotsInUse: 0, slotsTotal: 0, coresUsed: 0, cores: 0, cpuPercent: null,
  };
  const machines = new Map<string, NodeResources[]>();
  for (const report of reports) {
    usage.slotsInUse += report.slots.in_use;
    usage.slotsTotal += report.slots.total;
    const key = report.host_id ? `host:${report.host_id}` : `node:${report.node_id}`;
    machines.set(key, [...(machines.get(key) ?? []), report]);
  }
  usage.machines = machines.size;
  let measured = false;
  for (const group of machines.values()) {
    const withCpu = group.filter((report) => report.cpu);
    if (withCpu.length === 0) continue;
    measured = true;
    const hostCores = Math.max(0, ...group.map((report) => report.host_cores ?? 0));
    const wholeMachine = withCpu.filter((report) => report.scope === "host");
    if (wholeMachine.length > 0) {
      usage.coresUsed += Math.max(...wholeMachine.map((report) => report.cpu!.cores_used));
      usage.cores += hostCores || wholeMachine[0].cpu!.cores;
    } else {
      const limits = withCpu.reduce((sum, report) => sum + report.cpu!.cores, 0);
      usage.coresUsed += withCpu.reduce((sum, report) => sum + report.cpu!.cores_used, 0);
      usage.cores += hostCores > 0 ? Math.min(hostCores, limits) : limits;
    }
  }
  if (measured && usage.cores > 0) usage.cpuPercent = clampPercent((usage.coresUsed / usage.cores) * 100);
  return usage;
}

export interface CpuSeries {
  /** sampled_at of the last reading taken, so repeated polls add nothing. */
  last: string | null;
  values: number[];
}

/**
 * Appends each node's newest CPU reading. A reading is only added when the
 * node took a new sample (sampled_at changed), so extra refreshes do not
 * squeeze the time axis. Nodes that left are dropped; a gap (node down, no
 * reading) restarts the line rather than bridging it.
 */
export function pushHistory(history: Record<string, CpuSeries>, nodes: ClusterNode[]): Record<string, CpuSeries> {
  const next: Record<string, CpuSeries> = {};
  for (const node of nodes) {
    const previous = history[node.id] ?? { last: null, values: [] };
    const report = node.healthy ? node.resources : null;
    const percent = report?.cpu?.percent;
    if (!report || percent === undefined) {
      next[node.id] = { last: null, values: [] };
    } else if (report.sampled_at === previous.last) {
      next[node.id] = previous;
    } else {
      next[node.id] = { last: report.sampled_at, values: [...previous.values, clampPercent(percent)].slice(-HISTORY_LENGTH) };
    }
  }
  return next;
}

/**
 * SVG polyline points for a 0–100 series; the newest value sits at the right
 * edge. The y axis stretches to the series' own peak (never below `minPeak`),
 * because one busy core on a 16-core machine would otherwise be a flat line.
 */
export function sparklinePoints(values: number[], width: number, height: number, capacity = HISTORY_LENGTH, minPeak = 5): string {
  if (values.length === 0) return "";
  const peak = Math.max(minPeak, ...values.map(clampPercent));
  const step = width / Math.max(1, capacity - 1);
  const offset = width - (values.length - 1) * step;
  return values
    .map((value, index) => `${(offset + index * step).toFixed(1)},${(height - (clampPercent(value) / peak) * height).toFixed(1)}`)
    .join(" ");
}
