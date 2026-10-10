//go:build windows

package cne

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// **探明 cne 的 manifest.db 结构** —— 先看清有什么表、什么列，
// 再决定怎么读。方案 §2.1 要求"同时读运行状态和每个数据集是否真写了行"，
// 而这件事只能从这个库来。
//
// 只读；路径固定为本机实测位置。
func TestInspectManifestSchema(t *testing.T) {
	const path = `D:\workspace\stockTool\CNEquity\data\cnequity\meta\manifest.db`
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Skipf("打不开 manifest.db：%v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Skipf("连不上 manifest.db：%v", err)
	}

	rows, err := db.Query(`SELECT name, type FROM sqlite_master
		WHERE type IN ('table','view') ORDER BY name`)
	if err != nil {
		t.Fatalf("列表失败：%v", err)
	}
	var tables []string
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err == nil {
			tables = append(tables, kind+":"+name)
		}
	}
	rows.Close()
	t.Logf("共 %d 个对象：", len(tables))
	for _, name := range tables {
		t.Logf("  %s", name)
	}

	// 对每张表打列名与前 2 行（截断），看清字段
	for _, entry := range tables {
		name := entry
		if i := len("table:"); len(entry) > i && entry[:i] == "table:" {
			name = entry[i:]
		}
		cols, err := db.Query("SELECT COUNT(*) FROM pragma_table_info(?)", name)
		if err != nil {
			continue
		}
		var n int
		if cols.Next() {
			_ = cols.Scan(&n)
		}
		cols.Close()
		t.Logf("表 %s：%d 列", name, n)

		names, err := db.Query("SELECT name FROM pragma_table_info(?)", name)
		if err != nil {
			continue
		}
		var colNames []string
		for names.Next() {
			var cn string
			if err := names.Scan(&cn); err == nil {
				colNames = append(colNames, cn)
			}
		}
		names.Close()
		if len(colNames) > 0 {
			t.Logf("    列：%v", colNames)
		}

		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + name).Scan(&count); err == nil {
			t.Logf("    行数：%d", count)
		}
	}
}
