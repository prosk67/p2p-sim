/**
 * Calls refresh now and then intervalMs after each call settles, so slow
 * responses never overlap. Returns a function that stops polling.
 */
export function startPolling(refresh: () => Promise<void>, intervalMs: number): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const tick = () => {
    refresh().catch(() => undefined).finally(() => {
      if (!stopped) timer = setTimeout(tick, intervalMs);
    });
  };
  tick();
  return () => {
    stopped = true;
    clearTimeout(timer);
  };
}
