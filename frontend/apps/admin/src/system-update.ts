const versionPattern = /^(\d+)\.(\d+)\.(\d+)$/;

/** candidate 是合法 x.y.z 且高于 current 时返回 true。 */
export function isNewerVersion(candidate: string, current: string): boolean {
  const next = versionPattern.exec(candidate.trim()); const base = versionPattern.exec(current.trim());
  if (!next || !base) return false;
  for (let index = 1; index <= 3; index += 1) {
    const difference = Number(next[index]) - Number(base[index]);
    if (difference !== 0) return difference > 0;
  }
  return false;
}

export const updateStateText: Record<string, string> = { pending: "等待执行", running: "正在更新", succeeded: "更新完成", failed: "更新失败" };
