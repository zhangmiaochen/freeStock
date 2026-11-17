package service

import (
	"encoding/json"
	"fmt"
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
	state = StorageMigrationState{
		Status: "running", StartedAt: time.Now().Format(time.RFC3339),
		SourcePath: sourcePath, CorePath: coreTarget, AnalyticsPath: analyticsTarget,
	}
	if err := s.writeMigrationState(state); err != nil {
		return state, err
	}

	coreReport, err := portfolio.MigrateCoreDatabase(sourcePath, coreTarget)
	if err == nil {
		state.CoreQuick = coreReport.QuickCheck
	}
	if err == nil {
		analyticsReport, analyticsErr := portfolio.MigrateAnalyticsDatabase(sourcePath, analyticsTarget)
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
