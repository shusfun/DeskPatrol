package database

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"deskpatrol/internal/appconfig"
	"github.com/jackc/pgx/v5/pgxpool"
)

func DSN(cfg appconfig.Database) string {
	value := &url.URL{Scheme: "postgres", User: url.UserPassword(cfg.User, cfg.Password), Host: cfg.Host + ":" + strconv.Itoa(cfg.Port), Path: cfg.Name}
	query := value.Query()
	query.Set("sslmode", cfg.SSLMode)
	value.RawQuery = query.Encode()
	return value.String()
}

func Open(ctx context.Context, cfg appconfig.Database) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(DSN(cfg))
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL 配置无法解析: %w", err)
	}
	poolConfig.MaxConns = 8
	poolConfig.MinConns = 1
	poolConfig.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("创建 PostgreSQL 连接池失败: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接 PostgreSQL 失败: %w", err)
	}
	return pool, nil
}

const Schema = `
CREATE TABLE IF NOT EXISTS administrators (
  id BIGSERIAL PRIMARY KEY,
  login_name TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE administrators DROP COLUMN IF EXISTS totp_secret;
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  administrator_id BIGINT NOT NULL REFERENCES administrators(id) ON DELETE CASCADE,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS activation_codes (
  id UUID PRIMARY KEY,
  code_hash TEXT NOT NULL UNIQUE,
	code_ciphertext TEXT,
  label TEXT NOT NULL DEFAULT '',
  expires_at TIMESTAMPTZ NOT NULL,
  used_at TIMESTAMPTZ,
	revoked_at TIMESTAMPTZ,
	superseded_at TIMESTAMPTZ,
  installation_id TEXT,
  node_id TEXT,
  created_by BIGINT NOT NULL REFERENCES administrators(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE activation_codes ADD COLUMN IF NOT EXISTS code_ciphertext TEXT;
ALTER TABLE activation_codes ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;
ALTER TABLE activation_codes ADD COLUMN IF NOT EXISTS superseded_at TIMESTAMPTZ;
CREATE TABLE IF NOT EXISTS deployment_migrations (
  name TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
WITH applied AS (
  INSERT INTO deployment_migrations(name) VALUES('2026-08-17-connection-key') ON CONFLICT DO NOTHING RETURNING name
)
DELETE FROM activation_codes WHERE used_at IS NULL AND EXISTS (SELECT 1 FROM applied);
WITH applied AS (
  INSERT INTO deployment_migrations(name) VALUES('2026-08-17-recoverable-connection-keys') ON CONFLICT DO NOTHING RETURNING name
)
DELETE FROM activation_codes WHERE used_at IS NULL AND code_ciphertext IS NULL AND EXISTS (SELECT 1 FROM applied);
CREATE TABLE IF NOT EXISTS devices (
  id UUID PRIMARY KEY,
  installation_id TEXT NOT NULL UNIQUE,
	node_id TEXT,
	mesh_id TEXT,
	client_token_hash TEXT UNIQUE,
  name TEXT NOT NULL,
  architecture TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  screen_count INTEGER NOT NULL DEFAULT 1 CHECK (screen_count > 0),
  selected_display_id INTEGER CHECK (selected_display_id IS NULL OR selected_display_id BETWEEN 0 AND 65534),
  last_seen_at TIMESTAMPTZ,
	deleted_at TIMESTAMPTZ,
	deleted_by BIGINT REFERENCES administrators(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS system_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS enrollment_downloads (
  device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  agent_path TEXT NOT NULL,
  agent_sha256 TEXT NOT NULL,
  size_bytes BIGINT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS mesh_id TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS client_token_hash TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS deleted_by BIGINT REFERENCES administrators(id);
ALTER TABLE devices ADD COLUMN IF NOT EXISTS selected_display_id INTEGER;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'devices_selected_display_id_check') THEN
    ALTER TABLE devices ADD CONSTRAINT devices_selected_display_id_check CHECK (selected_display_id IS NULL OR selected_display_id BETWEEN 0 AND 65534);
  END IF;
END $$;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM deployment_migrations WHERE name = '2026-08-17-active-device-identifiers') THEN
    ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_node_id_key;
    ALTER TABLE devices DROP CONSTRAINT IF EXISTS devices_mesh_id_key;
    DROP INDEX IF EXISTS idx_devices_mesh_id;
    INSERT INTO deployment_migrations(name) VALUES('2026-08-17-active-device-identifiers');
  END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_node_id_active ON devices(node_id) WHERE node_id IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_mesh_id_active ON devices(mesh_id) WHERE mesh_id IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_client_token ON devices(client_token_hash) WHERE client_token_hash IS NOT NULL;
CREATE TABLE IF NOT EXISTS wall_layouts (
  administrator_id BIGINT PRIMARY KEY REFERENCES administrators(id) ON DELETE CASCADE,
  tile_count INTEGER NOT NULL DEFAULT 9 CHECK (tile_count IN (1,4,9,16)),
  device_order JSONB NOT NULL DEFAULT '[]'::jsonb,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS release_artifacts (
  id UUID PRIMARY KEY,
  version TEXT NOT NULL,
  platform TEXT NOT NULL,
  architecture TEXT NOT NULL,
  filename TEXT NOT NULL UNIQUE,
  size_bytes BIGINT NOT NULL,
  sha256 TEXT NOT NULL,
  local_path TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS release_jobs (
  id UUID PRIMARY KEY,
  version TEXT NOT NULL,
  status TEXT NOT NULL,
  progress BIGINT NOT NULL DEFAULT 0,
  total BIGINT NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS debug_sessions (
  id UUID PRIMARY KEY,
  device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  created_by BIGINT NOT NULL REFERENCES administrators(id),
  access_token_hash TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  closed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE debug_sessions ADD COLUMN IF NOT EXISTS access_token_hash TEXT;
UPDATE debug_sessions SET closed_at=COALESCE(closed_at,NOW()) WHERE access_token_hash IS NULL;
CREATE TABLE IF NOT EXISTS debug_audits (
  id BIGSERIAL PRIMARY KEY,
  session_id UUID NOT NULL REFERENCES debug_sessions(id) ON DELETE CASCADE,
  operation TEXT NOT NULL,
  script_sha256 TEXT NOT NULL DEFAULT '',
  duration_ms BIGINT NOT NULL DEFAULT 0,
  exit_code INTEGER,
  output_truncated BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS frontend_errors (
  event_id UUID PRIMARY KEY,
	device_id UUID REFERENCES devices(id) ON DELETE CASCADE,
  source TEXT NOT NULL,
  category TEXT NOT NULL,
  message TEXT NOT NULL,
  stack TEXT NOT NULL DEFAULT '',
  client_version TEXT NOT NULL DEFAULT '',
  occurred_at TIMESTAMPTZ NOT NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE frontend_errors ADD COLUMN IF NOT EXISTS device_id UUID REFERENCES devices(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_devices_last_seen_at ON devices(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_debug_sessions_device ON debug_sessions(device_id, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_debug_sessions_access_token ON debug_sessions(access_token_hash) WHERE access_token_hash IS NOT NULL;
`
