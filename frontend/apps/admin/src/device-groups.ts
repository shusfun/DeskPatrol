export const UNGROUPED_KEY = "ungrouped";

export type GroupedDevice = { id: string; groupId: string | null };
export type GroupInfo = { id: string; name: string };
export type WallSection<T> = { key: string; groupId: string | null; name: string; devices: T[]; visible: T[]; collapsed: boolean; hidden: number };

/** 返回设备实际归属的分组 ID；分组已不存在时视为未分组。 */
export function effectiveGroupId(device: GroupedDevice, groups: GroupInfo[]): string | null {
  return device.groupId && groups.some((group) => group.id === device.groupId) ? device.groupId : null;
}

export function sectionKey(groupId: string | null): string {
  return groupId ?? UNGROUPED_KEY;
}

/**
 * 按分组切分监控墙。宫格数量仍是同时预览的设备上限：展开的分组按顺序占用名额，
 * 折叠的分组不占名额，超出上限的设备记入 hidden 并不建立桌面连接。
 */
export function buildWallSections<T extends GroupedDevice>(devices: T[], groups: GroupInfo[], collapsedGroups: string[], tileCount: number): WallSection<T>[] {
  const ungrouped = devices.filter((device) => effectiveGroupId(device, groups) === null);
  const sections = [
    ...groups.map((group) => ({ groupId: group.id as string | null, name: group.name, devices: devices.filter((device) => device.groupId === group.id) })),
    ...(ungrouped.length > 0 || groups.length === 0 ? [{ groupId: null, name: "未分组", devices: ungrouped }] : []),
  ];
  let remaining = tileCount;
  return sections.map((section) => {
    const key = sectionKey(section.groupId);
    const collapsed = collapsedGroups.includes(key);
    const visible = collapsed ? [] : section.devices.slice(0, Math.max(remaining, 0));
    remaining -= visible.length;
    return { ...section, key, collapsed, visible, hidden: collapsed ? 0 : section.devices.length - visible.length };
  });
}

export function toggleCollapsedGroup(collapsedGroups: string[], key: string): string[] {
  return collapsedGroups.includes(key) ? collapsedGroups.filter((value) => value !== key) : [...collapsedGroups, key];
}
