/**
 * 缠论标注 → ECharts 图层的**纯函数**。
 *
 * 这里只做"映射"，不做任何缠论判定：
 *   - 笔 / 线段 → 折线；
 *   - 分型 → 三角散点；
 *   - 中枢 → markArea 矩形 + 上下沿标注；
 *   - 背驰段 → markArea 高亮；
 *   - 买卖点 → 带类型标签的三角散点。
 *
 * 后端标注全部按**日期**给出（不是下标），因为前端的周期聚合与后端同口径但仍然是
 * 两次独立计算，用日期映射比用下标更抗错位。找不到同日期时退回到"该日期之前最近的一根"。
 */
import type { Candle } from "@/lib/indicators";
import type {
  ChanlunAnalysis,
  ChanlunDivergence,
  ChanlunPivot,
  ChanlunSignal,
} from "@/lib/backend";

/** 图层：挂在主图上的 series + 需要合并进蜡烛 series 的 markArea。 */
export interface ChanlunLayers {
  series: Record<string, unknown>[];
  markAreas: Record<string, unknown>[];
  legendNames: string[];
}

const STROKE_COLOR = "#f59e0b";
const SEGMENT_COLOR = "#a78bfa";
const PIVOT_COLOR = "rgba(56,189,248,0.55)";
const PIVOT_FILL = "rgba(56,189,248,0.10)";
const DIVERGENCE_TOP = "rgba(239,68,68,0.08)";
const DIVERGENCE_BOTTOM = "rgba(34,197,94,0.08)";
const BUY_COLOR = "#ef4444";
const SELL_COLOR = "#22c55e";
const FRACTAL_TOP = "#fb923c";
const FRACTAL_BOTTOM = "#38bdf8";

/** K 线日期升序，返回"不晚于 date 的最近一根"的下标；没有则 -1。 */
export function indexOfDate(candles: Candle[], date: number): number {
  if (!date || candles.length === 0) return -1;
  let low = 0;
  let high = candles.length - 1;
  let best = -1;
  while (low <= high) {
    const mid = (low + high) >> 1;
    if (candles[mid].date === date) return mid;
    if (candles[mid].date < date) {
      best = mid;
      low = mid + 1;
    } else {
      high = mid - 1;
    }
  }
  return best;
}

/** 把一串"日期 + 价格"端点转成折线数据（用下标作 x，避免分类轴取值歧义）。 */
function polyline(
  candles: Candle[],
  points: { date: number; price: number }[],
): [number, number][] {
  const out: [number, number][] = [];
  for (const point of points) {
    const index = indexOfDate(candles, point.date);
    if (index < 0) continue;
    out.push([index, point.price]);
  }
  return out;
}

/** 笔的折线端点：首笔起点 + 每笔终点（笔首尾相接）。 */
function strokePoints(analysis: ChanlunAnalysis) {
  const strokes = analysis.strokes ?? [];
  if (strokes.length === 0) return [];
  const points = [{ date: strokes[0].startDate, price: strokes[0].startPrice }];
  for (const stroke of strokes) {
    points.push({ date: stroke.endDate, price: stroke.endPrice });
  }
  return points;
}

/** 线段的折线端点。 */
function segmentPoints(analysis: ChanlunAnalysis) {
  const segments = analysis.segments ?? [];
  if (segments.length === 0) return [];
  const points = [{ date: segments[0].startDate, price: segments[0].startPrice }];
  for (const segment of segments) {
    points.push({ date: segment.endDate, price: segment.endPrice });
  }
  return points;
}

/** 中枢矩形（markArea）。 */
export function pivotMarkAreas(candles: Candle[], pivots: ChanlunPivot[]): Record<string, unknown>[] {
  const areas: Record<string, unknown>[] = [];
  for (const pivot of pivots ?? []) {
    const start = indexOfDate(candles, pivot.startDate);
    const end = indexOfDate(candles, pivot.endDate);
    if (start < 0 || end < 0) continue;
    const extended = pivot.strokeCount > 3;
    areas.push([
      {
        xAxis: start,
        yAxis: pivot.zd,
        itemStyle: { color: PIVOT_FILL, borderColor: PIVOT_COLOR, borderWidth: 1, borderType: "dashed" },
        label: {
          show: true,
          position: "insideTopLeft",
          formatter: extended ? `中枢·延伸${pivot.strokeCount}` : "中枢",
          color: PIVOT_COLOR,
          fontSize: 10,
        },
      },
      { xAxis: end, yAxis: pivot.zg },
    ] as unknown as Record<string, unknown>);
  }
  return areas;
}

/** 背驰段高亮（markArea）：只画后一段，并标注力度比。 */
export function divergenceMarkAreas(
  candles: Candle[],
  divergences: ChanlunDivergence[],
): Record<string, unknown>[] {
  const areas: Record<string, unknown>[] = [];
  for (const div of divergences ?? []) {
    const start = indexOfDate(candles, div.legBStartDate);
    const end = indexOfDate(candles, div.legBEndDate);
    if (start < 0 || end < 0) continue;
    let low = Number.POSITIVE_INFINITY;
    let high = Number.NEGATIVE_INFINITY;
    for (let i = start; i <= end; i += 1) {
      low = Math.min(low, candles[i].low);
      high = Math.max(high, candles[i].high);
    }
    if (!Number.isFinite(low) || !Number.isFinite(high) || high <= low) continue;
    const isBottom = div.direction === "bottom";
    areas.push([
      {
        xAxis: start,
        yAxis: low,
        itemStyle: {
          color: isBottom ? DIVERGENCE_BOTTOM : DIVERGENCE_TOP,
          borderColor: isBottom ? "rgba(34,197,94,0.5)" : "rgba(239,68,68,0.5)",
          borderWidth: 1,
        },
        label: {
          show: true,
          position: "insideBottomRight",
          formatter: `${div.kindCN} ${div.ratio.toFixed(2)}`,
          color: isBottom ? SELL_COLOR : BUY_COLOR,
          fontSize: 10,
        },
      },
      { xAxis: end, yAxis: high },
    ] as unknown as Record<string, unknown>);
  }
  return areas;
}

/** 买卖点散点：买点用向上的三角（标在下方），卖点用向下的三角。
 *
 * 除了形成点（带类型标签），触发/确认也各画一个点：
 *   - 触发：空心小圆（分型确认那天）
 *   - 确认：实心圆（二买/二卖成立，或原趋势最后中枢被反向破坏那天）
 *
 * 三个点都在信号的参考价上，所以同一信号的阶段点是一条水平线上的三颗点，
 * 一眼能看出"形成 → 触发 → 确认"隔了多久；没有触发/确认的信号不会补点。
 */
export function signalSeries(candles: Candle[], signals: ChanlunSignal[]): Record<string, unknown> | null {
  const data: Record<string, unknown>[] = [];
  for (const signal of signals ?? []) {
    const date = signal.formDate || signal.triggerDate;
    const index = indexOfDate(candles, date);
    if (index < 0) continue;
    const isBuy = signal.direction === "buy";
    const color = isBuy ? BUY_COLOR : SELL_COLOR;
    const hollow = signal.state === "candidate";
    const label = `${signal.typeCN}${signal.state === "candidate" ? "·候选" : ""}`;
    data.push({
      value: [index, signal.price],
      symbol: "triangle",
      symbolRotate: isBuy ? 0 : 180,
      symbolSize: signal.state === "confirmed" ? 15 : 12,
      itemStyle: {
        color: hollow ? "transparent" : color,
        borderColor: color,
        borderWidth: hollow ? 2 : 1,
      },
      label: {
        show: true,
        position: isBuy ? "bottom" : "top",
        distance: 4,
        formatter: label,
        color,
        fontSize: 10,
        fontWeight: 600,
        backgroundColor: "rgba(9,11,16,0.72)",
        padding: [1, 2],
        borderRadius: 2,
      },
    });

    // 触发 / 确认：同一条信号在后续 K 线上的阶段点（候选还没有这些阶段）
    if (signal.state !== "candidate") {
      const used = new Set<number>([index]);
      const stages: { date: number; confirm: boolean }[] = [];
      if (signal.triggerDate) stages.push({ date: signal.triggerDate, confirm: false });
      if (signal.confirmDate) stages.push({ date: signal.confirmDate, confirm: true });
      for (const stage of stages) {
        const stageIndex = indexOfDate(candles, stage.date);
        if (stageIndex < 0 || used.has(stageIndex)) continue;
        used.add(stageIndex);
        data.push({
          value: [stageIndex, signal.price],
          symbol: "circle",
          symbolSize: stage.confirm ? 8 : 5,
          itemStyle: stage.confirm
            ? { color, borderColor: "#e5e7eb", borderWidth: 1.5, opacity: 0.95 }
            : { color: "transparent", borderColor: color, borderWidth: 1.5, opacity: 0.85 },
          label: { show: false },
        });
      }
    }
  }
  if (data.length === 0) return null;
  return {
    name: "买卖点",
    type: "scatter",
    xAxisIndex: 0,
    yAxisIndex: 0,
    data,
    silent: true,
    z: 12,
  };
}

/** 分型散点（小三角，作为结构参考）。 */
export function fractalSeries(candles: Candle[], analysis: ChanlunAnalysis): Record<string, unknown> | null {
  const data: Record<string, unknown>[] = [];
  for (const fractal of analysis.fractals ?? []) {
    const index = indexOfDate(candles, fractal.date);
    if (index < 0) continue;
    const isTop = fractal.type === "top";
    data.push({
      value: [index, fractal.price],
      symbol: "triangle",
      symbolRotate: isTop ? 180 : 0,
      symbolSize: 7,
      itemStyle: { color: isTop ? FRACTAL_TOP : FRACTAL_BOTTOM, opacity: 0.75 },
    });
  }
  if (data.length === 0) return null;
  return {
    name: "分型",
    type: "scatter",
    xAxisIndex: 0,
    yAxisIndex: 0,
    data,
    silent: true,
    z: 8,
  };
}

/**
 * 构造全部缠论图层。
 *
 * `analysis` 为 null 时返回空图层 —— 关闭开关时与原有图表完全一致。
 */
export function buildChanlunLayers(
  candles: Candle[],
  analysis: ChanlunAnalysis | null | undefined,
): ChanlunLayers {
  if (!analysis || candles.length === 0) {
    return { series: [], markAreas: [], legendNames: [] };
  }

  const series: Record<string, unknown>[] = [];
  const legendNames: string[] = [];

  const segments = polyline(candles, segmentPoints(analysis));
  if (segments.length >= 2) {
    legendNames.push("线段");
    series.push({
      name: "线段",
      type: "line",
      xAxisIndex: 0,
      yAxisIndex: 0,
      data: segments,
      showSymbol: false,
      silent: true,
      lineStyle: { width: 2.2, color: SEGMENT_COLOR },
      itemStyle: { color: SEGMENT_COLOR },
      z: 6,
    });
  }

  const strokes = polyline(candles, strokePoints(analysis));
  if (strokes.length >= 2) {
    legendNames.push("笔");
    series.push({
      name: "笔",
      type: "line",
      xAxisIndex: 0,
      yAxisIndex: 0,
      data: strokes,
      showSymbol: false,
      silent: true,
      lineStyle: { width: 1.2, color: STROKE_COLOR },
      itemStyle: { color: STROKE_COLOR },
      z: 5,
    });
  }

  const fractals = fractalSeries(candles, analysis);
  if (fractals) series.push(fractals);

  const signals = signalSeries(candles, analysis.signals ?? []);
  if (signals) {
    legendNames.push("买卖点");
    series.push(signals);
  }

  return {
    series,
    markAreas: [
      ...pivotMarkAreas(candles, analysis.pivots ?? []),
      ...divergenceMarkAreas(candles, analysis.divergences ?? []),
    ],
    legendNames,
  };
}

/** 某根 K 线上（形成时间相同）的信号，供 tooltip 使用。 */
/**
 * 返回某一天"该显示在提示里"的信号。
 *
 * 包含形成、触发、确认三个日期：图上三颗阶段点分别落在这些日子，
 * 鼠标停在其中任何一颗（对应那根 K 线）都能看到这条信号的完整时间线。
 */
export function signalsOnDate(analysis: ChanlunAnalysis | null | undefined, date: number): ChanlunSignal[] {
  if (!analysis) return [];
  return (analysis.signals ?? []).filter(
    (signal) =>
      signal.formDate === date || signal.triggerDate === date || signal.confirmDate === date,
  );
}
