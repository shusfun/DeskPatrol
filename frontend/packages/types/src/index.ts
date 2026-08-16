export type ApiEnvelope<T> = { data?: T; error?: string };

export type DatabaseConfig = {
  host: string;
  port: number;
  user: string;
  password: string;
  name: string;
  sslMode: "disable" | "require" | "verify-full";
};

export type SetupRequest = {
  publicUrl: string;
  agentPublicPort: number;
  storageDir: string;
  githubRepository: string;
  database: DatabaseConfig;
  admin: { loginName: string; password: string };
};

export type SetupStatus = { needsSetup: boolean; step: string; defaults?: SetupRequest };
export type SetupResult = { installed: true };
export type Administrator = { id: number; loginName: string; role: "super_admin" };
export type Device = {
  id: string;
  name: string;
  architecture: "amd64" | "arm64";
  status: "online" | "offline" | "locked" | "pending" | string;
  screenCount: number;
  lastSeenAt?: string;
  createdAt: string;
};
export type WallLayout = { tileCount: 1 | 4 | 9 | 16; deviceOrder: string[] };
export type ActivationCode = { id: string; label: string; expiresAt: string; usedAt?: string; createdAt: string };
export type ActivationCodeCreated = { id: string; code: string; expiresAt: string };
export type DownloadArtifact = { filename: string; version: string; platform: string; architecture: string; size: number; sha256: string; status: string; createdAt: string };
export type ReleaseJob = { id: string; version: string; status: "queued" | "downloading" | "ready" | "failed"; progress: number; total: number; error: string; createdAt: string; updatedAt: string };
export type DebugSession = { id: string; deviceId: string; token: string; expiresAt: string; status: "active" };
