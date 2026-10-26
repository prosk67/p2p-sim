import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startPolling } from "./poll";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("startPolling", () => {
  it("waits for a slow refresh to finish before scheduling the next one", async () => {
    let calls = 0;
    const refresh = () => { calls += 1; return new Promise<void>((resolve) => setTimeout(resolve, 5000)); };
    const stop = startPolling(refresh, 3000);
    expect(calls).toBe(1);
    await vi.advanceTimersByTimeAsync(4000); // the first refresh is still in flight
    expect(calls).toBe(1);
    await vi.advanceTimersByTimeAsync(1000 + 3000); // finished at 5 s, next at 8 s
    expect(calls).toBe(2);
    stop();
  });

  it("keeps polling after a failed refresh and stops when asked", async () => {
    let calls = 0;
    const stop = startPolling(() => { calls += 1; return Promise.reject(new Error("down")); }, 1000);
    await vi.advanceTimersByTimeAsync(2500);
    expect(calls).toBe(3);
    stop();
    await vi.advanceTimersByTimeAsync(5000);
    expect(calls).toBe(3);
  });
});
