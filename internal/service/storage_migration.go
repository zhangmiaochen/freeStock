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

// StorageMigrationState 是 UI 可见的迁移状态。状态文件在数据目录中，
// 与数据库一起备份/迁移，避免重启后重复执行大迁移。
type StorageMigrationState struct {
	Status         string `json:"status"` // idle/running/succeeded/failed
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

// GetStorageMigrationState 返回持久化迁移状态。
func (s *PortfolioService) GetStorageMigrationState() StorageMigrationState {
	path := s.migrationStatePath()
	state := StorageMigrationState{Status: "idle"}
	raw, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	if json.Unmarshal(raw, &state) != nil || state.Status == "" {
		return StorageMigrationState{Status: "idle"}
	}
	return state
}

// MigrateStorage 执行一次核心库+分析库迁移。
//
// 这是显式 UI 操作，不在启动时调用；同一进程单飞；成功后状态持久化，
// 相同源/目标再次点击不会重复运行。目标存在时底层迁移工具会拒绝覆盖。
func (s *PortfolioService) MigrateStorage() (StorageMigrationState, error) {
	storageMigrationMu.Lock()
	defer storageMigrationMu.Unlock()

	state := s.GetStorageMigrationState()
	if state.Status == "succeeded" {
		return state, nil
	}
	corePath := s.dbPath()
	if corePath == "" {
		return state, fmt.Errorf("无法确定组合库路径")
	}
	dir := filepath.Dir(corePath)
	sourcePath := corePath
	if strings.HasSuffix(strings.ToLower(filepath.Base(corePath)), "-core.db") {
		sourcePath = filepath.Join(dir, "portfolio.db")
	}
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
	core := s.dbPath()
	if core == "" {
		return "migration-state.json"
	}
	return filepath.Join(filepath.Dir(core), "migration-state.json")
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
