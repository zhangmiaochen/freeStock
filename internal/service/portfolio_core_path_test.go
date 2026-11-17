package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPortfolioCoreDBPathIsExplicitAndReversible(t *testing.T) {
	oldCore, hadCore := os.LookupEnv("FREESTOCK_PORTFOLIO_CORE_DB_PATH")
	oldCompat, hadCompat := os.LookupEnv("FREESTOCK_PORTFOLIO_DB_PATH")
	t.Cleanup(func() {
		if hadCore {
			_ = os.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", oldCore)
		} else {
			_ = os.Unsetenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH")
		}
		if hadCompat {
			_ = os.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", oldCompat)
		} else {
			_ = os.Unsetenv("FREESTOCK_PORTFOLIO_DB_PATH")
		}
	})
	_ = os.Unsetenv("FREESTOCK_PORTFOLIO_DB_PATH")
	_ = os.Unsetenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH")

	legacy := filepath.Join(t.TempDir(), "legacy", "portfolio.db")
	core := filepath.Join(t.TempDir(), "core", "portfolio-core.db")
	svc := NewPortfolioServiceAt(nil, legacy)
	if got := svc.dbPath(); got != legacy {
		t.Fatalf("显式测试路径应优先，得到 %q", got)
	}
	// dbPathOverride 仍然优先，不受环境变量影响。
	_ = os.Setenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH", core)
	if got := svc.dbPath(); got != legacy {
		t.Fatalf("测试 override 不应被环境变量覆盖，得到 %q", got)
	}

	// 生产构造器没有 override 时，核心库变量才生效。
	prod := &PortfolioService{}
	if got := prod.dbPath(); got != core {
		t.Fatalf("核心库变量应生效，得到 %q", got)
	}
	_ = os.Unsetenv("FREESTOCK_PORTFOLIO_CORE_DB_PATH")
	_ = os.Setenv("FREESTOCK_PORTFOLIO_DB_PATH", legacy)
	if got := prod.dbPath(); got != legacy {
		t.Fatalf("兼容覆盖变量应生效，得到 %q", got)
	}
}
