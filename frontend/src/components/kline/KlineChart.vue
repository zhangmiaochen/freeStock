<script setup lang="ts">
/**
 * K 线图表（ECharts）：主图（蜡烛 + 均线 + 布林 + 斐波那契辅助线）、成交量、MACD 三个面板联动。
 *
 * 只负责画图：均线配置、指标开关都由父组件传入，指标计算走 lib/indicators，
 * 斐波那契分割位走 lib/fibonacci。
 *
 * 斐波那契辅助线的特别之处：**区间是"当前可见窗口"的高低点**，
 * 所以缩放/平移时要重算。重算走 `setOption({series:[{markLine}]}, {notMerge:false})`
 * 这条**局部更新**路径，而不是整图 render()：
 *   - 不会重建 dataZoom（否则平移会被自己重置，甚至来回触发事件）；
 *   - 不闪、不打断十字光标；
 *   - 最多每帧一次，平移时不会掉帧。
 *
 * 框选统计（`showRangeSelect`）用的是 ECharts 的 brush 组件，是另一套交互：
 *   - 开启时**创建一个只盖住 K 线面板的透明层**接管指针事件，
 *     否则框选会与 dataZoom 的拖拽平移打架；
 *   - brush 的 y 范围按**落在哪个面板**折算，所以从成交量面板里框一段也算数
 *     （见 rangeFromBrushArea）；
 *   - 统计与口径在 lib/rangeStats，这里只负责把像素区间换成下标区间。
 */
import { BarChart, CandlestickChart, LineChart } from "echarts/charts";
import {
  AxisPointerComponent,
  BrushComponent,
  DataZoomComponent,
  GridComponent,
  LegendComponent,
  // ⚠ 少了这个：markLine（斐波那契辅助线）不会画出来，而且**不报错**，
  // 只是图上什么都没有 —— 由 tests/fibonacci.spec.ts 的 SSR 用例盯着
  MarkLineComponent,
  // ⚠ 同理：少了它，框选后的区间高亮（markArea）会**静默消失**，
  // 由 tests/rangeSelectChart.spec.ts 的 SSR 用例盯着
  MarkAreaComponent,
  TooltipComponent,
} from "echarts/components";
import * as echarts from "echarts/core";
import { CanvasRenderer } from "echarts/renderers";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";

import {
  DEFAULT_BOLL,
  DEFAULT_MACD,
  aggregateFlow,
  boll,
  macd,
  rollingMean,
  sma,
  type BollConfig,
  type Candle,
  type MaConfig,
  type MacdConfig,
  type MoneyFlowPoint,
  type Period,
} from "@/lib/indicators";
import { fibMarkLine } from "@/lib/fibonacci";
import { formatAmount, formatDate } from "@/lib/format";
import { selectionFromAxisValues, type RangeSelection } from "@/lib/rangeStats";

echarts.use([
  CandlestickChart,
  LineChart,
  BarChart,
  GridComponent,
  TooltipComponent,
  DataZoomComponent,
  AxisPointerComponent,
  LegendComponent,
  MarkAreaComponent,
  MarkLineComponent,
  BrushComponent,
  CanvasRenderer,
]);

const props = withDefaults(
  defineProps<{
    candles: Candle[];
    maConfigs: MaConfig[];
    period: Period;
    showBoll?: boolean;
    showMacd?: boolean;
    showVolume?: boolean;
    /** 是否显示资金流面板 */
    showFlow?: boolean;
    /** 是否显示斐波那契黄金分割辅助线 */
    showFibonacci?: boolean;
    /** 是否开启「框选统计」：在图上拖一个矩形，统计这一段的涨跌信息 */
    showRangeSelect?: boolean;
    /** 已选中的区间（下标，半开区间）：画一层高亮，让用户知道统计的是哪一段 */
    rangeSelection?: RangeSelection | null;
    /** 资金流序列（与后端 MoneyFlowPoint 对应） */
    moneyFlows?: MoneyFlowPoint[];
    bollConfig?: BollConfig;
    macdConfig?: MacdConfig;
    /** 用于判断是否需要重置缩放：股票或周期变化时重置 */
    resetKey?: string;
    /** 默认显示最近多少根 */
    defaultVisibleBars?: number;
  }>(),
  {
    showBoll: false,
    showMacd: true,
    showVolume: true,
    showFlow: true,
    showFibonacci: false,
    showRangeSelect: false,
    rangeSelection: null,
    moneyFlows: () => [],
    bollConfig: () => ({ ...DEFAULT_BOLL }),
    macdConfig: () => ({ ...DEFAULT_MACD }),
    resetKey: "",
    defaultVisibleBars: 160,
  },
);

const emit = defineEmits<{
  /** 框选完成：给出下标区间（半开）与对应的日期区间 */
  rangeSelected: [payload: { from: number; to: number }];
  /** 框选被清掉（工具栏清空、双击、切换标的等） */
  rangeCleared: [];
}>();

const UP = "#ef4444";
const DOWN = "#22c55e";
const BOLL_MID = "#94a3b8";
const BOLL_BAND = "#38bdf8";
const DIF_COLOR = "#f5c542";
const DEA_COLOR = "#38bdf8";
/** 资金流均线：主力净流入的平滑线 */
const FLOW_MEAN_COLOR = "#f5c542";
const FLOW_MEAN_WINDOW = 5;

const container = ref<HTMLElement | null>(null);
let chart: echarts.ECharts | null = null;
let observer: ResizeObserver | null = null;

const zoom = ref({ start: 0, end: 100 });
let lastResetKey = "";

/**
 * 本次渲染算好的均线序列，供 tooltip 复用。
 * 不能在 tooltip 里现算：鼠标一动就要对上千根 K 线重算 5 条均线，会明显卡顿。
 */
let maCache: { period: number; color: string; values: (number | null)[] }[] = [];

/** 本次渲染算好的资金流序列，同样供 tooltip 复用 */
let flowMainNet: (number | null)[] = [];
let flowMean: (number | null)[] = [];

/**
 * 斐波那契辅助线的 markLine 配置。
 *
 * 区间 = 当前可见窗口的最高/最低价（`zoom` 就是 dataZoom 的 start/end 百分比），
 * 所以缩放、平移之后要重新调用一次 —— 见 updateFibonacciOnly。
 */
function fibonacciMarkLine(): Record<string, unknown> {
  return fibMarkLine(props.candles, zoom.value.start, zoom.value.end);
}

/**
 * 选中区间的高亮（主图 series 的 markArea）。
 *
 * 用 markArea 而不是留着 brush 那层虚线框：brush 的选框横跨所有面板，
 * 而统计只统计 K 线 —— 高亮也只盖主图，两者才对得上。
 *
 * ⚠ `silent: true`：markArea 会吃掉鼠标事件，不设的话它会挡住十字光标与
 * 悬浮提示（与斐波那契 markLine 同一个坑）。
 */
function rangeHighlight(): Record<string, unknown> | null {
  const selection = props.rangeSelection;
  if (!selection) return null;
  const candles = props.candles;
  if (candles.length === 0) return null;

  const from = Math.max(0, Math.min(selection.from, candles.length - 1));
  const last = Math.max(from, Math.min(selection.to - 1, candles.length - 1));
  const start = candles[from];
  const end = candles[last];
  if (!start || !end) return null;

  return {
    silent: true,
    animation: false,
    itemStyle: { color: "rgba(59,130,246,0.12)", borderColor: "#3b82f6", borderWidth: 1 },
    label: { show: false },
    data: [[{ xAxis: start.date }, { xAxis: end.date }]],
  };
}

/** 按传入的周期格式化 X 轴标签 */
function axisLabel(date: number): string {
  const text = String(date);
  if (text.length !== 8) return text;
  const year = text.slice(2, 4);
  const month = text.slice(4, 6);
  const day = text.slice(6, 8);
  switch (props.period) {
    case "month":
      return `${year}-${month}`;
    case "year":
      return text.slice(0, 4);
    default:
      return `${year}-${month}-${day}`;
  }
}

/** 各面板的高度分配 */
function layout(height: number) {
  const topPad = 34;
  const bottomPad = 52;
  const gap = 22;

  const panels: { key: "main" | "volume" | "macd" | "flow"; weight: number }[] = [
    { key: "main", weight: 3.1 },
  ];
  if (props.showVolume) panels.push({ key: "volume", weight: 1.05 });
  if (props.showMacd) panels.push({ key: "macd", weight: 1.35 });
  // 资金流只在真的有数据时才占一个面板，避免给没有资金流的标的留一块空白
  if (props.showFlow && props.moneyFlows.length > 0) {
    panels.push({ key: "flow", weight: 1.15 });
  }

  const usable = Math.max(200, height - topPad - bottomPad - gap * (panels.length - 1));
  const totalWeight = panels.reduce((sum, panel) => sum + panel.weight, 0);

  let cursor = topPad;
  const result: { grid: Record<string, number>; index: number }[] = [];
  panels.forEach((panel, index) => {
    const panelHeight = (usable * panel.weight) / totalWeight;
    result.push({
      index,
      grid: { left: 62, right: 18, top: cursor, height: panelHeight },
    });
    cursor += panelHeight + gap;
  });
  return result;
}

function buildOption(): echarts.EChartsCoreOption {
  const candles = props.candles;
  const dates = candles.map((c) => c.date);
  const closes = candles.map((c) => c.close);
  const height = container.value?.clientHeight ?? 560;
  const grids = layout(height);
  const panelCount = grids.length;

  // 面板索引，顺序与 layout() 里的 panels 保持一致
  const hasVolume = props.showVolume;
  const hasMacd = props.showMacd;
  // 资金流面板只在有数据时才真正占位（与 layout 的判断保持一致）
  const hasFlow = props.showFlow && props.moneyFlows.length > 0;
  const volumePanel = hasVolume ? 1 : -1;
  const macdPanel = hasMacd ? (hasVolume ? 2 : 1) : -1;
  const flowPanel = hasFlow ? (hasVolume ? 1 : 0) + (hasMacd ? 1 : 0) + 1 : -1;
  /** 成交量与资金流是金额/手数，用 万/亿 缩写更可读 */
  const amountPanels = new Set([volumePanel, flowPanel].filter((index) => index >= 0));

  const xAxis = grids.map((item, index) => ({
    type: "category" as const,
    gridIndex: index,
    data: dates,
    boundaryGap: true,
    axisLine: { lineStyle: { color: "#3f3f46" } },
    axisTick: { show: false },
    splitLine: { show: false },
    axisLabel:
      index === panelCount - 1
        ? { color: "#a1a1aa", fontSize: 11, formatter: (value: string) => axisLabel(Number(value)) }
        : { show: false },
    axisPointer: { label: { show: index === panelCount - 1 } },
    min: "dataMin",
    max: "dataMax",
  }));

  const yAxis = grids.map((_, index) => ({
    type: "value" as const,
    gridIndex: index,
    scale: true,
    // 资金流面板标个名字，否则四个面板容易看混
    name: index === flowPanel ? "资金流" : undefined,
    nameTextStyle: { color: "#a1a1aa", fontSize: 11, align: "left" },
    nameGap: 8,
    axisLine: { show: false },
    axisTick: { show: false },
    splitLine: { lineStyle: { color: "rgba(255,255,255,0.06)" } },
    axisLabel: {
      color: "#a1a1aa",
      fontSize: 11,
      formatter: amountPanels.has(index)
        ? (value: number) => formatAmount(value)
        : (value: number) => (Math.abs(value) >= 1000 ? value.toFixed(0) : value.toFixed(2)),
    },
  }));

  const series: Record<string, unknown>[] = [];

  // 主图：蜡烛（挂斐波那契辅助线 + 框选高亮）
  series.push({
    name: "K线",
    type: "candlestick",
    xAxisIndex: 0,
    yAxisIndex: 0,
    data: candles.map((c) => [c.open, c.close, c.low, c.high]),
    itemStyle: {
      color: UP,
      color0: DOWN,
      borderColor: UP,
      borderColor0: DOWN,
    },
    ...(props.showFibonacci ? { markLine: fibonacciMarkLine() } : {}),
    ...(rangeHighlight() ? { markArea: rangeHighlight() } : {}),
  });

  // 均线（算一次，图例 / tooltip 共用）
  maCache = [];
  for (const config of props.maConfigs) {
    if (!config.visible) continue;
    const values = sma(closes, config.period);
    maCache.push({ period: config.period, color: config.color, values });
    series.push({
      name: `MA${config.period}`,
      type: "line",
      xAxisIndex: 0,
      yAxisIndex: 0,
      data: values,
      smooth: true,
      showSymbol: false,
      lineStyle: { width: 1.2, color: config.color },
      itemStyle: { color: config.color },
      emphasis: { disabled: true },
      z: 3,
    });
  }

  // 布林带（参数可自定义）
  if (props.showBoll) {
    const band = boll(closes, props.bollConfig.period, props.bollConfig.mult);
    series.push({
      name: "BOLL上轨",
      type: "line",
      xAxisIndex: 0,
      yAxisIndex: 0,
      data: band.upper,
      smooth: true,
      showSymbol: false,
      lineStyle: { width: 1, color: BOLL_BAND, type: "dashed" },
      itemStyle: { color: BOLL_BAND },
      z: 2,
    });
    series.push({
      name: "BOLL中轨",
      type: "line",
      xAxisIndex: 0,
      yAxisIndex: 0,
      data: band.mid,
      smooth: true,
      showSymbol: false,
      lineStyle: { width: 1, color: BOLL_MID },
      itemStyle: { color: BOLL_MID },
      z: 2,
    });
    series.push({
      name: "BOLL下轨",
      type: "line",
      xAxisIndex: 0,
      yAxisIndex: 0,
      data: band.lower,
      smooth: true,
      showSymbol: false,
      lineStyle: { width: 1, color: BOLL_BAND, type: "dashed" },
      itemStyle: { color: BOLL_BAND },
      z: 2,
    });
  }

  // 成交量
  if (hasVolume) {
    series.push({
      name: "成交量",
      type: "bar",
      xAxisIndex: volumePanel,
      yAxisIndex: volumePanel,
      data: candles.map((c) => c.volume),
      itemStyle: {
        color: (params: { dataIndex: number }) => {
          const candle = candles[params.dataIndex];
          return candle.close >= candle.open ? UP : DOWN;
        },
      },
      barWidth: "62%",
    });
  }

  // MACD（参数可自定义）
  if (hasMacd) {
    const result = macd(closes, props.macdConfig.fast, props.macdConfig.slow, props.macdConfig.signal);
    series.push({
      name: "MACD柱",
      type: "bar",
      xAxisIndex: macdPanel,
      yAxisIndex: macdPanel,
      data: result.bar,
      itemStyle: {
        color: (params: { dataIndex: number }) => (result.bar[params.dataIndex] >= 0 ? UP : DOWN),
      },
      barWidth: "62%",
    });
    series.push({
      name: "DIF",
      type: "line",
      xAxisIndex: macdPanel,
      yAxisIndex: macdPanel,
      data: result.dif,
      showSymbol: false,
      lineStyle: { width: 1.1, color: DIF_COLOR },
      itemStyle: { color: DIF_COLOR },
    });
    series.push({
      name: "DEA",
      type: "line",
      xAxisIndex: macdPanel,
      yAxisIndex: macdPanel,
      data: result.dea,
      showSymbol: false,
      lineStyle: { width: 1.1, color: DEA_COLOR },
      itemStyle: { color: DEA_COLOR },
    });
  }

  // 资金流（主力净流入柱 + N 期均线）
  flowMainNet = [];
  flowMean = [];
  if (hasFlow) {
    flowMainNet = aggregateFlow(candles, props.moneyFlows, props.period, "mainNet");
    flowMean = rollingMean(flowMainNet, FLOW_MEAN_WINDOW);

    series.push({
      name: "主力净流入",
      type: "bar",
      xAxisIndex: flowPanel,
      yAxisIndex: flowPanel,
      data: flowMainNet,
      itemStyle: {
        color: (params: { dataIndex: number }) => {
          const value = flowMainNet[params.dataIndex];
          return value !== null && value >= 0 ? UP : DOWN;
        },
      },
      barWidth: "62%",
    });
    series.push({
      name: `主力${FLOW_MEAN_WINDOW}期均`,
      type: "line",
      xAxisIndex: flowPanel,
      yAxisIndex: flowPanel,
      data: flowMean,
      showSymbol: false,
      smooth: true,
      lineStyle: { width: 1.2, color: FLOW_MEAN_COLOR },
      itemStyle: { color: FLOW_MEAN_COLOR },
    });
  }

  const axisIndexes = grids.map((_, index) => index);

  // 图例只作为颜色对照（显隐由「均线设置」面板控制），所以不可点击
  const legendNames: string[] = [];
  for (const config of props.maConfigs) {
    if (config.visible) legendNames.push(`MA${config.period}`);
  }
  if (props.showBoll) legendNames.push("BOLL上轨", "BOLL中轨", "BOLL下轨");

  return {
    animation: false,
    backgroundColor: "transparent",
    textStyle: { color: "#e4e4e7" },
    // 框选：brush 自己那层虚线框只在拖拽时出现（松手后由 clearBrushVisual 清掉），
    // 常驻的高亮用主图 series 的 markArea 画 —— 只盖住 K 线面板，不会糊到成交量上。
    // **不给它 toolbox**：清空与开关都在页面工具栏上，图上再放一排小图标是重复入口。
    // `brushMode: 'single'`：拖一次就替换上一次的框，符合"框一段看看"的用法。
    brush: {
      xAxisIndex: 0,
      brushType: "rect",
      brushMode: "single",
      transformable: false,
      throttleType: "debounce",
      throttleDelay: 120,
      brushStyle: { borderWidth: 1, color: "rgba(59,130,246,0.10)", borderColor: "#3b82f6" },
    },
    legend: {
      show: legendNames.length > 0,
      top: 2,
      left: 62,
      itemWidth: 14,
      itemHeight: 8,
      itemGap: 10,
      selectedMode: false,
      textStyle: { color: "#a1a1aa", fontSize: 11 },
      data: legendNames,
    },
    grid: grids.map((item) => item.grid),
    xAxis,
    yAxis,
    axisPointer: { link: [{ xAxisIndex: "all" }], label: { backgroundColor: "#3f3f46" } },
    tooltip: {
      trigger: "axis",
      axisPointer: { type: "cross" },
      backgroundColor: "rgba(24,24,27,0.95)",
      borderColor: "#3f3f46",
      textStyle: { color: "#e4e4e7", fontSize: 12 },
      formatter: (params: unknown) => {
        const list = params as { dataIndex: number }[];
        if (!list?.length) return "";
        const index = list[0].dataIndex;
        const candle = candles[index];
        if (!candle) return "";

        const prev = candles[index - 1]?.close ?? candle.open;
        const change = prev ? ((candle.close - prev) / prev) * 100 : 0;
        const color = change > 0 ? UP : change < 0 ? DOWN : "#a1a1aa";

        const rows: string[] = [
          `<div style="font-weight:600;margin-bottom:4px">${formatDate(candle.date)}</div>`,
          `<div>开 <b>${candle.open.toFixed(2)}</b>　高 <b>${candle.high.toFixed(2)}</b></div>`,
          `<div>低 <b>${candle.low.toFixed(2)}</b>　收 <b>${candle.close.toFixed(2)}</b></div>`,
          `<div>涨跌 <b style="color:${color}">${change > 0 ? "+" : ""}${change.toFixed(2)}%</b></div>`,
          `<div>量 ${formatAmount(candle.volume)}　额 ${formatAmount(candle.amount)}</div>`,
        ];

        const maRows: string[] = [];
        for (const item of maCache) {
          const value = item.values[index];
          if (value === null || value === undefined) continue;
          maRows.push(`<span style="color:${item.color}">MA${item.period} ${value.toFixed(2)}</span>`);
        }
        if (maRows.length) {
          rows.push(`<div style="margin-top:4px">${maRows.join("　")}</div>`);
        }

        // 资金流
        if (hasFlow) {
          const value = flowMainNet[index];
          if (value !== null && value !== undefined) {
            const flowColor = value >= 0 ? UP : DOWN;
            const mean = flowMean[index];
            rows.push(
              `<div style="margin-top:4px">主力净流入 <b style="color:${flowColor}">${
                value > 0 ? "+" : value < 0 ? "-" : ""
              }${formatAmount(Math.abs(value))}</b>${
                mean !== null && mean !== undefined
                  ? `　<span style="color:${FLOW_MEAN_COLOR}">${FLOW_MEAN_WINDOW}期均 ${
                      mean > 0 ? "+" : mean < 0 ? "-" : ""
                    }${formatAmount(Math.abs(mean))}</span>`
                  : ""
              }</div>`,
            );
          }
        }
        return rows.join("");
      },
    },
    dataZoom: [
      {
        type: "inside",
        xAxisIndex: axisIndexes,
        start: zoom.value.start,
        end: zoom.value.end,
        // ⚠ **框选时必须禁用它**。两者都用"左键拖"：不禁用的话同一次拖拽会
        // 既拉出选框、又把图平移走，而且平移会连带触发 Fibonacci 重算，
        // 结果是框选过程中图一直在动。滑条（下面那个 slider）不受影响，
        // 所以开着框选也还能缩放/平移。
        disabled: props.showRangeSelect,
      },
      {
        type: "slider",
        xAxisIndex: axisIndexes,
        bottom: 10,
        height: 20,
        start: zoom.value.start,
        end: zoom.value.end,
        borderColor: "#3f3f46",
        fillerColor: "rgba(63,63,70,0.35)",
        handleStyle: { color: "#71717a" },
        textStyle: { color: "#a1a1aa", fontSize: 10 },
      },
    ],
    series,
  };
}

function render() {
  if (!chart) return;
  chart.setOption(buildOption(), { notMerge: true, lazyUpdate: false });
}

/** 重入保护：局部更新时若又收到 datazoom 事件，不要递归 */
let fibUpdating = false;

/**
 * 重入保护：清 brush 选框时会派发一次 brush 动作，
 * 那个动作不该再被当成"用户框选"处理。
 */
let brushUpdating = false;

/**
 * 只更新斐波那契辅助线（缩放/平移时调用）。
 *
 * 用 `notMerge: false` 做**局部** setOption：只替换主图 series 的 markLine，
 * 不动其它 series、也不动 dataZoom —— 否则平移会被自己重置，
 * 甚至"改缩放 → 触发 datazoom → 再改缩放"来回打架。
 */
function updateFibonacciOnly() {
  if (!chart || !props.showFibonacci || fibUpdating) return;
  fibUpdating = true;
  try {
    chart.setOption({ series: [{ markLine: fibonacciMarkLine() }] }, { notMerge: false });
  } finally {
    fibUpdating = false;
  }
}

/**
 * 把 brush 画的矩形折算成 K 线下标区间。
 *
 * 两个要点：
 *  1. **y 只用来判断"框有没有落在图上"** —— 统计的始终是 K 线本身，
 *     从成交量面板里框一段也应该得到那段时间的 K 线统计（用户是照着
 *     时间轴框的，不会在意框在上半张还是下半张）。所以对每个面板求
 *     y 方向交集，只要有交集就算命中；从图下方的滑条区域划过去则不算。
 *  2. **像素到数据的换算必须交给 ECharts**（`convertFromPixel`），
 *     自己按格子宽度除会漏掉 boundaryGap 与 dataZoom 的偏移。
 */
function rangeFromBrushArea(area: { x: number; y: number; width: number; height: number }): {
  from: number;
  to: number;
} | null {
  if (!chart) return null;
  const total = props.candles.length;
  if (total <= 0) return null;

  const grid = (chart as unknown as { getModel?: () => unknown }).getModel?.() as
    | { getComponent?: (type: string, index?: number) => { coordinateSystem?: { getBoundingRect?: () => unknown } } }
    | undefined;
  const rects: { x: number; y: number; width: number; height: number }[] = [];
  if (grid?.getComponent) {
    const grids = (chart.getOption() as { grid?: unknown[] } | undefined)?.grid ?? [];
    for (let index = 0; index < Math.max(1, grids.length); index++) {
      const component = grid.getComponent("grid", index);
      const rect = component?.coordinateSystem?.getBoundingRect?.() as
        | { x: number; y: number; width: number; height: number }
        | undefined;
      if (rect && Number.isFinite(rect.y) && rect.height > 0) rects.push(rect);
    }
  }
  // 拿不到面板几何就不能可靠折算 —— 宁可不给结果，也别给一个错的下标区间
  if (rects.length === 0) return null;

  // ⚠ 先确认"这个矩形确实碰到了某个面板"：所有 x 轴共用同一份时间数据，
  // 所以 x 的像素→数值换算只做一次；y 只用来判断框有没有落在图上
  // （用户从图表下方空白处划过去时不该产出区间）。
  const brushTop = area.y;
  const brushBottom = area.y + area.height;
  const hit = rects.some((rect) => {
    const top = Math.max(brushTop, rect.y);
    const bottom = Math.min(brushBottom, rect.y + rect.height);
    return bottom > top;
  });
  if (!hit) return null;

  const converted = chart.convertFromPixel({ xAxisIndex: 0 }, [
    area.x,
    area.x + area.width,
  ]) as unknown;
  if (!Array.isArray(converted) || converted.length < 2) return null;

  return selectionFromAxisValues(total, Number(converted[0]), Number(converted[1]));
}

/** 清掉 brush 的选框（我们自己用 markArea 画高亮，不需要它那层虚线框）。 */
function clearBrushVisual() {
  if (!chart || brushUpdating) return;
  brushUpdating = true;
  try {
    chart.dispatchAction({ type: "brush", areas: [] });
  } finally {
    brushUpdating = false;
  }
}

/**
 * 开关框选模式。
 *
 * 关掉时清掉 brush 的选框 —— 否则图上会留下一层看不懂的虚线框，
 * 而那时已经不再响应框选操作了。
 *
 * 开/关都要同步 **inside 缩放的禁用状态**：它与框选都用左键拖，
 * 不禁用就是"一边拉框一边把图拖走"。只派发 `disabled` 这一个字段
 * （不带 start/end），否则会把用户当前的缩放窗口一起重置掉。
 */
function applyRangeSelectMode() {
  if (!chart) return;
  chart.dispatchAction({
    type: "dataZoom",
    dataZoomIndex: 0,
    disabled: props.showRangeSelect,
  });
  if (!props.showRangeSelect) clearBrushVisual();
}

/** 数据变化时决定是否重置缩放窗口（换股票 / 换周期才重置） */
function applyResetIfNeeded() {
  const key = props.resetKey;
  if (key !== lastResetKey) {
    lastResetKey = key;
    const total = props.candles.length;
    if (total > 0) {
      const visible = Math.min(props.defaultVisibleBars, total);
      zoom.value = { start: Math.max(0, ((total - visible) / total) * 100), end: 100 };
    } else {
      zoom.value = { start: 0, end: 100 };
    }
  }
}

onMounted(() => {
  if (!container.value) return;
  chart = echarts.init(container.value, undefined, { renderer: "canvas" });
  chart.on("datazoom", () => {
    const option = chart?.getOption() as { dataZoom?: { start?: number; end?: number }[] } | undefined;
    const item = option?.dataZoom?.[0];
    if (item && typeof item.start === "number" && typeof item.end === "number") {
      zoom.value = { start: item.start, end: item.end };
    }
    // 分割位以"当前可见窗口"为区间，所以缩放/平移后必须重算
    updateFibonacciOnly();
  });

  // 框选完成（松手）。**只认 brushEnd**：brush 动作在拖拽过程中会连续触发，
  // 在那里更新统计会让整页在拖动时反复重算。
  chart.on("brushEnd", ((params: unknown) => {
    if (brushUpdating) return;
    const payload = params as {
      areas?: { x: number; y: number; width: number; height: number }[];
    };
    const areas = payload?.areas ?? [];
    if (areas.length === 0) {
      // 内置清空把选框清掉了 → 通知父组件收起统计面板
      emit("rangeCleared");
      return;
    }
    const area = areas[0];
    const range = rangeFromBrushArea(area);
    // 框得太小 / 落在图外：不产生区间，也不清掉已有统计（用户可能只是手抖点了一下）
    if (range) emit("rangeSelected", range);
  }) as unknown as (params: unknown) => void);

  // 双击取消框选。
  //
  // brush 自己**不带**这个行为：ECharts 源码里 `removeOnClick` 默认 false，
  // 注释写明"单击不清是因为用户可能随手点一下，不希望框就这么没了"，
  // 所以"点一下清掉"的直觉在这里并不成立，双击得我们自己接。
  chart.getZr().on("dblclick", () => {
    if (!props.showRangeSelect) return;
    clearBrushVisual();
    emit("rangeCleared");
  });

  observer = new ResizeObserver(() => {
    chart?.resize();
    render();
  });
  observer.observe(container.value);

  applyResetIfNeeded();
  render();
  applyRangeSelectMode();
});

onBeforeUnmount(() => {
  observer?.disconnect();
  observer = null;
  chart?.dispose();
  chart = null;
});

// 蜡烛数组只比较引用（换股票/换周期会重建数组），避免对上万根 K 线做 deep watch
watch(
  () => props.candles,
  () => {
    applyResetIfNeeded();
    render();
  },
);

watch(
  () => [props.showBoll, props.showMacd, props.showVolume, props.showFlow, props.showFibonacci, props.period],
  () => render(),
);

// 框选开关：开时把指针层打开并复位上一次选框，关时收起
watch(
  () => props.showRangeSelect,
  () => applyRangeSelectMode(),
);

// 选中区间变化（含被清空）：整图重绘，但 dataZoom 用的是我们一直在同步的
// `zoom.value`，所以缩放窗口不会被重置（这正是 start/end 一直写在 option 里的原因）
watch(
  () => props.rangeSelection,
  () => render(),
);

// 资金流数据到达 / 变化时重绘（保留缩放）
watch(
  () => props.moneyFlows,
  () => render(),
);

// 布林线 / MACD 参数变化需要重算指标，但保留当前缩放
watch(
  () => [props.bollConfig, props.macdConfig],
  () => render(),
  { deep: true },
);

// 均线配置是小数组，可以 deep watch，允许父组件原地改属性
watch(
  () => props.maConfigs,
  () => render(),
  { deep: true },
);

const isEmpty = computed(() => props.candles.length === 0);
</script>

<template>
  <div class="relative h-full w-full">
    <div ref="container" class="h-full w-full" />

    <!--
      框选模式的**装饰层**：`pointer-events: none`，不吃任何事件。

      为什么不做成"拦住事件的透明层"：brush 走的是 zrender 在 canvas 上的
      mousedown，拦掉指针事件就等于把框选本身也拦掉了。真正要解决的
      "拖拽同时平移"是靠**禁用 inside 缩放**（见 buildOption 的 dataZoom 与
      applyRangeSelectMode），而不是靠挡事件。

      它只负责在鼠标移进图表区域时给出"可以拉框"的十字光标 ——
      canvas 自己不会因为这个模式把光标换掉（那需要另外的 zrender 光标管理）。
      双击取消框选绑在 zrender 上（见 onMounted），与这层无关。
    -->
    <div
      v-if="showRangeSelect && !isEmpty"
      class="pointer-events-none absolute inset-0 z-10 cursor-crosshair"
      title="按住左键拖出一个矩形来统计这一段（双击取消）"
    />

    <div
      v-if="isEmpty"
      class="text-muted-foreground absolute inset-0 flex items-center justify-center text-sm"
    >
      没有可显示的 K 线数据
    </div>
  </div>
</template>
