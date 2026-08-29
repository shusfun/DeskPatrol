import { describe, expect, it } from "vitest";
import { deviceTechnicalSubtitle, deviceTitle } from "./device-display";

describe("device display naming", () => {
  it("备注非空时作为主标题并保留 Windows 名称", () => {
    const device = { label: " 财务室 01 ", name: "WIN-FINANCE" };
    expect(deviceTitle(device)).toBe("财务室 01");
    expect(deviceTechnicalSubtitle(device)).toBe("Windows 名称：WIN-FINANCE");
  });

  it("备注为空时回退到 Windows 名称", () => {
    const device = { label: "  ", name: "WIN-01" };
    expect(deviceTitle(device)).toBe("WIN-01");
    expect(deviceTechnicalSubtitle(device)).toBeNull();
  });
});
