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

func New(config appconfig.Config, pool *pgxpool.Pool) *Service {
	return &Service{config: config, pool: pool, client: &http.Client{Timeout: 20 * time.Minute}, active: make(map[string]struct{})}
}

func (s *Service) Enqueue(ctx context.Context, version string) (string, error) {
	version = strings.TrimSpace(version)
	if !versionPattern.MatchString(version) {
		return "", errors.New("Release 版本必须使用 x.y.z 格式")
	}
	s.mu.Lock()
	if _, exists := s.active[version]; exists {
		s.mu.Unlock()
		return "", errors.New("该 Release 已在后台同步")
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
	if err == nil {
		err = s.downloadArtifacts(ctx, jobID, version, manifest)
	}
	if err != nil {
		_, _ = s.pool.Exec(context.Background(), `UPDATE release_jobs SET status='failed',error_message=$2,updated_at=NOW() WHERE id=$1`, jobID, safeError(err))
		return
	}
	_, _ = s.pool.Exec(context.Background(), `UPDATE release_jobs SET status='ready',progress=total,error_message='',updated_at=NOW() WHERE id=$1`, jobID)
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

func (s *Service) downloadArtifacts(ctx context.Context, jobID, version string, manifest Manifest) error {
	selected := make([]Artifact, 0, 2)
	var total int64
	for _, artifact := range manifest.Artifacts {
		if artifact.Platform != "windows" || (artifact.Architecture != "amd64" && artifact.Architecture != "arm64") {
			continue
		}
		if err := validateArtifact(artifact); err != nil {
			return err
		}
		selected = append(selected, artifact)
		total += artifact.Size
	}
	if len(selected) != 2 {
		return errors.New("Release manifest 必须且只能包含 Windows amd64、arm64 安装包")
	}
	if selected[0].Architecture == selected[1].Architecture {
		return errors.New("Release manifest 缺少一个 Windows 架构安装包")
	}
	_, _ = s.pool.Exec(ctx, `UPDATE release_jobs SET total=$2,updated_at=NOW() WHERE id=$1`, jobID, total)
	var progress int64
	for _, artifact := range selected {
		written, err := s.downloadOne(ctx, version, artifact, func(value int64) {
			_, _ = s.pool.Exec(context.Background(), `UPDATE release_jobs SET progress=$2,updated_at=NOW() WHERE id=$1`, jobID, progress+value)
		})
		if err != nil {
			return err
		}
		progress += written
	}
	return nil
}

func (s *Service) downloadOne(ctx context.Context, version string, artifact Artifact, progress func(int64)) (int64, error) {
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s", s.config.GitHubRepository, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+artifact.Filename, nil)
	if err != nil {
		return 0, err
	}
	response, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("下载安装包 %s 失败: %w", filepath.Base(artifact.Filename), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("下载安装包 %s 失败: HTTP %d", filepath.Base(artifact.Filename), response.StatusCode)
	}
	if response.ContentLength > 0 && response.ContentLength != artifact.Size {
		return 0, fmt.Errorf("安装包 %s 长度与 manifest 不一致", filepath.Base(artifact.Filename))
	}
	directory := filepath.Join(s.config.StorageDir, "releases", version, "downloads")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return 0, fmt.Errorf("创建 Release 缓存目录失败: %w", err)
	}
	filename := filepath.Base(artifact.Filename)
	temporary := filepath.Join(directory, "."+filename+".partial")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("创建安装包临时文件失败: %w", err)
	}
	hasher := sha256.New()
	written, copyErr := copyWithProgress(io.MultiWriter(file, hasher), io.LimitReader(response.Body, maxArtifactBytes+1), progress)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written != artifact.Size || written > maxArtifactBytes {
		_ = os.Remove(temporary)
		return 0, fmt.Errorf("写入安装包 %s 失败或长度不匹配", filename)
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != artifact.SHA256 {
		_ = os.Remove(temporary)
		return 0, fmt.Errorf("安装包 %s SHA-256 校验失败", filename)
	}
	finalPath := filepath.Join(directory, filename)
	if err := os.Rename(temporary, finalPath); err != nil {
		_ = os.Remove(temporary)
		return 0, fmt.Errorf("发布安装包 %s 失败: %w", filename, err)
	}
	id, _ := newUUID()
	_, err = s.pool.Exec(ctx, `INSERT INTO release_artifacts(id,version,platform,architecture,filename,size_bytes,sha256,local_path,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'ready') ON CONFLICT(filename) DO UPDATE SET version=EXCLUDED.version,size_bytes=EXCLUDED.size_bytes,sha256=EXCLUDED.sha256,local_path=EXCLUDED.local_path,status='ready',created_at=NOW()`, id, version, artifact.Platform, artifact.Architecture, filename, artifact.Size, artifact.SHA256, finalPath)
	if err != nil {
		return 0, fmt.Errorf("登记安装包 %s 失败: %w", filename, err)
	}
	return written, nil
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
