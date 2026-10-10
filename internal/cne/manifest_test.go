//go:build windows

package cne

import (
	"os"
	"testing"
)

// **用真实的 manifest.db 验证** —— 只看 schema 不够，要看真数据。
//
// 方案 §2.1 的核心断言是"退出码不能当成功判据"，所以要看清：
// 一次运行里各数据集分别是什么状态、有没有"进程失败但多数数据集成功"的情形。
func TestManifestRealData(t *testing.T) {
	const dir = `D:\workspace\stockTool\CNEquity`
	if _, err := os.Stat(ManifestPath(dir)); err != nil {
		t.Skipf("找不到 manifest.db：%v", err)
	}

	runs, err := RecentRuns(dir, 8)
	if err != nil {
		t.Fatalf("读运行记录失败：%v", err)
	}
	if len(runs) == 0 {
		t.Skip("manifest 里还没有运行记录")
	}
	t.Logf("最近 %d 次运行：", len(runs))
	for _, r := range runs {
		t.Logf("  %-28s %-10s 写 %8d 行  执行=%s 覆盖=%s  %s ~ %s",
			r.RunID, r.Status, r.RowsWritten, r.ExecutionStatus, r.CoverageStatus,
			r.StartedAt, r.FinishedAt)
	}

	detail, err := LatestRun(dir)
	if err != nil {
		t.Fatalf("读最近一次运行详情失败：%v", err)
	}
	if detail == nil {
		t.Skip("没有可读的运行详情")
	}
	t.Logf("")
	t.Logf("最近一次运行 %s（进程状态 %s）", detail.Run.RunID, detail.Run.Status)
	for _, d := range detail.Datasets {
		t.Logf("  %-26s %-18s 写 %7d 行  可用=%v %s",
			d.Dataset, d.Status, d.RowsWritten, d.UsableResult,
			firstNonEmpty(d.ReasonCode, d.ErrorCode))
	}
	t.Logf("成功=%d 失败=%d 上游无数据=%d 共写 %d 行",
		len(detail.Succeeded), len(detail.Failed), len(detail.Unavailable), detail.RowsWritten)
	t.Logf("结论：%s", detail.Conclusion)

	// 硬断言：结论里必须能看出**数据集粒度**，不能只有进程状态
	if detail.Conclusion == "" {
		t.Error("必须给出结论")
	}
	if len(detail.Datasets) > 0 && detail.RowsWritten == 0 &&
		len(detail.Succeeded) > 0 {
		t.Error("有数据集被算作成功，却一行都没写 —— 成功判据写错了")
	}
	// "写了行"与"算成功"必须一致
	for _, name := range detail.Succeeded {
		for _, d := range detail.Datasets {
			if d.Dataset == name && !d.WroteRows() {
				t.Errorf("%s 被算作成功但 rows_written=0", name)
			}
		}
	}
}

// 上游不可用的判定：方案 §9 要求这类**不显示"可重试"**，
// 而是说明"明天会自动重试"。
func TestIsUpstreamUnavailable(t *testing.T) {
	yes := []DatasetResult{
		{Status: "source unavailable"},
		{ReasonCode: "SOURCE_UNAVAILABLE"},
		{ErrorCode: "no_data"},
		{ErrorMessage: "upstream returned nothing"},
		{Status: "skipped", ReasonCode: "provider_unavailable"},
		{Status: "failed", ReasonCode: "source_transient"},       // 实测：上游暂时不可用
		{Status: "failed", ReasonCode: "source_payload_invalid"}, // 实测：上游数据有问题
	}
	no := []DatasetResult{
		{Status: "failed", ErrorCode: "E_TIMEOUT", ErrorMessage: "request timed out"},
		{Status: "failed", ErrorCode: "E_AUTH"},
		{Status: "success"},
	}
	for _, d := range yes {
		if !d.IsUpstreamUnavailable() {
			t.Errorf("应判为上游不可用：%+v", d)
		}
	}
	for _, d := range no {
		if d.IsUpstreamUnavailable() {
			t.Errorf("**不该**判为上游不可用（会被误显示成'明天重试'）：%+v", d)
		}
	}
}

// 结论措辞必须把"上游无数据"和"真失败"分开说 ——
// 前者明天会自动重试，后者需要人管。
func TestConclusionSeparatesUnavailableFromFailed(t *testing.T) {
	detail := &RunDetail{
		Run: RunSummary{RunID: "r1", Status: "failed"},
		Datasets: []DatasetResult{
			{Dataset: "hot_rank", Status: "success", RowsWritten: 100},
			{Dataset: "fund_flow", Status: "failed", ErrorCode: "E_TIMEOUT", ErrorMessage: "timed out"},
			{Dataset: "economic_calendar", Status: "source unavailable"},
		},
		Succeeded:   []string{"hot_rank"},
		Failed:      []string{"fund_flow"},
		Unavailable: []string{"economic_calendar"},
		RowsWritten: 100,
	}
	got := conclude(detail)
	for _, want := range []string{"3 个数据集", "100 行", "fund_flow", "economic_calendar", "明天会自动重试"} {
		if !contains(got, want) {
			t.Errorf("结论里缺少 %q：%s", want, got)
		}
	}
	// 进程状态要单独标注，避免被当成数据结论
	if !contains(got, "进程状态") {
		t.Errorf("应把进程状态单独说明，避免与数据结论混淆：%s", got)
	}
	// 不能简单说"成功"
	if got == "成功" || contains(got, "全部成功") {
		t.Errorf("**不能只凭进程状态给结论**：%s", got)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
