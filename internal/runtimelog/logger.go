package runtimelog

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"deskpatrol/internal/security"
)

const retentionDays = 14

type Logger struct {
	dir string
	mu  sync.Mutex
}

type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

type Query struct {
	Limit    int    `json:"limit"`
	Level    string `json:"level"`
	Contains string `json:"contains"`
}

func New(directory string) (*Logger, error) {
	directory = filepath.Clean(strings.TrimSpace(directory))
	if directory == "." || directory == "" {
		return nil, errors.New("Runtime 日志目录不正确")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	logger := &Logger{dir: directory}
	logger.cleanup()
	return logger, nil
}

func (l *Logger) Info(message string, values ...any)  { l.write("INFO", message, values...) }
func (l *Logger) Error(message string, values ...any) { l.write("ERROR", message, values...) }

func (l *Logger) write(level, message string, values ...any) {
	if l == nil {
		return
	}
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().Format(time.RFC3339), level, security.Redact(fmt.Sprintf(message, values...)))
	l.mu.Lock()
	defer l.mu.Unlock()
	file, err := os.OpenFile(filepath.Join(l.dir, "runtime-"+time.Now().Format("2006-01-02")+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = file.WriteString(line)
		_ = file.Close()
	}
}

func (l *Logger) Read(query Query) ([]Entry, error) {
	if l == nil {
		return nil, errors.New("Runtime 日志不可用")
	}
	if query.Limit <= 0 {
		query.Limit = 100
	}
	if query.Limit > 500 {
		query.Limit = 500
	}
	query.Level = strings.ToUpper(strings.TrimSpace(query.Level))
	if query.Level != "" && query.Level != "INFO" && query.Level != "ERROR" {
		return nil, errors.New("日志级别只支持 INFO 或 ERROR")
	}
	files, err := filepath.Glob(filepath.Join(l.dir, "runtime-*.log"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	entries := make([]Entry, 0)
	for _, path := range files {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			entry, ok := parse(scanner.Text())
			if ok && (query.Level == "" || entry.Level == query.Level) && (query.Contains == "" || strings.Contains(strings.ToLower(entry.Message), strings.ToLower(query.Contains))) {
				entries = append(entries, entry)
			}
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	if len(entries) > query.Limit {
		entries = entries[len(entries)-query.Limit:]
	}
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	return entries, nil
}

func parse(line string) (Entry, bool) {
	firstSpace := strings.IndexByte(line, ' ')
	if firstSpace < 1 {
		return Entry{}, false
	}
	timestamp, err := time.Parse(time.RFC3339, line[:firstSpace])
	if err != nil {
		return Entry{}, false
	}
	rest := line[firstSpace+1:]
	closing := strings.Index(rest, "] ")
	if !strings.HasPrefix(rest, "[") || closing < 2 {
		return Entry{}, false
	}
	return Entry{Timestamp: timestamp, Level: rest[1:closing], Message: rest[closing+2:]}, true
}

func (l *Logger) cleanup() {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "runtime-") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		dateText := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "runtime-"), ".log")
		date, err := time.Parse("2006-01-02", dateText)
		if err == nil && date.Before(cutoff) {
			_ = os.Remove(filepath.Join(l.dir, entry.Name()))
		}
	}
}
