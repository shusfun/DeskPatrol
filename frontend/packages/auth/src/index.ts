import { json, request } from "@deskpatrol/api";
import type { Administrator } from "@deskpatrol/types";

export const getCurrentAdministrator = () => request<Administrator>("/api/v1/auth/me");
export const login = (loginName: string, password: string) => request<Administrator>("/api/v1/auth/login", { method: "POST", body: json({ loginName, password }) });
export const logout = () => request<{ loggedOut: boolean }>("/api/v1/auth/logout", { method: "POST" });
