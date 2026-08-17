package releasesync

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"deskpatrol/internal/appconfig"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxArtifactBytes = 512 << 20

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

type Service struct {
	config appconfig.Config
	pool   *pgxpool.Pool
	client *http.Client
	mu     sync.Mutex
	active map[string]struct{}
}

type Manifest struct {
	SchemaVersion      int        `json:"schemaVersion"`
	Product            string     `json:"product"`
	Version            string     `json:"version"`
	Repository         string     `json:"repository"`
	MeshCentralVersion string     `json:"meshCentralVersion"`
	GeneratedAt        time.Time  `json:"generatedAt"`
	Artifacts          []Artifact `json:"artifacts"`
}

type Artifact struct {
	Filename     string `json:"filename"`
	Platform     string `json:"platform"`
	Architecture string `json:"architecture"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
}

type downloadedArtifact struct {
	Artifact
	LocalPath string
}

type committedReleaseError struct {
	err error
}

func (e *committedReleaseError) Error() string { return e.err.Error() }
func (e *committedReleaseError) Unwrap() error { return e.err }

func New(config appconfig.Config, pool *pgxpool.Pool) *Service {
	return &Service{config: config, pool: pool, client: &http.Client{Timeout: 20 * time.Minute}, active: make(map[string]struct{})}
}

func (s *Service) Enqueue(ctx context.Context, version string) (string, error) {
	version = strings.TrimSpace(version)
	if !versionPattern.MatchString(version) {
		return "", errors.New("Release 版本必须使用 x.y.z 格式")
	}
	current, err := s.currentVersion(ctx)
	if err != nil {
		return "", err
	}
	if current != "" && compareVersions(version, current) < 0 {
		return "", fmt.Errorf("不能同步低于当前版本 %s 的 Release", current)
	}
	s.mu.Lock()
	if len(s.active) > 0 {
		s.mu.Unlock()
		return "", errors.New("已有 Release 正在后台同步")
	}
	s.active[version] = struct{}{}
	s.mu.Unlock()
	jobID, err := newUUID()
	if err != nil {
		s.finish(version)
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO release_jobs(id,version,status) VALUES($1,$2,'queued')`, jobID, version); err != nil {
		s.finish(version)
		return "", fmt.Errorf("创建 Release 同步任务失败: %w", err)
	}
	go s.run(jobID, version)
	return jobID, nil
}

func (s *Service) run(jobID, version string) {
	defer s.finish(version)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	_, _ = s.pool.Exec(ctx, `UPDATE release_jobs SET status='downloading',updated_at=NOW() WHERE id=$1`, jobID)
	manifest, err := s.fetchManifest(ctx, version)
	var artifacts []downloadedArtifact
	if err == nil {
		artifacts, err = s.downloadArtifacts(ctx, jobID, version, manifest)
	}
	if err == nil {
		err = s.promote(ctx, jobID, version, artifacts)
	}
	if err != nil {
		_ = os.RemoveAll(filepath.Dir(s.stagingDirectory(jobID)))
		var committedErr *committedReleaseError
		if errors.As(err, &committedErr) {
			_, _ = s.pool.Exec(context.Background(), `UPDATE release_jobs SET error_message=$2,updated_at=NOW() WHERE id=$1`, jobID, safeError(err))
			return
		}
		s.recordFailure(jobID, version, err)
		return
	}
}

func (s *Service) recordFailure(jobID, version string, failure error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	message := safeError(fmt.Errorf("同步 v%s 失败: %w", version, failure))
	current, err := s.currentVersion(ctx)
	if err != nil || current == "" {
		_, _ = s.pool.Exec(ctx, `DELETE FROM release_jobs WHERE id<>$1`, jobID)
		_, _ = s.pool.Exec(ctx, `UPDATE release_jobs SET status='failed',error_message=$2,updated_at=NOW() WHERE id=$1`, jobID, message)
		return
	}
	_, _ = s.pool.Exec(ctx, `
WITH keep AS (
  SELECT id FROM release_jobs WHERE version=$1 AND status='ready' ORDER BY updated_at DESC,created_at DESC LIMIT 1
), pruned AS (
  DELETE FROM release_jobs WHERE id<>COALESCE((SELECT id FROM keep),$2)
)
UPDATE release_jobs SET status=CASE WHEN id=$2 THEN 'failed' ELSE status END,error_message=$3,updated_at=NOW()
WHERE id=COALESCE((SELECT id FROM keep),$2)`, current, jobID, message)
}

func (s *Service) RetainLatest(ctx context.Context) error {
	current, err := s.currentVersion(ctx)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if current == "" {
		if _, err := tx.Exec(ctx, `DELETE FROM release_artifacts`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM release_jobs`); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `DELETE FROM release_artifacts WHERE version<>$1`, current); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM release_jobs WHERE id NOT IN (SELECT id FROM release_jobs WHERE version=$1 AND status='ready' ORDER BY updated_at DESC,created_at DESC LIMIT 1)`, current); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.cleanupOlderReleaseDirectories(filepath.Join(s.config.StorageDir, "releases"), current)
}

func (s *Service) currentVersion(ctx context.Context) (string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT version FROM release_artifacts WHERE status='ready'`)
	if err != nil {
		return "", fmt.Errorf("读取当前 Release 版本失败: %w", err)
	}
	defer rows.Close()
	current := ""
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return "", err
		}
		if current == "" || compareVersions(version, current) > 0 {
			current = version
		}
	}
	return current, rows.Err()
}

func (s *Service) fetchManifest(ctx context.Context, version string) (Manifest, error) {
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s", s.config.GitHubRepository, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/manifest.json", nil)
	if err != nil {
		return Manifest{}, err
	}
	response, err := s.client.Do(req)
	if err != nil {
		return Manifest{}, fmt.Errorf("下载 Release manifest 失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("下载 Release manifest 失败: HTTP %d", response.StatusCode)
	}
	var manifest Manifest
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("解析 Release manifest 失败: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Product != "DeskPatrol" || manifest.Version != version || manifest.Repository != s.config.GitHubRepository || manifest.MeshCentralVersion != "1.2.5" {
		return Manifest{}, errors.New("Release manifest 元数据不匹配")
	}
	return manifest, nil
}

func (s *Service) downloadArtifacts(ctx context.Context, jobID, version string, manifest Manifest) ([]downloadedArtifact, error) {
	selected := make([]Artifact, 0, 2)
	var total int64
	for _, artifact := range manifest.Artifacts {
		if artifact.Platform != "windows" || (artifact.Architecture != "amd64" && artifact.Architecture != "arm64") {
			continue
		}
		if err := validateArtifact(artifact); err != nil {
			return nil, err
		}
		selected = append(selected, artifact)
		total += artifact.Size
	}
	if len(selected) != 2 {
		return nil, errors.New("Release manifest 必须且只能包含 Windows amd64、arm64 安装包")
	}
	if selected[0].Architecture == selected[1].Architecture {
		return nil, errors.New("Release manifest 缺少一个 Windows 架构安装包")
	}
	_, _ = s.pool.Exec(ctx, `UPDATE release_jobs SET total=$2,updated_at=NOW() WHERE id=$1`, jobID, total)
	stagingDirectory := s.stagingDirectory(jobID)
	if err := os.MkdirAll(stagingDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("创建 Release 暂存目录失败: %w", err)
	}
	artifacts := make([]downloadedArtifact, 0, len(selected))
	var progress int64
	for _, artifact := range selected {
		cached, written, err := s.downloadOne(ctx, version, stagingDirectory, artifact, func(value int64) {
			_, _ = s.pool.Exec(context.Background(), `UPDATE release_jobs SET progress=$2,updated_at=NOW() WHERE id=$1`, jobID, progress+value)
		})
		if err != nil {
			return nil, err
		}
		progress += written
		artifacts = append(artifacts, cached)
	}
	return artifacts, nil
}

func (s *Service) downloadOne(ctx context.Context, version, directory string, artifact Artifact, progress func(int64)) (downloadedArtifact, int64, error) {
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s", s.config.GitHubRepository, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+artifact.Filename, nil)
	if err != nil {
		return downloadedArtifact{}, 0, err
	}
	response, err := s.client.Do(req)
	if err != nil {
		return downloadedArtifact{}, 0, fmt.Errorf("下载安装包 %s 失败: %w", filepath.Base(artifact.Filename), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return downloadedArtifact{}, 0, fmt.Errorf("下载安装包 %s 失败: HTTP %d", filepath.Base(artifact.Filename), response.StatusCode)
	}
	if response.ContentLength > 0 && response.ContentLength != artifact.Size {
		return downloadedArtifact{}, 0, fmt.Errorf("安装包 %s 长度与 manifest 不一致", filepath.Base(artifact.Filename))
	}
	filename := filepath.Base(artifact.Filename)
	temporary := filepath.Join(directory, "."+filename+".partial")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return downloadedArtifact{}, 0, fmt.Errorf("创建安装包临时文件失败: %w", err)
	}
	hasher := sha256.New()
	written, copyErr := copyWithProgress(io.MultiWriter(file, hasher), io.LimitReader(response.Body, maxArtifactBytes+1), progress)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != artifact.Size || written > maxArtifactBytes {
		_ = os.Remove(temporary)
		return downloadedArtifact{}, 0, fmt.Errorf("写入安装包 %s 失败或长度不匹配", filename)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != artifact.SHA256 {
		_ = os.Remove(temporary)
		return downloadedArtifact{}, 0, fmt.Errorf("安装包 %s SHA-256 校验失败", filename)
	}
	finalPath := filepath.Join(directory, filename)
	if err := os.Rename(temporary, finalPath); err != nil {
		_ = os.Remove(temporary)
		return downloadedArtifact{}, 0, fmt.Errorf("发布安装包 %s 失败: %w", filename, err)
	}
	return downloadedArtifact{Artifact: artifact, LocalPath: finalPath}, written, nil
}

func (s *Service) stagingDirectory(jobID string) string {
	return filepath.Join(s.config.StorageDir, "releases", ".staging-"+jobID, "downloads")
}

func (s *Service) promote(ctx context.Context, jobID, version string, artifacts []downloadedArtifact) (err error) {
	if len(artifacts) != 2 {
		return errors.New("Release 暂存安装包不完整")
	}
	releasesDirectory := filepath.Join(s.config.StorageDir, "releases")
	stagingRoot := filepath.Dir(s.stagingDirectory(jobID))
	finalRoot := filepath.Join(releasesDirectory, version)
	backupRoot := filepath.Join(releasesDirectory, ".backup-"+jobID)
	if _, statErr := os.Stat(finalRoot); statErr == nil {
		if err := os.Rename(finalRoot, backupRoot); err != nil {
			return fmt.Errorf("备份当前 Release 失败: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.Rename(stagingRoot, finalRoot); err != nil {
		if _, restoreErr := os.Stat(backupRoot); restoreErr == nil {
			_ = os.Rename(backupRoot, finalRoot)
		}
		return fmt.Errorf("切换 Release 缓存失败: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if restoreErr := restoreReleaseCache(finalRoot, stagingRoot, backupRoot); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("恢复原 Release 缓存失败: %w", restoreErr))
		}
	}()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("创建 Release 切换事务失败: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM release_artifacts`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM release_jobs WHERE id<>$1`, jobID); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		id, err := newUUID()
		if err != nil {
			return err
		}
		path := filepath.Join(finalRoot, "downloads", filepath.Base(artifact.Filename))
		if _, err := tx.Exec(ctx, `INSERT INTO release_artifacts(id,version,platform,architecture,filename,size_bytes,sha256,local_path,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'ready')`, id, version, artifact.Platform, artifact.Architecture, artifact.Filename, artifact.Size, artifact.SHA256, path); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE release_jobs SET status='ready',progress=total,error_message='',updated_at=NOW() WHERE id=$1`, jobID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	cleanupErr := os.RemoveAll(backupRoot)
	cleanupErr = errors.Join(cleanupErr, s.cleanupOlderReleaseDirectories(releasesDirectory, version))
	if cleanupErr != nil {
		return &committedReleaseError{err: fmt.Errorf("清理旧 Release 缓存失败: %w", cleanupErr)}
	}
	return nil
}

func restoreReleaseCache(finalRoot, stagingRoot, backupRoot string) error {
	var restoreErr error
	if _, err := os.Stat(finalRoot); err == nil {
		if err := os.Rename(finalRoot, stagingRoot); err != nil {
			restoreErr = errors.Join(restoreErr, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		restoreErr = errors.Join(restoreErr, err)
	}
	if _, err := os.Stat(backupRoot); err == nil {
		if err := os.Rename(backupRoot, finalRoot); err != nil {
			restoreErr = errors.Join(restoreErr, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		restoreErr = errors.Join(restoreErr, err)
	}
	return restoreErr
}

func (s *Service) cleanupOlderReleaseDirectories(releasesDirectory, current string) error {
	entries, err := os.ReadDir(releasesDirectory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == current {
			continue
		}
		if !versionPattern.MatchString(entry.Name()) && !strings.HasPrefix(entry.Name(), ".staging-") && !strings.HasPrefix(entry.Name(), ".backup-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(releasesDirectory, entry.Name())); err != nil {
			return fmt.Errorf("清理旧 Release %s 失败: %w", entry.Name(), err)
		}
	}
	return nil
}

func compareVersions(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	if len(leftParts) != 3 || len(rightParts) != 3 {
		return strings.Compare(left, right)
	}
	for index := 0; index < 3; index++ {
		leftValue, _ := strconv.Atoi(leftParts[index])
		rightValue, _ := strconv.Atoi(rightParts[index])
		if leftValue < rightValue {
			return -1
		}
		if leftValue > rightValue {
			return 1
		}
	}
	return 0
}

func validateArtifact(artifact Artifact) error {
	if filepath.Base(artifact.Filename) != artifact.Filename || !strings.HasSuffix(strings.ToLower(artifact.Filename), ".exe") {
		return errors.New("Release manifest 包含不安全的 Windows 文件名")
	}
	if artifact.Size <= 0 || artifact.Size > maxArtifactBytes || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(artifact.SHA256) {
		return fmt.Errorf("Release 安装包 %s 元数据不正确", artifact.Filename)
	}
	return nil
}

func copyWithProgress(dst io.Writer, src io.Reader, update func(int64)) (int64, error) {
	buffer := make([]byte, 256<<10)
	var total, reported int64
	for {
		count, readErr := src.Read(buffer)
		if count > 0 {
			written, writeErr := dst.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil || written != count {
				return total, errors.New("写入下载内容失败")
			}
			if total-reported >= 1<<20 {
				update(total - reported)
				reported = total
			}
		}
		if errors.Is(readErr, io.EOF) {
			update(total - reported)
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func safeError(err error) string {
	value := err.Error()
	value = regexp.MustCompile(`https://[^\s]+`).ReplaceAllString(value, "[REMOTE_URL]")
	if len(value) > 2000 {
		value = value[:2000]
	}
	return value
}

func (s *Service) finish(version string) { s.mu.Lock(); delete(s.active, version); s.mu.Unlock() }

func newUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
