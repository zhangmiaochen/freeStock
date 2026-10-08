<script setup lang="ts">
/**
 * 筛选结果表格。
 *
 * 原项目 filter_stocks 不做排序（结果按代码升序），排序交给界面 ——
 * 这里保持一致：默认按代码升序，用户点表头才排序。
 */
import { computed, ref } from "vue";
import { ArrowUpDown } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import type { ScreenItem } from "@/lib/backend";
import { formatAmount, formatLargeNumber, formatPercent } from "@/lib/format";
import { cn } from "@/lib/utils";

const props = defineProps<{ items: ScreenItem[] }>();
const emit = defineEmits<{ select: [ScreenItem] }>();

type SortKey = "code" | "pctChg" | "totalMV" | "peTTM" | "pb" | "roe" | "turnover" | "drawdownPct" | "ps" | "institution" | "quarterGrowth" | "relativeStrength";

/**
 * 数据湖指标的列按需出现：数据集没入库时这些字段全是 0，
 * 显示一整列 0 只会让人困惑。
 */
const hasPS = computed(() => props.items.some((item) => item.ps !== 0));
const hasInstitution = computed(() => props.items.some((item) => item.institution !== 0));
const hasGrowth = computed(() => props.items.some((item) => item.quarterGrowth !== 0));
const hasRS = computed(() => props.items.some((item) => item.relativeStrength !== 0));

const sortKey = ref<SortKey>("code");
const sortAsc = ref(true);

function toggleSort(key: SortKey) {
  if (sortKey.value === key) {
    sortAsc.value = !sortAsc.value;
    return;
  }
  sortKey.value = key;
  // 数值列默认从高到低更符合"找最强的"直觉，代码列保持升序
  sortAsc.value = key === "code";
}

const sorted = computed(() => {
  const list = [...props.items];
  const direction = sortAsc.value ? 1 : -1;
  list.sort((a, b) => {
    if (sortKey.value === "code") {
      return a.code.localeCompare(b.code) * direction;
    }
    return ((a[sortKey.value] as number) - (b[sortKey.value] as number)) * direction;
  });
  return list;
});

const columns: { key: SortKey; label: string; align: "left" | "right" }[] = [
  { key: "code", label: "代码", align: "left" },
  { key: "pctChg", label: "涨跌幅", align: "right" },
  { key: "totalMV", label: "总市值", align: "right" },
  { key: "peTTM", label: "市盈率", align: "right" },
  { key: "pb", label: "市净率", align: "right" },
  { key: "roe", label: "ROE", align: "right" },
  { key: "turnover", label: "换手率", align: "right" },
];

/** 数据湖指标的可排序列（按需追加到表头后面） */
const lakeColumns = computed(() => {
  const out: { key: SortKey; label: string }[] = [];
  if (hasPS.value) out.push({ key: "ps", label: "市销率" });
  if (hasInstitution.value) out.push({ key: "institution", label: "机构%" });
  if (hasGrowth.value) out.push({ key: "quarterGrowth", label: "季报增长" });
  if (hasRS.value) out.push({ key: "relativeStrength", label: "RS" });
  return out;
});

function pctClass(value: number) {
  if (!Number.isFinite(value) || value === 0) return "text-flat";
  return value > 0 ? "text-up" : "text-down";
}
</script>

<template>
  <div class="bg-card overflow-hidden rounded-lg border">
    <div class="max-h-[calc(100vh-220px)] overflow-auto">
      <table class="w-full text-xs">
        <thead class="bg-muted/40 text-muted-foreground sticky top-0 z-10 backdrop-blur">
          <tr>
            <th
              v-for="column in columns"
              :key="column.key"
              class="cursor-pointer px-2.5 py-2 font-medium select-none"
              :class="column.align === 'right' ? 'text-right' : 'text-left'"
              @click="toggleSort(column.key)"
            >
              <span class="inline-flex items-center gap-1">
                {{ column.label }}
                <ArrowUpDown v-if="sortKey === column.key" class="size-3" :class="sortAsc ? 'rotate-180' : ''" />
              </span>
            </th>
            <th class="px-2.5 py-2 text-left font-medium">名称</th>
            <th
              v-for="column in lakeColumns"
              :key="column.key"
              class="cursor-pointer px-2.5 py-2 text-right font-medium select-none"
              :title="column.key === 'relativeStrength' ? '相对强度：个股涨幅 − 对标指数涨幅' : ''"
              @click="toggleSort(column.key)"
            >
              <span class="inline-flex items-center gap-1">
                {{ column.label }}
                <ArrowUpDown v-if="sortKey === column.key" class="size-3" :class="sortAsc ? 'rotate-180' : ''" />
              </span>
            </th>
            <th class="px-2.5 py-2 text-left font-medium">板块</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="item in sorted"
            :key="item.code"
            class="hover:bg-muted/30 cursor-pointer border-t"
            title="点击查看 K 线"
            @click="emit('select', item)"
          >
            <td class="tabular px-2.5 py-1.5 font-mono">{{ item.code }}</td>
            <td class="tabular px-2.5 py-1.5 text-right" :class="pctClass(item.pctChg)">
              {{ formatPercent(item.pctChg) }}
            </td>
            <td class="tabular px-2.5 py-1.5 text-right">{{ formatLargeNumber(item.totalMV) }}</td>
            <td class="tabular px-2.5 py-1.5 text-right">
              {{ item.peTTM ? item.peTTM.toFixed(2) : "—" }}
            </td>
            <td class="tabular px-2.5 py-1.5 text-right">
              {{ item.pb ? item.pb.toFixed(2) : "—" }}
            </td>
            <td class="tabular px-2.5 py-1.5 text-right" :class="item.roe > 0 ? 'text-up' : ''">
              {{ item.roe ? `${item.roe.toFixed(2)}%` : "—" }}
            </td>
            <td class="tabular px-2.5 py-1.5 text-right">
              {{ item.turnover ? `${item.turnover.toFixed(2)}%` : "—" }}
            </td>
            <td class="px-2.5 py-1.5">
              <span class="inline-flex items-center gap-1.5">
                <span class="max-w-[140px] truncate">{{ item.name || "—" }}</span>
                <Badge v-if="item.isST" variant="destructive" class="px-1 py-0 text-[10px]">ST</Badge>
              </span>
            </td>
            <td
              v-if="hasPS"
              class="tabular px-2.5 py-1.5 text-right"
            >
              {{ item.ps ? item.ps.toFixed(2) : "—" }}
            </td>
            <td v-if="hasInstitution" class="tabular px-2.5 py-1.5 text-right">
              {{ item.institution ? `${item.institution.toFixed(2)}%` : "—" }}
            </td>
            <td
              v-if="hasGrowth"
              class="tabular px-2.5 py-1.5 text-right"
              :class="item.quarterGrowth > 0 ? 'text-up' : item.quarterGrowth < 0 ? 'text-down' : ''"
            >
              {{ item.quarterGrowth ? `${item.quarterGrowth.toFixed(2)}%` : "—" }}
            </td>
            <td
              v-if="hasRS"
              class="tabular px-2.5 py-1.5 text-right"
              :class="pctClass(item.relativeStrength)"
              :title="item.benchmark ? `对标 ${item.benchmark}` : ''"
            >
              {{ formatPercent(item.relativeStrength) }}
            </td>
            <td class="px-2.5 py-1.5">
              <span v-if="item.boards?.length" class="text-muted-foreground text-[11px]">
                {{ item.boards.slice(0, 3).join("、") }}
                <span v-if="item.boards.length > 3">+{{ item.boards.length - 3 }}</span>
              </span>
              <span v-else class="text-muted-foreground">—</span>
            </td>
          </tr>
          <tr v-if="sorted.length === 0">
            <td colspan="9" class="text-muted-foreground px-2.5 py-10 text-center">
              没有符合条件的股票，试试放宽条件
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
