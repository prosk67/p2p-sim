import type { ClusterNode } from "../api/types";

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
  slotsInUse: number;
  slotsTotal: number;
  coresUsed: number;
  cores: number;
  /** Null until at least one node has a CPU rate. */
  cpuPercent: number | null;
}

/**
 * Pool-wide totals over the nodes that report usage. Host-scope nodes that
 * share one machine would each count the whole machine, so CPU is only summed
 * when it is safe: every reporting node is a container, or there is one node.
 */
export function poolUsage(nodes: ClusterNode[]): PoolUsage {
  const reports = nodes.flatMap((node) => (node.healthy && node.resources ? [node.resources] : []));
  const usage: PoolUsage = { reporting: reports.length, slotsInUse: 0, slotsTotal: 0, coresUsed: 0, cores: 0, cpuPercent: null };
  for (const report of reports) {
    usage.slotsInUse += report.slots.in_use;
    usage.slotsTotal += report.slots.total;
  }
  const cpus = reports.flatMap((report) => (report.cpu ? [report.cpu] : []));
  const summable = reports.length === 1 || reports.every((report) => report.scope === "container");
  if (cpus.length > 0 && summable) {
    for (const cpu of cpus) {
      usage.coresUsed += cpu.cores_used;
      usage.cores += cpu.cores;
    }
    // Containers without a CPU limit each report every core of the machine,
    // so the summed capacity can exceed it; the busy share is still the mean.
    usage.cpuPercent = cpus.reduce((sum, cpu) => sum + cpu.percent, 0) / cpus.length;
  }
  return usage;
}

/** Appends this poll's CPU reading per node and drops nodes that left. */
export function pushHistory(history: Record<string, number[]>, nodes: ClusterNode[]): Record<string, number[]> {
  const next: Record<string, number[]> = {};
  for (const node of nodes) {
    const previous = history[node.id] ?? [];
    const percent = node.healthy ? node.resources?.cpu?.percent : undefined;
    // A gap (node down, no reading) restarts the line rather than bridging it.
    next[node.id] = percent === undefined ? [] : [...previous, clampPercent(percent)].slice(-HISTORY_LENGTH);
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
