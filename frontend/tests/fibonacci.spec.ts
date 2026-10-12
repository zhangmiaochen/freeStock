import { describe, expect, it } from "vitest";

import {
  FIB_RATIOS,
  fibDirection,
  fibLines,
  fibMarkLine,
  fibRange,
  fibonacciOverlay,
  ratioText,
  visibleIndexRange,
} from "@/lib/fibonacci";
import type { Candle } from "@/lib/indicators";

/** 造一段 K 线：low/high 由传入的两个数组决定 */
function candles(lows: number[], highs: number[]): Candle[] {
  return lows.map((low, i) => ({
    date: 20260101 + i,
    open: (low + highs[i]) / 2,
    high: highs[i],
    low,
    close: (low + highs[i]) / 2,
    volume: 1000,
    amount: 1e6,
  }));
}

describe("斐波那契黄金分割", () => {
  it("比例文本按常见写法：整数不带小数，其余保留一位", () => {
    expect(ratioText(0)).toBe("0%");
    expect(ratioText(0.236)).toBe("23.6%");
    expect(ratioText(0.5)).toBe("50%");
    expect(ratioText(0.618)).toBe("61.8%");
    expect(ratioText(1)).toBe("100%");
  });

  it("dataZoom 百分比折算成下标区间", () => {
    expect(visibleIndexRange(100, 0, 100)).toEqual({ from: 0, to: 100 });
    expect(visibleIndexRange(100, 50, 100)).toEqual({ from: 50, to: 100 });
    expect(visibleIndexRange(100, 90, 100)).toEqual({ from: 90, to: 100 });
    // 非法/缺失时回落到全部可见
    expect(visibleIndexRange(100, undefined, undefined)).toEqual({ from: 0, to: 100 });
    expect(visibleIndexRange(100, Number.NaN, Number.NaN)).toEqual({ from: 0, to: 100 });
    // 视口退化成 0 根时至少留一根，否则算不出区间
    const degenerate = visibleIndexRange(10, 50, 50);
    expect(degenerate.to - degenerate.from).toBeGreaterThanOrEqual(1);
    // 空数据
    expect(visibleIndexRange(0, 0, 100)).toEqual({ from: 0, to: 0 });
  });

  it("区间取可见窗口的最高价与最低价（平移/缩放会变）", () => {
    const data = candles([10, 9, 8, 7, 6, 5], [11, 10, 9, 20, 8, 7]);

    const all = fibRange(data, 0, 100);
    expect(all).not.toBeNull();
    expect(all!.low).toBe(5);
    expect(all!.high).toBe(20);
    expect(all!.count).toBe(6);
    // 最高点在第 4 根、最低点在最后一根 → 高点在前 = 下跌段
    expect(all!.highIndex).toBe(3);
    expect(all!.lowIndex).toBe(5);
    expect(fibDirection(all)).toBe("down");

    // 只看前一半（下标 0~2）：区间变成 [8, 11]，高点仍在前面
    const zoomed = fibRange(data, 0, 50);
    expect(zoomed!.low).toBe(8);
    expect(zoomed!.high).toBe(11);
    expect(zoomed!.count).toBe(3);

    // 低点在前 = 上涨段
    const rising = fibRange(candles([5, 6, 7], [6, 7, 8]), 0, 100)!;
    expect(fibDirection(rising)).toBe("up");
    // 高低点相同（横盘到没有振幅）= 说不清方向
    expect(fibDirection(fibRange(candles([5, 5], [5, 5]), 0, 100))).toBe("flat");
  });

  it("分割位 = 高点 − 比例 × 区间，0%/100% 就是高低点本身", () => {
    const range = fibRange(candles([100], [200]), 0, 100)!;
    const lines = fibLines(range);

    expect(lines).toHaveLength(FIB_RATIOS.length);
    const byRatio = new Map(lines.map((line) => [line.downRatio, line]));

    expect(byRatio.get(0)!.price).toBeCloseTo(200, 6); // 高点
    expect(byRatio.get(1)!.price).toBeCloseTo(100, 6); // 低点
    expect(byRatio.get(0.5)!.price).toBeCloseTo(150, 6);
    expect(byRatio.get(0.618)!.price).toBeCloseTo(200 - 0.618 * 100, 6);
    expect(byRatio.get(0.236)!.price).toBeCloseTo(200 - 23.6, 6);

    // 两端点标"高点/低点"，中间档标两个方向的百分比
    expect(byRatio.get(0)!.label).toBe("高点 200.00");
    expect(byRatio.get(1)!.label).toBe("低点 100.00");
    expect(byRatio.get(0.618)!.label).toBe("↓61.8%/↑38.2%  138.20");
    // 两个方向是镜像读法：↓x% 与 ↑(1-x)% 是同一个价位
    expect(byRatio.get(0.236)!.upRatio).toBeCloseTo(0.764, 6);
    expect(byRatio.get(0.236)!.price).toBeCloseTo(200 - 0.236 * 100, 6);
    // 黄金档要能区分出来（画得更亮）
    expect(byRatio.get(0.618)!.golden).toBe(true);
    expect(byRatio.get(0.382)!.golden).toBe(true);
    expect(byRatio.get(0.5)!.golden).toBe(true);
    expect(byRatio.get(0.236)!.golden).toBe(false);
    expect(byRatio.get(0)!.edge).toBe(true);
  });

  it("区间为 0 或数据为空时不画线（否则是一堆重叠的线）", () => {
    expect(fibLines(null)).toEqual([]);

    const flat = fibRange(candles([100, 100], [100, 100]), 0, 100);
    expect(fibLines(flat)).toEqual([]);

    expect(fibonacciOverlay([], 0, 100).lines).toEqual([]);
    expect(fibonacciOverlay(candles([10], [20]), 0, 100).lines).toHaveLength(FIB_RATIOS.length);
  });

  it("分割位随可见窗口变化：平移后线跟着换", () => {
    const data = candles([10, 9, 8, 7, 6, 5, 4, 3], [12, 11, 10, 30, 8, 7, 6, 5]);

    const left = fibonacciOverlay(data, 0, 40); // 前 4 根（0~40% 会向上取整）：高 30、低 7
    const right = fibonacciOverlay(data, 60, 100); // 后 4 根：高 8、低 3

    expect(left.range!.high).toBe(30);
    expect(left.range!.low).toBe(7);
    expect(right.range!.high).toBe(8);
    expect(right.range!.low).toBe(3);

    // 同一比例对应的价格完全不同 —— 这就是"位置动态计算"
    const leftHalf = left.lines.find((line) => line.downRatio === 0.5)!;
    const rightHalf = right.lines.find((line) => line.downRatio === 0.5)!;
    expect(leftHalf.price).toBeCloseTo((30 + 7) / 2, 6);
    expect(rightHalf.price).toBeCloseTo((8 + 3) / 2, 6);
    expect(leftHalf.price).not.toBeCloseTo(rightHalf.price, 2);
  });

  it("markLine 结构正确：7 条线、黄金档更亮、端点标高低点", () => {
    const data = candles([100, 110, 120], [150, 160, 200]);
    const markLine = fibMarkLine(data, 0, 100) as {
      silent: boolean;
      symbol: string;
      data: { yAxis: number; lineStyle: { width: number; type: string }; label: { formatter: string } }[];
    };

    // 不能吃掉鼠标事件，否则悬浮与缩放会被辅助线拦住
    expect(markLine.silent).toBe(true);
    expect(markLine.symbol).toBe("none");
    expect(markLine.data).toHaveLength(FIB_RATIOS.length);

    // 第一条是高点、最后一条是低点（区间 100 ~ 200）
    expect(markLine.data[0].yAxis).toBeCloseTo(200, 6);
    expect(markLine.data[0].label.formatter).toBe("高点 200.00");
    expect(markLine.data[markLine.data.length - 1].yAxis).toBeCloseTo(100, 6);
    expect(markLine.data[markLine.data.length - 1].label.formatter).toBe("低点 100.00");

    // 黄金档线更粗
    const goldenIndex = FIB_RATIOS.indexOf(0.618);
    const normalIndex = FIB_RATIOS.indexOf(0.236);
    expect(markLine.data[goldenIndex].lineStyle.width).toBeGreaterThan(
      markLine.data[normalIndex].lineStyle.width,
    );
    expect(markLine.data[goldenIndex].label.formatter).toBe("↓61.8%/↑38.2%  138.20");
  });

  // ECharts 的 SSR 渲染：不需要 DOM/Canvas，用来验证 markLine **真的能画出线**。
  // 这条用例挡的是"配置写错了但纯函数测试全绿"的情况（例如 label 位置非法、
  // markLine 结构不对 → 图上什么都没有）。
  it("交给 ECharts 真的画得出分割线与标签（SSR）", async () => {
    const echarts = await import("echarts/core");
    const { CandlestickChart } = await import("echarts/charts");
    const { GridComponent, MarkLineComponent } = await import("echarts/components");
    const { SVGRenderer } = await import("echarts/renderers");
    echarts.use([CandlestickChart, GridComponent, MarkLineComponent, SVGRenderer]);

    const data = candles(
      [100, 105, 110, 108, 120, 130],
      [130, 140, 150, 145, 160, 180],
    );
    const chart = echarts.init(null, null, {
      renderer: "svg",
      ssr: true,
      width: 640,
      height: 320,
    });
    chart.setOption({
      xAxis: { type: "category", data: data.map((c) => c.date) },
      yAxis: { type: "value", min: "dataMin", max: "dataMax" },
      series: [
        {
          type: "candlestick",
          data: data.map((c) => [c.open, c.close, c.low, c.high]),
          markLine: fibMarkLine(data, 0, 100),
        },
      ],
    });

    const svg = chart.renderToSVGString();
    chart.dispose();

    // 标签文字（黄金档与端点）
    expect(svg).toContain("↓61.8%/↑38.2%");
    expect(svg).toContain("↓50%/↑50%");
    expect(svg).toContain("高点 180.00");
    expect(svg).toContain("低点 100.00");
    // 至少有 7 条水平线（markLine 画成 path）
    expect(svg).toContain("<path");
    expect(svg.match(/<path/g)?.length ?? 0).toBeGreaterThanOrEqual(FIB_RATIOS.length);
  });
});
