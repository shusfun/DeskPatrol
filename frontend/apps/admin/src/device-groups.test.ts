import { describe, expect, it } from "vitest";
import { buildWallSections, effectiveGroupId, toggleCollapsedGroup, UNGROUPED_KEY } from "./device-groups";

const groups = [{ id: "g-sales", name: "销售组" }, { id: "g-bd", name: "商务BD" }];
const device = (id: string, groupId: string | null) => ({ id, groupId });

describe("监控墙分组", () => {
  it("按分组顺序切分，未分组排在最后", () => {
    const sections = buildWallSections([device("a", "g-bd"), device("b", null), device("c", "g-sales")], groups, [], 16);
    expect(sections.map((section) => [section.name, section.devices.map((item) => item.id)])).toEqual([["销售组", ["c"]], ["商务BD", ["a"]], ["未分组", ["b"]]]);
  });

  it("没有未分组设备时不显示未分组，空分组保留", () => {
    const sections = buildWallSections([device("a", "g-sales")], groups, [], 16);
    expect(sections.map((section) => section.key)).toEqual(["g-sales", "g-bd"]);
    expect(sections[1].devices).toEqual([]);
  });

  it("没有任何分组时返回单个未分组分区", () => {
    const sections = buildWallSections([device("a", null)], [], [], 9);
    expect(sections).toHaveLength(1);
    expect(sections[0].key).toBe(UNGROUPED_KEY);
  });

  it("分组已被删除的设备归入未分组", () => {
    expect(effectiveGroupId(device("a", "g-gone"), groups)).toBeNull();
    const sections = buildWallSections([device("a", "g-gone")], groups, [], 9);
    expect(sections.at(-1)?.devices.map((item) => item.id)).toEqual(["a"]);
  });

  it("宫格数量作为全局上限，折叠分组不占用名额", () => {
    const devices = [device("s1", "g-sales"), device("s2", "g-sales"), device("s3", "g-sales"), device("b1", "g-bd"), device("b2", "g-bd")];
    const expanded = buildWallSections(devices, groups, [], 4);
    expect(expanded.map((section) => [section.visible.length, section.hidden])).toEqual([[3, 0], [1, 1]]);
    const collapsed = buildWallSections(devices, groups, ["g-sales"], 4);
    expect(collapsed[0]).toMatchObject({ collapsed: true, hidden: 0, visible: [] });
    expect(collapsed[1].visible.map((item) => item.id)).toEqual(["b1", "b2"]);
  });

  it("切换折叠状态", () => {
    expect(toggleCollapsedGroup([], "g-sales")).toEqual(["g-sales"]);
    expect(toggleCollapsedGroup(["g-sales", UNGROUPED_KEY], "g-sales")).toEqual([UNGROUPED_KEY]);
  });
});
