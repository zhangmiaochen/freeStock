package service

import (
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

// touchFile 建一个空文件：现在的路径选择会 stat 目标文件，
// 缺失时回退旧库（避免静默新建空库），所以用例必须把文件建出来。
func touchFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationStateAutomaticallyActivatesNewStores(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")
	writeMigrationFixture(t, dir, StorageMigrationState{Status: "succeeded", CoreQuick: "ok", CorePath: core, AnalyticsPath: analytics})
	touchFile(t, core)
	touchFile(t, analytics)

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	svc := &PortfolioService{}
	if got := svc.dbPath(); got != core {
		t.Fatalf("migration should activate core DB, got %q", got)
	}
	store, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if got := store.Path(); got != analytics {
		t.Fatalf("migration should activate analytics DB, got %q", got)
	}
}

func TestCoreActivatesBeforeAnalyticsMigrationFinishes(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")
	writeMigrationFixture(t, dir, StorageMigrationState{Status: "running", Phase: "analytics", CoreQuick: "ok", SourcePath: legacy, CorePath: core, AnalyticsPath: analytics})
	touchFile(t, core)
	touchFile(t, legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
	t.Setenv("FREESTOCK_ANALYTICS_DB_PATH", "")
	svc := &PortfolioService{}
	if got := svc.dbPath(); got != core {
		t.Fatalf("verified core DB should activate, got %q", got)
	}
	store, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if got := store.Path(); got != legacy {
		t.Fatalf("analysis reads should use legacy DB until migration completes, got %q", got)
	}
}

func TestMigrationFailuresKeepSourceAndTargetsUsable(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "legacy.db")
	targetPath := filepath.Join(dir, "core.db")
	source, err := portfolio.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	item, err := source.Create(portfolio.PortfolioInput{Name: "rollback", Type: portfolio.TypeManual, StrategyID: portfolio.StrategyTrendGrowth})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	// Existing target must be rejected, and source must remain readable.
	if err := os.WriteFile(targetPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := portfolio.MigrateCoreDatabase(sourcePath, targetPath); err == nil {
		t.Fatal("existing target should be refused")
	}
	if raw, err := os.ReadFile(targetPath); err != nil || string(raw) != "keep" {
		t.Fatalf("existing target modified: %q err=%v", raw, err)
	}

	// Once target is moved away, migration succeeds and source row remains.
	if err := os.Remove(targetPath); err != nil {
		t.Fatal(err)
	}
	if _, err := portfolio.MigrateCoreDatabase(sourcePath, targetPath); err != nil {
		t.Fatal(err)
	}
	core, err := portfolio.OpenCore(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if _, err := core.Get(item.ID); err != nil {
		t.Fatalf("migrated core row missing: %v", err)
	}
}
