package stockdb

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// **批量取数实验**：stockdb 的 `k1` 是否支持一次拿多个代码。
//
// 背景：全市场评分/回填的时间几乎全花在"每只股票一次 HTTP 往返"上
// （实测全市场约 7563 次、每只 2.7~5ms，合计 21~33 秒）。
// 如果 `k1` 能一次带多只，取数就能从"每只一次"变成"每批一次"。
//
// free-stockdb 的 README 说 `rd.get/keys/vals()` 支持
// "区间a>b，正则*，排序，截取[:num]，遍历，类SQL查询"，
// 但具体语法没有文档 —— 所以这里**逐个试**并核对返回的代码数。
//
// 只在 stockdb 服务可用时跑；设 FREESTOCK_BATCH_PROBE=1 开启。
func TestStockdbBatchProbe(t *testing.T) {
	if os.Getenv("FREESTOCK_BATCH_PROBE") != "1" {
		t.Skip("会向 stockdb 发请求：设 FREESTOCK_BATCH_PROBE=1 才跑")
	}
	client := NewClient("http://127.0.0.1:7899")
	ctx := context.Background()

	// 先确认服务可用
	if _, err := client.StockCodes(ctx); err != nil {
		t.Skipf("stockdb 不可用：%v", err)
	}

	// 取一批真实代码（StockCodes 返回 市场 -> 代码列表）
	buckets, err := client.StockCodes(ctx)
	if err != nil || len(buckets) == 0 {
		t.Skipf("拿不到代码表：%v", err)
	}
	all := make([]string, 0, 8192)
	for _, list := range buckets {
		all = append(all, list...)
	}
	if len(all) == 0 {
		t.Skip("代码表为空")
	}
	sort.Strings(all)
	sample := all[:min(200, len(all))]
	t.Logf("代码总数 %d，样本 %d 只：%s …", len(all), len(sample), strings.Join(sample[:min(5, len(sample))], ","))

	// countCodes 数出响应里出现了几个**不同的**一级键（代码）
	countCodes := func(raw any) (codes []string, rows int) {
		items, _ := ToSlice(raw)
		seen := map[string]bool{}
		for _, item := range items {
			rows++
			m, ok := ToMap(item)
			if !ok {
				continue
			}
			for _, key := range []string{"code", "ts_code", "symbol", "代码"} {
				if value, ok := m[key]; ok {
					if s := strings.TrimSpace(fmt.Sprint(value)); s != "" {
						seen[s] = true
					}
				}
			}
		}
		for code := range seen {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		return codes, rows
	}

	const k2 = "fwd:20261001,20261009"

	// ① 基线：单只
	began := time.Now()
	raw, err := client.Vals(ctx, TableDailyK, "key:"+sample[0], k2)
	if err != nil {
		t.Fatalf("单只查询失败：%v", err)
	}
	single := time.Since(began)
	singleCodes, singleRows := countCodes(raw)
	t.Logf("① 单只 key:%s → %s，%d 行，识别到 %d 个代码 %v",
		sample[0], single.Round(time.Millisecond), singleRows, len(singleCodes), singleCodes)

	// ② 逗号分隔（PowerShell 实测这条路能通）
	for _, n := range []int{10, 10, 10, 20, 20, 20, 40, 40, 40} {
		if n > len(sample) {
			break
		}
		batch := sample[:n]
		k1 := "key:" + strings.Join(batch, ",")
		began = time.Now()
		raw, err := client.Vals(ctx, TableDailyK, k1, k2)
		elapsed := time.Since(began)
		if err != nil {
			t.Logf("② 逗号 %3d 只 → 失败：%v", n, err)
			continue
		}
		codes, rows := countCodes(raw)
		present := map[string]bool{}
		for _, c := range codes {
			present[c] = true
		}
		missing := []string{}
		for _, c := range batch {
			if !present[c] {
				missing = append(missing, c)
			}
		}
		t.Logf("② 逗号 %3d 只 → %s，%d 行，识别到 %d 个代码，缺 %d 只 %v",
			n, elapsed.Round(time.Millisecond), rows, len(codes), len(missing), missing)
		if n == 100 && len(codes) > 1 {
			t.Logf("   ★ **批量成功**：一次请求拿到 %d 只（%s/只）",
				len(codes), (elapsed / time.Duration(len(codes))).Round(time.Microsecond))
		}
	}

	// ③ 试试 README 提到的其他表达式语法
	for _, k1 := range []string{
		"key:" + sample[0] + ">",                    // 区间 a>b
		"key:" + sample[0] + "|" + sample[1],        // 竖线
		"key:" + sample[0] + " " + sample[1],        // 空格
		"key:[" + sample[0] + "," + sample[1] + "]", // 方括号
		"key:" + sample[0][:4] + "**",               // 双星
		"key:" + sample[0][:4] + "*",                // 前缀星
	} {
		raw, err := client.Vals(ctx, TableDailyK, k1, k2)
		if err != nil {
			t.Logf("③ %-30q → 失败：%v", k1, err)
			continue
		}
		codes, rows := countCodes(raw)
		t.Logf("③ %-30q → %d 行，%d 个代码", k1, rows, len(codes))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
