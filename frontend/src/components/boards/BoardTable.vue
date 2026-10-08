<script setup lang="ts">
/**
 * 板块行情表格。
 *
 * 除了排序，这里还承担两件热力图表达不了的事：
 *   - 成分覆盖：MemberCount 与 QuotedCount 不一致时标出来（有停牌/未同步的成分），
 *     否则用户会奇怪为什么统计口径对不上；
 *   - 领涨/领跌股：热力图只有板块，定位龙头要看表格。
 */
import { computed, ref } from "vue";
import { ArrowUpDown, ChevronRight } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import type { BoardQuote } from "@/lib/backend";
import { formatAmount, formatPercent } from "@/lib/format";
import { cn } from "@/lib/utils";

const props = defineProps<{
  boards: BoardQuote[];
  selectedCode?: string;
}>();

const emit = defineEmits<{ select: [BoardQuote] }>();

type SortKey = "name" | "pctChg" | "pctChgEqual" | "amount" | "turnover" | "breadth";

const sortKey = ref<SortKey>("pctChg");
const sortAsc = ref(false);

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

const sorted = computed(() => {
  const list = [...props.boards];
  const direction = sortAsc.value ? 1 : -1;
  list.sort((a, b) => {
    switch (sortKey.value) {
      case "name":
        return a.name.localeCompare(b.name, "zh-CN") * direction;
      case "breadth":
        return (breadth(a) - breadth(b)) * direction;
      default:
        return ((a[sortKey.value] as number) - (b[sortKey.value] as number)) * direction;
    }
  });
  return list;
});

const columns: { key: SortKey; label: string; align: "left" | "right" }[] = [
  { key: "name", label: "板块", align: "left" },
  { key: "pctChg", label: "加权涨跌", align: "right" },
  { key: "pctChgEqual", label: "等权", align: "right" },
  { key: "amount", label: "成交额", align: "right" },
  { key: "turnover", label: "换手率", align: "right" },
  { key: "breadth", label: "涨 / 跌 / 平", align: "right" },
];

function pctClass(value: number) {
  if (!Number.isFinite(value) || value === 0) return "text-flat";
  return value > 0 ? "text-up" : "text-down";
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
              :class="column.align === 'right' ? 'text-right' : 'text-left'"
              @click="toggleSort(column.key)"
            >
              <span class="inline-flex items-center gap-1">
                {{ column.label }}
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
                  :title="`${board.memberCount - board.quotedCount} 只成分没有行情（停牌 / 退市 / 未同步）`"
                >
                  {{ board.quotedCount }}/{{ board.memberCount }}
                </Badge>
              </span>
            </td>
            <td class="tabular px-3 py-2 text-right font-medium" :class="pctClass(board.pctChg)">
              {{ formatPercent(board.pctChg) }}
            </td>
            <td class="tabular text-muted-foreground px-3 py-2 text-right">
              {{ formatPercent(board.pctChgEqual) }}
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
            <td colspan="7" class="text-muted-foreground px-3 py-8 text-center">
              没有可用的板块行情
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
