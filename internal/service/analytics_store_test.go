package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyticsDBFallsBackAndCutsOverExplicitly(t *testing.T) {
	old, had := os.LookupEnv("FREESTOCK_ANALYTICS_DB_PATH")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("FREESTOCK_ANALYTICS_DB_PATH", old)
		} else {
			_ = os.Unsetenv("FREESTOCK_ANALYTICS_DB_PATH")
		}
	})
	_ = os.Unsetenv("FREESTOCK_ANALYTICS_DB_PATH")

	legacy := filepath.Join(t.TempDir(), "legacy.db")
	svc := NewPortfolioServiceAt(nil, legacy)
	fallback, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Path() != legacy {
		t.Fatalf("未配置分析库应回退旧库，得到 %q", fallback.Path())
	}
	svc.Close()

	analyticsPath := filepath.Join(t.TempDir(), "analytics.db")
	_ = os.Setenv("FREESTOCK_ANALYTICS_DB_PATH", analyticsPath)
	svc = NewPortfolioServiceAt(nil, legacy)
	analytics, err := svc.analyticsDB()
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if analytics.Path() != analyticsPath {
		t.Fatalf("分析库路径未生效，得到 %q", analytics.Path())
	}
}
