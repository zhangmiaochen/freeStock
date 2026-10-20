<script setup lang="ts">
/**
 * 筛选结果表格。
 *
 * 原项目 filter_stocks 不做排序（结果按代码升序），排序交给界面 ——
 * 这里保持一致：默认按代码升序，用户点表头才排序。
 */
import { computed, ref, watch } from "vue";
import { ArrowUpDown } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import type { ScreenItem } from "@/lib/backend";
import { formatAmount, formatDate, formatLargeNumber, formatPercent } from "@/lib/format";
import { cn } from "@/lib/utils";

const props = defineProps<{ items: ScreenItem[] }>();
const emit = defineEmits<{ select: [ScreenItem] }>();

type SortKey =
  | "code"
  | "pctChg"
  | "totalMV"
  | "peTTM"
  | "pb"
  | "roe"
  | "turnover"
  | "drawdownPct"
  | "ps"
  | "institution"
  | "quarterGrowth"
  | "relativeStrength"
  | "chanlunScore"
  | "adaptability";

/**
 * 数据湖指标的列按需出现：数据集没入库时这些字段全是 0，
 * 显示一整列 0 只会让人困惑。
 */
const hasPS = computed(() => props.items.some((item) => item.ps !== 0));
const hasInstitution = computed(() => props.items.some((item) => item.institution !== 0));
const hasGrowth = computed(() => props.items.some((item) => item.quarterGrowth !== 0));
const hasRS = computed(() => props.items.some((item) => item.relativeStrength !== 0));
/** 启用缠论筛选时后端会填充 chanlun 字段；没启用时不显示这些列 */
const hasChanlun = computed(() => props.items.some((item) => item.chanlun));

const sortKey = ref<SortKey>("code");
const sortAsc = ref(true);
let chanlunDefaultApplied = false;

// 缠论启用时后端已按综合分降序返回，表格默认也按综合分排，避免一进来就被代码序打乱
watch(
  hasChanlun,
  (value) => {
    if (!value || chanlunDefaultApplied) return;
    chanlunDefaultApplied = true;
    sortKey.value = "chanlunScore";
    sortAsc.value = false;
  },
  { immediate: true },
);

function toggleSort(key: SortKey) {
  if (sortKey.value === key) {
    sortAsc.value = !sortAsc.value;
    return;
  }
  sortKey.value = key;
  // 数值列默认从高到低更符合"找最强的"直觉，代码列保持升序
  sortAsc.value = key === "code";
}

/** 取值：缠论相关列是嵌套字段，单独映射 */
function sortValue(item: ScreenItem, key: SortKey): number | string {
  switch (key) {
    case "code":
      return item.code;
    case "chanlunScore":
      return item.chanlun?.bestScore ?? 0;
    case "adaptability":
      return item.chanlun?.adaptability ?? 0;
    default:
      return (item[key] as number) ?? 0;
  }
}

const sorted = computed(() => {
  const list = [...props.items];
  const direction = sortAsc.value ? 1 : -1;
  list.sort((a, b) => {
    const left = sortValue(a, sortKey.value);
    const right = sortValue(b, sortKey.value);
    if (typeof left === "string" || typeof right === "string") {
      return String(left).localeCompare(String(right)) * direction;
    }
    return (left - right) * direction;
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

// ---- 缠论列 ----
const STATE_LABEL: Record<string, string> = {
  candidate: "候选",
  triggered: "触发",
  confirmed: "确认",
  invalid: "失效",
  expired: "过期",
};

function chanlunStateLabel(item: ScreenItem): string {
  const state = item.chanlun?.bestState;
  return state ? STATE_LABEL[state] ?? state : "";
}

/** 命中原因（悬停查看）：后端已经写好，这里只负责展示 */
function chanlunTitle(item: ScreenItem): string {
  return item.chanlun?.reasons?.join("\n") ?? "";
}

/** 命中日期要带上是哪一天：确认/触发/形成，和 K 线提示里的阶段对得上 */
function bestDateLabel(item: ScreenItem): string {
  const chanlun = item.chanlun;
  if (!chanlun?.bestDate) return "";
  const prefix =
    chanlun.bestState === "confirmed"
      ? "确认"
      : chanlun.bestState === "triggered"
        ? "触发"
        : "形成";
  return `${prefix} ${formatDate(chanlun.bestDate)}${ageLabel(chanlun.bestAgeDays)}`;
}

/** 信号有多旧：15 个月前确认的三买，不该被当成今天的机会 */
function ageLabel(days: number | undefined): string {
  if (!days || days <= 0) return "";
  if (days < 30) return ` · ${days}天前`;
  if (days < 365) return ` · ${Math.round(days / 30)}个月前`;
  return ` · ${(days / 365).toFixed(1)}年前`;
}

/** 买点用上涨色、卖点用下跌色（与全站红涨绿跌一致） */
function chanlunClass(item: ScreenItem): string {
  const type = item.chanlun?.bestType ?? "";
  if (!type) return "text-muted-foreground";
  return type.startsWith("buy") ? "text-up" : "text-down";
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
            <template v-if="hasChanlun">
              <th
                class="cursor-pointer px-2.5 py-2 text-left font-medium select-none"
                @click="toggleSort('chanlunScore')"
              >
                <span class="inline-flex items-center gap-1">
                  缠论信号
                  <ArrowUpDown v-if="sortKey === 'chanlunScore'" class="size-3" :class="sortAsc ? 'rotate-180' : ''" />
                </span>
              </th>
              <th
                class="cursor-pointer px-2.5 py-2 text-right font-medium select-none"
                @click="toggleSort('adaptability')"
              >
                <span class="inline-flex items-center gap-1">
                  适配性
                  <ArrowUpDown v-if="sortKey === 'adaptability'" class="size-3" :class="sortAsc ? 'rotate-180' : ''" />
                </span>
              </th>
            </template>
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
            <template v-if="hasChanlun">
              <td class="px-2.5 py-1.5" :title="chanlunTitle(item)">
                <span v-if="item.chanlun" class="inline-flex flex-wrap items-center gap-1">
                  <span class="font-medium" :class="chanlunClass(item)">
                    {{ item.chanlun.bestTypeCn || item.chanlun.stateCn }}
                  </span>
                  <Badge v-if="item.chanlun.bestState" variant="outline" class="px-1 py-0 text-[10px]">
                    {{ chanlunStateLabel(item) }}
                  </Badge>
                  <span v-if="item.chanlun.conflict" class="text-destructive text-[10px]">矛盾</span>
                  <span class="text-muted-foreground text-[10px]">
                    {{ item.chanlun.bestScore ? item.chanlun.bestScore.toFixed(1) : "" }}
                  </span>
                  <!-- 命中日期：同一个"三买确认"在不同分析窗口里可能是不同年份的结构 -->
                  <span v-if="item.chanlun.bestDate" class="text-muted-foreground/70 text-[9px]">
                    {{ bestDateLabel(item) }}
                  </span>
                </span>
                <span v-else class="text-muted-foreground">—</span>
              </td>
              <td class="tabular px-2.5 py-1.5 text-right">
                <span v-if="item.chanlun">
                  {{ item.chanlun.adaptGrade }}
                  <span class="text-muted-foreground text-[10px]">
                    {{ item.chanlun.adaptability.toFixed(0) }}
                  </span>
                </span>
                <span v-else class="text-muted-foreground">—</span>
              </td>
            </template>
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
