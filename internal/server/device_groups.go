package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ungroupedKey 是监控墙折叠状态里“未分组”分区的保留键，真实分组 ID 一律为 UUID。
const ungroupedKey = "ungrouped"

const maxCollapsedGroups = 200

var groupIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func normalizeGroupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("分组名称不能为空")
	}
	if utf8.RuneCountInString(name) > 50 {
		return "", errors.New("分组名称不能超过 50 个字")
	}
	return name, nil
}

func normalizeCollapsedGroups(values []string) ([]string, error) {
	if len(values) > maxCollapsedGroups {
		return nil, fmt.Errorf("折叠分组数量不能超过 %d", maxCollapsedGroups)
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != ungroupedKey && !groupIDPattern.MatchString(value) {
			return nil, errors.New("折叠分组 ID 不正确")
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (a *App) listDeviceGroups(w http.ResponseWriter, r *http.Request) {
	_, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	rows, err := pool.Query(r.Context(), `SELECT id,name,created_at FROM device_groups ORDER BY created_at, name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("读取设备分组失败: %w", err))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name string
		var created time.Time
		if err := rows.Scan(&id, &name, &created); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "createdAt": created})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("读取设备分组失败: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *App) createDeviceGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := normalizeGroupName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	_, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	id, err := uuid()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var created time.Time
	err = pool.QueryRow(r.Context(), `INSERT INTO device_groups(id,name) VALUES($1,$2) RETURNING created_at`, id, name).Scan(&created)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, errors.New("分组名称已存在"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("创建设备分组失败: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": name, "createdAt": created})
}

func (a *App) renameDeviceGroup(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := normalizeGroupName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !groupIDPattern.MatchString(groupID) {
		writeError(w, http.StatusNotFound, errors.New("分组不存在"))
		return
	}
	_, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	var created time.Time
	err = pool.QueryRow(r.Context(), `UPDATE device_groups SET name=$2 WHERE id=$1 RETURNING created_at`, groupID, name).Scan(&created)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, errors.New("分组不存在"))
		return
	}
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, errors.New("分组名称已存在"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("重命名设备分组失败: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": groupID, "name": name, "createdAt": created})
}

func (a *App) deleteDeviceGroup(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	if !groupIDPattern.MatchString(groupID) {
		writeError(w, http.StatusNotFound, errors.New("分组不存在"))
		return
	}
	_, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	tx, err := pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("开启分组删除事务失败: %w", err))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// 设备的 group_id 由外键 ON DELETE SET NULL 自动归入未分组。
	result, err := tx.Exec(r.Context(), `DELETE FROM device_groups WHERE id=$1`, groupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("删除设备分组失败: %w", err))
		return
	}
	if result.RowsAffected() != 1 {
		writeError(w, http.StatusNotFound, errors.New("分组不存在"))
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE wall_layouts SET collapsed_groups=(SELECT COALESCE(jsonb_agg(element),'[]'::jsonb) FROM jsonb_array_elements(collapsed_groups) AS element WHERE element<>to_jsonb($1::text)) WHERE collapsed_groups @> jsonb_build_array($1::text)`, groupID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("清理监控墙折叠分组失败: %w", err))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("提交分组删除事务失败: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (a *App) putDeviceGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GroupID *string `json:"groupId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.GroupID != nil && !groupIDPattern.MatchString(*req.GroupID) {
		writeError(w, http.StatusNotFound, errors.New("分组不存在"))
		return
	}
	_, pool, err := a.snapshot()
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err)
		return
	}
	tx, err := pool.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("开启设备分组事务失败: %w", err))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if req.GroupID != nil {
		// 锁定目标分组，避免与并发删除交错后把设备挂到已删除的分组上。
		var exists string
		err := tx.QueryRow(r.Context(), `SELECT id FROM device_groups WHERE id=$1 FOR SHARE`, *req.GroupID).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, errors.New("分组不存在"))
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("读取设备分组失败: %w", err))
			return
		}
	}
	result, err := tx.Exec(r.Context(), `UPDATE devices SET group_id=$2 WHERE id=$1 AND deleted_at IS NULL`, r.PathValue("id"), req.GroupID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("保存设备分组失败: %w", err))
		return
	}
	if result.RowsAffected() != 1 {
		writeError(w, http.StatusNotFound, errors.New("设备不存在"))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("提交设备分组事务失败: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groupId": req.GroupID})
}
