package service

import (
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
			state.TablesDone, state.TablesTotal = 0, len(portfolio.AnalyticsTables)
			_ = s.writeMigrationState(state)
		}
	} else if !os.IsNotExist(statErr) {
		err = statErr
	}
	var coreReport portfolio.CoreMigrationReport
	if err == nil && coreProgressPath == "" {
		coreReport, err = portfolio.MigrateCoreDatabaseWithProgress(sourcePath, coreTarget, func(table string, done, total int) {
			state.Phase = "core"
			state.CurrentTable = table
			state.TablesDone = done
			state.TablesTotal = total
			_ = s.writeMigrationState(state)
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
