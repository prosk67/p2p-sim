import { describe, expect, it } from "vitest";
import type { ClusterNode, NodeResources } from "../api/types";
import { formatBytes, formatRate, HISTORY_LENGTH, loadTone, poolUsage, pushHistory, sparklinePoints, type CpuSeries } from "./resources";

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
  it("counts containers on one machine against that machine's cores", () => {
    // The Compose demo: 3 unlimited containers on a 16-core host, 4 cores busy each.
    const busy = (id: string) => node(id, report({
      node_id: id, host_id: "h1", host_cores: 16, cpu: { cores_used: 4, cores: 16, percent: 25 },
    }));
    const usage = poolUsage([busy("node-1"), busy("node-2"), busy("node-3")]);
    expect(usage).toMatchObject({ reporting: 3, machines: 1, coresUsed: 12, cores: 16, cpuPercent: 75 });
  });

  it("uses total cores over total capacity across separate machines", () => {
    // A 4-core laptop flat out plus an idle 16-core PC is 20% of the pool, not 50%.
    const usage = poolUsage([
      node("laptop", report({ node_id: "laptop", host_id: "a", host_cores: 4, cpu: { cores_used: 4, cores: 4, percent: 100 } })),
      node("pc", report({ node_id: "pc", host_id: "b", host_cores: 16, cpu: { cores_used: 0, cores: 16, percent: 0 } })),
    ]);
    expect(usage).toMatchObject({ machines: 2, coresUsed: 4, cores: 20, cpuPercent: 20 });
  });

  it("caps capacity at the containers' CPU limits when they are smaller", () => {
    const limited = (id: string) => node(id, report({
      node_id: id, host_id: "h1", host_cores: 16, cpu: { cores_used: 1, cores: 2, percent: 50 },
    }));
    expect(poolUsage([limited("node-1"), limited("node-2")])).toMatchObject({ coresUsed: 2, cores: 4, cpuPercent: 50 });
  });

  it("takes whole-machine figures once per machine", () => {
    const host = (id: string) => node(id, report({
      node_id: id, scope: "host", host_id: "h1", host_cores: 8, pids: null, cpu: { cores_used: 2, cores: 8, percent: 25 },
    }));
    expect(poolUsage([host("node-1"), host("node-2")])).toMatchObject({ machines: 1, coresUsed: 2, cores: 8, cpuPercent: 25 });
  });

  it("sums slots, skips down or silent nodes, and treats nodes without a host id separately", () => {
    const usage = poolUsage([
      node("node-1", report()),
      node("node-2", report({ node_id: "node-2", slots: { in_use: 2, total: 2 }, cpu: { cores_used: 3, cores: 4, percent: 75 } })),
      node("node-3", null),
      node("node-4", report({ node_id: "node-4" }), false),
    ]);
    expect(usage).toMatchObject({ reporting: 2, machines: 2, slotsInUse: 3, slotsTotal: 4, coresUsed: 4, cores: 8, cpuPercent: 50 });
  });

  it("has no CPU figure before the first rate arrives", () => {
    expect(poolUsage([node("node-1", report({ cpu: null }))]).cpuPercent).toBeNull();
  });
});

describe("pushHistory", () => {
  it("adds a point only for a new sample, caps the length, resets on a gap and forgets removed nodes", () => {
    let history: Record<string, CpuSeries> = { gone: { last: "x", values: [1] } };
    for (let i = 0; i < HISTORY_LENGTH + 5; i += 1) {
      history = pushHistory(history, [node("node-1", report({ sampled_at: `t${i}` }))]);
    }
    expect(history["node-1"].values).toHaveLength(HISTORY_LENGTH);
    expect(history.gone).toBeUndefined();
    // Refreshing again before the node takes a new sample adds nothing.
    const again = pushHistory(history, [node("node-1", report({ sampled_at: `t${HISTORY_LENGTH + 4}` }))]);
    expect(again["node-1"]).toBe(history["node-1"]);
    expect(pushHistory(history, [node("node-1", null)])["node-1"].values).toEqual([]);
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
