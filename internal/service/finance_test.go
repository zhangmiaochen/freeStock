package service

import (
	"testing"

	"freestock/internal/stockdb"
)

func TestDeriveFundamentals(t *testing.T) {
	// 收盘价 100、PE 20、PB 4、总市值 100 亿
	quote := &stockdb.Quote{Code: "600000", Name: "测试股", Close: 100, PeTTM: 20, PB: 4, TotalMV: 100e8}
	d := deriveFundamentals(quote)

	// ROE = EPS / BPS = (100/20) / (100/4) = 5 / 25 = 20%
	if d.ROE != 20 {
		t.Errorf("ROE = %v，期望 20", d.ROE)
	}
	if d.EPS != 5 {
		t.Errorf("EPS = %v，期望 5", d.EPS)
	}
	if d.BPS != 25 {
		t.Errorf("BPS = %v，期望 25", d.BPS)
	}
	if d.NetProfitTTM != 5e8 {
		t.Errorf("净利润 = %v，期望 5e8", d.NetProfitTTM)
	}
	if d.NetAssets != 25e8 {
		t.Errorf("净资产 = %v，期望 25e8", d.NetAssets)
	}
	// 换一个价格，ROE 不应该变（ROE = PB/PE 与股价无关）
	quote.Close = 250
	if again := deriveFundamentals(quote); again.ROE != 20 {
		t.Errorf("股价变化后 ROE 应保持不变，得到 %v", again.ROE)
	}
}

func TestDeriveFundamentalsMissingData(t *testing.T) {
	// PE 为 0（亏损或数据缺失）时不应算出无穷大
	d := deriveFundamentals(&stockdb.Quote{Close: 10, PeTTM: 0, PB: 2, TotalMV: 1e10})
	if d.ROE != 0 || d.NetProfitTTM != 0 {
		t.Errorf("数据缺失时应返回 0，得到 %+v", d)
	}
}

func TestGetFinanceSnapshotAgainstLocalServer(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	snapshot, err := svc.GetFinanceSnapshot("600519")
	if err != nil {
		t.Fatalf("GetFinanceSnapshot 失败: %v", err)
	}

	t.Logf("代码=%s 名称=%s 交易日=%d 收盘=%.2f", snapshot.Code, snapshot.Name, snapshot.TradeDate, snapshot.Close)
	t.Logf("派生指标: ROE=%.2f%% EPS=%.4f BPS=%.4f 净利润TTM=%.1f亿 净资产=%.1f亿",
		snapshot.Derived.ROE, snapshot.Derived.EPS, snapshot.Derived.BPS,
		snapshot.Derived.NetProfitTTM/1e8, snapshot.Derived.NetAssets/1e8)
	t.Logf("分红: 股息率=%.2f%% 近12月每股派息=%.4f 历史次数=%d",
		snapshot.DividendYield, snapshot.DividendTTM, snapshot.DividendCount)
	if snapshot.MoneyFlow != nil {
		mf := snapshot.MoneyFlow
		t.Logf("资金流(%d, 共%d天): 主力净流入=%.1f万 5日=%.1f万 20日=%.1f万 超大单=%.1f万",
			mf.Date, mf.Days, mf.MainNet/1e4, mf.MainNet5/1e4, mf.MainNet20/1e4, mf.JumboNet/1e4)
	}

	// 贵州茅台的 ROE 长期在 25%~40% 区间
	if snapshot.Derived.ROE < 20 || snapshot.Derived.ROE > 45 {
		t.Errorf("茅台 ROE = %.2f%%，超出合理区间", snapshot.Derived.ROE)
	}
	if snapshot.Derived.NetAssets <= 0 || snapshot.Derived.NetProfitTTM <= 0 {
		t.Error("推导出的净资产 / 净利润应为正")
	}
	if snapshot.DividendYield <= 0 {
		t.Error("股息率应大于 0")
	}

	// 资金流：本机数据集里 600519 应该是有数据的
	if snapshot.MoneyFlow == nil {
		t.Fatal("没有取到资金流数据")
	}
	if snapshot.MoneyFlow.Days == 0 {
		t.Error("资金流天数为 0")
	}
	if snapshot.MoneyFlow.Date != snapshot.TradeDate {
		t.Logf("提示：资金流最新日期 %d 与交易日 %d 不同", snapshot.MoneyFlow.Date, snapshot.TradeDate)
	}

	// 数据可用性说明
	byKey := map[string]FinanceSourceStatus{}
	for _, item := range snapshot.Sources {
		byKey[item.Key] = item
	}
	for _, key := range []string{"derived", "dividend", "money_flow", "statement"} {
		if _, ok := byKey[key]; !ok {
			t.Errorf("缺少数据源说明 %q", key)
		}
	}
	for _, key := range []string{"derived", "dividend", "money_flow"} {
		if byKey[key].Available != true {
			t.Errorf("%q 应为可用", key)
		}
	}
	// 财报是否可得取决于本机有没有 cnequity 数据湖、以及财报是否已入库，
	// 所以这里不写死真假，只要求「必须有解释」，且解释与实际状态一致。
	if byKey["statement"].Detail == "" && byKey["statement"].Fallback == "" {
		t.Error("财报数据源必须给出说明（可用时说明来源，不可用时说明怎么补）")
	}
	if !byKey["statement"].Available && byKey["statement"].Fallback == "" {
		t.Error("财报不可用时应给出补充途径")
	}
}

func TestFinanceSnapshotStatementStatus(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	snapshot, err := svc.GetFinanceSnapshot("600519")
	if err != nil {
		t.Fatalf("GetFinanceSnapshot 失败: %v", err)
	}

	st := snapshot.Statements
	t.Logf("财报来源=%q 可用=%v 数据湖可用=%v 根目录=%q", st.Source, st.Available, st.LakeAvailable, st.LakeRoot)
	t.Logf("说明: %s", st.Detail)
	for _, item := range st.Datasets {
		t.Logf("  %-30s exists=%-5v rows=%-8d partitions=%d", item.Name, item.Exists, item.Rows, len(item.Partitions))
	}

	if st.Detail == "" {
		t.Error("无论可不可用，都必须能解释清楚状态")
	}
	if st.LakeAvailable && st.LakeRoot == "" {
		t.Error("数据湖可用时根目录不该为空")
	}
	// 可用与不可用应当和 items 是否拿到保持一致
	if st.Available && st.Source == "" {
		t.Error("财报可用时应标明来源")
	}
}

func TestGetFinancialStatementsDoesNotFail(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	// 无论数据湖在不在、财报入没入库，这个接口都不应该报错
	result, err := svc.GetFinancialStatements("600519", 50)
	if err != nil {
		t.Fatalf("GetFinancialStatements 不该报错: %v", err)
	}
	if result.Detail == "" {
		t.Error("应给出状态说明")
	}
	t.Logf("可用=%v 科目数=%d 报告期=%v", result.Available, len(result.Items), result.ReportPeriods)
	if result.Available && len(result.Items) == 0 {
		t.Error("标为可用却没有科目")
	}
}

func TestDataLakeStatusAndRoot(t *testing.T) {
	svc := newLocalService()

	info := svc.GetDataLakeStatus()
	t.Logf("数据湖: root=%q available=%v datasets=%v", info.Root, info.Available, info.Datasets)
	if info.Detail == "" {
		t.Error("应给出状态说明")
	}

	// 指向一个不存在的位置，应转为不可用而不是 panic
	broken := svc.SetDataLakeRoot(`Z:\definitely\not\here`)
	if broken.Available {
		t.Error("不存在的路径不该判定为可用")
	}
	if broken.Detail == "" {
		t.Error("不可用时也要说明怎么办")
	}

	// 用环境变量能探测到的真实位置恢复（本机没有数据湖就跳过）
	t.Setenv("FREESTOCK_CNLAKE", "")
	restored := svc.SetDataLakeRoot("")
	t.Logf("恢复自动探测: root=%q available=%v", restored.Root, restored.Available)
	if !restored.Available {
		t.Skip("本机没有数据湖，后续断言跳过")
	}
	if len(restored.Datasets) == 0 {
		t.Error("数据湖可用时应有数据集")
	}
}

// 资金流表里应该有历史区间数据
func TestMoneyFlowRangeAgainstLocalServer(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	client, _ := svc.snapshot()
	flows, err := client.MoneyFlowRange(t.Context(), "600519", 20260101, 20260930)
	if err != nil {
		t.Fatalf("MoneyFlowRange 失败: %v", err)
	}
	if len(flows) < 100 {
		t.Fatalf("资金流记录偏少: %d", len(flows))
	}
	// 必须按日期升序
	for i := 1; i < len(flows); i++ {
		if flows[i].Date <= flows[i-1].Date {
			t.Fatalf("资金流未按日期升序: %d <= %d", flows[i].Date, flows[i-1].Date)
		}
	}
	last := flows[len(flows)-1]
	t.Logf("资金流共 %d 条，%d ~ %d；最新主力净流入 %.2f 万",
		len(flows), flows[0].Date, last.Date, last.MainNet/1e4)
	if last.MainNet == 0 && last.JumboNet == 0 && last.BigNet == 0 {
		t.Error("最新一条资金流全为 0，字段可能解析错了")
	}
}

// 没有资金流记录的股票不应报错，只是返回空
func TestMoneyFlowRangeMissingCode(t *testing.T) {
	svc := newLocalService()
	requireLocalServer(t, svc)

	client, _ := svc.snapshot()
	flows, err := client.MoneyFlowRange(t.Context(), "999999", 20260101, 20260930)
	if err != nil {
		t.Fatalf("不存在的代码不应报错: %v", err)
	}
	if len(flows) != 0 {
		t.Errorf("不存在的代码应返回空，得到 %d 条", len(flows))
	}
}
