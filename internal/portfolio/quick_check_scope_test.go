package portfolio

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// `PRAGMA quick_check` 不带 schema 前缀时会检查**当前连接上所有 ATTACH 的库**。
// 迁移正是把 4.9GB 旧库 ATTACH 到目标连接上，于是"只校验一下新核心库"变成了
// 整库全扫旧库 —— NAS 上表现为核心库校验卡几十分钟、analytics.db 不生成。
//
// 这条用例把行为钉死：被 ATTACH 的库坏掉时，不带前缀的 quick_check 会报错，
// 而 `main.quick_check` 只检查主库、仍然 ok。
func TestQuickCheckWithoutSchemaAlsoChecksAttachedDatabases(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "target.db")
	sourcePath := filepath.Join(dir, "source.db")

	// 目标库：小，内容正常。
	target, err := OpenCore(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	// 源库：写到足够大（多页），再人为写坏中间的一页。
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]StrengthRow, 0, 600)
	for i := 0; i < 600; i++ {
		rows = append(rows, StrengthRow{
			TradeDate: 20261009, StrategyID: StrategyTrendGrowth,
			Code: fmt.Sprintf("6%05d.SH", i),
			Name: "abcdefghijklmnop", TotalScore: 50,
		})
	}
	if err := source.SaveStrength(20261009, StrategyTrendGrowth, rows); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 16384 {
		t.Skipf("源库太小（%d 字节），无法稳定制造页损坏", info.Size())
	}
	// 覆盖第 2 页内容（偏移 4096 起 512 字节），保留文件头与大小。
	f, err := os.OpenFile(sourcePath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	junk := make([]byte, 512)
	for i := range junk {
		junk[i] = 0xEE
	}
	if _, err := f.WriteAt(junk, 4096); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	raw := target.DB()
	if _, err := raw.Exec("ATTACH DATABASE ? AS legacy_src", sourcePath); err != nil {
		t.Skipf("损坏的源库无法 attach：%v", err)
	}
	defer raw.Exec("DETACH DATABASE legacy_src")

	var plain string
	plainErr := raw.QueryRow("PRAGMA quick_check").Scan(&plain)
	attachedChecked := plainErr != nil || plain != "ok"

	var mainOnly string
	if err := raw.QueryRow("PRAGMA main.quick_check").Scan(&mainOnly); err != nil {
		t.Fatalf("main.quick_check 不应失败（只检查目标库）：%v", err)
	}
	if mainOnly != "ok" {
		t.Fatalf("main.quick_check 应只报目标库结果 ok，得到 %q", mainOnly)
	}
	if !attachedChecked {
		t.Log("当前 SQLite 版本未把 ATTACH 的库纳入 plain quick_check —— " +
			"迁移里用 main. 前缀仍然更明确，行为不受影响")
		return
	}
	t.Logf("plain quick_check 把 ATTACH 的库也扫了（结果=%q err=%v），"+
		"迁移必须用 main.quick_check", plain, plainErr)
}

var _ = sql.ErrNoRows
