package service

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"freestock/internal/portfolio"
	"freestock/internal/score"
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
	// "成功"不等于"数据完整"：旧版本可能在没有行数校验时把空核心库判成成功，
	// 或者当时还没有回测库。直接 return 会让用户永远停在"列表为空/回测写失败"。
	// 这里按**实际数据**判断是否还需要补，需要就继续走迁移流程。
	if state.Status == "succeeded" {
		if reason := s.migrationIncompleteReason(state); reason == "" {
			return state, nil
		} else {
			slog.Warn("迁移状态是成功，但数据不完整，继续修复", "reason", reason)
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
	if err := s.writeMigrationState(state); err != nil {
		return state, err
	}

	var err error
	// 上次可能已经把分析表复制完、只差收尾。先尝试"接手校验"，成功就跳过复制，
	// 不把几小时的复制成果归档重做。回测段仍要照常执行。
	analyticsFinalized := false
	if finalized, finErr := s.finalizeExistingAnalytics(sourcePath, analyticsTarget, prior, &state); finalized {
		switch {
		case finErr == nil:
			analyticsFinalized = true
			state.AnalyticsQuick = "ok"
			if prior.CoreQuick == "ok" {
				state.CoreQuick = "ok"
			} else if _, quick, vErr := portfolio.ValidateCoreDatabase(coreTarget); vErr == nil {
				state.CoreQuick = quick
			}
			slog.Info("存储迁移：分析库直接接手完成（未重新复制）", "path", analyticsTarget)
		case errors.Is(finErr, portfolio.ErrAnalyticsIncomplete):
			// 缺行 = 上次复制确实没写完，归档重做是正确的。
			slog.Warn("存储迁移：分析库缺行，将归档后重新复制", "err", finErr)
		case portfolio.IsCorruptDatabaseError(finErr):
			// 文件根本不是可用库（被截断/写坏）→ 没有保留价值，归档重做。
			slog.Warn("存储迁移：分析库文件已损坏，将归档后重新复制", "err", finErr)
		default:
			// 其它错误（占用/IO/校验失败）不要动已复制好的文件：
			// 归档重做等于再花几小时，先让用户看到真实错误。
			slog.Error("存储迁移：分析库校验失败（文件已保留，未重新复制）", "err", finErr)
			state.Status = "failed"
			state.Error = fmt.Sprintf("分析库校验失败（已保留 %s，可稍后重试）：%v", analyticsTarget, finErr)
			_ = s.writeMigrationState(state)
			return state, finErr
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
		coreBegan := time.Now()
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
		if err == nil {
			slog.Info("存储迁移：核心库复制+校验完成",
				"elapsed", time.Since(coreBegan).Round(time.Millisecond).String())
		}
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
		analyticsBegan := time.Now()
		lastLogRows := int64(0)
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
			// 每 20 万行打一条日志：状态文件之外再给一个"确实在动"的证据，
			// 出问题时能区分"慢"和"卡死"。
			if rowsDone-lastLogRows >= 200000 {
				lastLogRows = rowsDone
				slog.Info("存储迁移进度", "table", table, "rowsDone", rowsDone,
					"elapsed", time.Since(analyticsBegan).Round(time.Second).String())
			}
		})
		if analyticsErr != nil {
			err = analyticsErr
		} else {
			state.AnalyticsQuick = analyticsReport.QuickCheck
			slog.Info("存储迁移：分析库复制+校验完成",
				"elapsed", time.Since(analyticsBegan).Round(time.Millisecond).String())
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
	// 关键：让进程里缓存的旧库句柄失效，否则迁移后继续操作会把数据写进旧库。
	s.invalidateStoreCache()
	// 迁移期间被跳过的"删组合遗留清理"，现在库已切换，可以安全补做。
	s.retryPendingCleanup()
	return state, nil
}

// finalizeExistingAnalytics 判断能否直接接手一个已有的分析库。
//
// **不再用状态里的 tablesDone 当判据**：收尾阶段会用自己的"检查到第几张表"
// 覆盖 tablesDone，若在收尾时被打断，下次读到的可能是 0/7 —— 按旧判据就会
// 误判成"没复制过"并把几小时的成果归档重做。
//
// 现在的判据只有一条：目标文件存在就尝试收尾；收尾内部会**逐表比对源/目标
// 行数**，对不上就返回错误，调用方再归档重做。判据从"状态自述"变成"数据本身"。
func (s *PortfolioService) finalizeExistingAnalytics(sourcePath, analyticsTarget string, prior StorageMigrationState, state *StorageMigrationState) (bool, error) {
	if _, statErr := os.Stat(analyticsTarget); statErr != nil {
		return false, nil
	}
	slog.Info("存储迁移：发现已有分析库，先按行数判断能否直接收尾", "path", analyticsTarget,
		"stateTablesDone", prior.TablesDone)
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
	// 归档要 rename 目标文件；Windows 上只要还有打开的句柄就会失败
	// （"The process cannot access the file because it is being used by another process"）。
	// 先释放缓存句柄——它们指向的正是即将被替换的文件。
	s.invalidateStoreCache()

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
		// 核心目标是否"比源库少行"——少行说明上次复制没做完（可能只搬了一部分，
		// 空库反而是最容易被发现的那种）。目标比源多行是正常的（迁移后新建过组合）。
		if incomplete, detail, checkErr := portfolio.CoreTargetIncomplete(sourcePath, coreTarget); checkErr != nil {
			slog.Warn("存储迁移：核对核心库行数失败，保留现有核心库", "err", checkErr)
		} else if incomplete {
			slog.Warn("存储迁移：核心库比源库少行，归档后重做", "detail", detail)
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

// migrationIncompleteReason 判断一个"成功"状态是否真的完整。
//
// 返回空串表示完整；否则返回人类可读的原因，调用方应继续走迁移补齐。
//
// 为什么不能只看 status：旧版本没有行数校验，可能把"核心库 0 行"判成成功，
// 或者在还没有回测库时就写了 succeeded。用户那边的表现就是"迁移完成，
// 但组合列表空白 / 回测写失败"。
func (s *PortfolioService) migrationIncompleteReason(state StorageMigrationState) string {
	if state.CoreQuick != "ok" || state.AnalyticsQuick != "ok" || state.BacktestQuick != "ok" {
		return "校验结果不完整"
	}
	if state.CorePath == "" || state.AnalyticsPath == "" || state.BacktestPath == "" {
		return "缺少库路径"
	}
	for _, path := range []string{state.CorePath, state.AnalyticsPath, state.BacktestPath} {
		if _, err := os.Stat(path); err != nil {
			return fmt.Sprintf("缺少文件 %s", filepath.Base(path))
		}
	}
	// 核心库"空壳"：源库有组合、核心库 0 个 —— 组合列表会空白。
	coreCount, err := countPortfoliosAt(state.CorePath)
	if err != nil {
		return fmt.Sprintf("核心库不可读：%v", err)
	}
	if coreCount == 0 {
		if legacyCount, legacyErr := countPortfoliosAt(s.legacyDBPath()); legacyErr == nil && legacyCount > 0 {
			return fmt.Sprintf("核心库 0 个组合，旧库有 %d 个", legacyCount)
		}
	}
	return ""
}

// countPortfoliosAt 统计某个库里的组合数（portfolio 表很小，代价可忽略）。
func countPortfoliosAt(path string) (int, error) {
	if path == "" {
		return 0, fmt.Errorf("路径为空")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	_, _ = db.Exec("PRAGMA busy_timeout = 5000")
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM portfolio`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// readStateFile 读取状态文件；不存在或解析失败返回 ok=false。
func readStateFile(path string) (StorageMigrationState, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return StorageMigrationState{}, false
	}
	var state StorageMigrationState
	if json.Unmarshal(raw, &state) != nil || state.Status == "" {
		return StorageMigrationState{}, false
	}
	return state, true
}

// LogStoragePaths 启动时把"程序认为自己在用哪几个文件"打出来。
//
// 报错信息（例如 no such table: portfolio）只说结果，不说路径；这一行让
// 用户一眼看到是不是 compose 里的环境变量填错了。
func (s *PortfolioService) LogStoragePaths() {
	state := s.migrationState()
	slog.Info("存储路径",
		"core", s.dbPath(),
		"legacy", s.legacyDBPath(),
		"coreEnv", strings.TrimSpace(os.Getenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH")),
		"analyticsEnv", strings.TrimSpace(os.Getenv("FREESTOCK_ANALYTICS_DB_PATH")),
		"backtestEnv", strings.TrimSpace(os.Getenv("FREESTOCK_BACKTEST_DB_PATH")),
		"stateStatus", state.Status,
		"stateCore", state.CorePath,
		"stateAnalytics", state.AnalyticsPath,
		"stateBacktest", state.BacktestPath)
}

// LogBreadthAdjustment 启动时打印"低吸档位阈值按什么口径折算"。
//
// 这个系数决定"广度到多少才开一层"。写死且不可见时，用户按原版的
// 1000/1500/2000/2600 对照会以为参数被改了；打印出来 + 可覆盖，才说得清。
func (s *PortfolioService) LogBreadthAdjustment() {
	scale := score.LowBuyBreadthAdjScale
	slog.Info("低吸广度阈值口径",
		"adjScale", scale,
		"tiers", fmt.Sprintf("%d/%d/%d/%d",
			score.LowBuyPositionThreshold(1), score.LowBuyPositionThreshold(2),
			score.LowBuyPositionThreshold(3), score.LowBuyPositionThreshold(4)),
		"hint", "复权口径默认 ×1.04；FREESTOCK_BREADTH_ADJ_SCALE=1 表示不修正")
}

// CheckStorageConsistency 启动时核对"核心库确实是组合数据的来源"。
//
// 迁移显示成功、但核心库里 0 个组合、旧库里有 —— 组合页面就会是空白。
// 这正是用户反复遇到的"迁移完成后列表为空"。这里不自动回退（否则用户真的
// 删光了组合，重启后又会"复活"），但会把事实写进 ERROR 日志并给出下一步。
func (s *PortfolioService) CheckStorageConsistency() {
	state := s.migrationState()
	if state.CoreQuick != "ok" || state.CorePath == "" {
		return
	}
	core, err := s.db()
	if err != nil {
		return
	}
	var coreCount int
	if err := core.DB().QueryRow(`SELECT COUNT(*) FROM portfolio`).Scan(&coreCount); err != nil {
		slog.Error("核心库读取组合数失败", "err", err, "path", core.Path())
		return
	}
	if coreCount > 0 {
		return
	}
	legacyPath := s.legacyDBPath()
	if legacyPath == "" || filepath.Clean(legacyPath) == filepath.Clean(core.Path()) {
		return
	}
	if _, statErr := os.Stat(legacyPath); statErr != nil {
		return
	}
	legacy, err := s.legacyDB()
	if err != nil {
		return
	}
	var legacyCount int
	if err := legacy.DB().QueryRow(`SELECT COUNT(*) FROM portfolio`).Scan(&legacyCount); err != nil || legacyCount == 0 {
		return
	}
	slog.Error("核心库没有任何组合，但旧库里有 —— 组合页面会是空白",
		"core", core.Path(), "coreCount", coreCount,
		"legacy", legacyPath, "legacyCount", legacyCount,
		"hint", "在设置页重新执行迁移（会校验并补上核心库）；只想回退旧库则删除 migration-state.json")
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
	// **拒绝过期写入**：如果文件里已经是"更新的成功状态"，而本次写入来自一个
	// 更早开始、还没退出的迁移进程（StartedAt 早于 CompletedAt），说明是僵尸
	// 进程在回写旧状态 —— 写入会把界面打回"迁移中"，甚至让 backtestDB 回退。
	if existing, ok := readStateFile(path); ok &&
		existing.Status == "succeeded" && state.Status == "running" {
		started, sErr := time.Parse(time.RFC3339, state.StartedAt)
		completed, cErr := time.Parse(time.RFC3339, existing.CompletedAt)
		if sErr == nil && cErr == nil && started.Before(completed) {
			slog.Warn("忽略过期迁移状态写入（已有更新的成功状态）",
				"staleStartedAt", state.StartedAt, "completedAt", existing.CompletedAt)
			return nil
		}
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
