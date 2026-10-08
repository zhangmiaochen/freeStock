import { describe, expect, it } from "vitest";

import {
  aggregate,
  aggregateFlow,
  boll,
  ema,
  macd,
  rollingMean,
  sma,
  type Candle,
  type MoneyFlowPoint,
} from "@/lib/indicators";

function candle(date: number, open: number, high: number, low: number, close: number, volume = 100): Candle {
  return { date, open, high, low, close, volume, amount: volume * close };
}

function flow(date: number, mainNet: number): MoneyFlowPoint {
  return { date, mainNet, jumboNet: 0, bigNet: 0, midNet: 0, smallNet: 0 };
}

describe("sma", () => {
  it("前 n-1 个为 null，之后是滑动平均", () => {
    expect(sma([1, 2, 3, 4, 5], 2)).toEqual([null, 1.5, 2.5, 3.5, 4.5]);
    expect(sma([2, 4, 6, 8], 4)).toEqual([null, null, null, 5]);
  });

  it("周期大于样本数时全为 null", () => {
    expect(sma([1, 2], 5)).toEqual([null, null]);
  });
});

describe("ema", () => {
  it("首值等于第一个样本，并单调逼近", () => {
    const out = ema([10, 10, 10, 10], 3);
    expect(out).toEqual([10, 10, 10, 10]);
  });

  it("上涨序列的 EMA 小于最新价但大于起始价", () => {
    const out = ema([1, 2, 3, 4, 5], 3);
    expect(out[0]).toBe(1);
    expect(out[4]).toBeGreaterThan(1);
    expect(out[4]).toBeLessThan(5);
  });
});

describe("boll", () => {
  it("中轨等于同周期均线，上下轨关于中轨对称", () => {
    const closes = [10, 11, 12, 13, 14, 15, 16];
    const result = boll(closes, 3, 2);
    const ma = sma(closes, 3);

    expect(result.mid).toEqual(ma);
    for (let i = 2; i < closes.length; i++) {
      const mid = result.mid[i]!;
      const upper = result.upper[i]!;
      const lower = result.lower[i]!;
      expect(upper).toBeGreaterThan(mid);
      expect(lower).toBeLessThan(mid);
      expect(upper - mid).toBeCloseTo(mid - lower, 10);
    }
    // 常数序列标准差为 0
    const flat = boll([5, 5, 5, 5], 3, 2);
    expect(flat.upper[3]).toBeCloseTo(5, 10);
    expect(flat.lower[3]).toBeCloseTo(5, 10);
  });
});

describe("macd", () => {
  it("DIF = EMA12 - EMA26，柱 = 2 x (DIF - DEA)", () => {
    const closes = Array.from({ length: 60 }, (_, i) => 100 + Math.sin(i / 5) * 10);
    const result = macd(closes);

    expect(result.dif).toHaveLength(closes.length);
    expect(result.dea).toHaveLength(closes.length);
    expect(result.bar).toHaveLength(closes.length);

    const emaFast = ema(closes, 12);
    const emaSlow = ema(closes, 26);
    for (let i = 0; i < closes.length; i++) {
      expect(result.dif[i]).toBeCloseTo(emaFast[i] - emaSlow[i], 10);
      expect(result.bar[i]).toBeCloseTo((result.dif[i] - result.dea[i]) * 2, 10);
    }
  });

  it("单边上涨时 DIF 为正", () => {
    const closes = Array.from({ length: 80 }, (_, i) => 10 + i);
    const result = macd(closes);
    expect(result.dif[79]).toBeGreaterThan(0);
  });
});

describe("aggregate", () => {
  const daily: Candle[] = [
    candle(20240101, 10, 11, 9, 10),
    candle(20240102, 10, 12, 10, 11),
    candle(20240105, 11, 13, 11, 12),
    candle(20240108, 12, 14, 12, 13),
    candle(20240112, 13, 15, 12, 14, 200),
  ];

  it("day 周期原样返回", () => {
    expect(aggregate(daily, "day")).toBe(daily);
  });

  it("周线按 ISO 周分组（2024-01-01 是 2024 第 1 周）", () => {
    const weekly = aggregate(daily, "week");
    expect(weekly).toHaveLength(2);

    expect(weekly[0]).toEqual({ date: 20240105, open: 10, high: 13, low: 9, close: 12, volume: 300, amount: 100 * 10 + 100 * 11 + 100 * 12 });
    expect(weekly[1]).toEqual({ date: 20240112, open: 12, high: 15, low: 12, close: 14, volume: 300, amount: 100 * 13 + 200 * 14 });
  });

  it("跨年时 ISO 周不会混到一起", () => {
    const crossYear = [
      candle(20231229, 10, 11, 9, 10),
      candle(20240101, 20, 21, 19, 20),
    ];
    const weekly = aggregate(crossYear, "week");
    expect(weekly).toHaveLength(2);
    expect(weekly[0].date).toBe(20231229);
    expect(weekly[1].date).toBe(20240101);
  });

  it("月线按自然月分组", () => {
    const monthly = aggregate(daily, "month");
    expect(monthly).toHaveLength(1);
    expect(monthly[0]).toEqual({
      date: 20240112,
      open: 10,
      high: 15,
      low: 9,
      close: 14,
      volume: 600,
      amount: 100 * 10 + 100 * 11 + 100 * 12 + 100 * 13 + 200 * 14,
    });
  });

  it("年线按自然年分组", () => {
    const yearly = aggregate(daily, "year");
    expect(yearly).toHaveLength(1);
    expect(yearly[0].open).toBe(10);
    expect(yearly[0].close).toBe(14);
    expect(yearly[0].high).toBe(15);
    expect(yearly[0].low).toBe(9);
  });

  it("空数组不报错", () => {
    expect(aggregate([], "week")).toEqual([]);
  });
});

describe("aggregateFlow", () => {
  // 2024-01-08(一) ~ 01-12(五) 为同一 ISO 周
  const daily = [
    candle(20240108, 10, 11, 9, 10.5),
    candle(20240109, 10.5, 12, 10, 11),
    candle(20240110, 11, 13, 10.5, 12),
    candle(20240111, 12, 14, 11, 13),
    candle(20240112, 13, 15, 12, 14),
  ];

  it("日周期按日期逐点对齐", () => {
    const flows = daily.map((c, i) => flow(c.date, (i + 1) * 100));
    expect(aggregateFlow(daily, flows, "day")).toEqual([100, 200, 300, 400, 500]);
  });

  it("周周期把区间内的资金流求和（流量不能取最后一根）", () => {
    const flows = daily.map((c, i) => flow(c.date, (i + 1) * 100));
    const weekly = aggregateFlow(daily, flows, "week");
    // 与图表蜡烛数一致
    expect(weekly).toHaveLength(aggregate(daily, "week").length);
    expect(weekly).toEqual([1500]);
  });

  it("月/年周期同样求和", () => {
    const flows = daily.map((c, i) => flow(c.date, (i + 1) * 100));
    expect(aggregateFlow(daily, flows, "month")).toEqual([1500]);
    expect(aggregateFlow(daily, flows, "year")).toEqual([1500]);
  });

  it("缺少资金流的交易日为 null，不影响其它周期", () => {
    // 只给第 1、3、5 天数据
    const flows = [flow(20240108, 100), flow(20240110, 300), flow(20240112, 500)];
    expect(aggregateFlow(daily, flows, "day")).toEqual([100, null, 300, null, 500]);
    // 周线仍然求和（900），不会因为中间有空洞就变 null
    expect(aggregateFlow(daily, flows, "week")).toEqual([900]);
  });

  it("完全没有资金流时全是 null", () => {
    expect(aggregateFlow(daily, [], "day")).toEqual([null, null, null, null, null]);
    expect(aggregateFlow(daily, [], "week")).toEqual([null]);
  });

  it("可以取其它口径（超大单 / 大单）", () => {
    const flows: MoneyFlowPoint[] = [
      { date: 20240108, mainNet: 1, jumboNet: 10, bigNet: 100, midNet: 0, smallNet: 0 },
      { date: 20240109, mainNet: 2, jumboNet: 20, bigNet: 200, midNet: 0, smallNet: 0 },
    ];
    const two = daily.slice(0, 2);
    expect(aggregateFlow(two, flows, "day", "jumboNet")).toEqual([10, 20]);
    expect(aggregateFlow(two, flows, "day", "bigNet")).toEqual([100, 200]);
  });
});

describe("rollingMean", () => {
  it("窗口内取平均", () => {
    expect(rollingMean([1, 2, 3, 4, 5], 2)).toEqual([1, 1.5, 2.5, 3.5, 4.5]);
  });

  it("遇到 null 跳过，取窗口内有效值的平均；全空才为 null", () => {
    expect(rollingMean([1, null, 3, null, null], 2)).toEqual([1, 1, 3, 3, null]);
  });

  it("window 为 1 时原样返回", () => {
    expect(rollingMean([1, null, 3], 1)).toEqual([1, null, 3]);
  });
});
