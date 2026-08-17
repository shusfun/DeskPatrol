export type DesktopViewerMode = "wall" | "detail" | "fullscreen";

export const frameIntervalByMode: Record<DesktopViewerMode, number> = {
  wall: 400,
  detail: 100,
  fullscreen: 50,
};

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
