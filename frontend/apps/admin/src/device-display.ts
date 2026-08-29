export type DeviceDisplayFields = { label: string; name: string };

export function deviceTitle(device: DeviceDisplayFields): string {
  return device.label.trim() || device.name;
}

export function deviceTechnicalSubtitle(device: DeviceDisplayFields): string | null {
  return device.label.trim() ? `Windows 名称：${device.name}` : null;
}
