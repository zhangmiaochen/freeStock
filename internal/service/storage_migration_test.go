package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationStateAutomaticallyActivatesNewStores(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "portfolio.db")
	core := filepath.Join(dir, "portfolio-core.db")
	analytics := filepath.Join(dir, "analytics.db")
	statePath := filepath.Join(dir, "migration-state.json")
	state := StorageMigrationState{Status: "succeeded", CorePath: core, AnalyticsPath: analytics}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(statePath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	t.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", "")
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
