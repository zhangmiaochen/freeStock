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
import type { ECharts, EChartsCoreOption } from "echarts/core";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";

import { echarts } from "@/lib/echarts";

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
import { buildChanlunLayers, signalsOnDate } from "@/lib/chanlun";
import { formatAmount, formatDate } from "@/lib/format";
import type { ChanlunAnalysis } from "@/lib/backend";
import {
  axisValuesFromCoordRange,
  selectionFromAxisValues,
  type RangeSelection,
} from "@/lib/rangeStats";

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
    /**
     * 缠论标注（后端分析结果）。
     *
     * **图只消费数据**：分型/笔/线段/中枢/背驰/买卖点都已经在 `internal/chanlun`
     * 里算好，这里只把它们映射成 ECharts 图层（见 lib/chanlun.ts）。
     * 传空时与原图表完全一致。
     */
    chanlun?: ChanlunAnalysis | null;
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
    chanlun: null,
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
/**
 * 框选选框的样式。
 *
 * ⚠ **字段名必须是 zrender 的 `fill` / `stroke` / `lineWidth`**，
 * 不是 canvas 那套 `color` / `borderColor`。写错了**不报错、也不生效**，
 * 选框会一直用 ECharts 的默认样式（浅灰填充），而那层填充会把 K 线盖住。
 *
 * 填充给得极淡：选框要能看清，但不能挡图 —— 常驻高亮由 markArea 负责，
 * 它只盖主图、颜色也更轻。
 */
const BRUSH_STYLE = { lineWidth: 1.5, stroke: "#3b82f6", fill: "rgba(59,130,246,0.08)" };
/** markArea 的常驻高亮（比选框更淡） */
const HIGHLIGHT_FILL = "rgba(59,130,246,0.12)";
const HIGHLIGHT_BORDER = "#3b82f6";

const container = ref<HTMLElement | null>(null);
let chart: ECharts | null = null;
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
    data: [
      [
        {
          xAxis: start.date,
          itemStyle: { color: HIGHLIGHT_FILL, borderColor: HIGHLIGHT_BORDER, borderWidth: 1 },
          label: { show: false },
        },
        { xAxis: end.date },
      ],
    ],
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

function buildOption(): EChartsCoreOption {
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

  // 缠论图层（关闭时为空；标注全部由后端给出，这里只做映射）
  const chanlunLayers = buildChanlunLayers(candles, props.chanlun);

  // 主图的高亮区域：框选统计 + 缠论中枢/背驰段。
  // 两者都用 markArea：框选那层只盖主图，缠论那层也只盖主图，合并成一个 series 更省事。
  const markAreaData: unknown[] = [];
  const highlight = rangeHighlight();
  if (highlight) markAreaData.push(...(highlight.data as unknown[]));
  markAreaData.push(...chanlunLayers.markAreas);

  // 主图：蜡烛（挂斐波那契辅助线 + 框选/缠论高亮）
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
    ...(markAreaData.length
      ? { markArea: { silent: true, animation: false, data: markAreaData } }
      : {}),
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

  // 缠论：笔 / 线段 / 分型 / 买卖点（主图叠加，全部 silent，不抢十字光标）
  for (const item of chanlunLayers.series) {
    series.push(item);
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
  // 缠论的笔 / 线段 / 买卖点也进图例，作为颜色对照
  for (const name of chanlunLayers.legendNames) {
    if (!legendNames.includes(name)) legendNames.push(name);
  }

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
      brushStyle: BRUSH_STYLE,
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

        // 缠论：把这一根 K 线上的信号直接写进悬浮提示，
        // 图上的标签只放类型名（避免遮挡），依据放到提示与右侧面板里。
        //
        // ⚠ 提示里的日期是**这根 K 线的日期**（信号的形成/触发点），不是确认日；
        // 而选股结果里显示的是"确认/触发/形成"中最贴近当前状态的那一天。
        // 两者是**同一条信号的不同阶段**，所以这里把三个阶段都写出来。
        const chanlunSignals = signalsOnDate(props.chanlun, candle.date);
        for (const signal of chanlunSignals) {
          const color = signal.direction === "buy" ? UP : DOWN;
          const when: string[] = [];
          if (signal.formDate) when.push(`形成 ${formatDate(signal.formDate)}`);
          if (signal.triggerDate && signal.triggerDate !== signal.formDate) {
            when.push(`触发 ${formatDate(signal.triggerDate)}`);
          }
          if (signal.confirmDate) when.push(`确认 ${formatDate(signal.confirmDate)}`);
          rows.push(
            `<div style="margin-top:4px;color:${color}">${signal.typeCN} · ${signal.stateCN}` +
              (when.length ? `　${when.join(" · ")}` : "") +
              `　综合分 ${signal.score.toFixed(1)}</div>`,
          );
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
  try {
    chart.setOption(buildOption(), { notMerge: true, lazyUpdate: false });
  } catch (err) {
    // ECharts 的 update 抛错后会把内部「正在更新」的标志留在 true 上，
    // 之后所有 setOption / resize 都会静默失效。这里重建实例把它救回来。
    // 最常见的原因是 series / 组件没有在 lib/echarts.ts 里注册。
    console.error(
      "[KlineChart] 图表更新失败，重建 ECharts 实例。请检查 lib/echarts.ts 是否注册了用到的 series/组件",
      err,
    );
    resetChart();
    return;
  }
  // ⚠ 每次都同步一次画笔状态：notMerge 会重建 brush 视图与 controller，
  // 而"光写 option"并不会真的启用/停用画笔（见 enableBrush / disableBrush）
  syncBrushEnabled();
}

/**
 * 让画笔的启用状态与 `showRangeSelect` 对齐。
 *
 * 开：补一次 `enableBrush`；关：`enableBrush(false)`。
 * **两个方向都要做** —— 只在开的时候 enable，关闭后拖拽照样会出框。
 */
function syncBrushEnabled() {
  if (props.showRangeSelect) enableBrush();
  else disableBrush();
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
 * 把 brush 给出的选区折算成 K 线下标区间。
 *
 * brush 事件里 `areas[i]` 的**真实结构**（实测 Chromium + ECharts 6.1.0）：
 *
 * ```js
 * {
 *   brushType: 'rect',
 *   panelId: 'grid--\0series\00\00',
 *   range:      [[200, 500], [150, 280]],   // 像素
 *   coordRange: [[20, 64], [139.37, 187.26]], // 轴数据坐标 ← 要的就是它
 *   coordRanges: [...], __rangeOffset: {...},
 * }
 * ```
 *
 * ⚠ **它没有 `x` / `y` / `width` / `height`**。早先这里按 `area.y`、
 * `area.height` 做过一个"框有没有落在图上"的判断 —— 那两个字段是 `undefined`，
 * 于是算术结果是 `NaN`、判断恒为假，**每一次框选都被拒掉**，
 * 表现就是"框了但统计永远不出来"。这个函数现在只认 `coordRange`。
 *
 * ⚠ 也**不要自己用 `convertFromPixel` 换算**：实测
 * `chart.convertFromPixel({ xAxisIndex: 0 }, [px, px])` 返回 `null`。
 * 而 `coordRange` 本来就是轴数据坐标，对 category 轴就是下标，
 * 且天然跟着 dataZoom 走（缩放后同一像素位置给出不同下标）。
 *
 * 也不需要用 y 去判断"框在哪个面板"：面板与时间轴一一对应，
 * 从成交量面板里框一段本来就该得到那段时间的 K 线统计。
 */
function rangeFromBrushArea(area: { coordRange?: unknown }): {
  from: number;
  to: number;
} | null {
  if (!chart) return null;
  const total = props.candles.length;
  if (total <= 0) return null;

  const values = axisValuesFromCoordRange(area.coordRange);
  if (!values) return null;

  return selectionFromAxisValues(total, values[0], values[1]);
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

/** 拿到 brush 的 controller（拿不到就返回 null）。 */
function brushController(): { enableBrush?: (option: unknown) => void } | null {
  if (!chart) return null;
  const model = (
    chart as unknown as { getModel?: () => { getComponent?: (type: string) => unknown } }
  ).getModel?.();
  const brushModel = model?.getComponent?.("brush");
  if (!brushModel) return null;

  const view = (
    chart as unknown as { getViewOfComponentModel?: (m: unknown) => unknown }
  ).getViewOfComponentModel?.(brushModel) as { _brushController?: unknown } | undefined;
  const controller = view?._brushController as { enableBrush?: (option: unknown) => void } | undefined;
  return controller ?? null;
}

/**
 * 真正把 brush "打开"。
 *
 * ⚠ **只写 `option.brush` 是不够的 —— 框选不会有任何反应**（实测 Chromium +
 * ECharts 6.1.0）。原因在 ECharts 内部：
 *
 *   - `BrushView._updateController` 会调用
 *     `brushController.enableBrush(brushModel.brushOption)`；
 *   - 而 `brushModel.brushOption` 只在 `setAreas` / `setBrushOption` 里才被赋值
 *     （`getInitialData` 里那一行在 ECharts 源码中是注释掉的）——
 *     **光写 option 时它一直是 `{}`**；
 *   - 于是 `enableBrush({brushType: undefined})` 里的
 *     `brushOption.brushType && this._doEnableBrush(...)` 为假，
 *     画笔永远不启用：`_brushType` 是 undefined、面板也没注册，
 *     拖拽时连 mousedown 都不会被处理。
 *
 * 表现形式极具误导性：`getOption().brush` 里字段一切正常、图表也不报错、
 * 只是拖拽毫无反应。**官方示例一律走 toolbox（`toolbox.feature.brush`
 * 会去 update 一次当前 brushOption）**，正是这个原因。
 *
 * 所以这里显式补这一步，并且 `render()` 之后每次都要补
 * （`notMerge: true` 会重建视图与 controller）。
 */
function enableBrush() {
  brushController()?.enableBrush?.({
    brushType: "rect",
    brushMode: "single",
    transformable: false,
    brushStyle: BRUSH_STYLE,
  });
}

/**
 * 关掉 brush。
 *
 * ⚠ **必须显式关**：`enableBrush` 会把画笔一直留着（`_doEnableBrush` 里
 * 只是往 zr 上注册处理器），光靠"下次 render 不再 enable"是关不掉的 ——
 * 那样关闭开关后拖拽照样会出框，只是我们的面板不显示，看起来像"拖了没反应"。
 * ECharts 里对应的开关就是 `enableBrush(false)`。
 */
function disableBrush() {
  brushController()?.enableBrush?.(false);
}

/**
 * 开关框选模式。
 *
 * 打开时补一次 `enableBrush`（render 里也会同步，但用户可能只点了开关、
 * 数据没变因而没有重绘）；关闭时**显式停用画笔**并清掉选框 ——
 * 只清选框是不够的，画笔还开着，拖拽照样出框。
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
  syncBrushEnabled();
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
  bindChartEvents();

  observer = new ResizeObserver(() => {
    // 尺寸以容器的实际布局为准。**不要**只调一次 resize 就走：
    // ECharts 内部一旦有过一次 update 抛错，会把"正在更新"的标志留在 true 上，
    // 之后所有 resize 直接 return（表现为"窗口缩放图不跟着变"）。
    // 这里用"resize 之后尺寸是否真的等于容器"来判断，必要时重建实例。
    const width = container.value?.clientWidth ?? 0;
    const height = container.value?.clientHeight ?? 0;
    if (chart && width > 0 && height > 0) {
      chart.resize();
      if (Math.abs(chart.getWidth() - width) > 1 || Math.abs(chart.getHeight() - height) > 1) {
        console.warn("[KlineChart] 图表尺寸未能跟随容器，重建 ECharts 实例");
        resetChart();
        return;
      }
    }
    render();
  });
  observer.observe(container.value);

  applyResetIfNeeded();
  render();
  applyRangeSelectMode();
  // 再兜一次：applyRangeSelectMode 里的 dataZoom 动作也会触发 brush 视图重绘，
  // 顺序上必须先让它跑完（见上面 datazoom 监听里的说明）
  syncBrushEnabled();
});

/** 绑定与图表实例相关的事件（重建实例后要重新绑一次）。 */
function bindChartEvents() {
  if (!chart) return;

  chart.on("datazoom", () => {
    const option = chart?.getOption() as { dataZoom?: { start?: number; end?: number }[] } | undefined;
    const item = option?.dataZoom?.[0];
    if (item && typeof item.start === "number" && typeof item.end === "number") {
      zoom.value = { start: item.start, end: item.end };
    }
    // 分割位以"当前可见窗口"为区间，所以缩放/平移后必须重算
    updateFibonacciOnly();
    // ⚠ **缩放会把画笔关掉**：dataZoom 一变，BrushView 就会重绘并调用
    // `enableBrush(brushModel.brushOption)`，而那里是 `{}` ——
    // `enableBrush({})` 会把已启用的画笔**停用**（实测 _brushType 由 rect 变 null）。
    // 不管的话，用户一拖滑条/滚轮缩放，框选就再也不响应了。
    syncBrushEnabled();
  });

  // 框选完成（松手）。**只认 brushEnd**：brush 动作在拖拽过程中会连续触发，
  // 在那里更新统计会让整页在拖动时反复重算。
  chart.on("brushEnd", ((params: unknown) => {
    if (brushUpdating) return;
    // ⚠ 只有 `coordRange` 是我们要的；`range` 是像素、没有 x/y/width/height
    const payload = params as { areas?: { coordRange?: unknown }[] };
    const areas = payload?.areas ?? [];
    if (areas.length === 0) {
      // 内置清空把选框清掉了 → 通知父组件收起统计面板
      emit("rangeCleared");
      return;
    }
    const range = rangeFromBrushArea(areas[0]);
    // 框得太小 / 取不到轴上坐标：不产生区间，也不清掉已有统计
    // （用户可能只是手抖点了一下）
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
}

/** 重建 ECharts 实例（尺寸不跟随 / 更新抛错后的兜底）。 */
let rebuilding = false;
function resetChart() {
  if (!container.value || rebuilding) return;
  rebuilding = true;
  try {
    chart?.dispose();
    chart = null;
    container.value.innerHTML = "";
    chart = echarts.init(container.value, undefined, { renderer: "canvas" });
    bindChartEvents();
    applyResetIfNeeded();
    render();
    applyRangeSelectMode();
    syncBrushEnabled();
  } finally {
    rebuilding = false;
  }
}

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

// 缠论标注到达 / 变化时重绘（保留当前缩放）
watch(
  () => props.chanlun,
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
