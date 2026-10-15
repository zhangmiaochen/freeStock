<script setup lang="ts">
/**
 * 多期限性价比排名表。
 *
 * ## 三条设计约束（都对应实测过的问题）
 *
 * 1. **必须虚拟列表**。全市场（limit=0）时这一档有 4400+ 行，每行 15 个单元格、
 *    外加风险标签与徽章 —— 一次性渲染是六万多个 DOM 节点，滚动直接卡死。
 *    这里改成"只渲染视口内的行"（`useVirtualList`），DOM 节点数与可视行数同阶。
 *
 * 2. **被排除的标的单独列出**（方案 §9）：评级为"暂无法可靠评估"的标的
 *    不与正常评估的混排，否则榜上会出现"数据缺失但分很高"的荒谬结果。
 *
 * 3. **搜索与股票列表页同一套规则**：代码 / 名称 / 拼音全拼 / 拼音首字母。
 *    拼音走后端 `GetNamePinyin`（复用 `kline.go` 的 `pinyinVariants`），
 *    不引第二套 JS 拼音库 —— 否则同一名称在两个页面会搜出不同结果。
 */
import { computed, ref, watch } from "vue";
import { useVirtualList } from "@vueuse/core";
import { ArrowUpDown, Info, TriangleAlert, X } from "@lucide/vue";
import type { HorizonRanking, RankRow } from "@/lib/backend";
import { api } from "@/lib/backend";
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

/** 表头的排序列：代码/名称/行业是文本列，单独处理。 */
type ColumnKey = SortKey | "code" | "name" | "industryName";

const sortKey = ref<ColumnKey>("rank");
const sortAsc = ref(true);
const keyword = ref("");
const onlyClean = ref(false);
const showExcluded = ref(false);
/** 行业筛选：空集合表示"不限行业"。 */
const industryFilter = ref<Set<string>>(new Set());

// ---------------------------------------------------------------------------
// 虚拟列表
// ---------------------------------------------------------------------------

/**
 * 行高必须与模板里的 `height` 完全一致。
 *
 * 虚拟列表靠"行高 × 下标"算位移，写错会让滚动位置与内容错位（点到的行与
 * 看到的不一致）。所以把它提成常量、并在模板里用同一个值。
 */
const ROW_HEIGHT = 29;
/** 视口高度（像素）。与原先 `max-h-[calc(100vh-320px)]` 的观感保持一致。 */
const VIEWPORT_HEIGHT = 560;
/** 上下各多渲染几行，滚动时不会看到空白。 */
const OVERSCAN = 6;

// ---------------------------------------------------------------------------
// 拼音索引
// ---------------------------------------------------------------------------

/**
 * 名称 → 拼音。键是名称（后端按入参原样返回）。
 *
 * 一次取当前 ranking 里全部名称的拼音：切换期限档位时**名称集合基本相同**，
 * 后端有缓存所以第二次是纯内存操作。只在确有中文名称时请求。
 */
const pinyinByName = ref<Map<string, { pinyin: string; initials: string }>>(new Map());
/** 已经请求过拼音的名称，避免重复发同一批请求。 */
const pinyinRequested = new Set<string>();

async function ensurePinyin(names: string[]) {
  const wanted = names.filter((name) => {
    if (!name || pinyinRequested.has(name)) return false;
    // 纯 ASCII 的名称（例如 "N茅台" 里的字母）没有拼音可算，跳过请求
    return /[\u4e00-\u9fa5]/.test(name);
  });
  if (wanted.length === 0) return;
  for (const name of wanted) pinyinRequested.add(name);
  try {
    const result = await api.getNamePinyin(wanted);
    const next = new Map(pinyinByName.value);
    for (const [name, info] of Object.entries(result ?? {})) {
      if (info) next.set(name, { pinyin: info.pinyin ?? "", initials: info.initials ?? "" });
    }
    pinyinByName.value = next;
  } catch {
    // 拼音拿不到不影响主流程（仍可按代码/名称搜），静默降级
    for (const name of wanted) pinyinRequested.delete(name);
  }
}

// ---------------------------------------------------------------------------
// 筛选与排序
// ---------------------------------------------------------------------------

/** 名称的拼音串（全拼 + 首字母一起返回，供子串匹配）。 */
function pinyinText(name: string): string {
  const info = pinyinByName.value.get(name);
  if (!info) return "";
  return `${info.pinyin} ${info.initials}`;
}

/** 当前档位里出现过的行业（用于筛选下拉）。 */
const industryOptions = computed(() => {
  const counts = new Map<string, { name: string; count: number }>();
  for (const row of props.ranking?.rows ?? []) {
    const code = row.industryCode ?? "";
    const name = row.industryName || "未分类";
    const existing = counts.get(code);
    if (existing) existing.count += 1;
    else counts.set(code, { name, count: 1 });
  }
  return [...counts.entries()]
    .map(([code, item]) => ({ code, name: item.name, count: item.count }))
    .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
});

const rows = computed<RankRow[]>(() => {
  const source = props.ranking?.rows ?? [];
  const text = keyword.value.trim().toLowerCase();
  const picked = industryFilter.value;

  let list = source.filter((row) => {
    if (picked.size > 0 && !picked.has(row.industryCode ?? "")) return false;
    if (text) {
      // 与股票列表页同一套匹配面：代码 / 名称 / 行业名 / 拼音全拼 / 拼音首字母。
      // 拼音串里多音字的所有写法都在（用空格分隔），所以子串匹配即可命中任一读法。
      const haystack = `${row.code}${row.name ?? ""}${row.industryName ?? ""}${pinyinText(row.name ?? "")}`.toLowerCase();
      if (!haystack.includes(text)) return false;
    }
    if (onlyClean.value && row.ratingCapped) return false;
    return true;
  });

  list = [...list];
  const direction = sortAsc.value ? 1 : -1;
  const key = sortKey.value;
  list.sort((a, b) => {
    if (key === "code") return a.code.localeCompare(b.code) * direction;
    if (key === "name") return (a.name ?? "").localeCompare(b.name ?? "") * direction;
    if (key === "industryName") return (a.industryName ?? "").localeCompare(b.industryName ?? "") * direction;
    const left = Number((a as unknown as Record<string, number>)[key] ?? 0);
    const right = Number((b as unknown as Record<string, number>)[key] ?? 0);
    if (left === right) return a.code.localeCompare(b.code);
    return (left - right) * direction;
  });
  return list;
});

function toggleSort(key: ColumnKey) {
  if (sortKey.value === key) {
    sortAsc.value = !sortAsc.value;
    return;
  }
  sortKey.value = key;
  // 排名、分位、损失这类"越小越好"的列默认升序；代码/名称/行业也升序；其余默认降序
  sortAsc.value =
    key === "rank" ||
    key === "code" ||
    key === "name" ||
    key === "industryName" ||
    key === "selfPercentile" ||
    key === "industryPercentile" ||
    key === "premiumVsMedian" ||
    key === "pessimisticLoss";
}

function toggleIndustry(code: string) {
  const next = new Set(industryFilter.value);
  if (next.has(code)) next.delete(code);
  else next.add(code);
  industryFilter.value = next;
}

function clearIndustry() {
  industryFilter.value = new Set();
}

/** 有数据才算出来的列（按需出列，避免整列 0/—）。 */
const hasIndustryPercentile = computed(() =>
  (props.ranking?.rows ?? []).some((row) => Number(row.industryPercentile ?? 0) > 0),
);
const hasPE = computed(() => (props.ranking?.rows ?? []).some((row) => Number(row.currentPE ?? 0) > 0));

const columns = computed(() => {
  const base: Array<{ key: ColumnKey; label: string; align: "left" | "right"; title?: string }> = [
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

/**
 * 表格改为固定列宽（`table-fixed` + `<colgroup>`）后的列宽表。
 *
 * 为什么要固定：虚拟列表要求**行高与列宽都可预测**，否则滚动时列宽会随
 * 视口内内容跳动（渲染的行变了、"最宽的那个"也变了）。代价是长文本要靠
 * `truncate` 处理，但排名表本来就是扫数值用的，可接受。
 */
const columnWidths = computed(() => {
  const widths: number[] = [52, 190, 130, 68];
  if (hasPE.value) widths.push(96);
  if (hasIndustryPercentile.value) widths.push(76);
  widths.push(88, 60, 60, 68, 76, 76, 76, 84, 78, 150);
  return widths;
});

// ---------------------------------------------------------------------------
// 虚拟列表接线
// ---------------------------------------------------------------------------

const { list: virtualRows, containerProps, wrapperProps } = useVirtualList(rows, {
  itemHeight: ROW_HEIGHT,
  overscan: OVERSCAN,
});

// 期限档位或数据变化 → 拉一次拼音（后端有缓存，切档位几乎免费）
watch(
  () => props.ranking,
  (ranking) => {
    const names = [...new Set((ranking?.rows ?? []).map((row) => row.name ?? ""))];
    void ensurePinyin(names);
  },
  { immediate: true },
);

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
        <template v-if="rows.length !== ranking.rows.length">
          · 筛出 <b class="text-foreground">{{ rows.length }}</b> 只
        </template>
      </span>
      <input
        v-model="keyword"
        placeholder="代码 / 名称 / 拼音首字母 / 行业"
        class="border-input bg-background ml-auto h-7 w-60 rounded-md border px-2 text-[11px] outline-none focus:border-ring"
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

    <!-- 行业筛选（多选） -->
    <div class="flex flex-wrap items-center gap-1.5 text-[11px]">
      <span class="text-muted-foreground shrink-0">行业</span>
      <button
        class="rounded border px-1.5 py-0.5"
        :class="industryFilter.size === 0 ? 'border-primary bg-primary/10 text-foreground' : 'text-muted-foreground hover:text-foreground'"
        @click="clearIndustry"
      >
        全部（{{ industryOptions.length }}）
      </button>
      <button
        v-for="option in industryOptions.slice(0, 24)"
        :key="option.code"
        class="rounded border px-1.5 py-0.5 whitespace-nowrap"
        :class="industryFilter.has(option.code) ? 'border-primary bg-primary/10 text-foreground' : 'text-muted-foreground hover:text-foreground'"
        :title="`${option.name} · ${option.count} 只`"
        @click="toggleIndustry(option.code)"
      >
        {{ option.name }} <span class="text-muted-foreground/70">{{ option.count }}</span>
      </button>
      <span v-if="industryOptions.length > 24" class="text-muted-foreground/70">
        （只列出前 24 个行业，共 {{ industryOptions.length }} 个；用搜索框可搜任意行业名）
      </span>
      <button
        v-if="industryFilter.size > 0"
        class="text-muted-foreground hover:text-foreground ml-1 inline-flex items-center gap-0.5 underline decoration-dotted"
        @click="clearIndustry"
      >
        <X class="size-3" /> 清除行业筛选
      </button>
    </div>

    <!-- 排名表（虚拟列表：只渲染视口内的行） -->
    <div class="bg-card overflow-hidden rounded-lg border">
      <div
        v-bind="containerProps"
        class="overflow-auto"
        :style="{ height: `${VIEWPORT_HEIGHT}px` }"
      >
        <div v-bind="wrapperProps">
          <!-- 表头：单独固定，不参与虚拟滚动（否则滚动时表头会跟着走） -->
          <div class="bg-muted/40 text-muted-foreground sticky top-0 z-10 backdrop-blur">
            <table class="w-full table-fixed text-xs">
              <colgroup>
                <col v-for="(width, index) in columnWidths" :key="index" :style="{ width: `${width}px` }" />
              </colgroup>
              <thead>
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
            </table>
          </div>

          <table class="w-full table-fixed text-xs">
            <colgroup>
              <col v-for="(width, index) in columnWidths" :key="index" :style="{ width: `${width}px` }" />
            </colgroup>
            <tbody>
              <tr
                v-for="item in virtualRows"
                :key="item.data.code"
                class="hover:bg-muted/30 cursor-pointer border-t"
                :style="{ height: `${ROW_HEIGHT}px` }"
                @click="emit('select', item.data)"
              >
                <td class="tabular text-muted-foreground px-2.5 text-right">{{ item.data.rank }}</td>
                <td class="tabular truncate px-2.5 font-mono">
                  {{ item.data.code }}
                  <span v-if="item.data.industryName" class="text-muted-foreground/60 ml-1 font-sans text-[10px]">
                    {{ item.data.industryName }}
                  </span>
                </td>
                <td class="truncate px-2.5">{{ item.data.name || "—" }}</td>
                <td class="tabular px-2.5 text-right font-medium" :class="scoreClass(item.data.score)">
                  {{ item.data.score.toFixed(1) }}
                </td>
                <td v-if="hasPE" class="tabular px-2.5 text-right">
                  <span :class="percentileClass(item.data.selfPercentile)">
                    {{ formatPctValue(item.data.selfPercentile) }}
                  </span>
                </td>
                <td v-if="hasIndustryPercentile" class="tabular px-2.5 text-right">
                  <span :class="percentileClass(item.data.industryPercentile)">
                    {{ formatPctValue(item.data.industryPercentile) }}
                  </span>
                </td>
                <td class="tabular px-2.5 text-right" :class="item.data.premiumVsMedian < 0 ? 'text-emerald-400' : item.data.premiumVsMedian > 0 ? 'text-amber-400' : ''">
                  {{ premiumText(item.data) }}
                </td>
                <td class="tabular px-2.5 text-right">{{ item.data.growthScore.toFixed(0) }}</td>
                <td class="tabular px-2.5 text-right">{{ item.data.qualityScore.toFixed(0) }}</td>
                <td class="tabular px-2.5 text-right">{{ formatPct(item.data.roe, 1, false) }}</td>
                <td class="tabular px-2.5 text-right">{{ formatPct(item.data.safetyMargin) }}</td>
                <td class="tabular px-2.5 text-right">{{ formatPct(item.data.expectedReturn) }}</td>
                <td class="tabular px-2.5 text-right text-rose-400/80">{{ formatPct(item.data.pessimisticLoss, 1, false) }}</td>
                <td class="tabular px-2.5 text-right">{{ item.data.returnRiskRatio > 0 ? item.data.returnRiskRatio.toFixed(2) : "—" }}</td>
                <td class="tabular text-muted-foreground px-2.5 text-right">{{ formatCap(item.data.marketCap) }}</td>
                <td class="px-2.5">
                  <div class="flex flex-wrap items-center gap-1">
                    <span class="rounded border px-1.5 py-0.5 text-[10px] whitespace-nowrap" :class="ratingClass(item.data.rating)">
                      {{ ratingLabel(item.data.rating) }}
                    </span>
                    <span
                      v-for="flag in rowRiskBadges(item.data)"
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
            </tbody>
          </table>

          <p v-if="rows.length === 0" class="text-muted-foreground px-2.5 py-10 text-center text-xs">
            没有符合条件的标的，试试放宽筛选
          </p>
        </div>
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
