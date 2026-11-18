package service

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 数据库体积与维护。
//
// 背景（实测，用户 NAS）：`portfolio.db` 涨到 **4.9 GB**，其中
// `stock_strength`（全市场 × 多策略 × 26 年的评分）占绝大部分。
// 在 7.9 MB/s 的读速下，一次碰大量页的查询要跑十分钟，前端表现为
// "组合列表卡住不动"。
//
// 所以这里提供三件事：
//  1. **压缩**：wal_checkpoint(TRUNCATE) + VACUUM —— 归零 WAL、回收空洞、
//     让文件在磁盘上重新连续（随机读退化是慢的另一半原因）。
//  2. **保留窗口**：评分是派生数据（可重算），超过窗口的老评分删掉，
//     让这张表从"随时长无限增长"变成"恒定大小"。
//  3. **体检**：把库体积/空洞/WAL/表行数打进日志，增长失控时能提前看到。

// dbMaintenancePrefs 是维护相关的开关（都用环境变量，避免改配置结构）。
type dbMaintenancePrefs struct {
	// Compact 表示启动时是否做压缩。
	Compact bool
	// RetentionDays > 0 时清理更早的评分；0 表示不清理（默认）。
	RetentionDays int
}

// dbMaintenancePrefs 从环境变量读取维护配置。
//
//	FREESTOCK_DB_COMPACT=1            启动时压缩（默认开；库小的时候会自动跳过）
//	FREESTOCK_SCORE_RETENTION_DAYS=N  评分保留天数，0/空 = 不清理
func dbMaintenancePrefsFromEnv() dbMaintenancePrefs {
	prefs := dbMaintenancePrefs{Compact: false}
	if raw := strings.TrimSpace(os.Getenv("FREESTOCK_DB_COMPACT")); raw != "" {
		if v, err := strconv.ParseBool(raw); err == nil {
			prefs.Compact = v
		}
	}
	if raw := strings.TrimSpace(os.Getenv("FREESTOCK_SCORE_RETENTION_DAYS")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			prefs.RetentionDays = v
		}
	}
	return prefs
}

// dbSizeStats 是一次体检的结果（都按字节）。
type dbSizeStats struct {
	Total    int64 // 主库 + WAL + SHM
	Main     int64 // 主库
	WAL      int64 // -wal
	Free     int64 // freelist_count × page_size（空洞）
	Pages    int64
	PageSize int64
}

// freeRatio 返回空洞占主库的比例。
func (s dbSizeStats) freeRatio() float64 {
	if s.Main <= 0 {
		return 0
	}
	return float64(s.Free) / float64(s.Main)
}

func (s dbSizeStats) String() string {
	return fmt.Sprintf("主库=%s WAL=%s 空洞=%s(%.0f%%)",
		humanMB(s.Main), humanMB(s.WAL), humanMB(s.Free), s.freeRatio()*100)
}

func humanMB(v int64) string {
	return fmt.Sprintf("%.1fMB", float64(v)/(1<<20))
}

// compactThresholdBytes 是"值得压缩"的体积门槛。小库压缩收益抵不上耗时。
const compactThresholdBytes = 256 << 20

// walThresholdBytes 是"WAL 太大必须 checkpoint"的门槛。
//
// ⚠ WAL 大的危害被低估了：SQLite 读之前要先回放它，5GB 的 WAL 在
// 机械盘上就是十分钟的卡顿，而体积统计里它甚至不算在主库里。
const walThresholdBytes = 64 << 20

// measureDB 读库的体积/空洞/WAL。
func measureDB(db *sql.DB, path string) dbSizeStats {
	var out dbSizeStats
	out.Main = fileSize(path)
	out.WAL = fileSize(path+"-wal") + fileSize(path+"-shm")
	out.Total = out.Main + out.WAL

	var pageSize, pageCount, freeCount int64
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err == nil {
		out.PageSize = pageSize
	}
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err == nil {
		out.Pages = pageCount
	}
	if err := db.QueryRow(`PRAGMA freelist_count`).Scan(&freeCount); err == nil {
		if pageSize > 0 {
			out.Free = freeCount * pageSize
		}
	}
	return out
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// CompactDatabase 归零 WAL 并重写数据库文件（VACUUM）。
//
// 返回压缩前后的体检结果，调用方负责打日志。
// 失败时返回已完成的统计与原错误 —— 压缩是维护动作，不该拖垮启动。
func (s *PortfolioService) CompactDatabase() (before, after dbSizeStats, err error) {
	store, err := s.db()
	if err != nil {
		return before, after, err
	}
	raw := store.DB()
	path := s.dbPath()
	before = measureDB(raw, path)

	if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		// checkpoint 失败不算致命：VACUUM 仍可能成功
		slog.Warn("WAL checkpoint 失败（继续尝试 VACUUM）", "err", err)
	}
	if _, err := raw.Exec(`VACUUM`); err != nil {
		after = measureDB(raw, path)
		return before, after, fmt.Errorf("VACUUM 失败（可能是磁盘空间不足，需要约 1 倍库体积的临时空间）：%w", err)
	}
	after = measureDB(raw, path)
	return before, after, nil
}

// MaintainDatabase 是启动时的维护入口（后台执行，不阻塞启动）。
//
// 只在**体积够大**或**WAL 过大**时才真正压缩，避免小库每次都白跑一遍。
func (s *PortfolioService) MaintainDatabase() {
	prefs := dbMaintenancePrefsFromEnv()
	store, err := s.db()
	if err != nil {
		return
	}
	// ⚠ **启动必须确保评分索引存在。**
	//
	// 批量回填会先 DROP idx_strength_lookup 再在结束时重建（省下 680 万次
	// B 树维护）。但如果那次回填**中途失败/被重启打断**，索引就永远缺着 ——
	// 之后任何查 stock_strength 的语句都退化成全表扫描，用户库 4.9GB、
	// 7.9MB/s 读速下就是十分钟卡顿（实测栈里 pread 卡 12 分钟）。
	// 这里是幂等的 CREATE INDEX IF NOT EXISTS，代价小于一次查询。
	//
	// ⚠ 评分表在**分析库**里：切到 core-only 核心库后这里必须去分析库建索引，
	// 否则每次启动都会对核心库报 no such table（噪声日志 + 索引始终缺着）。
	strengthStore := store
	if analytics, err := s.analyticsDB(); err == nil {
		strengthStore = analytics
	}
	beganIdx := time.Now()
	if err := strengthStore.EnsureStrengthLookupIndex(); err != nil {
		slog.Warn("确保评分查询索引失败（查询可能变慢）", "err", err)
	} else {
		slog.Info("评分查询索引已确认存在",
			"elapsed", time.Since(beganIdx).Round(time.Millisecond).String())
	}

	path := s.dbPath()
	stats := measureDB(store.DB(), path)
	slog.Info("数据库体检", "path", path, "stats", stats.String())

	needCompact := stats.Main >= compactThresholdBytes ||
		stats.WAL >= walThresholdBytes ||
		stats.freeRatio() >= 0.20
	if prefs.Compact && needCompact {
		began := time.Now()
		before, after, err := s.CompactDatabase()
		if err != nil {
			slog.Warn("数据库压缩失败（不影响使用）", "err", err,
				"before", before.String())
		} else {
			slog.Info("数据库压缩完成",
				"before", before.String(), "after", after.String(),
				"freed", humanMB(before.Total-after.Total),
				"elapsed", time.Since(began).Round(time.Millisecond).String())
		}
	}

	if prefs.RetentionDays > 0 {
		s.pruneOldScores(prefs.RetentionDays)
	}
}

// pruneOldScores 删除超过保留窗口的评分行。
//
// ⚠ 评分是**派生数据**：它由日线 + 策略参数算出，删掉可以重算。
// 所以按时间窗口清理是安全的，前提是保住"组合与回测真正会用到"的区间。
// 默认**不清理**（FREESTOCK_SCORE_RETENTION_DAYS 为空），由用户显式开启。
func (s *PortfolioService) pruneOldScores(retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	// 评分表在分析库（或未拆分时的旧单库）；不能对 core-only 核心库执行 DELETE。
	store, err := s.analyticsDB()
	if err != nil {
		return
	}
	db := store.DB()
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format("20060102")

	var minDate, maxDate string
	var rows int64
	if err := db.QueryRow(`SELECT COALESCE(MIN(trade_date),''), COALESCE(MAX(trade_date),''), COUNT(*) FROM stock_strength`).
		Scan(&minDate, &maxDate, &rows); err != nil {
		slog.Warn("读取评分表覆盖范围失败，跳过清理", "err", err)
		return
	}
	slog.Info("评分表覆盖范围", "min", minDate, "max", maxDate, "rows", rows,
		"retentionDays", retentionDays, "cutoff", cutoff)

	if minDate == "" || minDate >= cutoff {
		return // 没有超期数据
	}

	began := time.Now()
	res, err := db.Exec(`DELETE FROM stock_strength WHERE trade_date < ?`, cutoff)
	if err != nil {
		slog.Warn("清理历史评分失败（不影响使用）", "err", err)
		return
	}
	deleted, _ := res.RowsAffected()
	slog.Info("已清理历史评分",
		"cutoff", cutoff, "deleted", deleted,
		"elapsed", time.Since(began).Round(time.Millisecond).String(),
		"hint", "建议随后执行一次压缩（VACUUM）回收空间")
}

// scoreRetentionHint 在体检日志里给出建议值，帮助用户决定是否开启清理。
func scoreRetentionHint(db *sql.DB) string {
	var rows int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM stock_strength`).Scan(&rows); err != nil {
		return ""
	}
	// 每天约 7700 只 × 策略数；按行数反推天数没有意义，这里只给量级提示
	if rows > 3_000_000 {
		return fmt.Sprintf("评分表已有 %d 行，建议设置 FREESTOCK_SCORE_RETENTION_DAYS=1095（保留 3 年）", rows)
	}
	return ""
}

// dbPathDir 返回库文件所在目录（供日志使用）。
func dbPathDir(path string) string { return filepath.Dir(path) }
