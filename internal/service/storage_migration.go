package service

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"freestock/internal/portfolio"
)

// StorageMigrationState 是 UI 可见的迁移状态。
type StorageMigrationState struct {
	Status         string `json:"status"`
	StartedAt      string `json:"startedAt,omitempty"`
	CompletedAt    string `json:"completedAt,omitempty"`
	SourcePath     string `json:"sourcePath,omitempty"`
	CorePath       string `json:"corePath,omitempty"`
	AnalyticsPath  string `json:"analyticsPath,omitempty"`
	BacktestPath   string `json:"backtestPath,omitempty"`
	CoreQuick      string `json:"coreQuick,omitempty"`
	AnalyticsQuick string `json:"analyticsQuick,omitempty"`
	BacktestQuick  string `json:"backtestQuick,omitempty"`
	Error          string `json:"error,omitempty"`
	Phase          string `json:"phase,omitempty"`
	CurrentTable   string `json:"currentTable,omitempty"`
	TablesDone     int    `json:"tablesDone,omitempty"`
	TablesTotal    int    `json:"tablesTotal,omitempty"`
	RowsDone       int64  `json:"rowsDone,omitempty"`
	RowsTotal      int64  `json:"rowsTotal,omitempty"`
	// Archived 记录本次迁移归档了哪些上次遗留的目标文件（改名为 *.stale-*）。
	Archived string `json:"archived,omitempty"`
	// UpdatedAt 每次写状态时刷新。用来判断"状态停在 running 但进程已经没了"
	// （容器重启/进程被杀），这种状态必须能被识别为中断，否则按钮永远点不动。
	UpdatedAt string `json:"updatedAt,omitempty"`
}

var (
	storageMigrationMu sync.Mutex
	// migrationActive 表示**本进程**里确实有一个迁移在跑。
	// 状态文件里的 running 只代表"上次写到一半"，两者不是一回事。
	migrationActive atomic.Bool
)

// staleMigrationAfter 多久没有状态更新就认为上次迁移已中断。
// 迁移每张表/每批都会写状态，正常间隔远小于这个值。
const staleMigrationAfter = 120 * time.Second

// migrationState 读取状态文件；路径推导只使用旧库/配置路径，避免循环依赖。
func (s *PortfolioService) migrationState() StorageMigrationState {
	path := s.legacyDBPath()
	if path == "" {
		return StorageMigrationState{Status: "idle"}
	}
	var state StorageMigrationState
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), "migration-state.json"))
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Status == "" {
		return StorageMigrationState{Status: "idle"}
	}
	return state
}

// legacyDBPath 返回迁移前的固定旧库路径，不读取迁移后的 core 路径。
func (s *PortfolioService) legacyDBPath() string {
	if s.dbPathOverride != "" {
		return s.dbPathOverride
	}
	if path := strings.TrimSpace(os.Getenv("FREESTOCK_PORTFOLIO_DB_PATH")); path != "" {
		return path
	}
	configPath := ""
	if s.stock != nil && s.stock.cfg != nil {
		configPath = s.stock.cfg.Path()
	}
	if configPath == "" {
		return ""
	}
	return portfolio.DefaultPath(configPath)
}

// GetStorageMigrationState 返回持久化迁移状态。
//
// 如果文件里停在 running、但本进程并没有迁移在跑、且状态已超过
// staleMigrationAfter 没更新，就返回 `interrupted`：让界面能把按钮放回
// “继续迁移”，而不是永久禁用。
func (s *PortfolioService) GetStorageMigrationState() StorageMigrationState {
	state := s.migrationState()
	if state.Status != "running" || migrationActive.Load() {
		return state
	}
	if state.UpdatedAt == "" {
		state.Status = "interrupted"
		return state
	}
	updated, err := time.Parse(time.RFC3339, state.UpdatedAt)
	if err != nil || time.Since(updated) > staleMigrationAfter {
		state.Status = "interrupted"
	}
	return state
}

// MigrateStorage 执行一次核心库+分析库迁移。
//
// 迁移跑在后端进程里，**与页面生命周期无关**：切换页面/刷新页面不会中止它；
// 页面回来只需重新读状态文件即可继续显示进度。若已有迁移在跑，
// 这里不排队（排队会让第二个请求一直挂到几小时后），直接返回当前状态。
func (s *PortfolioService) MigrateStorage() (StorageMigrationState, error) {
	if !storageMigrationMu.TryLock() {
		return s.migrationState(), fmt.Errorf("迁移已在进行中，请等待当前任务完成（可刷新页面查看进度）")
	}
	defer storageMigrationMu.Unlock()
	migrationActive.Store(true)
	defer migrationActive.Store(false)

	state := s.migrationState()
	// 老版本可能已经"整体成功"，但那时没有回测库（回测表还在旧库）。
	// 这种状态不能直接返回：backtest.db 不存在 → backtestDB 回落旧库 →
	// 迁移后新建的组合一跑回测就撞 787。这里把它当作"只差回测库"继续补齐。
	upgradeBacktestOnly := false
	if state.Status == "succeeded" {
		if state.BacktestPath != "" {
			if _, statErr := os.Stat(state.BacktestPath); statErr == nil {
				return state, nil
			}
		}
		if state.CoreQuick == "ok" && state.AnalyticsQuick == "ok" {
			upgradeBacktestOnly = true
			slog.Info("存储迁移：已完成核心/分析库，仅补齐回测库")
		} else {
			return state, nil
		}
	}
	prior := state // 上一次落盘的状态（判断能否直接接手分析库）
	sourcePath := s.legacyDBPath()
	if sourcePath == "" {
		return state, fmt.Errorf("无法确定旧组合库路径")
	}
	dir := filepath.Dir(sourcePath)
	coreTarget := filepath.Join(dir, "portfolio-core.db")
	analyticsTarget := filepath.Join(dir, "analytics.db")
	backtestTarget := filepath.Join(dir, "backtest.db")
	slog.Info("存储迁移开始", "source", sourcePath,
		"core", coreTarget, "analytics", analyticsTarget, "backtest", backtestTarget)
	state = StorageMigrationState{
		Status: "running", Phase: "core", StartedAt: time.Now().Format(time.RFC3339),
		SourcePath: sourcePath, CorePath: coreTarget,
		AnalyticsPath: analyticsTarget, BacktestPath: backtestTarget,
	}
	if upgradeBacktestOnly {
		// 核心/分析库上一步已经是成功的，保留它们的校验结果，只补回测库。
		state.CoreQuick = prior.CoreQuick
		state.AnalyticsQuick = prior.AnalyticsQuick
		state.Phase = "backtest"
	}
	if err := s.writeMigrationState(state); err != nil {
		return state, err
	}

	var err error
	// 上次可能已经把分析表复制完、只差收尾。先尝试"接手校验"，成功就跳过复制，
	// 不把几小时的复制成果归档重做。回测段仍要照常执行。
	analyticsFinalized := upgradeBacktestOnly // 升级补齐时分析库已经成功，不再碰它
	if !upgradeBacktestOnly {
		if finalized, finErr := s.finalizeExistingAnalytics(sourcePath, analyticsTarget, prior, &state); finalized {
			if finErr != nil {
				slog.Warn("存储迁移：已有分析库收尾失败，将重新复制", "err", finErr)
			} else {
				analyticsFinalized = true
				state.AnalyticsQuick = "ok"
				if prior.CoreQuick == "ok" {
					state.CoreQuick = "ok"
				} else if _, quick, vErr := portfolio.ValidateCoreDatabase(coreTarget); vErr == nil {
					state.CoreQuick = quick
				}
				slog.Info("存储迁移：分析库直接接手完成（未重新复制）", "path", analyticsTarget)
			}
		}
	}

	// 上次失败/中止会留下目标文件；旧版本会因此直接拒绝迁移，用户就只能手删。
	// 这里先做**安全的归档**：分析库/回测库只有派生数据，可重算，一律改名备份后
	// 重做；核心库只有在"明显不完整"（源有组合、目标 0 个）时才归档，正常核心库
	// 继续复用。已经接手成功的分析库不再归档。
	if archived, archErr := s.archiveStaleTargets(sourcePath, coreTarget, analyticsTarget, backtestTarget, analyticsFinalized); archErr != nil {
		err = archErr
	} else if len(archived) > 0 {
		state.Archived = strings.Join(archived, "、")
		slog.Warn("存储迁移：已归档上次遗留的目标文件", "files", state.Archived)
		_ = s.writeMigrationState(state)
	}
	if err != nil {
		state.Status = "failed"
		state.Error = err.Error()
		_ = s.writeMigrationState(state)
		return state, err
	}

	coreProgressPath := ""
	if _, statErr := os.Stat(coreTarget); statErr == nil {
		slog.Info("存储迁移：发现已有核心目标，开始快速校验", "path", coreTarget)
		// 可复用的核心目标只做目标 quick_check，不再扫描旧大库。
		_, quick, validateErr := portfolio.ValidateCoreDatabase(coreTarget)
		if validateErr != nil {
			err = fmt.Errorf("已有核心目标校验失败：%w；请先备份后移走该目标", validateErr)
		} else {
			coreProgressPath = coreTarget
			state.CoreQuick = quick
			slog.Info("存储迁移：核心目标校验通过，进入分析库", "path", coreTarget)
			state.Phase = "analytics"
			state.CurrentTable = "准备分析库"
			state.TablesDone, state.TablesTotal = 0, len(portfolio.AnalyticsTables)
			state.RowsDone, state.RowsTotal = 0, 0
			if stateErr := s.writeMigrationState(state); stateErr != nil {
				slog.Error("写分析迁移初始状态失败", "err", stateErr)
			}
		}
	} else if !os.IsNotExist(statErr) {
		err = statErr
	}
	var coreReport portfolio.CoreMigrationReport
	if err == nil && coreProgressPath == "" {
		slog.Info("存储迁移：开始核心库表复制", "path", coreTarget)
		coreReport, err = portfolio.MigrateCoreDatabaseWithProgress(sourcePath, coreTarget, func(table string, done, total int) {
			state.Phase = "core"
			state.CurrentTable = table
			state.TablesDone = done
			state.TablesTotal = total
			if done == total {
				state.Phase = "core-verify"
				state.CurrentTable = "quick_check"
			}
			if stateErr := s.writeMigrationState(state); stateErr != nil {
				slog.Error("写核心迁移进度失败", "err", stateErr, "table", table)
			}
		})
	}
	if err == nil {
		state.CoreQuick = coreReport.QuickCheck
		if state.CoreQuick == "" {
			state.CoreQuick = "ok"
		}
		state.Phase = "analytics"
		state.CurrentTable = ""
		state.TablesDone, state.TablesTotal = 0, len(portfolio.AnalyticsTables)
		state.RowsDone, state.RowsTotal = 0, 0
		_ = s.writeMigrationState(state)
	}
	if err == nil && !analyticsFinalized {
		slog.Info("存储迁移：开始分析库复制", "path", analyticsTarget)
		analyticsReport, analyticsErr := portfolio.MigrateAnalyticsDatabaseWithProgress(sourcePath, analyticsTarget, func(table string, done, total int, rowsDone, rowsTotal int64) {
			state.CurrentTable = table
			state.TablesDone = done
			state.TablesTotal = total
			state.RowsDone = rowsDone
			if rowsTotal >= 0 {
				state.RowsTotal = rowsTotal
			}
			if table == "索引重建" || table == "完整性检查" {
				state.Phase = "analytics-finalize"
			}
			_ = s.writeMigrationState(state)
		})
		if analyticsErr != nil {
			err = analyticsErr
		} else {
			state.AnalyticsQuick = analyticsReport.QuickCheck
		}
	}
	// ---- 回测库：三张结果表搬到独立文件 ----
	if err == nil {
		slog.Info("存储迁移：开始回测库复制", "path", backtestTarget)
		state.Phase = "backtest"
		state.CurrentTable = "准备回测库"
		state.TablesDone, state.TablesTotal = 0, len(portfolio.BacktestTables)
		state.RowsDone, state.RowsTotal = 0, 0
		_ = s.writeMigrationState(state)
		backtestReport, backtestErr := portfolio.MigrateBacktestDatabaseWithProgress(sourcePath, backtestTarget, func(table string, done, total int, rowsDone int64) {
			state.CurrentTable = table
			state.TablesDone = done
			state.TablesTotal = total
			state.RowsDone, state.RowsTotal = rowsDone, 0
			_ = s.writeMigrationState(state)
		})
		if backtestErr != nil {
			err = backtestErr
		} else {
			state.BacktestQuick = backtestReport.QuickCheck
		}
	}
	if err != nil {
		state.Status = "failed"
		state.Error = err.Error()
		_ = s.writeMigrationState(state)
		return state, err
	}
	state.Status = "succeeded"
	state.CompletedAt = time.Now().Format(time.RFC3339)
	if err := s.writeMigrationState(state); err != nil {
		return state, err
	}
	return state, nil
}

// finalizeExistingAnalytics 判断能否直接接手一个已有的分析库。
//
// 只在"上一次迁移已经把全部表复制完"（状态里 tablesDone ≥ 总表数）时才尝试，
// 避免把半成品当成品。返回 finalized=true 表示目标存在且可收尾：
//   - err == nil：收尾成功，可以标记迁移完成；
//   - err != nil：收尾失败（行数不一致/校验失败），调用方应归档后重做。
func (s *PortfolioService) finalizeExistingAnalytics(sourcePath, analyticsTarget string, prior StorageMigrationState, state *StorageMigrationState) (bool, error) {
	if prior.TablesDone < len(portfolio.AnalyticsTables) {
		return false, nil
	}
	if _, statErr := os.Stat(analyticsTarget); statErr != nil {
		return false, nil
	}
	slog.Info("存储迁移：发现上次已复制完的分析库，改为只做校验收尾", "path", analyticsTarget,
		"tablesDone", prior.TablesDone)
	state.Phase = "analytics-finalize"
	_, err := portfolio.FinalizeAnalyticsDatabase(sourcePath, analyticsTarget, func(table string, done, total int) {
		state.CurrentTable = table
		state.TablesDone = done
		state.TablesTotal = total
		state.RowsDone, state.RowsTotal = 0, 0
		_ = s.writeMigrationState(*state)
	})
	return true, err
}

// archiveStaleTargets 处理上次迁移失败/中止留下的目标文件。
//
//   - analytics.db：只放派生数据（评分/信号/快照，可重算）→ 一律归档后重做；
//   - portfolio-core.db：只在**明显不完整**（源库有组合、目标 0 个）时归档；
//     正常核心库保留给上面的"快速校验后复用"分支。
//
// 归档 = 改名为 `<原名>.stale-<时间戳>`（连同 -wal/-shm），不删除任何数据。
func (s *PortfolioService) archiveStaleTargets(sourcePath, coreTarget, analyticsTarget, backtestTarget string, keepAnalytics bool) ([]string, error) {
	stamp := time.Now().Format("20060102-150405")
	archived := make([]string, 0, 3)

	if !keepAnalytics {
		if _, statErr := os.Stat(analyticsTarget); statErr == nil {
			if err := renameWithSidecars(analyticsTarget, analyticsTarget+".stale-"+stamp); err != nil {
				return archived, fmt.Errorf("归档上次遗留的分析库失败：%w", err)
			}
			archived = append(archived, filepath.Base(analyticsTarget)+".stale-"+stamp)
		} else if !os.IsNotExist(statErr) {
			return archived, statErr
		}
	}

	// 回测库只有派生结果，且迁移成本远低于分析库：一律归档后重做。
	if _, statErr := os.Stat(backtestTarget); statErr == nil {
		if err := renameWithSidecars(backtestTarget, backtestTarget+".stale-"+stamp); err != nil {
			return archived, fmt.Errorf("归档上次遗留的回测库失败：%w", err)
		}
		archived = append(archived, filepath.Base(backtestTarget)+".stale-"+stamp)
	} else if !os.IsNotExist(statErr) {
		return archived, statErr
	}

	if _, statErr := os.Stat(coreTarget); statErr == nil {
		if incomplete := coreTargetLooksEmpty(sourcePath, coreTarget); incomplete {
			if err := renameWithSidecars(coreTarget, coreTarget+".stale-"+stamp); err != nil {
				return archived, fmt.Errorf("归档不完整的核心库失败：%w", err)
			}
			archived = append(archived, filepath.Base(coreTarget)+".stale-"+stamp)
		}
	} else if !os.IsNotExist(statErr) {
		return archived, statErr
	}
	return archived, nil
}

// coreTargetLooksEmpty 判断核心目标是否"没搬进来东西"：源库有组合、目标没有。
// 两个 COUNT 都只扫很小的 portfolio 表，不会碰到 4.9GB 的评分表。
func coreTargetLooksEmpty(sourcePath, coreTarget string) bool {
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		return false
	}
	defer source.Close()
	_, _ = source.Exec("PRAGMA busy_timeout = 15000")
	target, err := sql.Open("sqlite", coreTarget)
	if err != nil {
		return false
	}
	defer target.Close()
	_, _ = target.Exec("PRAGMA busy_timeout = 15000")
	var sourceCount, targetCount int
	if err := source.QueryRow(`SELECT COUNT(*) FROM portfolio`).Scan(&sourceCount); err != nil {
		return false
	}
	if err := target.QueryRow(`SELECT COUNT(*) FROM portfolio`).Scan(&targetCount); err != nil {
		// 目标连 portfolio 表都读不出来 → 肯定是坏的，归档重做。
		return true
	}
	return sourceCount > 0 && targetCount == 0
}

// renameWithSidecars 连同 SQLite 的 -wal/-shm 一起改名。
func renameWithSidecars(path, newPath string) error {
	if err := os.Rename(path, newPath); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			_ = os.Rename(path+suffix, newPath+suffix)
		}
	}
	return nil
}

// ResumeInterruptedMigration 在启动时接着跑上次被中断的迁移。
//
// 典型场景：更新容器/重启进程时迁移正在跑，状态文件停在 running。
// 这里在后台续跑，恢复逻辑（复用核心库、接手已完成的分析库、归档半成品、
// 补回测库）都会自动生效，用户不需要重新点。
//
// 只处理 running：failed 需要用户自己决定重试（可能要先处理磁盘/权限问题）。
func (s *PortfolioService) ResumeInterruptedMigration() {
	state := s.migrationState()
	if state.Status != "running" {
		return
	}
	slog.Info("检测到上次未完成的存储迁移，后台续跑",
		"phase", state.Phase, "table", state.CurrentTable,
		"tablesDone", state.TablesDone, "tablesTotal", state.TablesTotal)
	if resumed, err := s.MigrateStorage(); err != nil {
		slog.Error("续跑存储迁移失败（可在设置页手动重试）", "err", err, "state", resumed.Status)
		return
	}
	slog.Info("存储迁移续跑完成", "status", s.migrationState().Status)
}

func (s *PortfolioService) migrationStatePath() string {
	path := s.legacyDBPath()
	if path == "" {
		return "migration-state.json"
	}
	return filepath.Join(filepath.Dir(path), "migration-state.json")
}

func (s *PortfolioService) writeMigrationState(state StorageMigrationState) error {
	path := s.migrationStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 每次落盘都刷新心跳：判断"running 是否已成孤儿"就靠它。
	state.UpdatedAt = time.Now().Format(time.RFC3339)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
