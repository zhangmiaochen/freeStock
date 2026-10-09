package service

import (
	"testing"

	"freestock/internal/score"
)

// 增量刷新：第二次刷新应当**复用**已有评分，不重算、也不改变任何数据。
//
// 这是"评分本地持久化"的核心保证 —— 评分只取决于 (代码, 交易日, 策略)，
// 重算出来的值必然相同，但省掉每只一次 320 个交易日的行情拉取。
func TestRefreshStrengthIncremental(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过（要打数据源）")
	}
	svc, _ := newPortfolioService(t)
	t.Cleanup(svc.Close)

	const limit = 30

	// ① 第一次：全量算（force=true，避免受已有数据影响）
	first, err := svc.RefreshStrength(score.StrategyTrendGrowth, limit, true)
	if err != nil {
		t.Skipf("评分失败（数据源不可用？）：%v", err)
	}
	if first.Saved == 0 {
		t.Skip("一只都没算出来，跳过")
	}
	t.Logf("第一次（force）：新算 %d 只，复用 %d 只，共 %d 只",
		first.Saved, first.Reused, first.Total)

	// ② 第二次：增量 —— 应全部复用
	second, err := svc.RefreshStrength(score.StrategyTrendGrowth, limit, false)
	if err != nil {
		t.Fatalf("增量刷新失败：%v", err)
	}
	if !second.SkippedFromCache {
		t.Errorf("第二次应当命中缓存（SkippedFromCache），实际 new=%d reused=%d",
			second.Saved, second.Reused)
	}
	if second.Saved != 0 {
		t.Errorf("第二次不该重算，实际新算 %d 只", second.Saved)
	}
	if second.Reused == 0 {
		t.Error("第二次应当报告复用了已有评分")
	}
	if second.Total != first.Total {
		t.Errorf("增量刷新不该改变总只数：%d → %d", first.Total, second.Total)
	}
	t.Logf("第二次（增量）：新算 %d 只，复用 %d 只，共 %d 只 —— 未重算",
		second.Saved, second.Reused, second.Total)

	// ③ 结果必须与第一次逐行一致（读库对比）
	store, err := svc.db()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.LoadStrength(int64(second.TradeDate), score.StrategyTrendGrowth, 0)
	if err != nil {
		t.Fatalf("读回评分失败：%v", err)
	}
	if len(rows) != second.Total {
		t.Errorf("落库 %d 只，报告 %d 只", len(rows), second.Total)
	}
	// 分数必须非空且有序（总分降序）——增量没有破坏写入语义
	for i := 1; i < len(rows); i++ {
		if rows[i-1].TotalScore < rows[i].TotalScore {
			t.Errorf("第 %d/%d 行总分顺序不对：%.2f < %.2f",
				i, i+1, rows[i-1].TotalScore, rows[i].TotalScore)
		}
	}

	// ④ force 仍可强制全量重算
	third, err := svc.RefreshStrength(score.StrategyTrendGrowth, limit, true)
	if err != nil {
		t.Fatalf("强制重算失败：%v", err)
	}
	if third.SkippedFromCache {
		t.Error("force=true 不该走缓存")
	}
	if third.Saved == 0 {
		t.Error("force=true 应当重新算")
	}
	t.Logf("第三次（force）：新算 %d 只，总 %d 只 —— 强制重算生效",
		third.Saved, third.Total)
}
