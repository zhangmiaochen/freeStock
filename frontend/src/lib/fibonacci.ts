/**
 * 斐波那契黄金分割辅助线（纯函数，方便单测）。
 *
 * 口径（与行情软件里的"斐波那契回撤"一致，但**区间是动态的**）：
 *
 *   区间 = **当前可见窗口**的最高价与最低价
 *   分割位 = 高点 − 比例 × (高点 − 低点)
 *
 * 之所以用"可见窗口"而不是固定的某段行情：用户缩放/平移图表时，
 * 关心的就是屏幕上这一段的高低点 —— 区间跟着视口走，
 * 分割位才有意义（这也是"位置动态计算"的实现方式）。
 *
 * **两个方向**指的是同一个价位的两种读法，界面上直接标出来：
 *   - `↓ x%`：从**高点**回落的回撤比例（涨势回撤 → 该价位是**支撑**参考）；
 *   - `↑ y%`：从**低点**反弹的比例（跌势反弹 → 该价位是**压力**参考）。
 *   两者互为镜像：`↓23.6%` 与 `↑76.4%` 是同一个价位，所以一条线标两个数就够，
 *   画两遍只会得到重叠的线。
 */

import type { Candle } from "@/lib/indicators";

/** 回撤比例（0 与 1 就是区间的低点与高点）。 */
export const FIB_RATIOS = [0, 0.236, 0.382, 0.5, 0.618, 0.786, 1] as const;

/** 黄金比例相关的那几档：画得亮一点，其余弱化，避免一堆线糊成一片。 */
export const FIB_GOLDEN_RATIOS = [0.382, 0.5, 0.618] as const;

export const FIB_COLOR = "#eab308";
export const FIB_GOLDEN_COLOR = "#fbbf24";
export const FIB_EDGE_COLOR = "#71717a";

/** 一条分割线。 */
export interface FibLine {
  /** 价格（y 轴值） */
  price: number;
  /** 从高点回落的回撤比例（0~1），0 表示就是高点 */
  downRatio: number;
  /** 从低点反弹的比例（1-downRatio），0 表示就是低点 */
  upRatio: number;
  /** ≥100% 的档位用百分比整数，其余保留一位小数（23.6% / 38.2% …） */
  downLabel: string;
  upLabel: string;
  /** 画在图上的一句话，例如 `↓61.8/↑38.2  3521.40` */
  label: string;
  /** 是否是黄金档（0.382 / 0.5 / 0.618） */
  golden: boolean;
  /** 是否是区间的两个端点（低点/高点） */
  edge: boolean;
  color: string;
}

/** 可见区间：视口内的最高价、最低价与根数。 */
export interface FibRange {
  low: number;
  high: number;
  /** 区间内最低点/最高点出现的下标（用于判断走势方向） */
  lowIndex: number;
  highIndex: number;
  /** 参与计算的 K 线根数 */
  count: number;
  /** 起点下标（含） */
  from: number;
  /** 终点下标（不含） */
  to: number;
  /** 视口内最后一个下标——判断"低点在前"还是"高点在前"要用 */
  lastIndex: number;
}

/** 比例 → 文本：23.6% / 50% / 78.6%（按常见写法保留一位小数，整数不带小数） */
export function ratioText(ratio: number): string {
  const percent = ratio * 100;
  if (Math.abs(percent - Math.round(percent)) < 1e-9) return `${Math.round(percent)}%`;
  return `${percent.toFixed(1)}%`;
}

/**
 * 把 dataZoom 的 start/end 百分比折算成下标区间。
 *
 * ECharts 的 start/end 是"轴上的百分比"，两端各占半根（`boundaryGap: true`），
 * 所以这里按 `round(total * start/100)` 取整并把 end 变成不含端点的下标。
 * 百分比缺失/非法时回落到"全部可见"。
 */
export function visibleIndexRange(
  total: number,
  start?: number,
  end?: number,
): { from: number; to: number } {
  if (total <= 0) return { from: 0, to: 0 };
  const safeStart = Number.isFinite(start) ? Math.max(0, Math.min(100, start as number)) : 0;
  const safeEnd = Number.isFinite(end) ? Math.max(0, Math.min(100, end as number)) : 100;
  let from = Math.floor((total * safeStart) / 100);
  let to = Math.ceil((total * safeEnd) / 100);
  if (from < 0) from = 0;
  if (to > total) to = total;
  // 至少留一根，否则视口退化时算不出区间
  if (to - from < 1) {
    if (from > 0) from = Math.max(0, to - 1);
    else to = Math.min(total, from + 1);
  }
  return { from, to };
}

/** 取可见窗口的最高/最低价（区间退化时返回 null）。 */
export function fibRange(candles: Candle[], start?: number, end?: number): FibRange | null {
  if (!candles || candles.length === 0) return null;
  const { from, to } = visibleIndexRange(candles.length, start, end);
  if (to <= from) return null;

  let low = Number.POSITIVE_INFINITY;
  let high = Number.NEGATIVE_INFINITY;
  let lowIndex = from;
  let highIndex = from;
  for (let i = from; i < to; i++) {
    const candle = candles[i];
    if (!candle || !Number.isFinite(candle.high) || !Number.isFinite(candle.low)) continue;
    if (candle.low < low) {
      low = candle.low;
      lowIndex = i;
    }
    if (candle.high > high) {
      high = candle.high;
      highIndex = i;
    }
  }
  if (!Number.isFinite(low) || !Number.isFinite(high)) return null;
  return { low, high, lowIndex, highIndex, count: to - from, from, to, lastIndex: to - 1 };
}

/**
 * 计算分割线。
 *
 * 区间为 0（只有一根 K 线、或高开低收全相等）时返回空数组 ——
 * 这时画出来的是"一堆叠在一起的线"，没有意义。
 */
export function fibLines(range: FibRange | null): FibLine[] {
  if (!range) return [];
  const span = range.high - range.low;
  if (!(span > 0)) return [];

  return FIB_RATIOS.map((downRatio) => {
    const upRatio = 1 - downRatio;
    const price = range.high - downRatio * span;
    const golden = (FIB_GOLDEN_RATIOS as readonly number[]).includes(downRatio);
    const edge = downRatio === 0 || downRatio === 1;
    const color = edge ? FIB_EDGE_COLOR : golden ? FIB_GOLDEN_COLOR : FIB_COLOR;
    const downLabel = ratioText(downRatio);
    const upLabel = ratioText(upRatio);
    // 端点直接写"高点/低点"，比写 0% / 100% 更好认
    const label = edge
      ? `${downRatio === 0 ? "高点" : "低点"} ${price.toFixed(2)}`
      : `↓${downLabel}/↑${upLabel}  ${price.toFixed(2)}`;
    return {
      price,
      downRatio,
      upRatio,
      downLabel,
      upLabel,
      label,
      golden,
      edge,
      color,
    };
  });
}

/** 一次算完：可见区间 + 分割线。 */
export function fibonacciOverlay(
  candles: Candle[],
  start?: number,
  end?: number,
): { range: FibRange | null; lines: FibLine[] } {
  const range = fibRange(candles, start, end);
  return { range, lines: fibLines(range) };
}

/**
 * 这个区间的"走势方向"：先用**低点在前还是高点在前**判断，
 * 再看谁离当前更近 —— 用于在提示里告诉用户哪一套读法是当前的主读法。
 */
export function fibDirection(range: FibRange | null): "up" | "down" | "flat" {
  if (!range) return "flat";
  if (range.high === range.low) return "flat";
  if (range.lowIndex < range.highIndex) return "up";
  if (range.highIndex < range.lowIndex) return "down";
  return "flat";
}

/**
 * 构造 ECharts 的 `markLine` 配置（挂在主图蜡烛上）。
 *
 * 放在 lib 里而不是组件里，是为了能在**没有 DOM/Canvas** 的单测里直接断言结构
 * （数据条数、每条的 yAxis、标签、黄金档线宽），再用 ECharts 的 SSR 渲染
 * 验证它真的能画出线与文字。
 */
export function fibMarkLine(candles: Candle[], start?: number, end?: number): Record<string, unknown> {
  const { lines } = fibonacciOverlay(candles, start, end);
  return {
    silent: true,
    symbol: "none",
    animation: false,
    label: {
      show: true,
      // 贴在线的右端上方：既不压在蜡烛上，也不会被 y 轴标签挡住
      position: "insideEndTop",
      distance: 3,
      fontSize: 10,
      fontWeight: "normal",
    },
    data: lines.map((line) => ({
      yAxis: line.price,
      lineStyle: {
        color: line.color,
        width: line.golden ? 1.4 : 1,
        type: line.edge ? "solid" : "dashed",
        opacity: line.edge ? 0.9 : 0.7,
      },
      label: { color: line.color, formatter: line.label },
    })),
  };
}
