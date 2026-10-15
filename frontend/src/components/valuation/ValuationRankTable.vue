<script setup lang="ts">
/**
 * 多期限性价比排名表。
 *
 * 两条必须体现的口径（来自方案 §9）：
 *  1. **被排除的标的单独列出**（评级为"暂无法可靠评估"的），不与正常评估的混排 ——
 *     否则榜上会出现"数据缺失但分很高"的荒谬结果；
 *  2. 榜上不只有分数，还要有关键优势、主要风险与估值折溢价（方案 §9 末段）。
 */
import { computed, ref } from "vue";
import { ArrowUpDown, Info, TriangleAlert } from "@lucide/vue";
import type { HorizonRanking, RankRow } from "@/lib/backend";
import { formatCap, formatMultiple, formatPct, formatPctValue, percentileClass, ratingClass, ratingLabel, riskSeverityClass, riskSeverityLabel, scoreClass } from "@/lib/valuation";

const props = defineProps<{
  ranking: HorizonRanking | null;
  /** 该档期限的标签，用于文案。 */
  horizonLabel: string;
}>();

const emit = defineEmits<{ (e: "select", row: RankRow): void }>();

type SortKey =
  | "rank"
  | "score"
  | "selfPercentile"
  | "industryPercentile"
  | "premiumVsMedian"
  | "growthScore"
  | "qualityScore"
  | "roe"
  | "safetyMargin"
  | "expectedReturn"
  | "pessimisticLoss"
  | "returnRiskRatio"
  | "marketCap";

/** 表头的排序列：代码/名称是文本列，单独处理。 */
type ColumnKey = SortKey | "code" | "name";

const sortKey = ref<ColumnKey>("rank");
const sortAsc = ref(true);
const keyword = ref("");
const onlyClean = ref(false);
const showExcluded = ref(false);

function toggleSort(key: ColumnKey) {
  if (sortKey.value === key) {
    sortAsc.value = !sortAsc.value;
    return;
  }
  sortKey.value = key;
  // 排名、分位、损失这类"越小越好"的列默认升序；代码/名称也升序；其余默认降序
  sortAsc.value =
    key === "rank" ||
    key === "code" ||
    key === "name" ||
    key === "selfPercentile" ||
    key === "industryPercentile" ||
    key === "premiumVsMedian" ||
    key === "pessimisticLoss";
}

const rows = computed<RankRow[]>(() => {
  const source = props.ranking?.rows ?? [];
  const text = keyword.value.trim().toLowerCase();
  let list = source.filter((row) => {
    if (text && !`${row.code}${row.name}${row.industryName}`.toLowerCase().includes(text)) return false;
    if (onlyClean.value && row.ratingCapped) return false;
    return true;
  });
  list = [...list];
  const direction = sortAsc.value ? 1 : -1;
  const key = sortKey.value;
  list.sort((a, b) => {
    if (key === "code") return a.code.localeCompare(b.code) * direction;
    if (key === "name") return (a.name ?? "").localeCompare(b.name ?? "") * direction;
    const left = Number((a as unknown as Record<string, number>)[key] ?? 0);
    const right = Number((b as unknown as Record<string, number>)[key] ?? 0);
    if (left === right) return a.code.localeCompare(b.code);
    return (left - right) * direction;
  });
  return list;
});

/** 有数据才算出来的列（按需出列，避免整列 0/—）。 */
const hasIndustryPercentile = computed(() =>
  (props.ranking?.rows ?? []).some((row) => Number(row.industryPercentile ?? 0) > 0),
);
const hasPE = computed(() => (props.ranking?.rows ?? []).some((row) => Number(row.currentPE ?? 0) > 0));

const columns = computed(() => {
  const base: Array<{ key: SortKey | "code" | "name"; label: string; align: "left" | "right"; title?: string }> = [
    { key: "rank", label: "#", align: "right", title: "全市场排名（按综合分降序）" },
    { key: "code", label: "代码", align: "left", title: "点代码可跳到 K 线图表" },
    { key: "name", label: "名称", align: "left" },
    { key: "score", label: "综合分", align: "right", title: "风险约束与数据质量乘子之后的分" },
  ];
  if (hasPE.value) base.push({ key: "selfPercentile", label: "自身估值分位", align: "right", title: "当前 PE 在自身近 5 年历史中的分位（越低越便宜，自建重建口径）" });
  if (hasIndustryPercentile.value) base.push({ key: "industryPercentile", label: "行业分位", align: "right", title: "所属行业加权 PE 的历史分位" });
  base.push(
    { key: "premiumVsMedian", label: "对行业中位", align: "right", title: "个股 PE 相对行业中位数 PE 的折溢价（负数=折价）" },
    { key: "growthScore", label: "成长", align: "right", title: "成长能力维度分" },
    { key: "qualityScore", label: "质量", align: "right", title: "成长质量与盈利质量维度分" },
    { key: "roe", label: "ROE", align: "right", title: "TTM 归母净利润 ÷ 平均归母净资产" },
    { key: "safetyMargin", label: "安全边际", align: "right", title: "（基准合理价值 − 当前价格）÷ 基准合理价值" },
    { key: "expectedReturn", label: "预期收益", align: "right", title: "该期限三情景的概率加权收益（含分红）" },
    { key: "pessimisticLoss", label: "悲观损失", align: "right", title: "悲观情景下的亏损幅度（有 5% 下限）" },
    { key: "returnRiskRatio", label: "风险收益比", align: "right", title: "预期收益 ÷ 悲观损失" },
    { key: "marketCap", label: "市值", align: "right" },
  );
  return base;
});

/** 「对行业中位」为什么是「—」：行业聚合不可用时该字段恒为 0，必须区分「0%」与「算不出来」。 */
function premiumText(row: RankRow): string {
  // 行业聚合不可用 → 相对估值整块没算，此时 0 是"缺失"而不是"持平"
  if (!Number(row.industryWeightedPE) && !Number(row.industryPercentile)) return "—";
  if (!Number(row.currentPE)) return "—";
  return formatPct(row.premiumVsMedian);
}

function rowRiskBadges(row: RankRow) {
  return (row.riskFlags ?? []).slice(0, 2);
}
</script>

<template>
  <div v-if="!ranking" class="text-muted-foreground px-4 py-10 text-center text-xs">
    该期限暂无评估结果
  </div>
  <div v-else class="space-y-3">
    <!-- 工具条 -->
    <div class="flex flex-wrap items-center gap-2 text-[11px]">
      <span class="text-muted-foreground">
        {{ horizonLabel }} · 参与排名 <b class="text-foreground">{{ ranking.scored }}</b> 只
        <template v-if="ranking.excludedCount > 0">
          · 未参与 <b class="text-foreground">{{ ranking.excludedCount }}</b> 只（数据不足或风险阻断）
        </template>
      </span>
      <input
        v-model="keyword"
        placeholder="按代码 / 名称 / 行业筛选"
        class="border-input bg-background ml-auto h-7 w-52 rounded-md border px-2 text-[11px] outline-none focus:border-ring"
      />
      <label class="text-muted-foreground flex cursor-pointer items-center gap-1.5">
        <input v-model="onlyClean" type="checkbox" class="accent-primary size-3" />
        只看未被风险封顶
      </label>
      <button
        v-if="ranking.excludedCount > 0"
        class="text-muted-foreground hover:text-foreground underline decoration-dotted"
        @click="showExcluded = !showExcluded"
      >
        {{ showExcluded ? "隐藏" : "查看" }}未参与排名的标的
      </button>
    </div>

    <!-- 排名表 -->
    <div class="bg-card overflow-hidden rounded-lg border">
      <div class="max-h-[calc(100vh-320px)] overflow-auto">
        <table class="w-full text-xs">
          <thead class="bg-muted/40 text-muted-foreground sticky top-0 z-10 backdrop-blur">
            <tr>
              <th
                v-for="column in columns"
                :key="column.key"
                class="cursor-pointer px-2.5 py-2 font-medium whitespace-nowrap select-none"
                :class="column.align === 'right' ? 'text-right' : 'text-left'"
                :title="column.title"
                @click="toggleSort(column.key)"
              >
                <span class="inline-flex items-center gap-1">
                  {{ column.label }}
                  <ArrowUpDown v-if="sortKey === column.key" class="size-3" :class="sortAsc ? 'rotate-180' : ''" />
                </span>
              </th>
              <th class="px-2.5 py-2 text-left font-medium">评级 / 风险</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in rows"
              :key="row.code"
              class="hover:bg-muted/30 cursor-pointer border-t"
              @click="emit('select', row)"
            >
              <td class="tabular text-muted-foreground px-2.5 py-1.5 text-right">{{ row.rank }}</td>
              <td class="tabular px-2.5 py-1.5 font-mono whitespace-nowrap">
                {{ row.code }}
                <span v-if="row.industryName" class="text-muted-foreground/60 ml-1 font-sans text-[10px]">
                  {{ row.industryName }}
                </span>
              </td>
              <td class="px-2.5 py-1.5 whitespace-nowrap">{{ row.name || "—" }}</td>
              <td class="tabular px-2.5 py-1.5 text-right font-medium" :class="scoreClass(row.score)">
                {{ row.score.toFixed(1) }}
              </td>
              <td v-if="hasPE" class="tabular px-2.5 py-1.5 text-right">
                <span :class="percentileClass(row.selfPercentile)">
                  {{ formatPctValue(row.selfPercentile) }}
                </span>
              </td>
              <td v-if="hasIndustryPercentile" class="tabular px-2.5 py-1.5 text-right">
                <span :class="percentileClass(row.industryPercentile)">
                  {{ formatPctValue(row.industryPercentile) }}
                </span>
              </td>
              <td class="tabular px-2.5 py-1.5 text-right" :class="row.premiumVsMedian < 0 ? 'text-emerald-400' : row.premiumVsMedian > 0 ? 'text-amber-400' : ''">
                {{ premiumText(row) }}
              </td>
              <td class="tabular px-2.5 py-1.5 text-right">{{ row.growthScore.toFixed(0) }}</td>
              <td class="tabular px-2.5 py-1.5 text-right">{{ row.qualityScore.toFixed(0) }}</td>
              <td class="tabular px-2.5 py-1.5 text-right">{{ formatPct(row.roe, 1, false) }}</td>
              <td class="tabular px-2.5 py-1.5 text-right">{{ formatPct(row.safetyMargin) }}</td>
              <td class="tabular px-2.5 py-1.5 text-right">{{ formatPct(row.expectedReturn) }}</td>
              <td class="tabular px-2.5 py-1.5 text-right text-rose-400/80">{{ formatPct(row.pessimisticLoss, 1, false) }}</td>
              <td class="tabular px-2.5 py-1.5 text-right">{{ row.returnRiskRatio > 0 ? row.returnRiskRatio.toFixed(2) : "—" }}</td>
              <td class="tabular text-muted-foreground px-2.5 py-1.5 text-right">{{ formatCap(row.marketCap) }}</td>
              <td class="px-2.5 py-1.5">
                <div class="flex flex-wrap items-center gap-1">
                  <span class="rounded border px-1.5 py-0.5 text-[10px] whitespace-nowrap" :class="ratingClass(row.rating)">
                    {{ ratingLabel(row.rating) }}
                  </span>
                  <span
                    v-for="flag in rowRiskBadges(row)"
                    :key="flag.code"
                    class="rounded border px-1.5 py-0.5 text-[10px] whitespace-nowrap"
                    :class="riskSeverityClass(flag.severity)"
                    :title="flag.reason"
                  >
                    {{ riskSeverityLabel(flag.severity) }}
                  </span>
                </div>
              </td>
            </tr>
            <tr v-if="rows.length === 0">
              <td :colspan="columns.length + 1" class="text-muted-foreground px-2.5 py-10 text-center">
                没有符合条件的标的，试试放宽筛选
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 首行展开：关键优势与主要风险（方案 §9：榜上不能只有分数） -->
    <div v-if="rows.length > 0" class="bg-card space-y-2 rounded-lg border px-4 py-3 text-[11px] leading-relaxed">
      <p class="text-foreground flex items-center gap-1.5 text-xs font-medium">
        <Info class="size-3.5" /> 榜首 {{ rows[0].code }} {{ rows[0].name }} 的依据
      </p>
      <p v-if="rows[0].keyStrengths?.length" class="text-muted-foreground">
        <b class="text-emerald-400">关键优势：</b>{{ rows[0].keyStrengths.join("；") }}
      </p>
      <p v-if="rows[0].mainRisks?.length" class="text-muted-foreground">
        <b class="text-amber-400">主要风险：</b>{{ rows[0].mainRisks.join("；") }}
      </p>
      <p class="text-muted-foreground/80">
        该期限的合理价值：基准
        <b class="text-foreground">{{ formatMultiple(rows[0].fairBase) }}</b> 元
        （现价 {{ formatMultiple(rows[0].price) }} 元）· 目标价
        <b class="text-foreground">{{ formatMultiple(rows[0].targetPrice) }}</b> 元 ·
        安全边际 <b class="text-foreground">{{ formatPct(rows[0].safetyMargin) }}</b> ·
        潜在收益 <b class="text-foreground">{{ formatPct(rows[0].upside) }}</b>
      </p>
      <p class="text-muted-foreground/70">
        安全边际与潜在收益是**两个不同口径**：前者以基准合理价值为分母、衡量"跌到多少才安全"；
        后者以当前价格为分母、衡量"能涨多少"。两者不可混用。
      </p>
    </div>

    <!-- 未参与排名的标的 -->
    <div v-if="showExcluded && ranking.excluded.length > 0" class="bg-card overflow-hidden rounded-lg border">
      <div class="border-b px-4 py-2">
        <p class="flex items-center gap-1.5 text-xs font-medium">
          <TriangleAlert class="size-3.5 text-amber-400" />
          未参与排名的标的（{{ ranking.excluded.length }} 只）
        </p>
        <p class="text-muted-foreground mt-1 text-[11px] leading-relaxed">
          方案要求：被风险规则阻断或无法可靠评估的标的**单独标记，不与正常评估标的混合排序**。
          它们的分数仍然列出供参考，但**没有排名**。
        </p>
      </div>
      <div class="max-h-72 overflow-auto">
        <table class="w-full text-xs">
          <thead class="bg-muted/40 text-muted-foreground sticky top-0 backdrop-blur">
            <tr>
              <th class="px-2.5 py-2 text-left font-medium">代码</th>
              <th class="px-2.5 py-2 text-left font-medium">名称</th>
              <th class="px-2.5 py-2 text-right font-medium">约束前</th>
              <th class="px-2.5 py-2 text-right font-medium">最终</th>
              <th class="px-2.5 py-2 text-left font-medium">不参与排名的原因</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in ranking.excluded" :key="row.code" class="border-t">
              <td class="tabular px-2.5 py-1.5 font-mono">{{ row.code }}</td>
              <td class="px-2.5 py-1.5">{{ row.name || "—" }}</td>
              <td class="tabular text-muted-foreground px-2.5 py-1.5 text-right">{{ row.rawScore.toFixed(1) }}</td>
              <td class="tabular px-2.5 py-1.5 text-right">{{ row.score.toFixed(1) }}</td>
              <td class="text-muted-foreground px-2.5 py-1.5">{{ row.reason }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 该期限的分数分布 -->
    <div class="bg-card rounded-lg border px-4 py-3 text-[11px]">
      <p class="text-foreground mb-2 text-xs font-medium">该期限的评分分布</p>
      <div class="text-muted-foreground flex flex-wrap items-center gap-x-5 gap-y-1">
        <span>均分 <b class="text-foreground">{{ ranking.distribution.mean.toFixed(1) }}</b></span>
        <span>中位 <b class="text-foreground">{{ ranking.distribution.median.toFixed(1) }}</b></span>
        <span>P10 <b class="text-foreground">{{ ranking.distribution.p10.toFixed(1) }}</b></span>
        <span>P25 <b class="text-foreground">{{ ranking.distribution.p25.toFixed(1) }}</b></span>
        <span>P75 <b class="text-foreground">{{ ranking.distribution.p75.toFixed(1) }}</b></span>
        <span>P90 <b class="text-foreground">{{ ranking.distribution.p90.toFixed(1) }}</b></span>
      </div>
      <div class="mt-2 flex items-end gap-1">
        <div
          v-for="(count, index) in ranking.distribution.histogram"
          :key="index"
          class="flex-1"
          :title="`${index * 10}~${index * 10 + 10} 分：${count} 只`"
        >
          <div
            class="bg-primary/60 rounded-sm"
            :style="{ height: `${Math.max(2, (count / Math.max(1, Math.max(...ranking.distribution.histogram))) * 44)}px` }"
          />
          <p class="text-muted-foreground/70 mt-0.5 text-center text-[9px]">{{ index * 10 }}</p>
        </div>
      </div>
    </div>
  </div>
</template>
