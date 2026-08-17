package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"deskpatrol/internal/appconfig"
	"deskpatrol/internal/buildinfo"
	"deskpatrol/internal/connectionkey"
	"deskpatrol/internal/credentialcrypto"
	"deskpatrol/internal/database"
	"deskpatrol/internal/meshcentral"
	"deskpatrol/internal/releasesync"
	"deskpatrol/internal/security"
	"deskpatrol/internal/setup"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "deskpatrol_session"

var errDeviceDeleted = errors.New("设备已被管理员删除，请重新激活")

type App struct {
	configPath string
	assetsDir  string
	logger     *slog.Logger
	mu         sync.RWMutex
	config     *appconfig.Config
	pool       *pgxpool.Pool
	releases   *releasesync.Service
	mesh       meshController
}

type meshController interface {
	AddDeviceGroup(context.Context, string, string) (string, error)
	RemoveDeviceGroup(context.Context, string) error
	MoveToDeviceGroup(context.Context, string, string) error
	CreateDesktopShare(context.Context, string, int) (string, error)
	DownloadAgent(context.Context, string, string, string) (string, int64, error)
	RunPowerShell(context.Context, string, string, time.Duration) (meshcentral.CommandResult, error)
}

type principal struct {
	ID        int64
	LoginName string
}

type contextKey string

const (
	principalKey      contextKey = "principal"
	debugTokenHashKey contextKey = "debug_token_hash"
)

func New(configPath, assetsDir string, logger *slog.Logger) (*App, error) {
	app := &App{configPath: configPath, assetsDir: filepath.Clean(assetsDir), logger: logger}
	if !appconfig.NeedsSetup(configPath) {
		if err := app.reload(context.Background()); err != nil {
			return nil, err
		}
	}
	return app, nil
}

func (a *App) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pool != nil {
		a.pool.Close()
		a.pool = nil
	}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /api/setup/status", a.setupStatus)
	mux.HandleFunc("POST /api/setup/test-db", a.setupTestDatabase)
	mux.HandleFunc("POST /api/setup/install", a.setupInstall)
	mux.HandleFunc("POST /api/internal/meshcentral/events", a.meshCentralEvent)
	mux.HandleFunc("POST /api/v1/auth/login", a.login)
	mux.Handle("POST /api/v1/auth/logout", a.auth(http.HandlerFunc(a.logout)))
	mux.Handle("GET /api/v1/auth/me", a.auth(http.HandlerFunc(a.me)))
	mux.Handle("GET /api/v1/devices", a.auth(http.HandlerFunc(a.listDevices)))
	mux.Handle("DELETE /api/v1/devices/{id}", a.auth(http.HandlerFunc(a.deleteDevice)))
	mux.Handle("POST /api/v1/devices/{id}/desktop-ticket", a.auth(http.HandlerFunc(a.createDesktopTicket)))
	mux.Handle("GET /api/v1/wall-layout", a.auth(http.HandlerFunc(a.getWallLayout)))
	mux.Handle("PUT /api/v1/wall-layout", a.auth(http.HandlerFunc(a.putWallLayout)))
	mux.Handle("GET /api/v1/activation-codes", a.auth(http.HandlerFunc(a.listActivationCodes)))
	mux.Handle("POST /api/v1/activation-codes", a.auth(http.HandlerFunc(a.createActivationCode)))
	mux.Handle("POST /api/v1/activation-codes/{id}/copy", a.auth(http.HandlerFunc(a.copyActivationCode)))
	mux.HandleFunc("POST /api/v1/client/activate", a.activateClient)
	mux.HandleFunc("POST /api/v1/client/agent/prepare", a.prepareEnrollmentAgent)
	mux.HandleFunc("GET /api/v1/client/agent/{token}", a.downloadEnrollmentAgent)
	mux.HandleFunc("GET /api/v1/client/status", a.clientStatus)
	mux.HandleFunc("POST /api/v1/client/runtime-errors", a.ingestClientErrors)
	mux.Handle("GET /api/v1/downloads", a.auth(http.HandlerFunc(a.listDownloads)))
	mux.Handle("GET /api/v1/downloads/{filename}", a.auth(http.HandlerFunc(a.download)))
	mux.Handle("GET /api/v1/releases/jobs", a.auth(http.HandlerFunc(a.listReleaseJobs)))
	mux.Handle("POST /api/v1/releases/sync", a.auth(http.HandlerFunc(a.syncRelease)))
	mux.Handle("GET /api/v1/audit-logs", a.auth(http.HandlerFunc(a.listAuditLogs)))
	mux.Handle("POST /api/v1/runtime-debug/sessions", a.auth(http.HandlerFunc(a.createDebugSession)))
	mux.Handle("DELETE /api/v1/runtime-debug/sessions/{id}", a.auth(http.HandlerFunc(a.closeDebugSession)))
	mux.Handle("POST /api/v1/runtime-debug/sessions/{id}/inspect", a.debugAuth(http.HandlerFunc(a.runDiagnosticInspection)))
	mux.Handle("POST /api/v1/runtime-debug/sessions/{id}/powershell", a.debugAuth(http.HandlerFunc(a.runPowerShell)))
	mux.HandleFunc("/", a.static)
	return a.securityHeaders(a.requestLog(mux))
}

func (a *App) reload(ctx context.Context) error {
	cfg, err := appconfig.Load(a.configPath)
	if err != nil {
		return err
	}
	pool, err := database.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	a.mu.Lock()
	old := a.pool
	a.pool = pool
	a.config = &cfg
	a.releases = releasesync.New(cfg, pool)
	a.mesh = meshcentral.NewController(cfg.MeshLoginKey, cfg.PluginToken, cfg.StorageDir)
	a.mu.Unlock()
	if old != nil {
		old.Close()
	}
	if err := a.releases.RetainLatest(ctx); err != nil {
		return fmt.Errorf("收口 Release 缓存失败: %w", err)
	}
	return nil
}

func (a *App) snapshot() (*appconfig.Config, *pgxpool.Pool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.config == nil || a.pool == nil {
		return nil, nil, errors.New("系统尚未完成初始化")
	}
	cfg := *a.config
	return &cfg, a.pool, nil
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	status := map[string]any{"status": "setup", "version": buildinfo.Version}
	if !appconfig.NeedsSetup(a.configPath) {
		_, pool, err := a.snapshot()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("PostgreSQL 健康检查失败: %w", err))
			return
		}
		status["status"] = "healthy"
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *App) setupStatus(w http.ResponseWriter, _ *http.Request) {
	needsSetup := appconfig.NeedsSetup(a.configPath)
	data := map[string]any{"needsSetup": needsSetup, "step": "complete"}
	if needsSetup {
		defaults, err := setup.Defaults(a.configPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		data["step"] = "welcome"
		data["defaults"] = defaults
	}
	writeJSON(w, http.StatusOK, data)
}

func (a *App) setupTestDatabase(w http.ResponseWriter, r *http.Request) {
	if !appconfig.NeedsSetup(a.configPath) {
		writeError(w, http.StatusForbidden, errors.New("系统已完成初始化"))
		return
	}
	var req struct {
		Database appconfig.Database `json:"database"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := setup.TestDatabase(r.Context(), req.Database); err != nil {
		a.logger.Error("setup database test failed", "error", security.Redact(err.Error()))
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"connected": true})
}

func (a *App) setupInstall(w http.ResponseWriter, r *http.Request) {
	var req setup.Request
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := setup.Install(r.Context(), a.configPath, req)
	if err != nil {
		a.logger.Error("setup install failed", "error", security.Redact(err.Error()))
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := a.reload(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("初始化已写入，但运行服务加载失败: %w", err))
		return
	}
	if _, err := a.releaseService().Enqueue(r.Context(), buildinfo.Version); err != nil {
		a.logger.Error("initial release sync enqueue failed", "error", security.Redact(err.Error()))
	}
	writeJSON(w, http.StatusCreated, result)
}

func (a *App) meshCentralEvent(w http.ResponseWriter, r *http.Request) {
	cfg, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	provided := r.Header.Get("X-DeskPatrol-Plugin-Token")
	if len(provided) != len(cfg.PluginToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(cfg.PluginToken)) != 1 {
		writeError(w, http.StatusUnauthorized, errors.New("MeshCentral 插件认证失败"))
		return
	}
	var event struct {
		Type         string `json:"type"`
		NodeID       string `json:"nodeId"`
		MeshID       string `json:"meshId"`
		Name         string `json:"name"`
		AgentType    int    `json:"agentType"`
		AgentVersion int    `json:"agentVersion"`
	}
	if !decodeJSON(w, r, &event) {
		return
	}
	if (event.Type != "agent_stable" && event.Type != "agent_heartbeat" && event.Type != "agent_offline") || !strings.HasPrefix(event.NodeID, "node/") || !strings.HasPrefix(event.MeshID, "mesh/") {
		writeError(w, http.StatusBadRequest, errors.New("MeshCentral Agent 事件格式不正确"))
		return
	}
	if event.Type != "agent_stable" {
		status := "online"
		if event.Type == "agent_offline" {
			status = "offline"
		}
		result, err := pool.Exec(r.Context(), `UPDATE devices SET status=$2,last_seen_at=CASE WHEN $2='online' THEN NOW() ELSE last_seen_at END WHERE node_id=$1 AND deleted_at IS NULL`, event.NodeID, status)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if result.RowsAffected() != 1 {
			deleted, deletedErr := meshEventMatchesDeletedDevice(r.Context(), pool, event.NodeID, event.MeshID)
			if deletedErr != nil {
				writeError(w, http.StatusInternalServerError, deletedErr)
				return
			}
			if deleted {
				writeJSON(w, http.StatusOK, map[string]bool{"accepted": true, "ignored": true})
				return
			}
			writeError(w, http.StatusConflict, errors.New("MeshCentral NodeID 尚未绑定激活记录"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
		return
	}
	tx, err := pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var deviceID string
	err = tx.QueryRow(r.Context(), `SELECT id FROM devices WHERE deleted_at IS NULL AND (node_id=$1 OR (node_id IS NULL AND mesh_id=$2)) FOR UPDATE`, event.NodeID, event.MeshID).Scan(&deviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		deleted, deletedErr := meshEventMatchesDeletedDevice(r.Context(), pool, event.NodeID, event.MeshID)
		if deletedErr != nil {
			writeError(w, http.StatusInternalServerError, deletedErr)
			return
		}
		if deleted {
			writeJSON(w, http.StatusOK, map[string]bool{"accepted": true, "ignored": true})
			return
		}
		writeError(w, http.StatusConflict, errors.New("MeshCentral NodeID 尚未绑定激活记录"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE devices SET node_id=$2,name=CASE WHEN $3='' THEN name ELSE $3 END,status='online',last_seen_at=NOW() WHERE id=$1`, deviceID, event.NodeID, event.Name); err != nil {
		writeError(w, http.StatusConflict, fmt.Errorf("绑定 MeshCentral NodeID 失败: %w", err))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	controller := a.meshController()
	permanentMeshID, err := a.ensurePermanentMesh(r.Context(), pool, controller)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if event.MeshID != permanentMeshID {
		if err := controller.MoveToDeviceGroup(r.Context(), event.NodeID, permanentMeshID); err != nil {
			writeError(w, http.StatusBadGateway, fmt.Errorf("迁移设备到正式设备池失败: %w", err))
			return
		}
	}
	if _, err := pool.Exec(r.Context(), `UPDATE devices SET mesh_id=$2 WHERE id=$1`, deviceID, permanentMeshID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}

func meshEventMatchesDeletedDevice(ctx context.Context, pool *pgxpool.Pool, nodeID, meshID string) (bool, error) {
	var found bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices WHERE deleted_at IS NOT NULL AND (node_id=$1 OR (node_id IS NULL AND mesh_id=$2)))`, nodeID, meshID).Scan(&found)
	return found, err
}

func (a *App) releaseService() *releasesync.Service {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.releases
}

func (a *App) meshController() meshController {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mesh
}

func (a *App) ensurePermanentMesh(ctx context.Context, pool *pgxpool.Pool, controller meshController) (string, error) {
	var meshID string
	err := pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key='meshcentral.permanent_mesh_id'`).Scan(&meshID)
	if err == nil {
		return meshID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	created, err := controller.AddDeviceGroup(ctx, "DeskPatrol Devices", "DeskPatrol 正式设备池")
	if err != nil {
		return "", fmt.Errorf("创建 MeshCentral 正式设备池失败: %w", err)
	}
	result, err := pool.Exec(ctx, `INSERT INTO system_settings(key,value) VALUES('meshcentral.permanent_mesh_id',$1) ON CONFLICT(key) DO NOTHING`, created)
	if err != nil {
		_ = controller.RemoveDeviceGroup(context.Background(), created)
		return "", err
	}
	if result.RowsAffected() == 1 {
		return created, nil
	}
	_ = controller.RemoveDeviceGroup(context.Background(), created)
	if err := pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key='meshcentral.permanent_mesh_id'`).Scan(&meshID); err != nil {
		return "", err
	}
	return meshID, nil
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LoginName string `json:"loginName"`
		Password  string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	var administrator principal
	var passwordHash string
	err = pool.QueryRow(r.Context(), `SELECT id,login_name,password_hash FROM administrators WHERE login_name=$1`, strings.TrimSpace(req.LoginName)).Scan(&administrator.ID, &administrator.LoginName, &passwordHash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, errors.New("账号或密码不正确"))
		return
	}
	token, tokenHash, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	expiresAt := time.Now().Add(12 * time.Hour)
	if _, err := pool.Exec(r.Context(), `INSERT INTO sessions(token_hash,administrator_id,expires_at) VALUES($1,$2,$3)`, tokenHash, administrator.ID, expiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("创建登录会话失败: %w", err))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(cfg.PublicURL, "https://"), SameSite: http.SameSiteStrictMode, Expires: expiresAt})
	writeJSON(w, http.StatusOK, map[string]any{"id": administrator.ID, "loginName": administrator.LoginName})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookie)
	if cookie != nil {
		_, pool, err := a.snapshot()
		if err == nil {
			_, _ = pool.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, hash(cookie.Value))
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"loggedOut": true})
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(principalKey).(principal)
	writeJSON(w, http.StatusOK, map[string]any{"id": user.ID, "loginName": user.LoginName, "role": "super_admin"})
}

func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			writeError(w, http.StatusUnauthorized, errors.New("请先登录"))
			return
		}
		_, pool, err := a.snapshot()
		if err != nil {
			writeError(w, http.StatusPreconditionFailed, err)
			return
		}
		var user principal
		err = pool.QueryRow(r.Context(), `SELECT a.id,a.login_name FROM sessions s JOIN administrators a ON a.id=s.administrator_id WHERE s.token_hash=$1 AND s.expires_at>NOW()`, hash(cookie.Value)).Scan(&user.ID, &user.LoginName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, errors.New("登录会话已失效"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, user)))
	})
}

func (a *App) debugAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		if authorization == "" {
			a.auth(next).ServeHTTP(w, r)
			return
		}
		token, ok := bearerToken(authorization)
		if !ok {
			writeError(w, http.StatusUnauthorized, errors.New("诊断访问口令不正确"))
			return
		}
		ctx := context.WithValue(r.Context(), debugTokenHashKey, hash(token))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) listDevices(w http.ResponseWriter, r *http.Request) {
	_, pool, _ := a.snapshot()
	rows, err := pool.Query(r.Context(), `SELECT id,name,architecture,status,screen_count,last_seen_at,created_at FROM devices WHERE deleted_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, architecture, status string
		var screens int
		var lastSeen *time.Time
		var created time.Time
		if err := rows.Scan(&id, &name, &architecture, &status, &screens, &lastSeen, &created); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if lastSeen == nil || time.Since(*lastSeen) > 90*time.Second {
			status = "offline"
		}
		items = append(items, map[string]any{"id": id, "name": name, "architecture": architecture, "status": status, "screenCount": screens, "lastSeenAt": lastSeen, "createdAt": created})
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *App) deleteDevice(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(principalKey).(principal)
	_, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	tx, err := pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	deviceID := r.PathValue("id")
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("获取设备删除锁失败: %w", err))
		return
	}
	var installationID string
	err = tx.QueryRow(r.Context(), `SELECT installation_id FROM devices WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, deviceID).Scan(&installationID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, errors.New("设备不存在或已经删除"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE devices SET deleted_at=NOW(),deleted_by=$2,status='deleted' WHERE id=$1`, deviceID, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("删除设备失败: %w", err))
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE activation_codes SET revoked_at=COALESCE(revoked_at,NOW()),code_ciphertext=NULL WHERE installation_id=$1`, installationID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("撤销设备连接密钥失败: %w", err))
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE debug_sessions SET closed_at=COALESCE(closed_at,NOW()) WHERE device_id=$1`, deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("关闭设备诊断会话失败: %w", err))
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM enrollment_downloads WHERE device_id=$1`, deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("废止 Agent 下载票据失败: %w", err))
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE wall_layouts SET device_order=(SELECT COALESCE(jsonb_agg(element),'[]'::jsonb) FROM jsonb_array_elements(device_order) AS element WHERE element<>to_jsonb($1::text)) WHERE device_order @> jsonb_build_array($1::text)`, deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("清理监控墙设备顺序失败: %w", err))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("提交设备删除事务失败: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (a *App) createDesktopTicket(w http.ResponseWriter, r *http.Request) {
	cfg, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	var nodeID, status string
	var lastSeen *time.Time
	if err := pool.QueryRow(r.Context(), `SELECT node_id,status,last_seen_at FROM devices WHERE id=$1 AND node_id IS NOT NULL AND deleted_at IS NULL`, r.PathValue("id")).Scan(&nodeID, &status, &lastSeen); err != nil {
		writeError(w, http.StatusNotFound, errors.New("设备不存在或尚未完成连接"))
		return
	}
	if status != "online" || lastSeen == nil || time.Since(*lastSeen) > 90*time.Second {
		writeError(w, http.StatusConflict, errors.New("设备当前不在线"))
		return
	}
	shareURL, err := a.meshController().CreateDesktopShare(r.Context(), nodeID, 5)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("创建桌面查看票据失败: %w", err))
		return
	}
	shareURL, err = canonicalDesktopShareURL(shareURL, cfg.PublicURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, errors.New("MeshCentral 返回了非本站桌面分享地址"))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"url": shareURL, "expiresAt": time.Now().Add(5 * time.Minute), "viewOnly": true})
}

func canonicalDesktopShareURL(shareURL, publicURL string) (string, error) {
	publicOrigin, publicErr := url.Parse(publicURL)
	parsed, shareErr := url.Parse(shareURL)
	if publicErr != nil || shareErr != nil || !sameHTTPSOrigin(parsed, publicOrigin) || parsed.Path != "/sharing" {
		return "", errors.New("桌面分享地址不正确")
	}
	parsed.Scheme = publicOrigin.Scheme
	parsed.Host = publicOrigin.Host
	return parsed.String(), nil
}

func sameHTTPSOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || left.User != nil || right.User != nil || left.Scheme != "https" || right.Scheme != "https" {
		return false
	}
	leftHost, rightHost := strings.ToLower(left.Hostname()), strings.ToLower(right.Hostname())
	if leftHost == "" || leftHost != rightHost {
		return false
	}
	effectivePort := func(value *url.URL) string {
		if value.Port() != "" {
			return value.Port()
		}
		return "443"
	}
	return effectivePort(left) == effectivePort(right)
}

func (a *App) getWallLayout(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(principalKey).(principal)
	_, pool, _ := a.snapshot()
	var tileCount int
	var order []byte
	err := pool.QueryRow(r.Context(), `SELECT tile_count,device_order FROM wall_layouts WHERE administrator_id=$1`, user.ID).Scan(&tileCount, &order)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"tileCount": 9, "deviceOrder": []string{}})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tileCount": tileCount, "deviceOrder": jsonRaw(order)})
}

type jsonRaw []byte

func (value jsonRaw) MarshalJSON() ([]byte, error) { return value, nil }

func (a *App) putWallLayout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TileCount   int      `json:"tileCount"`
		DeviceOrder []string `json:"deviceOrder"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.TileCount != 1 && req.TileCount != 4 && req.TileCount != 9 && req.TileCount != 16 {
		writeError(w, http.StatusBadRequest, errors.New("宫格数量只支持 1、4、9、16"))
		return
	}
	user := r.Context().Value(principalKey).(principal)
	_, pool, _ := a.snapshot()
	if _, err := pool.Exec(r.Context(), `INSERT INTO wall_layouts(administrator_id,tile_count,device_order) VALUES($1,$2,$3) ON CONFLICT(administrator_id) DO UPDATE SET tile_count=EXCLUDED.tile_count,device_order=EXCLUDED.device_order,updated_at=NOW()`, user.ID, req.TileCount, req.DeviceOrder); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (a *App) listActivationCodes(w http.ResponseWriter, r *http.Request) {
	_, pool, _ := a.snapshot()
	if _, err := pool.Exec(r.Context(), `UPDATE activation_codes SET code_ciphertext=NULL WHERE used_at IS NULL AND expires_at<=NOW() AND code_ciphertext IS NOT NULL`); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("清理过期连接密钥失败: %w", err))
		return
	}
	rows, err := pool.Query(r.Context(), `SELECT id,label,expires_at,used_at,revoked_at,superseded_at,code_ciphertext,created_at FROM activation_codes ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, label string
		var expires, created time.Time
		var used, revoked, superseded *time.Time
		var ciphertext *string
		if err := rows.Scan(&id, &label, &expires, &used, &revoked, &superseded, &ciphertext, &created); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		status := activationCodeStatus(expires, used, revoked, superseded)
		items = append(items, map[string]any{"id": id, "label": label, "expiresAt": expires, "usedAt": used, "createdAt": created, "status": status, "canCopy": status == "unused" && ciphertext != nil && *ciphertext != ""})
	}
	writeJSON(w, http.StatusOK, items)
}

func activationCodeStatus(expires time.Time, used, revoked, superseded *time.Time) string {
	if revoked != nil {
		return "revoked"
	}
	if superseded != nil {
		return "superseded"
	}
	if used != nil {
		return "used"
	}
	if !time.Now().Before(expires) {
		return "expired"
	}
	return "unused"
}

func (a *App) createActivationCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label string `json:"label"`
		Days  int    `json:"days"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Days == 0 {
		req.Days = 7
	}
	if req.Days < 1 || req.Days > 30 || len(req.Label) > 100 {
		writeError(w, http.StatusBadRequest, errors.New("有效期必须介于 1 和 30 天，备注不能超过 100 字符"))
		return
	}
	code, err := activationCode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	id, _ := uuid()
	expires := time.Now().Add(time.Duration(req.Days) * 24 * time.Hour)
	user := r.Context().Value(principalKey).(principal)
	cfg, pool, _ := a.snapshot()
	connectionKey, err := connectionkey.Build(cfg.PublicURL, code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("生成连接密钥失败: %w", err))
		return
	}
	cipher, err := credentialcrypto.New(cfg.SessionSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("初始化连接密钥加密失败: %w", err))
		return
	}
	ciphertext, err := cipher.Encrypt(code, []byte("activation-code:"+id))
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("加密连接密钥失败: %w", err))
		return
	}
	if _, err := pool.Exec(r.Context(), `INSERT INTO activation_codes(id,code_hash,code_ciphertext,label,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6)`, id, hash(code), ciphertext, strings.TrimSpace(req.Label), expires, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "connectionKey": connectionKey, "expiresAt": expires})
}

func (a *App) copyActivationCode(w http.ResponseWriter, r *http.Request) {
	cfg, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	tx, err := pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var codeHash, ciphertext string
	var expires time.Time
	var used, revoked, superseded *time.Time
	err = tx.QueryRow(r.Context(), `SELECT code_hash,COALESCE(code_ciphertext,''),expires_at,used_at,revoked_at,superseded_at FROM activation_codes WHERE id=$1 FOR UPDATE`, r.PathValue("id")).Scan(&codeHash, &ciphertext, &expires, &used, &revoked, &superseded)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, errors.New("连接密钥不存在"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	switch activationCodeStatus(expires, used, revoked, superseded) {
	case "used":
		writeError(w, http.StatusConflict, errors.New("连接密钥已经使用，不能再次复制"))
		return
	case "expired":
		if _, err := tx.Exec(r.Context(), `UPDATE activation_codes SET code_ciphertext=NULL WHERE id=$1`, r.PathValue("id")); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("清理过期连接密钥失败: %w", err))
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeError(w, http.StatusGone, errors.New("连接密钥已过期，请重新生成"))
		return
	case "superseded":
		writeError(w, http.StatusConflict, errors.New("连接密钥已被新密钥替换"))
		return
	case "revoked":
		writeError(w, http.StatusConflict, errors.New("连接密钥已撤销"))
		return
	}
	if ciphertext == "" {
		writeError(w, http.StatusConflict, errors.New("旧版本连接密钥无法恢复，请重新生成"))
		return
	}
	cipher, err := credentialcrypto.New(cfg.SessionSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("初始化连接密钥解密失败: %w", err))
		return
	}
	code, err := cipher.Decrypt(ciphertext, []byte("activation-code:"+r.PathValue("id")))
	if err != nil || hash(code) != codeHash {
		writeError(w, http.StatusInternalServerError, errors.New("连接密钥密文校验失败"))
		return
	}
	connectionKey, err := connectionkey.Build(cfg.PublicURL, code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("生成连接密钥失败: %w", err))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "connectionKey": connectionKey, "expiresAt": expires})
}

func (a *App) activateClient(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code           string `json:"code"`
		InstallationID string `json:"installationId"`
		Architecture   string `json:"architecture"`
		DeviceName     string `json:"deviceName"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Architecture != "amd64" && req.Architecture != "arm64" || strings.TrimSpace(req.InstallationID) == "" || len(req.DeviceName) > 128 {
		writeError(w, http.StatusBadRequest, errors.New("客户端激活参数不正确"))
		return
	}
	if len(req.InstallationID) != 32 || len(strings.TrimSpace(req.DeviceName)) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("安装 ID 或设备名称不正确"))
		return
	}
	_, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	tx, err := pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var codeID string
	var expiresAt time.Time
	var usedAt *time.Time
	var boundInstallation *string
	var revokedAt, supersededAt *time.Time
	err = tx.QueryRow(r.Context(), `SELECT id,expires_at,used_at,installation_id,revoked_at,superseded_at FROM activation_codes WHERE code_hash=$1 FOR UPDATE`, hash(strings.TrimSpace(req.Code))).Scan(&codeID, &expiresAt, &usedAt, &boundInstallation, &revokedAt, &supersededAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, errors.New("激活码不存在"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if revokedAt != nil {
		writeError(w, http.StatusConflict, errors.New("连接密钥已撤销，请生成新密钥"))
		return
	}
	if supersededAt != nil {
		writeError(w, http.StatusConflict, errors.New("连接密钥已被新密钥替换，请使用最新密钥"))
		return
	}
	if time.Now().After(expiresAt) {
		if _, err := tx.Exec(r.Context(), `UPDATE activation_codes SET code_ciphertext=NULL WHERE id=$1`, codeID); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("清理过期连接密钥失败: %w", err))
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeError(w, http.StatusGone, errors.New("激活码已过期"))
		return
	}
	if usedAt != nil && (boundInstallation == nil || *boundInstallation != req.InstallationID) {
		writeError(w, http.StatusConflict, errors.New("激活码已被其他设备使用"))
		return
	}
	var deviceID, deviceName string
	var nodeID *string
	newEnrollment := usedAt == nil
	if !newEnrollment {
		err = tx.QueryRow(r.Context(), `SELECT id,name,node_id FROM devices WHERE installation_id=$1 AND deleted_at IS NULL FOR UPDATE`, req.InstallationID).Scan(&deviceID, &deviceName, &nodeID)
		if err != nil {
			writeError(w, http.StatusConflict, errors.New("激活恢复记录不存在，请联系管理员"))
			return
		}
	} else {
		existingErr := tx.QueryRow(r.Context(), `SELECT id,name,node_id FROM devices WHERE installation_id=$1 FOR UPDATE`, req.InstallationID).Scan(&deviceID, &deviceName, &nodeID)
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			if existingErr != nil {
				writeError(w, http.StatusInternalServerError, existingErr)
				return
			}
			status := "offline"
			if nodeID == nil || strings.TrimSpace(*nodeID) == "" {
				status = "pending"
			}
			deviceName = strings.TrimSpace(req.DeviceName)
			if _, err := tx.Exec(r.Context(), `UPDATE devices SET name=$2,architecture=$3,status=$4,deleted_at=NULL,deleted_by=NULL WHERE id=$1`, deviceID, deviceName, req.Architecture, status); err != nil {
				writeError(w, http.StatusInternalServerError, fmt.Errorf("恢复设备激活记录失败: %w", err))
				return
			}
		} else {
			deviceID, _ = uuid()
			deviceName = strings.TrimSpace(req.DeviceName)
			if _, err := tx.Exec(r.Context(), `INSERT INTO devices(id,installation_id,name,architecture,status) VALUES($1,$2,$3,$4,'pending')`, deviceID, req.InstallationID, deviceName, req.Architecture); err != nil {
				writeError(w, http.StatusConflict, fmt.Errorf("创建设备激活记录失败: %w", err))
				return
			}
		}
		if _, err := tx.Exec(r.Context(), `UPDATE activation_codes SET superseded_at=NOW(),code_ciphertext=NULL WHERE installation_id=$1 AND id<>$2 AND revoked_at IS NULL AND superseded_at IS NULL`, req.InstallationID, codeID); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("替换旧连接密钥失败: %w", err))
			return
		}
		if _, err := tx.Exec(r.Context(), `UPDATE activation_codes SET used_at=NOW(),installation_id=$2,code_ciphertext=NULL WHERE id=$1`, codeID, req.InstallationID); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	deviceToken, deviceTokenHash, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE devices SET client_token_hash=$2 WHERE id=$1`, deviceID, deviceTokenHash); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("提交激活事务失败: %w", err))
		return
	}
	writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[newEnrollment], map[string]any{
		"deviceId": deviceID, "deviceName": deviceName, "deviceToken": deviceToken, "agentSetupRequired": nodeID == nil || strings.TrimSpace(*nodeID) == "",
	})
}

func (a *App) prepareEnrollmentAgent(w http.ResponseWriter, r *http.Request) {
	cfg, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	deviceID, err := authenticateDevice(r, pool)
	if err != nil {
		writeDeviceAuthError(w, err)
		return
	}
	connection, err := pool.Acquire(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer connection.Release()
	var locked bool
	if err := connection.QueryRow(r.Context(), `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, deviceID).Scan(&locked); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("获取 Agent 准备锁失败: %w", err))
		return
	}
	if !locked {
		writeError(w, http.StatusConflict, errors.New("Agent 准备任务正在执行"))
		return
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, unlockErr := connection.Exec(unlockContext, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, deviceID); unlockErr != nil {
			a.logger.Error("agent preparation lock release failed", "deviceId", deviceID, "error", security.Redact(unlockErr.Error()))
			_ = connection.Conn().Close(unlockContext)
		}
	}()

	var architecture string
	var meshID *string
	var deletedAt *time.Time
	if err := connection.QueryRow(r.Context(), `SELECT architecture,mesh_id,deleted_at FROM devices WHERE id=$1`, deviceID).Scan(&architecture, &meshID, &deletedAt); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if deletedAt != nil {
		writeError(w, http.StatusGone, errDeviceDeleted)
		return
	}
	var agentPath, agentSHA string
	var agentSize int64
	cacheErr := connection.QueryRow(r.Context(), `SELECT agent_path,agent_sha256,size_bytes FROM enrollment_downloads WHERE device_id=$1`, deviceID).Scan(&agentPath, &agentSHA, &agentSize)
	if cacheErr == nil {
		valid, validationErr := validateEnrollmentFile(agentPath, agentSHA, agentSize)
		if validationErr != nil {
			a.logger.Warn("cached enrollment validation failed", "deviceId", deviceID, "error", security.Redact(validationErr.Error()))
		} else if valid {
			a.respondWithEnrollmentAgent(w, r, connection, cfg, deviceID, agentPath, agentSHA, agentSize)
			return
		}
	} else if !errors.Is(cacheErr, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, cacheErr)
		return
	}

	controller := a.meshController()
	if meshID == nil || strings.TrimSpace(*meshID) == "" {
		created, createErr := controller.AddDeviceGroup(r.Context(), "DeskPatrol Enrollment "+deviceID[:8], "一次性激活设备组")
		if createErr != nil {
			writeError(w, http.StatusBadGateway, fmt.Errorf("创建 MeshCentral 临时设备组失败: %w", createErr))
			return
		}
		if _, updateErr := connection.Exec(r.Context(), `UPDATE devices SET mesh_id=$2 WHERE id=$1`, deviceID, created); updateErr != nil {
			_ = controller.RemoveDeviceGroup(context.Background(), created)
			writeError(w, http.StatusInternalServerError, fmt.Errorf("保存 MeshCentral 临时设备组失败: %w", updateErr))
			return
		}
		meshID = &created
	}
	agentPath = filepath.Join(cfg.StorageDir, "enrollments", deviceID, "MeshAgent.exe")
	prepareContext, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	agentSHA, agentSize, err = controller.DownloadAgent(prepareContext, *meshID, architecture, agentPath)
	cancel()
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("生成 MeshAgent 安装文件失败: %w", err))
		return
	}
	a.respondWithEnrollmentAgent(w, r, connection, cfg, deviceID, agentPath, agentSHA, agentSize)
}

func (a *App) respondWithEnrollmentAgent(w http.ResponseWriter, r *http.Request, connection *pgxpool.Conn, cfg *appconfig.Config, deviceID, agentPath, agentSHA string, agentSize int64) {
	downloadToken, tokenHash, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	expiresAt := time.Now().Add(15 * time.Minute)
	if _, err := connection.Exec(r.Context(), `INSERT INTO enrollment_downloads(device_id,token_hash,agent_path,agent_sha256,size_bytes,expires_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(device_id) DO UPDATE SET token_hash=EXCLUDED.token_hash,agent_path=EXCLUDED.agent_path,agent_sha256=EXCLUDED.agent_sha256,size_bytes=EXCLUDED.size_bytes,expires_at=EXCLUDED.expires_at,created_at=NOW()`, deviceID, tokenHash, agentPath, agentSHA, agentSize, expiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agentDownloadUrl": cfg.PublicURL + "/api/v1/client/agent/" + downloadToken,
		"agentSha256":      agentSHA,
		"agentSize":        agentSize,
		"expiresAt":        expiresAt,
	})
}

func validateEnrollmentFile(path, expectedSHA string, expectedSize int64) (bool, error) {
	if len(expectedSHA) != 64 || expectedSize < 1024 || expectedSize > 256<<20 {
		return false, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return false, nil
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), expectedSHA), nil
}

func (a *App) downloadEnrollmentAgent(w http.ResponseWriter, r *http.Request) {
	_, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	var path, digest string
	err = pool.QueryRow(r.Context(), `SELECT agent_path,agent_sha256 FROM enrollment_downloads WHERE token_hash=$1 AND expires_at>NOW()`, hash(r.PathValue("token"))).Scan(&path, &digest)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("Agent 下载票据不存在或已过期"))
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("打开 Agent 安装文件失败: %w", err))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusInternalServerError, errors.New("Agent 安装文件无效"))
		return
	}
	w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
	w.Header().Set("Content-Disposition", `attachment; filename="MeshAgent.exe"`)
	w.Header().Set("ETag", `"sha256-`+digest+`"`)
	w.Header().Set("X-Content-SHA256", digest)
	http.ServeContent(w, r, "MeshAgent.exe", info.ModTime(), file)
}

func (a *App) ingestClientErrors(w http.ResponseWriter, r *http.Request) {
	_, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	deviceID, err := authenticateDevice(r, pool)
	if err != nil {
		writeDeviceAuthError(w, err)
		return
	}
	var req struct {
		Items []struct {
			EventID       string    `json:"eventId"`
			Category      string    `json:"category"`
			Message       string    `json:"message"`
			Stack         string    `json:"stack"`
			ClientVersion string    `json:"clientVersion"`
			OccurredAt    time.Time `json:"occurredAt"`
		} `json:"items"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Items) < 1 || len(req.Items) > 200 {
		writeError(w, http.StatusBadRequest, errors.New("前端异常批次数量不正确"))
		return
	}
	tx, err := pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, item := range req.Items {
		if item.EventID == "" || item.Category == "" || item.OccurredAt.IsZero() || len(item.Category) > 64 || len(item.Message) > 8192 || len(item.Stack) > 32768 || len(item.ClientVersion) > 64 {
			writeError(w, http.StatusBadRequest, errors.New("前端异常事件格式不正确"))
			return
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO frontend_errors(event_id,device_id,source,category,message,stack,client_version,occurred_at) VALUES($1,$2,'wails',$3,$4,$5,$6,$7) ON CONFLICT(event_id) DO NOTHING`, item.EventID, deviceID, security.Redact(item.Category), security.Redact(item.Message), security.Redact(item.Stack), item.ClientVersion, item.OccurredAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("保存前端异常失败: %w", err))
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"accepted": len(req.Items)})
}

func (a *App) clientStatus(w http.ResponseWriter, r *http.Request) {
	_, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	deviceID, err := authenticateDevice(r, pool)
	if err != nil {
		writeDeviceAuthError(w, err)
		return
	}
	var name, status string
	var screenCount int
	var lastSeen *time.Time
	if err := pool.QueryRow(r.Context(), `SELECT name,status,screen_count,last_seen_at FROM devices WHERE id=$1`, deviceID).Scan(&name, &status, &screenCount, &lastSeen); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if lastSeen == nil || time.Since(*lastSeen) > 90*time.Second {
		status = "offline"
	}
	writeJSON(w, http.StatusOK, map[string]any{"deviceId": deviceID, "deviceName": name, "status": status, "screenCount": screenCount, "lastSeenAt": lastSeen})
}

func authenticateDevice(r *http.Request, pool *pgxpool.Pool) (string, error) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") || len(authorization) < 20 {
		return "", errors.New("设备令牌缺失")
	}
	var deviceID string
	var deletedAt *time.Time
	if err := pool.QueryRow(r.Context(), `SELECT id,deleted_at FROM devices WHERE client_token_hash=$1`, hash(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")))).Scan(&deviceID, &deletedAt); err != nil {
		return "", err
	}
	if deletedAt != nil {
		return "", errDeviceDeleted
	}
	return deviceID, nil
}

func writeDeviceAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, errDeviceDeleted) {
		writeError(w, http.StatusGone, errDeviceDeleted)
		return
	}
	writeError(w, http.StatusUnauthorized, errors.New("设备认证失败"))
}

func (a *App) listDownloads(w http.ResponseWriter, r *http.Request) {
	_, pool, _ := a.snapshot()
	rows, err := pool.Query(r.Context(), `SELECT filename,version,platform,architecture,size_bytes,sha256,status,created_at FROM release_artifacts WHERE status='ready' ORDER BY created_at DESC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var filename, version, platform, architecture, digest, status string
		var size int64
		var created time.Time
		if err := rows.Scan(&filename, &version, &platform, &architecture, &size, &digest, &status, &created); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items = append(items, map[string]any{"filename": filename, "version": version, "platform": platform, "architecture": architecture, "size": size, "sha256": digest, "status": status, "createdAt": created})
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *App) listReleaseJobs(w http.ResponseWriter, r *http.Request) {
	_, pool, _ := a.snapshot()
	rows, err := pool.Query(r.Context(), `SELECT id,version,status,progress,total,error_message,created_at,updated_at FROM release_jobs ORDER BY created_at DESC LIMIT 1`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, version, status, errorMessage string
		var progress, total int64
		var created, updated time.Time
		if err := rows.Scan(&id, &version, &status, &progress, &total, &errorMessage, &created, &updated); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items = append(items, map[string]any{"id": id, "version": version, "status": status, "progress": progress, "total": total, "error": errorMessage, "createdAt": created, "updatedAt": updated})
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *App) syncRelease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	service := a.releaseService()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("Release 同步服务不可用"))
		return
	}
	jobID, err := service.Enqueue(r.Context(), req.Version)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": jobID, "version": req.Version, "status": "queued"})
}

func (a *App) download(w http.ResponseWriter, r *http.Request) {
	filename := filepath.Base(r.PathValue("filename"))
	_, pool, _ := a.snapshot()
	var path, digest string
	err := pool.QueryRow(r.Context(), `SELECT local_path,sha256 FROM release_artifacts WHERE filename=$1 AND status='ready'`, filename).Scan(&path, &digest)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("安装包不存在"))
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("打开安装包失败: %w", err))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusInternalServerError, errors.New("安装包不是有效文件"))
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("ETag", `"sha256-`+digest+`"`)
	w.Header().Set("X-Content-SHA256", digest)
	http.ServeContent(w, r, filename, info.ModTime(), file)
}

func (a *App) createDebugSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID string `json:"deviceId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	user := r.Context().Value(principalKey).(principal)
	_, pool, _ := a.snapshot()
	id, err := uuid()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("生成诊断会话编号失败: %w", err))
		return
	}
	token, tokenHash, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("生成诊断访问口令失败: %w", err))
		return
	}
	expires := time.Now().Add(15 * time.Minute)
	if _, err := pool.Exec(r.Context(), `INSERT INTO debug_sessions(id,device_id,created_by,access_token_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, id, req.DeviceID, user.ID, tokenHash, expires); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("创建诊断会话失败: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "deviceId": req.DeviceID, "token": token, "expiresAt": expires, "status": "active"})
}

func (a *App) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	_, pool, _ := a.snapshot()
	rows, err := pool.Query(r.Context(), `SELECT a.id,a.operation,a.script_sha256,a.duration_ms,a.exit_code,a.output_truncated,a.created_at,s.id,d.id,d.name,u.login_name FROM debug_audits a JOIN debug_sessions s ON s.id=a.session_id JOIN devices d ON d.id=s.device_id JOIN administrators u ON u.id=s.created_by ORDER BY a.created_at DESC LIMIT 500`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, duration int64
		var operation, scriptSHA, sessionID, deviceID, deviceName, administrator string
		var exitCode *int
		var truncated bool
		var created time.Time
		if err := rows.Scan(&id, &operation, &scriptSHA, &duration, &exitCode, &truncated, &created, &sessionID, &deviceID, &deviceName, &administrator); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items = append(items, map[string]any{"id": id, "operation": operation, "scriptSha256": scriptSHA, "durationMs": duration, "exitCode": exitCode, "outputTruncated": truncated, "createdAt": created, "sessionId": sessionID, "deviceId": deviceID, "deviceName": deviceName, "administrator": administrator})
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *App) closeDebugSession(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(principalKey).(principal)
	_, pool, _ := a.snapshot()
	result, err := pool.Exec(r.Context(), `UPDATE debug_sessions SET closed_at=NOW() WHERE id=$1 AND created_by=$2 AND closed_at IS NULL`, r.PathValue("id"), user.ID)
	if err != nil || result.RowsAffected() != 1 {
		writeError(w, http.StatusNotFound, errors.New("诊断会话不存在或已关闭"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"closed": true})
}

func (a *App) runPowerShell(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Script         string `json:"script"`
		TimeoutSeconds int    `json:"timeoutSeconds"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Script) == "" || len(req.Script) > 32*1024 || req.TimeoutSeconds < 0 || req.TimeoutSeconds > 120 {
		writeError(w, http.StatusBadRequest, errors.New("PowerShell 脚本或超时参数不正确"))
		return
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	a.executeDebugCommand(w, r, "powershell", req.Script, timeout)
}

func (a *App) runDiagnosticInspection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cfg, _, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	host, _ := url.Parse(cfg.PublicURL)
	script, operation, timeout, err := diagnosticInspection(req.Kind, host.Hostname(), cfg.AgentPublicPort)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a.executeDebugCommand(w, r, operation, script, timeout)
}

func (a *App) executeDebugCommand(w http.ResponseWriter, r *http.Request, operation, script string, timeout time.Duration) {
	_, pool, _ := a.snapshot()
	var sessionID, nodeID string
	var err error
	if tokenHash, ok := r.Context().Value(debugTokenHashKey).(string); ok {
		err = pool.QueryRow(r.Context(), `SELECT s.id,d.node_id FROM debug_sessions s JOIN devices d ON d.id=s.device_id WHERE s.id=$1 AND s.access_token_hash=$2 AND s.expires_at>NOW() AND s.closed_at IS NULL AND d.node_id IS NOT NULL`, r.PathValue("id"), tokenHash).Scan(&sessionID, &nodeID)
	} else if user, ok := r.Context().Value(principalKey).(principal); ok {
		err = pool.QueryRow(r.Context(), `SELECT s.id,d.node_id FROM debug_sessions s JOIN devices d ON d.id=s.device_id WHERE s.id=$1 AND s.created_by=$2 AND s.expires_at>NOW() AND s.closed_at IS NULL AND d.node_id IS NOT NULL`, r.PathValue("id"), user.ID).Scan(&sessionID, &nodeID)
	} else {
		writeError(w, http.StatusUnauthorized, errors.New("缺少诊断访问凭据"))
		return
	}
	if err != nil {
		writeError(w, http.StatusGone, errors.New("诊断会话不存在、口令不正确或已过期"))
		return
	}
	digest := sha256.Sum256([]byte(script))
	var auditID int64
	if err := pool.QueryRow(r.Context(), `INSERT INTO debug_audits(session_id,operation,script_sha256) VALUES($1,$2,$3) RETURNING id`, sessionID, operation, hex.EncodeToString(digest[:])).Scan(&auditID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	started := time.Now()
	commandContext, cancel := context.WithTimeout(r.Context(), timeout+7*time.Second)
	defer cancel()
	result, err := a.meshController().RunPowerShell(commandContext, nodeID, script, timeout)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		exitCode := -1
		status := http.StatusBadGateway
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
			exitCode = -2
		}
		_, _ = pool.Exec(context.Background(), `UPDATE debug_audits SET duration_ms=$2,exit_code=$3 WHERE id=$1`, auditID, duration, exitCode)
		writeError(w, status, err)
		return
	}
	_, _ = pool.Exec(context.Background(), `UPDATE debug_audits SET duration_ms=$2,exit_code=$3,output_truncated=$4 WHERE id=$1`, auditID, duration, result.ExitCode, result.OutputTruncated)
	writeJSON(w, http.StatusOK, result)
}

func diagnosticInspection(kind, host string, port int) (string, string, time.Duration, error) {
	host = strings.ReplaceAll(host, "'", "''")
	scripts := map[string]string{
		"status":    `$value=Get-CimInstance Win32_OperatingSystem|Select-Object Caption,Version,LastBootUpTime,LocalDateTime;$value|ConvertTo-Json -Compress`,
		"processes": `Get-Process|Sort-Object CPU -Descending|Select-Object -First 50 Name,Id,CPU,WorkingSet,StartTime|ConvertTo-Json -Compress`,
		"services":  `Get-Service|Sort-Object Status,DisplayName|Select-Object Name,DisplayName,Status,StartType|ConvertTo-Json -Compress`,
		"displays":  `Get-CimInstance -Namespace root\wmi -Class WmiMonitorID|Select-Object InstanceName,Active,ManufacturerName,ProductCodeID,SerialNumberID|ConvertTo-Json -Compress`,
		"events":    `Get-WinEvent -FilterHashtable @{LogName='System','Application';Level=1,2;StartTime=(Get-Date).AddHours(-24)} -MaxEvents 50|Select-Object TimeCreated,LogName,ProviderName,Id,LevelDisplayName,Message|ConvertTo-Json -Compress`,
		"meshagent": `Get-CimInstance Win32_Service -Filter "Name='Mesh Agent'"|Select-Object Name,State,StartMode,ProcessId,PathName|ConvertTo-Json -Compress`,
		"network":   fmt.Sprintf(`Test-NetConnection -ComputerName '%s' -Port %d -InformationLevel Detailed|Select-Object ComputerName,RemoteAddress,RemotePort,NameResolutionResults,TcpTestSucceeded|ConvertTo-Json -Compress`, host, port),
		"package":   fmt.Sprintf(`$value=[ordered]@{capturedAt=(Get-Date).ToString('o');operatingSystem=(Get-CimInstance Win32_OperatingSystem|Select-Object Caption,Version,LastBootUpTime);computer=(Get-CimInstance Win32_ComputerSystem|Select-Object Manufacturer,Model,TotalPhysicalMemory);displays=@(Get-CimInstance -Namespace root\wmi -Class WmiMonitorID|Select-Object InstanceName,Active);meshAgent=(Get-CimInstance Win32_Service -Filter "Name='Mesh Agent'"|Select-Object Name,State,StartMode,ProcessId);network=(Test-NetConnection -ComputerName '%s' -Port %d -InformationLevel Detailed|Select-Object ComputerName,RemoteAddress,RemotePort,TcpTestSucceeded);recentEvents=@(Get-WinEvent -FilterHashtable @{LogName='System','Application';Level=1,2;StartTime=(Get-Date).AddHours(-24)} -MaxEvents 50|Select-Object TimeCreated,LogName,ProviderName,Id,LevelDisplayName,Message)};$value|ConvertTo-Json -Depth 5 -Compress`, host, port),
	}
	script, ok := scripts[kind]
	if !ok {
		return "", "", 0, errors.New("诊断检查类型不受支持")
	}
	timeout := 30 * time.Second
	if kind == "package" || kind == "events" {
		timeout = 60 * time.Second
	}
	return script, "inspect_" + kind, timeout, nil
}

func (a *App) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, errors.New("接口不存在"))
		return
	}
	if _, err := os.Stat(filepath.Join(a.assetsDir, "index.html")); err != nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("管理端静态资源不可用: %w", err))
		return
	}
	relative := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), string(filepath.Separator))
	path := filepath.Join(a.assetsDir, relative)
	if path != a.assetsDir && !strings.HasPrefix(path, a.assetsDir+string(filepath.Separator)) {
		writeError(w, http.StatusBadRequest, errors.New("静态资源路径不正确"))
		return
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		http.ServeFile(w, r, path)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.assetsDir, "index.html"))
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		a.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "durationMs", time.Since(started).Milliseconds())
	})
}

func newToken() (string, string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", "", fmt.Errorf("生成会话令牌失败: %w", err)
	}
	token := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value)
	return token, hash(token), nil
}

func hash(value string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(digest[:])
}

func bearerToken(authorization string) (string, bool) {
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func activationCode() (string, error) {
	value := make([]byte, 15)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成激活码失败: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value)
	return strings.Join([]string{encoded[0:6], encoded[6:12], encoded[12:18], encoded[18:24]}, "-"), nil
}

func uuid() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
