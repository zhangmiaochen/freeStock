package service

import (
	"os"
	"testing"
)

// 诊断默认关闭：不设环境变量时不应该去读全市场原始日线。
func TestBreadthBasisDiagnosticOffByDefault(t *testing.T) {
	os.Unsetenv("FREESTOCK_BREADTH_DIAG")
	svc := &PortfolioService{}
	defer svc.Close()
	// 不 panic、不读库：直接返回
	svc.logBreadthBasisDiagnostic(20261119, nil)
}
