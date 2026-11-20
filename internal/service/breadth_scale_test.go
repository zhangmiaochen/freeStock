package service

import (
	"os"
	"testing"

	"freestock/internal/cnlake"
	"freestock/internal/score"
)

// P1：广度修正系数必须可覆盖、可解释。
func TestBreadthAdjScaleFromEnv(t *testing.T) {
	origMode := adjustMode
	origScale := score.LowBuyBreadthAdjScale
	t.Cleanup(func() {
		adjustMode = origMode
		score.LowBuyBreadthAdjScale = origScale
		os.Unsetenv("FREESTOCK_BREADTH_ADJ_SCALE")
	})

	adjustMode = cnlake.AdjHFQ
	os.Unsetenv("FREESTOCK_BREADTH_ADJ_SCALE")
	if got := breadthAdjScaleFromEnv(); got != 1.04 {
		t.Fatalf("复权默认应为 1.04，得到 %v", got)
	}
	os.Setenv("FREESTOCK_BREADTH_ADJ_SCALE", "1")
	if got := breadthAdjScaleFromEnv(); got != 1 {
		t.Fatalf("显式 1 应生效，得到 %v", got)
	}
	os.Setenv("FREESTOCK_BREADTH_ADJ_SCALE", "1.1")
	if got := breadthAdjScaleFromEnv(); got != 1.1 {
		t.Fatalf("显式 1.1 应生效，得到 %v", got)
	}
	os.Setenv("FREESTOCK_BREADTH_ADJ_SCALE", "abc")
	if got := breadthAdjScaleFromEnv(); got != 1.04 {
		t.Fatalf("非法值应回退 1.04，得到 %v", got)
	}

	// 原始价口径默认 1.0
	adjustMode = cnlake.AdjNone
	os.Unsetenv("FREESTOCK_BREADTH_ADJ_SCALE")
	if got := breadthAdjScaleFromEnv(); got != 1 {
		t.Fatalf("原始价默认应为 1.0，得到 %v", got)
	}

	// 档位阈值必须跟着系数走
	score.LowBuyBreadthAdjScale = 1.04
	if got := score.LowBuyPositionThreshold(1); got != 1040 {
		t.Fatalf("1.04 时第一档应为 1040，得到 %d", got)
	}
	score.LowBuyBreadthAdjScale = 1
	if got := score.LowBuyPositionThreshold(1); got != 1000 {
		t.Fatalf("1.0 时第一档应为 1000，得到 %d", got)
	}
}
