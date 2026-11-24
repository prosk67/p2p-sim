import type { ReactNode } from "react";
import type { NodeResources as Usage } from "../api/types";
import { clampPercent, formatBytes, formatRate, loadTone, sparklinePoints, type LoadTone } from "../lib/resources";

const SPARK_WIDTH = 72;
const SPARK_HEIGHT = 20;

function Meter({ label, percent, tone, children, title }: {
  label: string;
  percent: number | null;
  tone?: LoadTone;
  children: ReactNode;
  title?: string;
}) {
  const filled = percent === null ? 0 : clampPercent(percent);
  return (
    <div className="usage-meter" title={title}>
      <dt>{label}</dt>
      <dd>
        <span
          className={`usage-bar tone-${tone ?? loadTone(filled)}`}
          role="meter"
          aria-label={label}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(filled)}
        >
          <i style={{ width: `${filled}%` }} />
        </span>
        <span className="usage-value">{children}</span>
      </dd>
    </div>
  );
}

/** One node's usage line on the Cluster page: slots, CPU, memory, network, processes. */
export function NodeResources({ usage, cpuHistory }: { usage: Usage; cpuHistory: number[] }) {
  const { slots, cpu, memory, net, pids } = usage;
  const slotPercent = slots.total > 0 ? (slots.in_use / slots.total) * 100 : 0;
  const memoryPercent = memory && memory.limit_bytes > 0 ? (memory.used_bytes / memory.limit_bytes) * 100 : null;
  const hostScope = usage.scope === "host";

  return (
    <dl className="node-usage">
      <Meter
        label="Slots"
        percent={slotPercent}
        // A full node is busy, not in trouble: extra tasks are placed elsewhere.
        tone={slots.in_use >= slots.total ? "warn" : "good"}
        title="Simulations running on this node out of its max_concurrent_tasks limit"
      >
        <strong>{slots.in_use}</strong> / {slots.total} busy
      </Meter>

      <Meter
        label="CPU"
        percent={cpu ? cpu.percent : null}
        title={cpu ? `Averaged over the last 2 seconds. docker stats shows this as ${(cpu.cores_used * 100).toFixed(0)}% (100% = one core)` : "Waiting for the node's second reading"}
      >
        {cpu ? <><strong>{cpu.cores_used.toFixed(2)} cores</strong> · {cpu.percent.toFixed(0)}% of {cpu.cores}</> : "--"}
        {cpuHistory.length > 1 && (
          <svg className="usage-spark" viewBox={`0 0 ${SPARK_WIDTH} ${SPARK_HEIGHT}`} width={SPARK_WIDTH} height={SPARK_HEIGHT} role="img" aria-label="CPU over the last two minutes, scaled to its own peak">
            <polyline points={sparklinePoints(cpuHistory, SPARK_WIDTH, SPARK_HEIGHT)} />
          </svg>
        )}
      </Meter>

      <Meter label="Memory" percent={memoryPercent} title="Used / limit, not counting reclaimable file cache">
        {memory ? <><strong>{formatBytes(memory.used_bytes)}</strong> / {formatBytes(memory.limit_bytes)}</> : "--"}
      </Meter>

      <div className="usage-fact" title="Network transfer rate over the last 2 seconds, all interfaces except loopback">
        <dt>Network</dt>
        <dd>{net ? <>↓ {formatRate(net.rx_bytes_per_sec)} · ↑ {formatRate(net.tx_bytes_per_sec)}</> : "--"}</dd>
      </div>

      {pids !== null && (
        <div className="usage-fact" title="Processes and threads in this node's container, including running simulators">
          <dt>PIDs</dt>
          <dd>{pids}</dd>
        </div>
      )}

      {hostScope && (
        <div className="usage-scope" title="No container limits were found for this node, so CPU, memory and network describe the whole machine it runs on">
          Whole machine
        </div>
      )}
    </dl>
  );
}
