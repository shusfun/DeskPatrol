declare global {
  interface Window {
    wails: { Call: { ByName<T>(name: string, ...args: unknown[]): Promise<T> } };
  }
}
export {};
