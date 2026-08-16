package runtimelog

import (
	"strings"
	"testing"
)

func TestLoggerRedactsAndFilters(t *testing.T) {
	logger, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logger.Info(`connected token="secret"`)
	logger.Error("connection failed")
	entries, err := logger.Read(Query{Level: "ERROR", Limit: 10})
	if err != nil || len(entries) != 1 || entries[0].Message != "connection failed" {
		t.Fatalf("日志筛选结果不正确: %#v %v", entries, err)
	}
	all, err := logger.Read(Query{Limit: 10})
	if err != nil || len(all) != 2 {
		t.Fatalf("日志读取失败: %#v %v", all, err)
	}
	if strings.Contains(all[1].Message, "secret") {
		t.Fatalf("日志未脱敏: %s", all[1].Message)
	}
}
