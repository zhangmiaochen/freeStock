package portfolio

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// copyTableByRowID 把 `<srcAlias>.<table>` 的行按 rowid 分批复制到主库同名表。
//
// 为什么分批：一整张几百万行的表放在一个事务里，NAS 上会长时间占着 journal 和
// 写锁（实测表现为"界面几十分钟没动静"）。每批只扫一段 rowid 范围、单独提交，
// 进度随时可见；Go 内存里只保存这一批的 rowid（batchSize 个 int64）。
//
// 前提：目标表已清空、源库在迁移期间不会被写入（迁移对源库只读）。
func copyTableByRowID(raw *sql.DB, srcAlias, table string, batchSize int, onProgress func(copied int64)) (int64, error) {
	quoted := srcAlias + "." + quoteIdent(table)
	lastRowID := int64(0)
	var copied int64
	for {
		rows, err := raw.Query("SELECT rowid FROM "+quoted+" WHERE rowid > ? ORDER BY rowid LIMIT ?", lastRowID, batchSize)
		if err != nil {
			return copied, fmt.Errorf("读取 %s 的 rowid 失败：%w", table, err)
		}
		ids := make([]int64, 0, batchSize)
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return copied, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return copied, err
		}
		rows.Close()
		if len(ids) == 0 {
			return copied, nil
		}
		lo, hi := lastRowID+1, ids[len(ids)-1]
		tx, err := raw.Begin()
		if err != nil {
			return copied, err
		}
		res, err := tx.Exec("INSERT INTO "+quoteIdent(table)+
			" SELECT * FROM "+quoted+" WHERE rowid >= ? AND rowid <= ? ORDER BY rowid", lo, hi)
		if err != nil {
			_ = tx.Rollback()
			return copied, fmt.Errorf("复制 %s（rowid %d..%d）失败：%w", table, lo, hi, err)
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			_ = tx.Rollback()
			return copied, err
		}
		if inserted != int64(len(ids)) {
			_ = tx.Rollback()
			return copied, fmt.Errorf("%s 批次行数不一致：扫描=%d 写入=%d", table, len(ids), inserted)
		}
		if err := tx.Commit(); err != nil {
			return copied, fmt.Errorf("提交 %s 的批次失败：%w", table, err)
		}
		copied += inserted
		lastRowID = hi
		if onProgress != nil {
			onProgress(copied)
		}
	}
}

// attachSource 把源库以指定别名挂到目标连接上；返回 detach 函数。
//
// ATTACH 不支持参数绑定，路径按 SQLite 字面量规则转义单引号。
func attachSource(raw *sql.DB, sourcePath, alias string) (func(), error) {
	quoted := "'" + strings.ReplaceAll(filepath.ToSlash(sourcePath), "'", "''") + "'"
	if _, err := raw.Exec("ATTACH DATABASE " + quoted + " AS " + alias); err != nil {
		return nil, fmt.Errorf("附加旧库失败：%w", err)
	}
	return func() { _, _ = raw.Exec("DETACH DATABASE " + alias) }, nil
}

// countTable 统计某个库里的表行数。
func countTable(raw *sql.DB, qualifiedTable string) (int, error) {
	var count int
	if err := raw.QueryRow("SELECT COUNT(*) FROM " + qualifiedTable).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
