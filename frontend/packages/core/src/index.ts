export function formatDate(value?: string) {
  if (!value) return "-";
  return new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

export function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}

export class BrowserCapabilityError extends Error {
  constructor(message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = "BrowserCapabilityError";
  }
}

type BrowserStorage = Pick<Storage, "getItem" | "removeItem" | "setItem">;

function getStorage(kind: "localStorage" | "sessionStorage"): BrowserStorage {
  try {
    const value = globalThis.window?.[kind];
    if (!value) throw new Error(`${kind} unavailable`);
    return value;
  } catch (error) {
    throw new BrowserCapabilityError(`浏览器 ${kind} 不可用`, { cause: error });
  }
}

function storageApi(kind: "localStorage" | "sessionStorage"): BrowserStorage {
  return {
    getItem(key) {
      try { return getStorage(kind).getItem(key); }
      catch (error) {
        if (error instanceof BrowserCapabilityError) throw error;
        throw new BrowserCapabilityError(`读取浏览器存储失败：${key}`, { cause: error });
      }
    },
    removeItem(key) {
      try { getStorage(kind).removeItem(key); }
      catch (error) {
        if (error instanceof BrowserCapabilityError) throw error;
        throw new BrowserCapabilityError(`清除浏览器存储失败：${key}`, { cause: error });
      }
    },
    setItem(key, value) {
      try { getStorage(kind).setItem(key, value); }
      catch (error) {
        if (error instanceof BrowserCapabilityError) throw error;
        throw new BrowserCapabilityError(`写入浏览器存储失败：${key}`, { cause: error });
      }
    },
  };
}

export const browserStorage = {
  local: storageApi("localStorage"),
  session: storageApi("sessionStorage"),
} as const;
