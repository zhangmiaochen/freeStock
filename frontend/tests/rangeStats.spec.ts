import { describe, expect, it } from "vitest";

import type { Candle } from "@/lib/indicators";
import {
  RANGE_METRIC_NOTES,
  calendarDaysBetween,
  rangeStats,
  selectionFromAxisValues,
} from "@/lib/rangeStats";

/** 造一根 K 线 */
function bar(
  date: number,
  open: number,
  close: number,
  high: number,
  low: number,
  volume = 1000,
  amount = 1_000_000,
): Candle {
  return { date, open, close, high, low, volume, amount };
}

/**
 * 一段 5 根的小行情（2026-01-05 ~ 01-09，周一~周五）：
 *
 *   i  date      open  close  high  low
 *   0  20260105  10.0  10.5   10.8  9.8
 *   1  20260106  10.5  11.0   11.2  10.4
 *   2  20260107  11.0  10.0   11.1   9.9     ← 高位回落
 *   3  20260108  10.0  9.5     10.2   9.2
 *   4  20260109  9.5   10.2    10.4   9.4
 */
const SERIES: Candle[] = [
  bar(20260105, 10.0, 10.5, 10.8, 9.8),
  bar(20260106, 10.5, 11.0, 11.2, 10.4),
  bar(20260107, 11.0, 10.0, 11.1, 9.9),
  bar(20260108, 10.0, 9.5, 10.2, 9.2),
  bar(20260109, 9.5, 10.2, 10.4, 9.4),
];

describe("框选区间统计", () => {
  it("区间涨跌幅用首根开盘价 → 末根收盘价", () => {
    // 整段：首开 10.0 → 末收 10.2 = +2%
    const all = rangeStats(SERIES, 0, 5)!;
    expect(all.count).toBe(5);
    expect(all.startPrice).toBe(10.0);
    expect(all.endPrice).toBe(10.2);
    expect(all.changePct).toBeCloseTo(2, 6);
    expect(all.changeAmount).toBeCloseTo(0.2, 6);

    // 只取第 2~5 根（下标 1..4）：11.0 之后一路到 10.2
    const part = rangeStats(SERIES, 1, 5)!;
    expect(part.count).toBe(4);
    expect(part.startPrice).toBe(10.5);
    expect(part.endPrice).toBe(10.2);
    expect(part.changePct).toBeCloseTo(((10.2 - 10.5) / 10.5) * 100, 6);
    expect(part.changePct).toBeLessThan(0);

    // 第 2~3 根（下标 1..2）：10.5 开 → 10.0 收
    const mid = rangeStats(SERIES, 1, 3)!;
    expect(mid.count).toBe(2);
    expect(mid.endPrice).toBe(10.0);
  });

  it("用首根开盘价而不是首根收盘价 —— 跳空不能丢", () => {
    // 单独造一根"高开"：开 10、收 12（当天 +20%）
    const gap = [bar(20260105, 10, 12, 12, 9.9), bar(20260106, 12, 13, 13, 11.8)];
    const stats = rangeStats(gap, 0, 2)!;
    // 首开 10 → 末收 13 = +30%；若按"首收 12"起算就只有 +8.33%
    expect(stats.changePct).toBeCloseTo(30, 6);
    expect(stats.changePct).not.toBeCloseTo(((13 - 12) / 12) * 100, 3);
  });

  it("最高/最低取区间内的极值，并给出与起点价的差与出现日期", () => {
    const stats = rangeStats(SERIES, 0, 5)!;
    expect(stats.high).toBe(11.2);
    expect(stats.highDate).toBe(20260106);
    expect(stats.low).toBe(9.2);
    expect(stats.lowDate).toBe(20260108);

    expect(stats.highPct).toBeCloseTo(((11.2 - 10) / 10) * 100, 6); // +12%
    expect(stats.lowPct).toBeCloseTo(((9.2 - 10) / 10) * 100, 6); // −8%
    // 振幅 = (高 − 低) / 首开
    expect(stats.amplitudePct).toBeCloseTo(((11.2 - 9.2) / 10) * 100, 6);
  });

  it("高低点的先后决定走势方向", () => {
    // 整段：高点在 i=1、低点在 i=3 → 先高后低 = 下跌结构
    expect(rangeStats(SERIES, 0, 5)!.direction).toBe("down");
    // 只取第 3~5 根（低 9.2 在前、高 10.4 在后）→ 上涨结构
    expect(rangeStats(SERIES, 3, 5)!.direction).toBe("up");
    // 只有一根有效数据之外的情况：高=低 → 横盘
    const flat = [bar(20260105, 10, 10, 10, 10), bar(20260106, 10, 10, 10, 10)];
    expect(rangeStats(flat, 0, 2)!.direction).toBe("flat");
  });

  it("最大回撤在收盘价序列上算（不用盘中最低价）", () => {
    const stats = rangeStats(SERIES, 0, 5)!;
    // 收盘序列：10.5 → 11.0 → 10.0 → 9.5 → 10.2
    // 峰值 11.0（i=1），谷底 9.5（i=3）→ 回撤 (11 − 9.5)/11 = 13.6364%
    expect(stats.maxDrawdownPct).toBeCloseTo(((11 - 9.5) / 11) * 100, 6);
    expect(stats.maxDrawdownDate).toBe(20260108);

    // 用盘中最低价会更大：(11.2 − 9.2)/11.2 = 17.857% —— 这正是要避免的虚高
    expect(stats.maxDrawdownPct).toBeLessThan(((11.2 - 9.2) / 11.2) * 100);
  });

  it("一路上涨时最大回撤为 0（不是负数）", () => {
    const up = [bar(20260105, 10, 11, 11, 9.9), bar(20260106, 11, 12, 12, 10.9)];
    const stats = rangeStats(up, 0, 2)!;
    expect(stats.maxDrawdownPct).toBe(0);
  });

  it("上涨/下跌天数与日均波动按逐日涨跌幅统计", () => {
    const stats = rangeStats(SERIES, 0, 5)!;
    // 逐日：首根用「开→收」= (10.5−10)/10 = +5%；之后 10.5→11 +4.7619%；
    // 11→10 −9.0909%；10→9.5 −5%；9.5→10.2 +7.3684%
    const expected = [5, 4.7619047619, -9.0909090909, -5, 7.3684210526];
    const absMean = expected.reduce((sum, v) => sum + Math.abs(v), 0) / expected.length;
    expect(stats.volatilityPct).toBeCloseTo(absMean, 4);
    expect(stats.upDays).toBe(3); // +5、+4.76、+7.37
    expect(stats.downDays).toBe(2); // −9.09、−5
  });

  it("第一天用「首开 → 首收」，所以首日的跳空不会被丢", () => {
    // 首根 10 开 11 收：如果不算这一天，涨跌天数就会少一天
    const data = [bar(20260105, 10, 11, 11, 9.9), bar(20260106, 11, 10.5, 11, 10.4)];
    const stats = rangeStats(data, 0, 2)!;
    expect(stats.upDays).toBe(1); // 第 1 天 +10%
    expect(stats.downDays).toBe(1); // 第 2 天 −4.55%
  });

  it("平盘既不算涨也不算跌", () => {
    const data = [
      bar(20260105, 10, 10, 10, 10),
      bar(20260106, 10, 10, 10, 10),
      bar(20260107, 10, 11, 11, 10),
    ];
    const stats = rangeStats(data, 0, 3)!;
    expect(stats.upDays).toBe(1);
    expect(stats.downDays).toBe(0);
  });

  it("累计成交额与成交量", () => {
    const data = [
      bar(20260105, 10, 10.5, 11, 9.8, 100, 1000),
      bar(20260106, 10.5, 11, 11.2, 10.4, 200, 2000),
    ];
    const stats = rangeStats(data, 0, 2)!;
    expect(stats.totalVolume).toBe(300);
    expect(stats.totalAmount).toBe(3000);
  });

  it("指数没有成交额：总额为 0，界面据此隐藏这一项", () => {
    const index = [
      bar(20260105, 3000, 3050, 3060, 2990, 0, 0),
      bar(20260106, 3050, 3100, 3110, 3040, 0, 0),
    ];
    const stats = rangeStats(index, 0, 2)!;
    expect(stats.totalAmount).toBe(0);
    expect(stats.changePct).toBeCloseTo(((3100 - 3000) / 3000) * 100, 6);
  });

  it("自然日跨度与交易日数分开给（春节这类长假会差很多）", () => {
    const stats = rangeStats(SERIES, 0, 5)!;
    expect(stats.startDate).toBe(20260105);
    expect(stats.endDate).toBe(20260109);
    expect(stats.count).toBe(5); // 交易日
    expect(stats.calendarDays).toBe(5); // 周一到周五，含首尾正好 5 天

    // 跨一个月的两个交易日：交易日 2 个、自然日 31 天
    const spread = [bar(20260101, 10, 10, 10, 10), bar(20260131, 10, 11, 11, 10)];
    const wide = rangeStats(spread, 0, 2)!;
    expect(wide.count).toBe(2);
    expect(wide.calendarDays).toBe(31);
  });

  it("少于 2 根返回 null（单根不构成区间涨跌）", () => {
    expect(rangeStats(SERIES, 0, 1)).toBeNull();
    expect(rangeStats(SERIES, 2, 3)).toBeNull();
    expect(rangeStats([], 0, 1)).toBeNull();
    // 空区间
    expect(rangeStats(SERIES, 3, 3)).toBeNull();
  });

  it("下标越界会被夹住，不会算出 NaN", () => {
    const stats = rangeStats(SERIES, -5, 999)!;
    expect(stats.count).toBe(5);
    expect(Number.isFinite(stats.changePct)).toBe(true);
    expect(Number.isFinite(stats.maxDrawdownPct)).toBe(true);
    expect(Number.isFinite(stats.volatilityPct)).toBe(true);

    // 起点越界、终点合法
    expect(rangeStats(SERIES, -3, 2)!.count).toBe(2);
  });

  it("区间退化（只有一根有效数据）时返回 null 而不是编数字", () => {
    // 中间那根 high/low 是 NaN → 有效数据只剩 1 根
    const dirty = [bar(20260105, 10, 10.5, 11, 9.8), { ...bar(20260106, 10, 10, 10, 10), high: Number.NaN }];
    expect(rangeStats(dirty, 0, 2)).toBeNull();
  });

  it("首根开盘价为 0（脏数据）时比例算 0 而不是 Infinity", () => {
    const zero = [bar(20260105, 0, 10, 10, 0), bar(20260106, 10, 11, 11, 10)];
    const stats = rangeStats(zero, 0, 2)!;
    expect(stats.changePct).toBe(0);
    expect(stats.amplitudePct).toBe(0);
    expect(Number.isFinite(stats.changePct)).toBe(true);
  });
});

describe("自然日跨度", () => {
  it("含首尾：同一天算 1 天", () => {
    expect(calendarDaysBetween(20260105, 20260105)).toBe(1);
    expect(calendarDaysBetween(20260105, 20260106)).toBe(2);
    expect(calendarDaysBetween(20260101, 20260131)).toBe(31);
  });

  it("跨月跨年都算对", () => {
    expect(calendarDaysBetween(20261231, 20270101)).toBe(2);
    // 2026 不是闰年
    expect(calendarDaysBetween(20260201, 20260301)).toBe(29);
  });

  it("非法日期返回 0，而不是 NaN", () => {
    expect(calendarDaysBetween(0, 20260105)).toBe(0);
    expect(calendarDaysBetween(20260105, 123)).toBe(0);
    // 2026-02-31 不存在
    expect(calendarDaysBetween(20260231, 20260301)).toBe(0);
    expect(calendarDaysBetween(20260105, 20261301)).toBe(0);
  });

  it("顺序颠倒时返回 0（而不是负数）", () => {
    expect(calendarDaysBetween(20260110, 20260105)).toBe(0);
  });
});

describe("刷选结果换算成下标区间", () => {
  it("正常情况：闭区间转成半开区间", () => {
    expect(selectionFromAxisValues(100, 10, 20)).toEqual({ from: 10, to: 21 });
    expect(selectionFromAxisValues(100, 0, 0)).toEqual({ from: 0, to: 1 });
  });

  it("从右往左框也一样（拖拽方向不该影响结果）", () => {
    expect(selectionFromAxisValues(100, 20, 10)).toEqual({ from: 10, to: 21 });
  });

  it("小数下标取最近的一根", () => {
    // 10.4 → 10、10.6 → 11
    expect(selectionFromAxisValues(100, 10.4, 20.4)).toEqual({ from: 10, to: 21 });
    expect(selectionFromAxisValues(100, 10.6, 20.6)).toEqual({ from: 11, to: 22 });
  });

  it("越界的下标被夹到数组范围内", () => {
    // 框得比数据还宽 → 就是全部
    expect(selectionFromAxisValues(10, -5, 999)).toEqual({ from: 0, to: 10 });
    // 两个端点都在数组右侧之外 → 一起塌到最后一根（用户意图是"最后那一段"），
    // 而不是因为 to 被夹到 9 而 from 还是 50 就误判成空区间
    expect(selectionFromAxisValues(10, 50, 60)).toEqual({ from: 9, to: 10 });
    // 左侧同理
    expect(selectionFromAxisValues(10, -60, -50)).toEqual({ from: 0, to: 1 });
    // 部分越界：左端在界内、右端越界
    expect(selectionFromAxisValues(10, 5, 60)).toEqual({ from: 5, to: 10 });
  });

  it("非法输入返回 null，不产出乱区间", () => {
    expect(selectionFromAxisValues(0, 0, 10)).toBeNull();
    expect(selectionFromAxisValues(-1, 0, 10)).toBeNull();
    expect(selectionFromAxisValues(100, Number.NaN, 10)).toBeNull();
    expect(selectionFromAxisValues(100, 10, Number.POSITIVE_INFINITY)).toBeNull();
  });
});

describe("口径说明文案", () => {
  // 这几句话是用户判断数字含义的唯一依据，写含糊了数字就会被误读，
  // 所以把它们也钉住（改了文案就必须同步改这里，等于强制复核一遍口径）。
  it("每个可解释的指标都有一条口径说明，且指到由谁算的", () => {
    const keys = RANGE_METRIC_NOTES.map((item) => item.key);
    expect(keys).toContain("changePct");
    expect(keys).toContain("maxDrawdownPct");
    expect(keys).toContain("amplitudePct");
    expect(keys).toContain("volatilityPct");

    const change = RANGE_METRIC_NOTES.find((item) => item.key === "changePct")!;
    expect(change.note).toContain("开盘");
    expect(change.note).toContain("收盘");

    const drawdown = RANGE_METRIC_NOTES.find((item) => item.key === "maxDrawdownPct")!;
    expect(drawdown.note).toContain("收盘价");

    const volatility = RANGE_METRIC_NOTES.find((item) => item.key === "volatilityPct")!;
    expect(volatility.note).toContain("未年化");
  });

  it("每一项都有标签，不会出现空白标题", () => {
    for (const item of RANGE_METRIC_NOTES) {
      expect(item.label.length).toBeGreaterThan(0);
      expect(item.note.length).toBeGreaterThan(0);
    }
  });
});
