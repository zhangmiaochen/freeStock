<script setup lang="ts">
/**
 * 8 大指数走势对比（ECharts 折线，归一化到窗口首日 = 100）。
 *
 * 为什么要归一化：上证指数 3800 点和创业板指 3000 点没有可比性，
 * 直接画在一张图上只有点位高的那几条看得见。归一化之后比的是**涨跌幅**，
 * 这正是"最近走势"要回答的问题。
 */
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { LineChart } from "echarts/charts";
import { GridComponent, LegendComponent, TooltipComponent } from "echarts/components";
import * as echarts from "echarts/core";
import { CanvasRenderer } from "echarts/renderers";

import type { MarketIndexStat } from "@/lib/backend";
import { formatDate, formatPercent } from "@/lib/format";
import { indexColor } from "@/lib/temperature";

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer]);

const props = defineProps<{
  dates: number[];
  indices: MarketIndexStat[];
  /** 窗口交易日数（提示文案用） */
  window: number;
  /** 所选周期标签 */
  periodLabel: string;
}>();

const container = ref<HTMLElement | null>(null);
const chart = shallowRef<echarts.ECharts | null>(null);
let observer: ResizeObserver | null = null;

function buildOption(): echarts.EChartsCoreOption {
  const dates = props.dates ?? [];
  const labels = dates.map((date) => formatDate(date));

  return {
    animation: false,
    backgroundColor: "transparent",
    grid: { left: 48, right: 16, top: 34, bottom: 40 },
    legend: {
      type: "scroll",
      top: 0,
      textStyle: { color: "#a1a1aa", fontSize: 11 },
      itemWidth: 14,
      itemHeight: 8,
    },
    tooltip: {
      trigger: "axis",
      backgroundColor: "rgba(24,24,27,0.96)",
      borderColor: "#3f3f46",
      textStyle: { color: "#e4e4e7", fontSize: 12 },
      valueFormatter: (value: unknown) => (typeof value === "number" ? formatPercent(value - 100) : "—"),
    },
    xAxis: {
      type: "category",
      data: labels,
      axisLine: { lineStyle: { color: "#3f3f46" } },
      axisLabel: { color: "#71717a", fontSize: 10 },
      boundaryGap: false,
    },
    yAxis: {
      type: "value",
      scale: true,
      axisLabel: {
        color: "#71717a",
        fontSize: 10,
        formatter: (value: number) => `${(value - 100).toFixed(1)}%`,
      },
      splitLine: { lineStyle: { color: "rgba(113,113,122,0.2)" } },
    },
    series: (props.indices ?? []).map((item, i) => ({
      name: item.name,
      type: "line",
      showSymbol: false,
      smooth: false,
      lineStyle: { width: 1.6, color: indexColor(i) },
      itemStyle: { color: indexColor(i) },
      emphasis: { focus: "series" },
      data: item.norm ?? [],
    })),
  };
}

function render() {
  if (!chart.value) return;
  if (!props.indices?.length || !props.dates?.length) {
    chart.value.clear();
    return;
  }
  // notMerge：切换周期后指数序列可能变少，合并会留下旧线
  chart.value.setOption(buildOption(), { notMerge: true });
}

onMounted(() => {
  if (!container.value) return;
  chart.value = echarts.init(container.value, undefined, { renderer: "canvas" });
  observer = new ResizeObserver(() => chart.value?.resize());
  observer.observe(container.value);
  render();
});

onBeforeUnmount(() => {
  observer?.disconnect();
  observer = null;
  chart.value?.dispose();
  chart.value = null;
});

watch(() => [props.dates, props.indices], render, { deep: false });
</script>

<template>
  <section class="bg-card flex h-full flex-col rounded-lg border">
    <header class="flex items-baseline gap-2 border-b px-4 py-2">
      <span class="text-sm font-medium">指数走势对比</span>
      <span class="text-muted-foreground text-[11px]">
        最近 {{ window }} 个交易日 · 以窗口首日 = 100 归一化（比的是涨跌幅，不是点位）
      </span>
      <span class="text-muted-foreground ml-auto text-[11px]">周期 {{ periodLabel }}</span>
    </header>
    <div ref="container" class="h-[300px] w-full" />
  </section>
</template>
