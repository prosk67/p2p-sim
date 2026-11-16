import { describe, expect, it } from "vitest";
import type { ClusterNode, NodeResources } from "../api/types";
import { formatBytes, formatRate, HISTORY_LENGTH, loadTone, poolUsage, pushHistory, sparklinePoints } from "./resources";

const report = (overrides: Partial<NodeResources> = {}): NodeResources => ({
  node_id: "node-1",
  scope: "container",
  slots: { in_use: 1, total: 2 },
  cpu: { cores_used: 1, cores: 4, percent: 25 },
  memory: { used_bytes: 100, limit_bytes: 200 },
  pids: 5,
  net: { rx_bytes_per_sec: 0, tx_bytes_per_sec: 0 },
  sampled_at: "2026-11-15T00:00:00Z",
  ...overrides,
});

const node = (id: string, resources: NodeResources | null, healthy = true): ClusterNode => ({
  id, url: `http://${id}`, healthy, observer_id: id, latency_ms: 1, peers_seen: {}, resources,
});

describe("formatting", () => {
  it("formats bytes in binary units", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1536)).toBe("1.50 KB");
    expect(formatBytes(212 * 1024 * 1024)).toBe("212 MB");
    expect(formatBytes(1.5 * 1024 ** 3)).toBe("1.50 GB");
    expect(formatBytes(null)).toBe("--");
    expect(formatRate(2048)).toBe("2.00 KB/s");
    expect(formatRate(undefined)).toBe("--");
  });

  it("bands load", () => {
    expect([loadTone(10), loadTone(75), loadTone(95)]).toEqual(["good", "warn", "bad"]);
  });
});

describe("poolUsage", () => {
  it("sums slots and CPU over reporting container nodes", () => {
    const usage = poolUsage([
      node("node-1", report()),
      node("node-2", report({ slots: { in_use: 2, total: 2 }, cpu: { cores_used: 3, cores: 4, percent: 75 } })),
      node("node-3", null),
      node("node-4", report(), false),
    ]);
    expect(usage).toMatchObject({ reporting: 2, slotsInUse: 3, slotsTotal: 4, coresUsed: 4, cores: 8, cpuPercent: 50 });
  });

  it("does not add up whole-machine figures from several host-scope nodes", () => {
    const usage = poolUsage([node("node-1", report({ scope: "host" })), node("node-2", report({ scope: "host" }))]);
    expect(usage.cpuPercent).toBeNull();
    expect(usage.slotsTotal).toBe(4);
  });

  it("has no CPU figure before the first rate arrives", () => {
    expect(poolUsage([node("node-1", report({ cpu: null }))]).cpuPercent).toBeNull();
  });
});

describe("pushHistory", () => {
  it("appends, caps, resets on a gap and forgets removed nodes", () => {
    let history: Record<string, number[]> = { gone: [1] };
    for (let i = 0; i < HISTORY_LENGTH + 5; i += 1) history = pushHistory(history, [node("node-1", report())]);
    expect(history["node-1"]).toHaveLength(HISTORY_LENGTH);
    expect(history.gone).toBeUndefined();
    expect(pushHistory(history, [node("node-1", null)])["node-1"]).toEqual([]);
  });
});

describe("sparklinePoints", () => {
  it("pins the newest sample to the right edge and inverts the y axis", () => {
    expect(sparklinePoints([0, 100], 100, 20, 11)).toBe("90.0,20.0 100.0,0.0");
    // Scaled to its own peak, so a low but varying load is still visible.
    expect(sparklinePoints([5, 10], 100, 20, 11)).toBe("90.0,10.0 100.0,0.0");
    expect(sparklinePoints([1, 1], 100, 20, 11)).toBe("90.0,16.0 100.0,16.0");
    expect(sparklinePoints([], 100, 20)).toBe("");
  });
});
