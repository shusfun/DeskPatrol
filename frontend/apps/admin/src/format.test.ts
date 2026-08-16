import { describe, expect, it } from "vitest";
import { formatBytes } from "@deskpatrol/core";

describe("formatBytes", () => {
  it("按安装包大小显示单位", () => {
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB");
  });
});
