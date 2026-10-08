<script setup lang="ts">
/**
 * 板块热力图（ECharts treemap）。
 *
 * 面积 = 成交额，颜色 = 涨跌幅。用 treemap 而不是树图/柱图，
 * 是因为它能在有限空间里同时表达「体量」和「涨跌」两个维度，
 * 这也是行情软件里板块页的通行做法。
 */
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { BarChart3 } from "@lucide/vue";
import * as echarts from "echarts/core";
import { TreemapChart } from "echarts/charts";
import { TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";

import type { BoardQuote } from "@/lib/backend";
import { formatAmount, formatPercent } from "@/lib/format";
import { heatColor, heatTextColor } from "@/lib/heat";

echarts.use([TreemapChart, TooltipComponent, CanvasRenderer]);

const props = defineProps<{
  boards: BoardQuote[];
  /** 最多显示多少个板块（按成交额取前 N） */
  limit?: number;
}>();

const emit = defineEmits<{ select: [BoardQuote] }>();

const container = ref<HTMLElement | null>(null);
const chart = shallowRef<echarts.ECharts | null>(null);
let observer: ResizeObserver | null = null;

/** 实际参与渲染的板块：按成交额取前 N，太小的板块在热力图里只是噪声 */
function visibleBoards(): BoardQuote[] {
  const limit = props.limit ?? 60;
  const sorted = [...props.boards].sort((a, b) => b.amount - a.amount);
  return sorted.slice(0, limit);
}

function buildOption(): echarts.EChartsCoreOption {
  const items = visibleBoards();
  return {
    animation: false,
    backgroundColor: "transparent",
    tooltip: {
      backgroundColor: "rgba(24,24,27,0.96)",
      borderColor: "#3f3f46",
      textStyle: { color: "#e4e4e7", fontSize: 12 },
      formatter: (info: { data?: { board?: BoardQuote } }) => {
        const board = info.data?.board;
        if (!board) return "";
        const color = board.pctChg > 0 ? "#ef4444" : board.pctChg < 0 ? "#22c55e" : "#a1a1aa";
        const leader = board.leader?.name
          ? `${board.leader.name} <b style="color:#ef4444">${formatPercent(board.leader.pctChg)}</b>`
          : "—";
        return [
          `<div style="font-weight:600;margin-bottom:4px">${board.name}</div>`,
          `<div>加权涨跌 <b style="color:${color}">${formatPercent(board.pctChg)}</b>　等权 ${formatPercent(board.pctChgEqual)}</div>`,
          `<div>成交额 <b>${formatAmount(board.amount)}</b>　换手 ${board.turnover.toFixed(2)}%</div>`,
          `<div>成分 <b>${board.memberCount}</b> 只　涨/跌 <b style="color:#ef4444">${board.upCount}</b>/<b style="color:#22c55e">${board.downCount}</b></div>`,
          `<div>领涨 ${leader}</div>`,
        ].join("");
      },
    },
    series: [
      {
        type: "treemap",
        roam: false,
        nodeClick: false,
        breadcrumb: { show: false },
        width: "100%",
        height: "100%",
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        itemStyle: { borderColor: "#18181b", borderWidth: 2, gapWidth: 2 },
        label: {
          show: true,
          position: "inside",
          formatter: (params: { data?: { board?: BoardQuote } }) => {
            const board = params.data?.board;
            if (!board) return "";
            return `{name|${board.name}}\n{pct|${formatPercent(board.pctChg)}}`;
          },
          rich: {
            name: { fontSize: 12, fontWeight: 600, lineHeight: 16 },
            pct: { fontSize: 11, lineHeight: 15 },
          },
        },
        upperLabel: { show: false },
        data: items.map((board) => ({
          name: board.name,
          // value 决定面积；成交额为 0 的给个极小值，否则会被挤成看不见的细条
          value: Math.max(board.amount, 1),
          board,
          itemStyle: {
            color: heatColor(board.pctChg),
            borderColor: "#18181b",
          },
          label: { color: heatTextColor(board.pctChg) },
        })),
      },
    ],
  };
}

function render() {
  if (!chart.value || !container.value) return;
  if (props.boards.length === 0) {
    chart.value.clear();
    return;
  }
  // 关掉默认的 setOption 合并行为，否则板块变少时旧方块会残留
  chart.value.setOption(buildOption(), { notMerge: true });
}

onMounted(() => {
  if (!container.value) return;
  chart.value = echarts.init(container.value, undefined, { renderer: "canvas" });
  // ECharts 的点击回调参数类型很宽（ECElementEvent），这里只取我们要的 data
  chart.value.on("click", (params) => {
    const board = (params as { data?: { board?: BoardQuote } }).data?.board;
    if (board) emit("select", board);
  });
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

watch(() => props.boards, render, { deep: false });
watch(() => props.limit, render);
</script>

<template>
  <div class="bg-card relative h-full w-full rounded-lg border">
    <div v-if="boards.length === 0" class="text-muted-foreground absolute inset-0 flex flex-col items-center justify-center gap-2 text-sm">
      <BarChart3 class="size-5" />
      <span>没有可用的板块行情</span>
    </div>
    <div v-else ref="container" class="h-full w-full" />
    <p
      v-if="boards.length > (limit ?? 60)"
      class="text-muted-foreground pointer-events-none absolute right-3 bottom-2 text-[11px]"
    >
      按成交额显示前 {{ limit ?? 60 }} 个板块（共 {{ boards.length }} 个）
    </p>
  </div>
</template>
