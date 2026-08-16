import type { ApiEnvelope } from "@deskpatrol/types";

export class ApiError extends Error {
  constructor(message: string, readonly status: number) { super(message); }
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers: { ...(init.body ? { "Content-Type": "application/json" } : {}), ...init.headers },
  });
  const payload = await response.json().catch(() => ({ error: `服务返回了无法解析的响应 (${response.status})` })) as ApiEnvelope<T>;
  if (!response.ok) throw new ApiError(payload.error || `请求失败 (${response.status})`, response.status);
  if (payload.data === undefined) throw new ApiError("服务响应缺少 data", response.status);
  return payload.data;
}

export const json = (value: unknown) => JSON.stringify(value);
