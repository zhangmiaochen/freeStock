<script setup lang="ts">
/**
 * 板块行情表格。
 *
 * 列分两类：
 *   - 「当日」与「成交额 / 换手率 / 涨跌家数 / 领涨股」是**当日**口径（成分股当日行情聚合）；
 *   - 「5日 / 10日 / …」是**区间**口径（最近 N 个交易日的区间涨跌幅，等权与区间涨跌家数
 *     在悬停提示里，避免列太多）。
 *
 * 区间涨跌幅**算不出来**的成分（次新股在区间起点还没有行情）会被后端排除，
 * 所以这里必须显示「—」而不是 0%：0% 会被读成"没涨没跌"。
 */
import { computed, ref, watch } from "vue";
import { ArrowUpDown, ChevronRight } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import type { BoardPeriodWindow, BoardQuote } from "@/lib/backend";
import { formatAmount, formatPercent } from "@/lib/format";
import {
  DEFAULT_BOARD_PERIOD,
  TODAY_PERIOD,
  periodDetailText,
  periodLabel,
  periodPct,
  periodWindow,
  periodWindowText,
} from "@/lib/boardPeriod";

const props = defineProps<{
  boards: BoardQuote[];
  selectedCode?: string;
  /** 表格里并列显示哪些区间周期（交易日，升序） */
  periods?: number[];
  /** 当前选中的周期：高亮该列，并作为默认排序 */
  periodDays?: number;
  /** 各周期的日历窗口（用于悬停提示） */
  windows?: BoardPeriodWindow[];
}>();

const emit = defineEmits<{ select: [BoardQuote] }>();

/** 排序键：today = 当日，p:N = N 日区间，其余是当日的其它列。 */
type SortKey = "name" | "today" | `p:${number}` | "amount" | "turnover" | "breadth";

const sortKey = ref<SortKey>(`p:${DEFAULT_BOARD_PERIOD}`);
const sortAsc = ref(false);

/** 选中的周期变了 → 默认按这个周期排序（默认周期 5 日）。 */
watch(
  () => props.periodDays,
  (days) => {
    if (days) sortKey.value = periodsSortKey(days);
  },
  { immediate: true },
);

function periodsSortKey(days: number): SortKey {
  return days === TODAY_PERIOD ? "today" : `p:${days}`;
}

function toggleSort(key: SortKey) {
  if (sortKey.value === key) {
    sortAsc.value = !sortAsc.value;
    return;
  }
  sortKey.value = key;
  // 数值列默认从高到低，名称列默认从低到高更符合直觉
  sortAsc.value = key === "name";
}

/** 涨跌家数用「上涨占比」参与排序，比单看上涨家数更能反映板块内部结构 */
function breadth(board: BoardQuote): number {
  const total = board.upCount + board.downCount + board.flatCount;
  return total > 0 ? board.upCount / total : 0;
}

/** 排序用的数值；undefined 表示这个板块在该周期算不出来（永远排在最后）。 */
function sortValue(board: BoardQuote, key: SortKey): number | undefined {
  switch (key) {
    case "name":
      return undefined;
    case "today":
      return board.pctChg;
    case "amount":
      return board.amount;
    case "turnover":
      return board.turnover;
    case "breadth":
      return breadth(board);
    default:
      return periodPct(board, Number(key.slice(2)));
  }
}

const sorted = computed(() => {
  const list = [...props.boards];
  const key = sortKey.value;
  const direction = sortAsc.value ? 1 : -1;

  list.sort((a, b) => {
    if (key === "name") return a.name.localeCompare(b.name, "zh-CN") * direction;
    const left = sortValue(a, key);
    const right = sortValue(b, key);
    // 缺失值不参与比较：无论升序降序都排最后，否则「—」会挤到榜首
    if (left === undefined && right === undefined) return 0;
    if (left === undefined) return 1;
    if (right === undefined) return -1;
    if (left === right) return 0;
    return (left - right) * direction;
  });
  return list;
});

type Column = { key: SortKey; label: string; align: "left" | "right"; title?: string; days?: number };

const columns = computed<Column[]>(() => {
  const list: Column[] = [{ key: "name", label: "板块", align: "left" }];
  list.push({
    key: "today",
    label: "当日",
    align: "right",
    title: "当日流通市值加权涨跌幅（等权见单元格提示）",
    days: TODAY_PERIOD,
  });
  for (const days of props.periods ?? []) {
    list.push({
      key: periodsSortKey(days),
      label: periodLabel(days),
      align: "right",
      title: `最近 ${days} 个交易日的流通市值加权涨跌幅（等权与区间涨跌家数见单元格提示）`,
      days,
    });
  }
  list.push({ key: "amount", label: "成交额", align: "right", title: "成分股当日成交额合计" });
  list.push({ key: "turnover", label: "换手率", align: "right", title: "当日：Σ成交额 ÷ Σ流通市值" });
  list.push({ key: "breadth", label: "涨 / 跌 / 平", align: "right", title: "当日成分股涨跌家数" });
  return list;
});

/** 表头里的周期列（用于悬停提示与高亮） */
function columnWindow(column: Column): BoardPeriodWindow | undefined {
  if (!column.days || column.days === TODAY_PERIOD) return undefined;
  return periodWindow(props.windows, column.days);
}

function headerTitle(column: Column): string {
  const window = columnWindow(column);
  const base = column.title ?? "";
  if (!window) return base;
  return `${base}\n${periodWindowText(window)}`;
}

function pctClass(value: number | undefined) {
  if (value === undefined || !Number.isFinite(value) || value === 0) return "text-flat";
  return value > 0 ? "text-up" : "text-down";
}

function cellTitle(board: BoardQuote, days: number): string {
  return periodDetailText(board, days, days === TODAY_PERIOD ? undefined : periodWindow(props.windows, days));
}

/** 区间列的单元格：算不出来时显示「—」，不能显示 0% */
function cellText(board: BoardQuote, days: number): string {
  const value = periodPct(board, days);
  return value === undefined ? "—" : formatPercent(value);
}
</script>

<template>
  <div class="bg-card overflow-hidden rounded-lg border">
    <div class="max-h-[520px] overflow-auto">
      <table class="w-full text-xs">
        <thead class="bg-muted/40 text-muted-foreground sticky top-0 z-10 backdrop-blur">
          <tr>
            <th
              v-for="column in columns"
              :key="column.key"
              class="cursor-pointer px-3 py-2 font-medium select-none"
              :class="[
                column.align === 'right' ? 'text-right' : 'text-left',
                column.days === periodDays ? 'text-foreground bg-muted/60' : '',
              ]"
              :title="headerTitle(column)"
              @click="toggleSort(column.key)"
            >
              <span class="inline-flex items-center gap-1">
                {{ column.label }}
                <span
                  v-if="column.days === periodDays"
                  class="bg-primary inline-block size-1.5 rounded-full"
                  title="当前选中的周期"
                />
                <ArrowUpDown
                  v-if="sortKey === column.key"
                  class="size-3"
                  :class="sortAsc ? 'rotate-180' : ''"
                />
              </span>
            </th>
            <th class="px-3 py-2 text-left font-medium">领涨股</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="board in sorted"
            :key="board.code"
            class="hover:bg-muted/30 cursor-pointer border-t transition-colors"
            :class="board.code === selectedCode ? 'bg-muted/50' : ''"
            @click="emit('select', board)"
          >
            <td class="px-3 py-2">
              <span class="inline-flex items-center gap-1.5">
                <ChevronRight
                  class="size-3 shrink-0 transition-transform"
                  :class="board.code === selectedCode ? 'rotate-90' : ''"
                />
                <span class="font-medium">{{ board.name }}</span>
                <!-- 成分覆盖不全时说清楚，否则涨跌家数加起来对不上会很困惑 -->
                <Badge
                  v-if="board.quotedCount < board.memberCount"
                  variant="outline"
                  class="px-1 py-0 text-[10px]"
                  :title="`${board.memberCount - board.quotedCount} 只成分当日没有行情（停牌 / 退市 / 未同步）`"
                >
                  {{ board.quotedCount }}/{{ board.memberCount }}
                </Badge>
              </span>
            </td>
            <td
              class="tabular px-3 py-2 text-right font-medium"
              :class="pctClass(board.pctChg)"
              :title="cellTitle(board, TODAY_PERIOD)"
            >
              {{ formatPercent(board.pctChg) }}
            </td>
            <td
              v-for="days in periods ?? []"
              :key="days"
              class="tabular px-3 py-2 text-right"
              :class="[pctClass(periodPct(board, days)), days === periodDays ? 'bg-muted/40 font-medium' : '']"
              :title="cellTitle(board, days)"
            >
              {{ cellText(board, days) }}
            </td>
            <td class="tabular px-3 py-2 text-right">{{ formatAmount(board.amount) }}</td>
            <td class="tabular px-3 py-2 text-right">
              {{ board.turnover ? `${board.turnover.toFixed(2)}%` : "—" }}
            </td>
            <td class="tabular px-3 py-2 text-right">
              <span class="text-up">{{ board.upCount }}</span>
              <span class="text-muted-foreground"> / </span>
              <span class="text-down">{{ board.downCount }}</span>
              <span class="text-muted-foreground"> / {{ board.flatCount }}</span>
            </td>
            <td class="px-3 py-2">
              <span v-if="board.leader?.name" class="inline-flex items-center gap-1.5">
                <span class="truncate">{{ board.leader.name }}</span>
                <span class="tabular shrink-0 text-up">{{ formatPercent(board.leader.pctChg) }}</span>
              </span>
              <span v-else class="text-muted-foreground">—</span>
            </td>
          </tr>
          <tr v-if="sorted.length === 0">
            <td :colspan="columns.length + 1" class="text-muted-foreground px-3 py-8 text-center">
              没有可用的板块行情
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
