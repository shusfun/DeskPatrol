package server

import (
	"strings"
	"testing"
)

func TestNormalizeGroupName(t *testing.T) {
	if name, err := normalizeGroupName("  销售组 "); err != nil || name != "销售组" {
		t.Fatalf("分组名称应去除首尾空白: name=%q err=%v", name, err)
	}
	if _, err := normalizeGroupName("   "); err == nil {
		t.Fatal("空白分组名称必须被拒绝")
	}
	if _, err := normalizeGroupName(strings.Repeat("组", 50)); err != nil {
		t.Fatalf("50 个汉字的分组名称应允许: %v", err)
	}
	if _, err := normalizeGroupName(strings.Repeat("组", 51)); err == nil {
		t.Fatal("超过 50 个字的分组名称必须被拒绝")
	}
}

func TestNormalizeCollapsedGroups(t *testing.T) {
	id := "0f8e7a52-3c41-4d6b-9a2e-1b5c7d9e0a11"
	values, err := normalizeCollapsedGroups([]string{id, ungroupedKey, id})
	if err != nil || len(values) != 2 || values[0] != id || values[1] != ungroupedKey {
		t.Fatalf("折叠分组应保留顺序并去重: values=%v err=%v", values, err)
	}
	if values, err := normalizeCollapsedGroups(nil); err != nil || values == nil || len(values) != 0 {
		t.Fatalf("缺省折叠分组应归一为空数组: values=%#v err=%v", values, err)
	}
	for _, invalid := range []string{"", "sales", strings.ToUpper(id), id + "0"} {
		if _, err := normalizeCollapsedGroups([]string{invalid}); err == nil {
			t.Fatalf("非法折叠分组 ID 必须被拒绝: %q", invalid)
		}
	}
	tooMany := make([]string, maxCollapsedGroups+1)
	for index := range tooMany {
		tooMany[index] = ungroupedKey
	}
	if _, err := normalizeCollapsedGroups(tooMany); err == nil {
		t.Fatal("超出上限的折叠分组数量必须被拒绝")
	}
}
