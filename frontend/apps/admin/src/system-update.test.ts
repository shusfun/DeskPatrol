import { describe, expect, it } from "vitest";
import { isNewerVersion } from "./system-update";

describe("系统更新版本比较", () => {
  it("按数字逐段比较", () => {
    expect(isNewerVersion("0.1.23", "0.1.22")).toBe(true);
    expect(isNewerVersion("0.1.100", "0.1.99")).toBe(true);
    expect(isNewerVersion("1.0.0", "0.9.9")).toBe(true);
  });

  it("相同、更低或格式不正确时不允许更新", () => {
    expect(isNewerVersion("0.1.22", "0.1.22")).toBe(false);
    expect(isNewerVersion("0.1.21", "0.1.22")).toBe(false);
    expect(isNewerVersion("v0.1.23", "0.1.22")).toBe(false);
    expect(isNewerVersion("latest", "0.1.22")).toBe(false);
  });
});
