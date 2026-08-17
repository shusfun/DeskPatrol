import { describe, expect, it } from "vitest";
import { frameIntervalByMode, physicalDisplayIds, resolveDisplaySelection, RollingFpsCounter, targetFps } from "./desktop-viewer";

describe("桌面查看器模式", () => {
  it("固定使用 2.5、10 和 20 FPS", () => {
    expect(frameIntervalByMode).toEqual({ wall: 400, detail: 100, fullscreen: 50 });
    expect(targetFps("wall")).toBe(2.5);
    expect(targetFps("detail")).toBe(10);
    expect(targetFps("fullscreen")).toBe(20);
  });
});

describe("物理显示器选择", () => {
  it("排除全部屏幕并按编号排序", () => {
    expect(physicalDisplayIds({ "3": "Display 3", "1": "Display 1", "65535": "All Displays" })).toEqual([1, 3]);
  });

  it("首次选择第一屏，原屏移除后明确切换", () => {
    expect(resolveDisplaySelection(null, [2, 4])).toEqual({ displayId: 2, changed: true, removed: false });
    expect(resolveDisplaySelection(4, [2, 4])).toEqual({ displayId: 4, changed: false, removed: false });
    expect(resolveDisplaySelection(4, [2, 3])).toEqual({ displayId: 2, changed: true, removed: true });
  });
});

describe("实际 FPS", () => {
  it("统计三秒窗口并允许静止画面归零", () => {
    const counter = new RollingFpsCounter();
    for (let timestamp = 100; timestamp <= 3000; timestamp += 100) counter.record(timestamp);
    expect(counter.value(3000)).toBe(10);
    expect(counter.value(6100)).toBe(0);
  });
});
