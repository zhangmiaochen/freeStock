package portfolio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AnalyticsStore 是派生/历史数据专用的数据库句柄。
//
// 第一阶段先复用 Store 的成熟 schema/API，避免复制评分和回测 SQL；类型边界
// 先建立起来，后续把 strength/signal/snapshot/backtest 方法逐步迁移到这里。
// 核心库和分析库拥有不同文件句柄后，页面进程可以先切核心库，分析迁移可灰度。
type AnalyticsStore struct {
	*Store
}

// OpenAnalytics 打开独立分析库。默认使用完整兼容 schema，保证旧表数据可以
// 直接复制并校验；后续 AnalyticsStore schema 会收窄到 AnalyticsTables。
func OpenAnalytics(path string) (*AnalyticsStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("分析库路径不能为空")
	}
	store, err := Open(path)
	if err != nil {
		return nil, err
	}
	return &AnalyticsStore{Store: store}, nil
}

// DefaultAnalyticsPath 返回与组合库同目录的分析库路径。
func DefaultAnalyticsPath(corePath string) string {
	if corePath == "" {
		return "analytics.db"
	}
	return filepath.Join(filepath.Dir(corePath), "analytics.db")
}

// AnalyticsPathFromEnv 返回显式分析库路径；空值表示仍使用旧单库。
func AnalyticsPathFromEnv(corePath string) string {
	if path := strings.TrimSpace(os.Getenv("FREESTOCK_ANALYTICS_DB_PATH")); path != "" {
		return path
	}
	return ""
}
