import { afterEach, describe, expect, it, vi } from "vitest";
import { THEME_STORAGE_KEY, applyThemeToDocument, getStoredTheme, resolveTheme } from "./theme";

class MemoryStorage {
  private readonly values = new Map<string, string>();
  getItem(key: string) { return this.values.get(key) ?? null; }
  removeItem(key: string) { this.values.delete(key); }
  setItem(key: string, value: string) { this.values.set(key, value); }
}

function installBrowser(dark = false) {
  const localStorage = new MemoryStorage();
  const classes = new Set<string>();
  const root = {
    classList: { toggle: (name: string, enabled: boolean) => enabled ? classes.add(name) : classes.delete(name) },
    dataset: {} as Record<string, string>,
    style: {} as Record<string, string>,
  };
  vi.stubGlobal("window", {
    localStorage,
    sessionStorage: new MemoryStorage(),
    matchMedia: () => ({ matches: dark, addEventListener: vi.fn(), removeEventListener: vi.fn() }),
  });
  vi.stubGlobal("document", { documentElement: root });
  return { classes, localStorage, root };
}

afterEach(() => vi.unstubAllGlobals());

describe("共享主题 Token", () => {
  it("读取 DeskPatrol 主题存储键", () => {
    const { localStorage } = installBrowser();
    localStorage.setItem(THEME_STORAGE_KEY, "dark");
    expect(getStoredTheme()).toBe("dark");
    localStorage.setItem(THEME_STORAGE_KEY, "invalid");
    expect(getStoredTheme()).toBe("system");
  });

  it("跟随系统解析明暗主题", () => {
    installBrowser(true);
    expect(resolveTheme("system")).toBe("dark");
    expect(resolveTheme("light")).toBe("light");
  });

  it("同步根节点 class、data-theme 和 color-scheme", () => {
    const { classes, root } = installBrowser();
    applyThemeToDocument("dark");
    expect(classes.has("dark")).toBe(true);
    expect(root.dataset.theme).toBe("dark");
    expect(root.style.colorScheme).toBe("dark");
  });
});
