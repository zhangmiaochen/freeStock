<script setup lang="ts">
/**
 * 板块热力图（ECharts treemap）。
 *
 * 面积 = 当日成交额，颜色 = **所选周期**的涨跌幅。用 treemap 而不是树图/柱图，
 * 是因为它能在有限空间里同时表达「体量」和「涨跌」两个维度，
 * 这也是行情软件里板块页的通行做法。
 *
 * 面积固定用当日成交额（体量是"当前热度"，不该随回看周期变化），
 * 颜色跟着周期走 —— 所以换周期时方块的大小不变、颜色整体换一套。
 */
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from "vue";
import { BarChart3 } from "@lucide/vue";
import * as echarts from "echarts/core";
import { TreemapChart } from "echarts/charts";
import { TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";

import type { BoardPeriodWindow, BoardQuote } from "@/lib/backend";
import { formatAmount, formatPercent } from "@/lib/format";
import { heatColor, heatTextColor } from "@/lib/heat";
import { DEFAULT_BOARD_PERIOD, periodLabel, periodPct, periodWindow, periodWindowText } from "@/lib/boardPeriod";

echarts.use([TreemapChart, TooltipComponent, CanvasRenderer]);

const props = defineProps<{
  boards: BoardQuote[];
  /** 最多显示多少个板块（按成交额取前 N） */
  limit?: number;
  /** 颜色按哪个周期着色（交易日；1 = 当日） */
  periodDays?: number;
  /** 各周期的日历窗口（用于悬停提示） */
  windows?: BoardPeriodWindow[];
}>();

const emit = defineEmits<{ select: [BoardQuote] }>();

const container = ref<HTMLElement | null>(null);
const chart = shallowRef<echarts.ECharts | null>(null);
let observer: ResizeObserver | null = null;

/** 当前着色用的周期（默认 5 日） */
function days(): number {
  return props.periodDays ?? DEFAULT_BOARD_PERIOD;
}

/** 板块在着色周期上的涨跌幅；undefined 表示这个周期算不出来（显示「—」）。 */
function pctValue(board: BoardQuote): number | undefined {
  return periodPct(board, days());
}

/** 着色用的数值：算不出来时按 0（中性色），提示与标签里会显示「—」。 */
function pct(board: BoardQuote): number {
  return pctValue(board) ?? 0;
}

/** 标签上的涨跌幅：算不出来时必须是「—」，否则 0% 会被读成"没涨没跌"。 */
function pctText(board: BoardQuote): string {
  const value = pctValue(board);
  return value === undefined ? "—" : formatPercent(value);
}

/** 实际参与渲染的板块：按成交额取前 N，太小的板块在热力图里只是噪声 */
function visibleBoards(): BoardQuote[] {
  const limit = props.limit ?? 60;
  const sorted = [...props.boards].sort((a, b) => b.amount - a.amount);
  return sorted.slice(0, limit);
}

function tooltipOf(board: BoardQuote): string {
  const period = days();
  const value = periodPct(board, period);
  const label = periodLabel(period);
  const color = value === undefined || value === 0 ? "#a1a1aa" : value > 0 ? "#ef4444" : "#22c55e";
  const window = period === 1 ? undefined : periodWindow(props.windows, period);
  const leader = (period === 1 ? board.leader : board.periods?.find((item) => item.days === period)?.leader) ?? board.leader;
  const leaderText = leader?.name
    ? `${leader.name} <b style="color:#ef4444">${formatPercent(leader.pctChg)}</b>`
    : "—";

  const rows = [
    `<div style="font-weight:600;margin-bottom:4px">${board.name}</div>`,
    `<div>${label} <b style="color:${color}">${value === undefined ? "—" : formatPercent(value)}</b>　当日 <b>${formatPercent(board.pctChg)}</b></div>`,
    `<div>当日成交额 <b>${formatAmount(board.amount)}</b>　换手 ${board.turnover.toFixed(2)}%</div>`,
    `<div>成分 <b>${board.memberCount}</b> 只　当日涨/跌 <b style="color:#ef4444">${board.upCount}</b>/<b style="color:#22c55e">${board.downCount}</b></div>`,
    `<div>区间领涨 ${leaderText}</div>`,
  ];
  if (window) {
    rows.push(`<div style="color:#a1a1aa;margin-top:4px">${periodWindowText(window)}</div>`);
  }
  return rows.join("");
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
        return tooltipOf(board);
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
            return `{name|${board.name}}\n{pct|${pctText(board)}}`;
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
            color: heatColor(pct(board)),
            borderColor: "#18181b",
          },
          label: { color: heatTextColor(pct(board)) },
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
// 换周期只换颜色与提示，方块面积（当日成交额）不变
watch(() => props.periodDays, render);
watch(() => props.windows, render, { deep: false });
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
      面积＝当日成交额，颜色＝{{ periodLabel(periodDays ?? DEFAULT_BOARD_PERIOD) }}涨跌幅；
      按成交额显示前 {{ limit ?? 60 }} 个板块（共 {{ boards.length }} 个）
    </p>
  </div>
</template>
