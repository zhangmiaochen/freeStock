import { describe, expect, it } from "vitest";

import {
  buildChanlunLayers,
  divergenceMarkAreas,
  indexOfDate,
  pivotMarkAreas,
  signalSeries,
  signalsOnDate,
} from "@/lib/chanlun";
import type { ChanlunAnalysis, ChanlunDivergence, ChanlunPivot, ChanlunSignal } from "@/lib/backend";
import type { Candle } from "@/lib/indicators";

function candles(count: number): Candle[] {
  return Array.from({ length: count }, (_, i) => ({
    date: 20260101 + i,
    open: 10 + i * 0.1,
    high: 10.5 + i * 0.1,
    low: 9.5 + i * 0.1,
    close: 10.2 + i * 0.1,
    volume: 1000,
    amount: 1e6,
  }));
}

function pivot(overrides: Partial<ChanlunPivot> = {}): ChanlunPivot {
  return {
    index: 0,
    level: "day",
    zg: 12,
    zd: 10,
    startDate: 20260103,
    endDate: 20260108,
    startIndex: 2,
    endIndex: 7,
    startStroke: 0,
    endStroke: 3,
    strokeCount: 4,
    status: "extension",
    enterStroke: -1,
    leaveStroke: 4,
    confirmDate: 20260109,
    confirmed: true,
    note: "",
    ...overrides,
  } as ChanlunPivot;
}

function divergence(overrides: Partial<ChanlunDivergence> = {}): ChanlunDivergence {
  return {
    index: 0,
    date: 20260110,
    kind: "trend",
    kindCN: "趋势背驰",
    direction: "bottom",
    level: "day",
    pivotIndex: 0,
    legAStart: 0,
    legAEnd: 5,
    legBStart: 6,
    legBEnd: 9,
    legAStartDate: 20260101,
    legAEndDate: 20260106,
    legBStartDate: 20260107,
    legBEndDate: 20260110,
    strengthA: 1,
    strengthB: 0.72,
    ratio: 0.72,
    dims: [],
    newExtreme: true,
    confirmed: true,
    note: "",
    ...overrides,
  } as ChanlunDivergence;
}

function signal(overrides: Record<string, unknown> = {}): ChanlunSignal {
  return {
    type: "buy1",
    typeCN: "一买",
    direction: "buy",
    level: "day",
    state: "confirmed",
    stateCN: "确认",
    price: 10,
    score: 78.4,
    scoreFactors: [],
    formDate: 20260105,
    triggerDate: 20260106,
    confirmDate: 20260109,
    invalidDate: 0,
    expireDate: 0,
    formIndex: 4,
    triggerIndex: 5,
    confirmIndex: 8,
    invalidIndex: -1,
    expireIndex: -1,
    pivotIndex: 0,
    divergenceIndex: 0,
    reasons: ["下跌趋势背驰"],
    conflict: false,
    conflictNote: "",
    note: "",
    ...overrides,
  } as unknown as ChanlunSignal;
}

function analysis(overrides: Partial<ChanlunAnalysis> = {}): ChanlunAnalysis {
  return {
    code: "600519",
    name: "贵州茅台",
    level: "day",
    levelCN: "日线",
    adjust: "qfq",
    tradeDate: 20260120,
    data: {
      bars: 20,
      startDate: 20260101,
      endDate: 20260120,
      mergedBars: 18,
      hasIntraday: false,
      precisionNote: "只有日 K：精度有限",
    },
    mergedBars: [],
    fractals: [
      {
        date: 20260104,
        type: "bottom",
        price: 9.8,
        index: 3,
        mergedIndex: 3,
        confirmDate: 20260105,
        confirmIndex: 4,
        level: "day",
      },
    ],
    strokes: [
      {
        index: 0,
        direction: 1,
        startDate: 20260103,
        endDate: 20260106,
        startPrice: 10,
        endPrice: 12,
        startIndex: 2,
        endIndex: 5,
        startMerged: 2,
        endMerged: 5,
        high: 12,
        low: 10,
        confirmDate: 20260107,
        confirmed: true,
        level: "day",
        note: "",
      },
    ],
    segments: [],
    pivots: [pivot()],
    trend: { type: "down", typeCN: "下跌趋势", level: "day", startDate: 20260101, endDate: 20260120, pivotIndex: [0], note: "" },
    position: {
      phase: "pullback",
      phaseCN: "回抽段",
      pivotIndex: 0,
      barsFromPivot: 3,
      note: "",
    },
    divergences: [divergence()],
    signals: [signal()],
    state: {
      name: "reversal-candidate-buy",
      nameCN: "下跌趋势背驰（反转候选买）",
      note: "",
      sinceDate: 20260101,
      watchSignals: ["buy1"],
    },
    adaptability: {
      score: 72,
      grade: "B",
      liquidity: 80,
      amount: 1e9,
      history: 90,
      completeness: 60,
      stability: 70,
      penalty: 10,
      reasons: [],
    },
    notes: [],
    disclaimer: "仅作参考，不构成投资建议",
    ...overrides,
  } as ChanlunAnalysis;
}

describe("缠论标注 → 图表映射", () => {
  it("日期按下标映射：命中返回原下标，缺失时退回到之前最近一根", () => {
    const data = candles(10);
    expect(indexOfDate(data, 20260101)).toBe(0);
    expect(indexOfDate(data, 20260105)).toBe(4);
    // 20260104 与 20260105 之间存在（周末）→ 退回 20260103
    expect(indexOfDate(data, 20260104)).toBe(3);
    // 早于第一根 → -1
    expect(indexOfDate(data, 20250101)).toBe(-1);
  });

  it("关闭开关（analysis 为空）时没有任何图层", () => {
    const layers = buildChanlunLayers(candles(10), null);
    expect(layers.series).toEqual([]);
    expect(layers.markAreas).toEqual([]);
    expect(layers.legendNames).toEqual([]);
  });

  it("笔 / 线段 / 买卖点进入 series，中枢与背驰进入 markArea", () => {
    const data = candles(20);
    const layers = buildChanlunLayers(data, analysis());
    const names = layers.series.map((item) => item.name);
    expect(names).toContain("笔");
    expect(names).toContain("买卖点");
    // 线段数据为空时不渲染
    expect(names).not.toContain("线段");
    // 1 个中枢 + 1 个背驰段
    expect(layers.markAreas).toHaveLength(2);
  });

  it("中枢矩形用 zg/zd 与起止下标，并标注延伸", () => {
    const data = candles(20);
    const areas = pivotMarkAreas(data, [pivot()]);
    expect(areas).toHaveLength(1);
    const area = areas[0] as unknown as [
      { xAxis: number; yAxis: number; label: { formatter: string } },
      { xAxis: number; yAxis: number },
    ];
    expect(area[0].xAxis).toBe(2);
    expect(area[0].yAxis).toBe(10);
    expect(area[1].xAxis).toBe(7);
    expect(area[1].yAxis).toBe(12);
    expect(area[0].label.formatter).toContain("延伸");
  });

  it("背驰段高亮取该段的价格区间，并按方向着色", () => {
    const data = candles(20);
    const areas = divergenceMarkAreas(data, [divergence()]);
    expect(areas).toHaveLength(1);
    const area = areas[0] as unknown as [
      { xAxis: number; yAxis: number; itemStyle: { color: string }; label: { formatter: string } },
      { xAxis: number; yAxis: number },
    ];
    expect(area[0].xAxis).toBe(6);
    expect(area[1].xAxis).toBe(9);
    // 底背驰用绿色系，并且标注力度比
    expect(area[0].itemStyle.color).toContain("34,197,94");
    expect(area[0].label.formatter).toContain("0.72");
  });

  it("买卖点散点：形成用三角，触发/确认各画一个阶段点", () => {
    const data = candles(20);
    const confirmed = signalSeries(data, [signal()]) as {
      data: {
        value: number[];
        symbol: string;
        symbolRotate: number;
        itemStyle: { color: string };
      }[];
    };
    // 形成（三角）+ 触发（空心圆）+ 确认（实心圆）
    expect(confirmed.data).toHaveLength(3);
    expect(confirmed.data.map((item) => item.value[0])).toEqual([4, 5, 8]);
    expect(confirmed.data[0].symbol).toBe("triangle");
    expect(confirmed.data[0].symbolRotate).toBe(0);
    expect(confirmed.data[0].itemStyle.color).not.toBe("transparent");
    expect(confirmed.data[1].symbol).toBe("circle");
    expect(confirmed.data[1].itemStyle.color).toBe("transparent");
    expect(confirmed.data[2].symbol).toBe("circle");
    expect(confirmed.data[2].itemStyle.color).not.toBe("transparent");

    // 候选还没有触发/确认，只画形成点
    const candidate = signalSeries(data, [
      signal({ state: "candidate", stateCN: "候选", triggerDate: 0, confirmDate: 0 }),
    ]) as { data: { itemStyle: { color: string } }[] };
    expect(candidate.data).toHaveLength(1);
    expect(candidate.data[0].itemStyle.color).toBe("transparent");

    // 同一天既形成又触发时不重复画点
    const sameDay = signalSeries(data, [
      signal({ formDate: 20260105, triggerDate: 20260105, confirmDate: 20260105 }),
    ]) as { data: unknown[] };
    expect(sameDay.data).toHaveLength(1);

    const sell = signalSeries(data, [signal({ type: "sell1", typeCN: "一卖", direction: "sell" })]) as {
      data: { symbolRotate: number }[];
    };
    expect(sell.data[0].symbolRotate).toBe(180);
  });

  it("同一天的信号可以被 tooltip 取出", () => {
    const data = analysis();
    expect(signalsOnDate(data, 20260105)).toHaveLength(1);
    expect(signalsOnDate(data, 20260106)).toHaveLength(1); // 触发日也算
    expect(signalsOnDate(data, 20260109)).toHaveLength(1); // 确认日也算
    expect(signalsOnDate(data, 20260107)).toHaveLength(0);
    expect(signalsOnDate(null, 20260105)).toHaveLength(0);
  });
});
