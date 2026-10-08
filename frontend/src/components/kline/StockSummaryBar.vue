<script setup lang="ts">
/**
 * K 线页面上方的股票简要信息。
 *
 * 数据说明（很重要）：
 *   - 「估值」「分红」来自 free-stockdb 的日K与复权表，是原始披露数据。
 *   - 「财务（推导）」里的 ROE / 每股收益 / 每股净资产 / 净利润 / 净资产
 *     **不是财报表读数**，而是用日K自带的 PE_TTM 与 PB 反推出来的 TTM 口径值：
 *         ROE = EPS / BPS = (价/PE) ÷ (价/PB) = PB ÷ PE
 *         净利润(TTM) = 总市值 ÷ PE_TTM      净资产 = 总市值 ÷ PB
 *     好处是全市场逐日更新、零额外数据源；代价是看不到营收/毛利率等科目。
 *   - 「资金流」来自 free-stockdb 的「资金流」表。
 *
 * 版式刻意做得紧凑：图表才是主角，信息栏每行只用一行高度。
 */
import { computed, ref } from "vue";

import { Badge } from "@/components/ui/badge";
import type { StockProfile } from "@/lib/backend";
import {
  formatAmount,
  formatDate,
  formatLargeNumber,
  formatPercent,
  formatPrice,
  formatTurnover,
  isFundCode,
  trendClass,
} from "@/lib/format";

const props = defineProps<{ profile: StockProfile }>();

/** 板块默认只显示前几个，避免概念多的股票把信息栏撑得很长 */
const BOARD_PREVIEW = 6;
const showAllBoards = ref(false);
const visibleBoards = computed(() => {
  const boards = props.profile.boards ?? [];
  return showAllBoards.value ? boards : boards.slice(0, BOARD_PREVIEW);
});
const hiddenBoardCount = computed(() =>
  Math.max(0, (props.profile.boards?.length ?? 0) - BOARD_PREVIEW),
);


interface StatItem {
  label: string;
  value: string;
  title?: string;
  /** 资金流这类需要按正负着色的项 */
  trend?: number;
}

function dash(value: number, format: (v: number) => string): string {
  return Number.isFinite(value) && value !== 0 ? format(value) : "—";
}

const isFund = computed(() => isFundCode(props.profile.code));

const priceText = computed(() => formatPrice(props.profile.close, isFund.value));

const changeText = computed(() => {
  const diff = props.profile.close - props.profile.preClose;
  if (!Number.isFinite(diff) || !props.profile.preClose) return "—";
  return `${diff > 0 ? "+" : ""}${diff.toFixed(isFund.value ? 3 : 2)}`;
});

/** 当日行情 */
const quoteStats = computed<StatItem[]>(() => [
  { label: "今开", value: dash(props.profile.open, (v) => formatPrice(v, isFund.value)) },
  { label: "最高", value: dash(props.profile.high, (v) => formatPrice(v, isFund.value)) },
  { label: "最低", value: dash(props.profile.low, (v) => formatPrice(v, isFund.value)) },
  { label: "昨收", value: dash(props.profile.preClose, (v) => formatPrice(v, isFund.value)) },
  { label: "振幅", value: dash(props.profile.amplitude, (v) => `${v.toFixed(2)}%`) },
  { label: "成交量", value: dash(props.profile.volume, formatAmount) },
  { label: "成交额", value: dash(props.profile.amount, formatAmount) },
  { label: "换手率", value: formatTurnover(props.profile.turnover) },
  { label: "量比", value: dash(props.profile.volRatio, (v) => v.toFixed(2)) },
]);

/** 估值与分红（原始披露口径） */
const valuationStats = computed<StatItem[]>(() => [
  { label: "总市值", value: dash(props.profile.totalMV, formatLargeNumber) },
  { label: "流通市值", value: dash(props.profile.floatMV, formatLargeNumber) },
  { label: "市盈(TTM)", value: dash(props.profile.peTTM, (v) => v.toFixed(2)), title: "市盈率 TTM，亏损时为负" },
  { label: "市净率", value: dash(props.profile.pb, (v) => v.toFixed(2)) },
  {
    label: "股息率",
    value: dash(props.profile.dividendYield, (v) => `${v.toFixed(2)}%`),
    title: "近 12 个月每股派息 ÷ 最新价",
  },
  {
    label: "每股分红",
    value: dash(props.profile.dividendTTM, (v) => `${v.toFixed(4)} 元`),
    title: "近 12 个月每股派息合计",
  },
  {
    label: "除息日",
    value: props.profile.latestDivDate ? formatDate(props.profile.latestDivDate) : "—",
    title: props.profile.latestDiv ? `最近一次每股派息 ${props.profile.latestDiv} 元` : "最近除息日",
  },
  { label: "总股本", value: dash(props.profile.totalShare, (v) => `${(v / 1e8).toFixed(2)} 亿股`) },
  { label: "流通股本", value: dash(props.profile.floatShare, (v) => `${(v / 1e8).toFixed(2)} 亿股`) },
  {
    label: "分红次数",
    value: props.profile.dividendCount > 0 ? `${props.profile.dividendCount} 次` : "—",
    title: "历史除权（分红/送转）次数",
  },
]);

/** 财务指标（由 PE/PB 推导，TTM 口径） */
const derivedStats = computed<StatItem[]>(() => [
  {
    label: "ROE(TTM)",
    value: dash(props.profile.roe, (v) => `${v.toFixed(2)}%`),
    title: "净资产收益率 = PB ÷ PE。由行情推导，非财报口径",
  },
  {
    label: "每股收益",
    value: dash(props.profile.eps, (v) => `${v.toFixed(4)} 元`),
    title: "EPS(TTM) = 收盘价 ÷ PE_TTM",
  },
  {
    label: "每股净资产",
    value: dash(props.profile.bps, (v) => `${v.toFixed(4)} 元`),
    title: "BPS = 收盘价 ÷ PB",
  },
  {
    label: "净利润TTM",
    value: dash(props.profile.netProfitTTM, formatLargeNumber),
    title: "归母净利润(TTM) = 总市值 ÷ PE_TTM",
  },
  {
    label: "净资产",
    value: dash(props.profile.netAssets, formatLargeNumber),
    title: "净资产 = 总市值 ÷ PB",
  },
]);

/** 资金流（本机「资金流」表） */
const flowStats = computed<StatItem[]>(() => {
  // 正数补 "+"，负数补 "-"，再统一格式化绝对值
  const signed = (v: number) => (v > 0 ? "+" : v < 0 ? "-" : "") + formatAmount(Math.abs(v));
  const has = props.profile.moneyFlowDay > 0;
  return [
    {
      label: "主力净流入",
      value: has ? signed(props.profile.mainNetToday) : "—",
      title: "当日主力（超大单+大单）净流入",
      trend: props.profile.mainNetToday,
    },
    {
      label: "5日主力",
      value: has ? signed(props.profile.mainNet5D) : "—",
      title: "近 5 个交易日主力净流入合计",
      trend: props.profile.mainNet5D,
    },
    {
      label: "20日主力",
      value: has ? signed(props.profile.mainNet20D) : "—",
      title: "近 20 个交易日主力净流入合计",
      trend: props.profile.mainNet20D,
    },
    {
      label: "资金流日期",
      value: has ? formatDate(props.profile.moneyFlowDay) : "—",
      title: "资金流数据的最新交易日",
    },
  ];
});
</script>

<template>
  <section class="bg-card rounded-lg border px-3 py-2.5">
    <!-- 第一行：名称 + 价格 -->
    <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
      <h2 class="truncate text-base font-semibold">{{ profile.name || profile.code }}</h2>
      <Badge variant="outline" class="tabular font-mono text-[11px]">{{ profile.code }}</Badge>
      <Badge variant="secondary" class="text-[11px]">{{ profile.marketCN }}</Badge>
      <Badge v-if="profile.isST" variant="destructive" class="text-[11px]">ST</Badge>

      <span class="tabular text-xl font-semibold" :class="trendClass(profile.pctChg)">
        {{ priceText }}
      </span>
      <span class="tabular text-xs font-medium" :class="trendClass(profile.pctChg)">
        {{ changeText }}
      </span>
      <span class="tabular text-xs font-medium" :class="trendClass(profile.pctChg)">
        {{ formatPercent(profile.pctChg) }}
      </span>

      <span class="text-muted-foreground ml-auto text-[11px]">
        最新交易日 {{ formatDate(profile.tradeDate) }}
      </span>
    </div>

    <!-- 所属板块：申万一级在最前，后面是二级/三级与概念 -->
    <div v-if="profile.boards?.length" class="mt-1.5 flex flex-wrap items-center gap-1">
      <span class="text-muted-foreground text-[11px]">板块</span>
      <Badge
        v-for="(name, index) in visibleBoards"
        :key="name"
        :variant="index === 0 ? 'default' : 'outline'"
        class="text-[11px]"
      >
        {{ name }}
      </Badge>
      <button
        v-if="hiddenBoardCount > 0"
        type="button"
        class="text-muted-foreground hover:text-foreground text-[11px]"
        :title="profile.boards.join('、')"
        @click="showAllBoards = !showAllBoards"
      >
        {{ showAllBoards ? "收起" : `+${hiddenBoardCount}` }}
      </button>
    </div>

    <!-- 第二行：当日行情 -->
    <div class="mt-2 grid grid-cols-3 gap-x-4 gap-y-1 sm:grid-cols-5 lg:grid-cols-9">
      <div v-for="item in quoteStats" :key="item.label" class="flex min-w-0 items-baseline">
        <span class="text-muted-foreground shrink-0 text-[11px]">{{ item.label }}</span>
        <span class="tabular ml-1.5 truncate text-xs">{{ item.value }}</span>
      </div>
    </div>

    <!-- 第三行：估值与分红 -->
    <div class="mt-1 grid grid-cols-3 gap-x-4 gap-y-1 sm:grid-cols-5 lg:grid-cols-10">
      <div
        v-for="item in valuationStats"
        :key="item.label"
        class="flex min-w-0 items-baseline"
        :title="item.title"
      >
        <span class="text-muted-foreground shrink-0 text-[11px]">{{ item.label }}</span>
        <span class="tabular ml-1.5 truncate text-xs">{{ item.value }}</span>
      </div>
    </div>

    <!-- 第四行：财务（由 PE/PB 推导，非财报） -->
    <div class="mt-1 grid grid-cols-3 gap-x-4 gap-y-1 sm:grid-cols-5">
      <div
        v-for="item in derivedStats"
        :key="item.label"
        class="flex min-w-0 items-baseline"
        :title="item.title"
      >
        <span class="text-muted-foreground shrink-0 text-[11px]">{{ item.label }}</span>
        <span class="tabular ml-1.5 truncate text-xs">{{ item.value }}</span>
      </div>
    </div>

    <!-- 第五行：资金流 -->
    <div class="mt-1 grid grid-cols-2 gap-x-4 gap-y-1 sm:grid-cols-4">
      <div
        v-for="item in flowStats"
        :key="item.label"
        class="flex min-w-0 items-baseline"
        :title="item.title"
      >
        <span class="text-muted-foreground shrink-0 text-[11px]">{{ item.label }}</span>
        <span
          class="tabular ml-1.5 truncate text-xs"
          :class="item.trend === undefined ? '' : trendClass(item.trend)"
        >
          {{ item.value }}
        </span>
      </div>
    </div>
  </section>
</template>
