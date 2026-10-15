package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"freestock/internal/cnlake"
)

// TestDerivedCacheRoundTrip 钉住派生快照的"写→读"闭环与失效判据。
//
// ⚠ 浮点比较用 almostEqual 而不是 `!=`：分红是"逐条派现累加"得来的，
// 而快照读回来之后**累加顺序变了**（写入时按 data.actionDetails 的 map 顺序、
// 读回时按分片里行的顺序）。浮点加法不满足结合律，所以两次的和会在
// 最低位不同 —— 实测 `0.8450 != 0.8450`，打印到 4 位看起来一样。
// 这类差异是**数学上正确**的，用容差比较；不要为了提高精度去改累加顺序，
// 那只是把不确定性藏起来。
func almostEqual(a, b, tolerance float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	scale := 1.0
	if abs := func(v float64) float64 {
		if v < 0 {
			return -v
		}
		return v
	}; abs(a) > scale {
		scale = abs(a)
	} else if abs(b) > scale {
		scale = abs(b)
	}
	return diff <= tolerance*scale
}

// 为什么必须有这条用例：派生快照是纯性能优化，一旦它悄悄失效，
// 用户看到的是"还是那么慢"而不是报错 —— 没有断言就会一直没人发现。
// 反过来，一旦它**失效判据错了**，用户看到的是"数据不更新"，
// 那是更严重的问题。两侧都要钉。
func TestDerivedCacheRoundTrip(t *testing.T) {
	lake := cnlake.Open("")
	if !lake.Available() {
		t.Skip("本机没有可用数据湖，跳过")
	}
	dir := valuationCacheDir(lake)
	if dir == "" {
		t.Fatal("valuationCacheDir 返回空 —— lake.Root() 没拿到路径，派生快照会永远写不出来")
	}
	t.Logf("派生快照目录: %s", dir)

	// 用临时目录做往返，不污染真实缓存
	tmp := filepath.Join(t.TempDir(), valuationCacheDirName)
	svc, _ := newIsolatedService(t)

	started := time.Now()
	data, err := svc.readValuationDataset(lake, nil)
	if err != nil {
		t.Fatalf("读原始分区失败: %v", err)
	}
	t.Logf("从数据湖原始分区读入 %s（行情 %d 只）",
		time.Since(started).Round(time.Millisecond), len(data.bars))

	// ① 写快照（**同步**，这样测试能看到结果；生产里是异步的）
	writeStarted := time.Now()
	if err := writeDerivedCache(tmp, data, data.industrySystem, time.Since(started)); err != nil {
		t.Fatalf("写派生快照失败: %v", err)
	}
	t.Logf("写派生快照 %s", time.Since(writeStarted).Round(time.Millisecond))

	// 文件都在
	// 文件都在（分片表 + 元数据）
	for _, name := range append(cacheTableFiles("bars", "statements", "shares", "actions"), "meta.json") {
		info, err := os.Stat(filepath.Join(tmp, name))
		if err != nil {
			t.Fatalf("缺文件 %s: %v", name, err)
		}
		t.Logf("   %-20s %8.1f MB", name, float64(info.Size())/1024/1024)
	}

	// ② 失效判定应当说"可用"
	latest, err := lake.LatestTradeDate()
	if err != nil {
		t.Fatalf("LatestTradeDate: %v", err)
	}
	_, usable, meta := derivedCacheUsableAt(tmp, latest)
	if !usable {
		t.Fatal("刚写完的快照应当判定为可用")
	}
	if meta.Bars == 0 || meta.Statements == 0 {
		t.Errorf("元数据行数为 0（bars=%d statements=%d）—— countCacheRows 没生效", meta.Bars, meta.Statements)
	}
	t.Logf("元数据：schema=%d 评估日=%d 行情=%d 财报=%d 股本=%d 公司行为=%d",
		meta.Schema, meta.EvalDate, meta.Bars, meta.Statements, meta.Shares, meta.Actions)

	// ③ 失效判定：数据湖更新了（模拟 evalDate 落后一天）必须判为不可用
	if _, usable, _ := derivedCacheUsableAt(tmp, latest+1); usable {
		t.Error("数据湖更新后（最新交易日更新）派生快照必须失效，否则用户会看到过期数据")
	}

	// ④ 失效判定：schema 变了必须判为不可用
	savedSchema := valuationCacheSchema
	if savedSchema != 1 {
		t.Logf("当前 schema 版本 %d", savedSchema)
	}
	// 直接改 meta.json 里的 schema，验证判据读的是文件里的值
	metaPath := filepath.Join(tmp, "meta.json")
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("读 meta.json 失败: %v", err)
	}
	tampered := strings.Replace(string(raw), `"schema": 1`, `"schema": 999`, 1)
	if tampered == string(raw) {
		// schema 字段名或格式变了，退回用结构体改写
		var rewritten cacheMeta
		if err := json.Unmarshal(raw, &rewritten); err != nil {
			t.Fatalf("解析 meta.json 失败: %v", err)
		}
		rewritten.Schema = 999
		out, _ := json.Marshal(rewritten)
		tampered = string(out)
	}
	if err := os.WriteFile(metaPath, []byte(tampered), 0o644); err != nil {
		t.Fatalf("写 meta.json 失败: %v", err)
	}
	if _, usable, _ := derivedCacheUsableAt(tmp, latest); usable {
		t.Error("schema 版本不符时必须判为不可用，否则会读到口径已变的旧数据")
	}
	// 恢复，供下面读回验证
	if err := os.WriteFile(metaPath, raw, 0o644); err != nil {
		t.Fatalf("恢复 meta.json 失败: %v", err)
	}
	if _, usable, _ := derivedCacheUsableAt(tmp, latest); !usable {
		t.Fatal("恢复后应重新判定为可用")
	}

	// ⑤ 读回来，内容必须与原始一致
	readStarted := time.Now()
	restored, err := readDerivedCache(lake, tmp, meta)
	if err != nil {
		t.Fatalf("读派生快照失败: %v", err)
	}
	t.Logf("读派生快照 %s", time.Since(readStarted).Round(time.Millisecond))

	if len(restored.bars) != len(data.bars) {
		t.Errorf("行情标的数 %d != 原始 %d", len(restored.bars), len(data.bars))
	}
	if len(restored.reports) != len(data.reports) {
		t.Errorf("财报标的数 %d != 原始 %d", len(restored.reports), len(data.reports))
	}
	if len(restored.shares) != len(data.shares) {
		t.Errorf("股本标的数 %d != 原始 %d", len(restored.shares), len(data.shares))
	}

	// 逐只抽验：行情根数与最新收盘价、最新报告期、股本、分红
	checked := 0
	for _, code := range []string{"600519", "000001", "300750", "601899"} {
		origBars, ok := data.bars[code]
		if !ok {
			continue
		}
		gotBars, ok := restored.bars[code]
		if !ok {
			t.Errorf("%s 在派生快照里丢了", code)
			continue
		}
		if len(gotBars) != len(origBars) {
			t.Errorf("%s 行情根数 %d != 原始 %d", code, len(gotBars), len(origBars))
		}
		last := origBars[len(origBars)-1]
		gotLast := gotBars[len(gotBars)-1]
		if gotLast.Close != last.Close || gotLast.Date() != last.Date() {
			t.Errorf("%s 末根行情不符：%.2f@%d vs %.2f@%d",
				code, gotLast.Close, gotLast.Date(), last.Close, last.Date())
		}
		if len(restored.reports[code]) != len(data.reports[code]) {
			t.Errorf("%s 报告期数 %d != 原始 %d",
				code, len(restored.reports[code]), len(data.reports[code]))
		}
		if len(restored.shares[code]) != len(data.shares[code]) {
			t.Errorf("%s 股本记录数 %d != 原始 %d",
				code, len(restored.shares[code]), len(data.shares[code]))
		}
		if !almostEqual(restored.dividendPaid[code], data.dividendPaid[code], 1e-9) {
			t.Errorf("%s 近 12 个月派息 %.6f != 原始 %.6f",
				code, restored.dividendPaid[code], data.dividendPaid[code])
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("没有抽验到任何标的 —— 抽验列表与数据湖不符")
	}
	t.Logf("逐只抽验 %d 只通过", checked)

	// ⑥ 参照数据也必须补上（名称 / 资产类型 / 行业归属不进快照，现读）
	if restored.names["600519"] == "" {
		t.Error("派生快照读回后缺少标的名称 —— fillReferenceData 没被调用")
	}
	if restored.industrySystem == "" || len(restored.industries) == 0 {
		t.Error("派生快照读回后缺少行业归属 —— fillReferenceData 没被调用")
	}
	t.Logf("参照数据：名称 %d 个、行业归属 %d 个、体系 %s",
		len(restored.names), len(restored.industries), restored.industrySystem)
}
