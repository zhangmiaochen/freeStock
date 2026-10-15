package service

import (
	"testing"

	"freestock/internal/cnlake"
)

// TestParallelEvaluationIsDeterministic 钉住"并发评估的结果与串行一致且可复现"。
//
// ## 为什么必须钉这一条
//
// 全市场评估的两遍（全市场行业基准、逐只评估）为了压进 10 秒预算改成了
// **并发**（见 valuationWorkers）。并发引入的风险不是"崩溃"，而是
// **结果不确定**：同样的输入两次跑出不同的排名、或者某些标的偶尔丢失。
// 这种问题在界面上表现为"数字偶尔跳一下"，极难发现也极难复现。
//
// 这条用例的做法是：**连跑两次全市场评估，逐字段比对**。
// 因为并发度是 8，两次运行的完成顺序必然不同 —— 只要有任何一处
// 依赖了迭代顺序（map 遍历顺序、channel 到达顺序），对比就会失败。
//
// 注意：这里刻意用 `refresh=true` 两次，跳过报告缓存，
// 让两次都真正走一遍并发的评估流程。
func TestParallelEvaluationIsDeterministic(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	svc, _ := newIsolatedService(t)

	// 用较小的范围让用例跑得快些；并发路径与 limit 无关
	const limit = 800

	first, err := svc.GetValuationReport(limit, true)
	if err != nil {
		t.Fatalf("第一次评估失败: %v", err)
	}
	second, err := svc.GetValuationReport(limit, true)
	if err != nil {
		t.Fatalf("第二次评估失败: %v", err)
	}

	if len(first.Rankings) != len(second.Rankings) {
		t.Fatalf("两次的期限档数不同：%d vs %d", len(first.Rankings), len(second.Rankings))
	}

	for index := range first.Rankings {
		a, b := first.Rankings[index], second.Rankings[index]
		if a.Horizon.Key != b.Horizon.Key {
			t.Errorf("第 %d 档的期限不同：%s vs %s", index, a.Horizon.Key, b.Horizon.Key)
			continue
		}
		if len(a.Rows) != len(b.Rows) {
			t.Errorf("%s：行数不同 %d vs %d —— 并发下有标的被丢了或重复",
				a.Horizon.Key, len(a.Rows), len(b.Rows))
			continue
		}
		for i := range a.Rows {
			x, y := a.Rows[i], b.Rows[i]
			if x.Code != y.Code {
				t.Errorf("%s 第 %d 行代码不同：%s vs %s（排名顺序不稳定）",
					a.Horizon.Key, i, x.Code, y.Code)
				break
			}
			if x.Score != y.Score || x.RawScore != y.RawScore {
				t.Errorf("%s 第 %d 行（%s）分数不同：%.6f/%.6f vs %.6f/%.6f",
					a.Horizon.Key, i, x.Code, x.Score, x.RawScore, y.Score, y.RawScore)
				break
			}
			if x.PremiumVsMedian != y.PremiumVsMedian {
				t.Errorf("%s 第 %d 行（%s）对行业中位不同：%.6f vs %.6f",
					a.Horizon.Key, i, x.Code, x.PremiumVsMedian, y.PremiumVsMedian)
				break
			}
		}
		t.Logf("%s：%d 行逐字段一致", a.Horizon.Key, len(a.Rows))
	}

	// 行业聚合也必须一致（它由并发构建的快照汇总而来）
	if len(first.Industries) != len(second.Industries) {
		t.Fatalf("行业数不同：%d vs %d", len(first.Industries), len(second.Industries))
	}
	industriesOf := make(map[string]float64, len(second.Industries))
	for i := range second.Industries {
		industriesOf[second.Industries[i].Code] = second.Industries[i].WeightedPE
	}
	mismatched := 0
	for i := range first.Industries {
		item := &first.Industries[i]
		other, ok := industriesOf[item.Code]
		if !ok {
			t.Errorf("第二次少了行业 %s", item.Code)
			continue
		}
		if item.WeightedPE != other {
			mismatched++
			if mismatched <= 3 {
				t.Errorf("行业 %s 的加权 PE 不同：%.6f vs %.6f", item.Code, item.WeightedPE, other)
			}
		}
	}
	if mismatched == 0 {
		t.Logf("行业 %d 个的加权 PE 全部一致", len(first.Industries))
	}

	// 覆盖计数也必须一致（skipped 是并发累加的原子里）
	if first.Snapshot.Evaluated != second.Snapshot.Evaluated {
		t.Errorf("评估只数不同：%d vs %d", first.Snapshot.Evaluated, second.Snapshot.Evaluated)
	}
	t.Logf("两次评估均覆盖 %d 只、行业 %d 个", first.Snapshot.Evaluated, len(first.Industries))
}
