export type DesktopViewerMode = "wall" | "detail" | "fullscreen";

export const frameIntervalByMode: Record<DesktopViewerMode, number> = {
  wall: 400,
  detail: 100,
  fullscreen: 50,
};

export const displayRequestIntervalMs = 1_000;
export const displayRefreshIntervalMs = 5_000;
export const ticketRenewalLeadMs = 15_000;

export function ticketRenewalDelayMs(expiresAt: string, now = Date.now()) {
  const expiry = Date.parse(expiresAt);
  if (!Number.isFinite(expiry)) return null;
  return Math.max(1_000, expiry - now - ticketRenewalLeadMs);
}

export function displayRequestDue(now: number, lastRequest: number, hasPhysicalDisplays: boolean) {
  const interval = hasPhysicalDisplays ? displayRefreshIntervalMs : displayRequestIntervalMs;
  return now - lastRequest >= interval;
}

export function targetFps(mode: DesktopViewerMode) {
  return 1000 / frameIntervalByMode[mode];
}

export function physicalDisplayIds(displays: Record<string, string> | null | undefined) {
  if (!displays) return [];
  return Object.keys(displays)
    .map(Number)
    .filter((value) => Number.isInteger(value) && value >= 0 && value < 65535)
    .sort((left, right) => left - right);
}

export function resolveDisplaySelection(saved: number | null, displays: number[]) {
  if (displays.length === 0) return { displayId: null, changed: false, removed: false };
  if (saved !== null && displays.includes(saved)) return { displayId: saved, changed: false, removed: false };
  return { displayId: displays[0], changed: true, removed: saved !== null };
}

export class RollingFpsCounter {
  private readonly updates: number[] = [];

  constructor(private readonly windowMs = 3_000) {}

  record(timestamp: number) {
    this.updates.push(timestamp);
    this.prune(timestamp);
  }

  value(timestamp: number) {
    this.prune(timestamp);
    return this.updates.length * 1000 / this.windowMs;
  }

  reset() {
    this.updates.length = 0;
  }

  private prune(timestamp: number) {
    const cutoff = timestamp - this.windowMs;
    while (this.updates.length > 0 && this.updates[0] <= cutoff) this.updates.shift();
  }
}

export class FrameActivityCounter {
  private readonly counter: RollingFpsCounter;
  private lastEvent: number | undefined;

  constructor(private readonly minIntervalMs: number, windowMs = 3_000) {
    this.counter = new RollingFpsCounter(windowMs);
  }

  record(timestamp: number) {
    if (this.lastEvent !== undefined && timestamp - this.lastEvent < this.minIntervalMs) return false;
    this.lastEvent = timestamp;
    this.counter.record(timestamp);
    return true;
  }

  value(timestamp: number) {
    return this.counter.value(timestamp);
  }

  reset() {
    this.lastEvent = undefined;
    this.counter.reset();
  }
}
