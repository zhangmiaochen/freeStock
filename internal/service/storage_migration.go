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
	CoreQuick      string `json:"coreQuick,omitempty"`
	AnalyticsQuick string `json:"analyticsQuick,omitempty"`
	Error          string `json:"error,omitempty"`
	Phase          string `json:"phase,omitempty"`
	CurrentTable   string `json:"currentTable,omitempty"`
	TablesDone     int    `json:"tablesDone,omitempty"`
	TablesTotal    int    `json:"tablesTotal,omitempty"`
	RowsDone       int64  `json:"rowsDone,omitempty"`
	RowsTotal      int64  `json:"rowsTotal,omitempty"`
	// Archived 记录本次迁移归档了哪些上次遗留的目标文件（改名为 *.stale-*）。
	Archived string `json:"archived,omitempty"`
}

var storageMigrationMu sync.Mutex

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
func (s *PortfolioService) GetStorageMigrationState() StorageMigrationState {
	return s.migrationState()
}

// MigrateStorage 执行一次核心库+分析库迁移。
func (s *PortfolioService) MigrateStorage() (StorageMigrationState, error) {
	storageMigrationMu.Lock()
	defer storageMigrationMu.Unlock()

	state := s.migrationState()
	if state.Status == "succeeded" {
		return state, nil
	}
	sourcePath := s.legacyDBPath()
	if sourcePath == "" {
		return state, fmt.Errorf("无法确定旧组合库路径")
	}
	dir := filepath.Dir(sourcePath)
	coreTarget := filepath.Join(dir, "portfolio-core.db")
	analyticsTarget := filepath.Join(dir, "analytics.db")
	slog.Info("存储迁移开始", "source", sourcePath, "core", coreTarget, "analytics", analyticsTarget)
	state = StorageMigrationState{
		Status: "running", Phase: "core", StartedAt: time.Now().Format(time.RFC3339),
		SourcePath: sourcePath, CorePath: coreTarget, AnalyticsPath: analyticsTarget,
	}
	if err := s.writeMigrationState(state); err != nil {
		return state, err
	}

	var err error
	// 上次失败/中止会留下目标文件；旧版本会因此直接拒绝迁移，用户就只能手删。
	// 这里先做**安全的归档**：分析库只有派生数据（评分/信号/快照），可重算，
	// 一律改名备份后重做；核心库只有在"明显不完整"（源有组合、目标 0 个）时
	// 才归档，正常核心库继续复用。
	if archived, archErr := s.archiveStaleTargets(sourcePath, coreTarget, analyticsTarget); archErr != nil {
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
	if err == nil {
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

// archiveStaleTargets 处理上次迁移失败/中止留下的目标文件。
//
//   - analytics.db：只放派生数据（评分/信号/快照，可重算）→ 一律归档后重做；
//   - portfolio-core.db：只在**明显不完整**（源库有组合、目标 0 个）时归档；
//     正常核心库保留给上面的"快速校验后复用"分支。
//
// 归档 = 改名为 `<原名>.stale-<时间戳>`（连同 -wal/-shm），不删除任何数据。
func (s *PortfolioService) archiveStaleTargets(sourcePath, coreTarget, analyticsTarget string) ([]string, error) {
	stamp := time.Now().Format("20060102-150405")
	archived := make([]string, 0, 2)

	if _, statErr := os.Stat(analyticsTarget); statErr == nil {
		if err := renameWithSidecars(analyticsTarget, analyticsTarget+".stale-"+stamp); err != nil {
			return archived, fmt.Errorf("归档上次遗留的分析库失败：%w", err)
		}
		archived = append(archived, filepath.Base(analyticsTarget)+".stale-"+stamp)
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
	target, err := sql.Open("sqlite", coreTarget)
	if err != nil {
		return false
	}
	defer target.Close()
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
