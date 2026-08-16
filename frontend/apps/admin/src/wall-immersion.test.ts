import { describe, expect, it, vi } from "vitest";
import { WALL_TOOLBAR_HIDE_DELAY_MS, createToolbarAutoHideController, isImmersionExitKey, moveDeviceOrder } from "./wall-immersion";

describe("监控墙沉浸状态", () => {
  it("显示操作栏并在 2.5 秒后隐藏", () => {
    const visible: boolean[] = [];
    let callback: () => void = () => {};
    const clearTimeout = vi.fn();
    const setTimeout = vi.fn((next: () => void) => { callback = next; return 17; });
    const controller = createToolbarAutoHideController((value) => visible.push(value), { clearTimeout, setTimeout });

    controller.show();
    controller.scheduleHide();
    expect(setTimeout).toHaveBeenCalledWith(expect.any(Function), WALL_TOOLBAR_HIDE_DELAY_MS);
    callback();
    expect(visible).toEqual([true, false]);
  });

  it("只使用 Escape 退出沉浸", () => {
    expect(isImmersionExitKey("Escape")).toBe(true);
    expect(isImmersionExitKey("Enter")).toBe(false);
  });

  it("按拖拽目标生成稳定设备顺序", () => {
    expect(moveDeviceOrder(["a", "b", "c"], "a", "c")).toEqual(["b", "c", "a"]);
    expect(moveDeviceOrder(["a", "b"], "missing", "b")).toEqual(["a", "b"]);
  });
});
