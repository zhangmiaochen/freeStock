<script setup lang="ts">
/**
 * K 线图表（ECharts）：主图（蜡烛 + 均线 + 布林）、成交量、MACD 三个面板联动。
 *
 * 只负责画图：均线配置、指标开关都由父组件传入，指标计算走 lib/indicators。
 */
import { BarChart, CandlestickChart, LineChart } from "echarts/charts";
import {
  AxisPointerComponent,
  DataZoomComponent,
  GridComponent,
  LegendComponent,
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
import { formatAmount, formatDate } from "@/lib/format";

echarts.use([
  CandlestickChart,
  LineChart,
  BarChart,
  GridComponent,
  TooltipComponent,
  DataZoomComponent,
  AxisPointerComponent,
  LegendComponent,
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
    moneyFlows: () => [],
    bollConfig: () => ({ ...DEFAULT_BOLL }),
    macdConfig: () => ({ ...DEFAULT_MACD }),
    resetKey: "",
    defaultVisibleBars: 160,
  },
);

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

  // 主图：蜡烛
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
      { type: "inside", xAxisIndex: axisIndexes, start: zoom.value.start, end: zoom.value.end },
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
  });

  observer = new ResizeObserver(() => {
    chart?.resize();
    render();
  });
  observer.observe(container.value);

  applyResetIfNeeded();
  render();
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
  () => [props.showBoll, props.showMacd, props.showVolume, props.showFlow, props.period],
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
    <div
      v-if="isEmpty"
      class="text-muted-foreground absolute inset-0 flex items-center justify-center text-sm"
    >
      没有可显示的 K 线数据
    </div>
  </div>
</template>
