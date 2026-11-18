package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"freestock/internal/portfolio"
)

func writeMigrationFixture(t *testing.T, dir string, state StorageMigrationState) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "migration-state.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationStateAutomaticallyActivatesNewStores(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")
	writeMigrationFixture(t, dir, StorageMigrationState{Status: "succeeded", CoreQuick: "ok", CorePath: core, AnalyticsPath: analytics})

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	svc := &PortfolioService{}
	if got := svc.dbPath(); got != core {
		t.Fatalf("迁移成功后应自动使用核心库，得到 %q", got)
	}
	store, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if got := store.Path(); got != analytics {
		t.Fatalf("迁移成功后应自动使用分析库，得到 %q", got)
	}
}

// 核心热库已单独校验完成即可切换组合列表；分析库仍迁移中时，分析读必须回退旧库。
func TestCoreActivatesBeforeAnalyticsMigrationFinishes(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")
	writeMigrationFixture(t, dir, StorageMigrationState{Status: "running", Phase: "analytics", CoreQuick: "ok", SourcePath: legacy, CorePath: core, AnalyticsPath: analytics})

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	svc := &PortfolioService{}
	if got := svc.dbPath(); got != core {
		t.Fatalf("已验证的核心库应提前启用，得到 %q", got)
	}
	store, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if got := store.Path(); got != legacy {
		t.Fatalf("分析迁移完成前应回退旧库，得到 %q", got)
	}
}

// 验证已有核心库复用后会继续分析迁移，而不是卡在 core 5/5。
func TestMigrateStorageResumesFromValidatedCore(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")

	source, err := portfolio.Open(legacy)
	if err != nil {
		t.Fatal(err)
	}
	pf, err := source.Create(portfolio.PortfolioInput{Name: "resume", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendGrowth})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := portfolio.MigrateCoreDatabase(legacy, core); err != nil {
		t.Fatal(err)
	}

	writeMigrationFixture(t, dir, StorageMigrationState{
		Status: "running", Phase: "core", SourcePath: legacy, CorePath: core,
		AnalyticsPath: analytics, CurrentTable: "portfolio_money", TablesDone: 5, TablesTotal: 5,
	})
	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	svc := &PortfolioService{}
	state, err := svc.MigrateStorage()
	if err != nil {
		t.Fatalf("恢复迁移失败: %v", err)
	}
	if state.Status != "succeeded" || state.CoreQuick != "ok" || state.AnalyticsQuick != "ok" {
		t.Fatalf("迁移未完成: %+v", state)
	}
	if state.TablesDone != len(portfolio.AnalyticsTables) {
		t.Fatalf("分析表数=%d, want=%d", state.TablesDone, len(portfolio.AnalyticsTables))
	}
	if _, err := os.Stat(analytics); err != nil {
		t.Fatalf("分析库未创建: %v", err)
	}
	// 核心和分析库都能独立打开；评分表为空时 count 仍应为 0。
	coreStore, err := portfolio.OpenCore(core)
	if err != nil {
		t.Fatal(err)
	}
	defer coreStore.Close()
	var gotName string
	if err := coreStore.DB().QueryRow(`SELECT name FROM portfolio WHERE portfolio_id=?`, pf.ID).Scan(&gotName); err != nil {
		t.Fatal(err)
	}
	if gotName != "resume" {
		t.Fatalf("核心组合丢失: %q", gotName)
	}
	analyticsDB, err := sql.Open("sqlite", analytics)
	if err != nil {
		t.Fatal(err)
	}
	defer analyticsDB.Close()
	var count int
	if err := analyticsDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM stock_strength`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("源评分为空，分析库行数应为 0，实际 %d", count)
	}
}
