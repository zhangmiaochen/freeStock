<script setup lang="ts">
/**
 * 指数性价比评估面板（方案 §8 / §10.5）。
 *
 * 强调"独立模型"这件事：这里的九个维度**不是**成分股评分的加权，
 * 而是指数层面的量（结构与集中度、加权与中位数的背离、有安全边际的成分权重）。
 */
import { computed } from "vue";
import { Info } from "@lucide/vue";
import type { IndexEvaluation, IndexValuationResult, ValuationIndexOption } from "@/lib/backend";
import { formatMultiple, formatPct, formatPctValue, percentileClass, ratingClass, ratingLabel, scoreClass, DIMENSION_LABELS, DIMENSION_ORDER, HORIZON_LABELS } from "@/lib/valuation";

const props = defineProps<{
  options: ValuationIndexOption[];
  detail: IndexValuationResult | null;
  loading: boolean;
  error: string;
  activeSymbol: string;
}>();

const emit = defineEmits<{ (e: "select", symbol: string): void }>();

const index = computed<IndexEvaluation | null>(() => props.detail?.index ?? null);

function dimensionRows(item: IndexEvaluation) {
  const score = item.scores?.[3] ?? item.scores?.[0];
  if (!score) return [];
  return DIMENSION_ORDER.map((key) => ({
    key,
    label: DIMENSION_LABELS[key] ?? String(key),
    score: Number(score.dimensions?.[key] ?? 0),
    weight: Number(score.weights?.[key] ?? 0),
  }));
}

/**
 * 指数该期限的"目标空间"。
 *
 * ⚠ 指数的 `Outlook` 里**没有** upside 字段（那是个股 FairValue 的字段）：
 * 个股的潜在收益 =（乐观价值 − 现价）÷ 现价，而指数的目标价是从归一化点位
 * （100）推出来的，直接用"目标价 ÷ 100 − 1"就是同一口径。
 * 这里统一从 outlook 里算，避免依赖不存在的字段。
 */
function outlookUpside(score: { outlook?: { targetPrice?: number } }): number {
  const target = Number(score.outlook?.targetPrice ?? 0);
  if (!Number.isFinite(target) || target <= 0) return 0;
  // 指数模型里价格锚定为 100（见 valuation/indexNeutralFair），所以这就是收益率
  return target / 100 - 1;
}
</script>

<template>
  <section class="bg-card rounded-lg border">
    <header class="flex flex-wrap items-center gap-2 border-b px-4 py-2.5">
      <h2 class="text-sm font-medium">指数性价比评估</h2>
      <span class="text-muted-foreground text-[11px]">
        独立于个股的模型 —— 不是成分股评分的加权平均
      </span>
      <div class="ml-auto flex flex-wrap gap-1">
        <button
          v-for="option in options"
          :key="option.symbol"
          class="rounded-md border px-2 py-1 text-[11px] transition"
          :class="[
            activeSymbol === option.symbol ? 'border-primary bg-primary/10 text-foreground' : 'border-border text-muted-foreground hover:text-foreground',
            option.hasConstituents ? '' : 'opacity-60',
          ]"
          :title="option.hasConstituents ? `${option.constituents} 个成分` : option.note"
          @click="emit('select', option.symbol)"
        >
          {{ option.name }}
          <span v-if="!option.hasConstituents" class="ml-1 text-[9px]">无成分</span>
        </button>
      </div>
    </header>

    <div v-if="loading" class="text-muted-foreground px-4 py-8 text-xs">正在读取指数评估…</div>
    <div v-else-if="error" class="px-4 py-6 text-xs text-rose-400">{{ error }}</div>

    <div
      v-else-if="detail && !detail.available"
      class="text-muted-foreground space-y-1.5 px-4 py-6 text-xs leading-relaxed"
    >
      <p class="text-foreground text-sm font-medium">该指数暂无法做结构类评估</p>
      <p class="max-w-2xl">{{ detail.message }}</p>
    </div>

    <div v-else-if="index" class="space-y-3 px-4 py-4">
      <!-- 整体估值 -->
      <div class="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">加权 PE（总市值 ÷ 总盈利）</p>
          <p class="tabular text-sm font-medium">{{ formatMultiple(index.weightedPE) }}</p>
        </div>
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">中位 PE（成分股 PE 中位数）</p>
          <p class="tabular text-sm font-medium">{{ formatMultiple(index.medianPE) }}</p>
          <p class="text-muted-foreground/70 text-[10px]">
            与加权背离
            <b :class="index.medianPE > 0 && index.weightedPE / index.medianPE < 0.85 ? 'text-emerald-400' : index.medianPE > 0 && index.weightedPE / index.medianPE > 1.15 ? 'text-amber-400' : 'text-foreground'">
              {{ index.medianPE > 0 ? formatPct(index.weightedPE / index.medianPE - 1) : "—" }}
            </b>
          </p>
        </div>
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">加权 PB / PS</p>
          <p class="tabular text-sm font-medium">
            {{ formatMultiple(index.weightedPB) }} / {{ formatMultiple(index.weightedPS) }}
          </p>
        </div>
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">历史估值分位</p>
          <p class="tabular text-sm font-medium" :class="percentileClass(index.pePercentile)">
            {{ formatPctValue(index.pePercentile) }}
          </p>
          <p class="text-muted-foreground/70 text-[10px]">
            {{ index.pePercentile > 0 ? "越高越贵" : "成分快照只有 1 个，无法时点正确重建" }}
          </p>
        </div>
      </div>

      <!-- 结构 -->
      <div class="rounded-md border px-3 py-2.5">
        <p class="text-foreground mb-2 text-xs font-medium">估值分布与结构</p>
        <div class="text-muted-foreground grid gap-x-6 gap-y-1 text-[11px] sm:grid-cols-2 lg:grid-cols-4">
          <span>便宜成分权重（自身分位 ≤30%）<b class="text-emerald-400">{{ formatPct(index.cheapWeight, 0, false) }}</b></span>
          <span>偏贵成分权重（自身分位 ≥70%）<b class="text-amber-400">{{ formatPct(index.expensiveWeight, 0, false) }}</b></span>
          <span>亏损成分权重 <b class="text-foreground">{{ formatPct(index.lossWeight, 0, false) }}</b></span>
          <span>成分自身分位中位 <b class="text-foreground">{{ formatPctValue(index.medianSelfPercentile) }}</b></span>
          <span>有安全边际的成分权重 <b class="text-emerald-400">{{ formatPct(index.marginWeight, 0, false) }}</b></span>
          <span>安全边际 &gt;10% 的成分权重 <b class="text-emerald-400">{{ formatPct(index.margin10Weight, 0, false) }}</b></span>
          <span>前 10 大权重合计 <b class="text-foreground">{{ formatPct(index.top10Weight, 0, false) }}</b></span>
          <span>最大行业权重 <b class="text-foreground">{{ formatPct(index.topIndustryWeight, 0, false) }}</b></span>
          <span>加权 ROE <b class="text-foreground">{{ formatPct(index.weightedROE, 1, false) }}</b></span>
          <span>ROE 中位 <b class="text-foreground">{{ formatPct(index.medianROE, 1, false) }}</b></span>
          <span>加权营收增速 <b class="text-foreground">{{ formatPct(index.weightedRevenueYoY) }}</b></span>
          <span>加权成长 / 质量分 <b class="text-foreground">{{ (index.weightedGrowthScore ?? 0).toFixed(0) }} / {{ (index.weightedQualityScore ?? 0).toFixed(0) }}</b></span>
          <span>成长 / 质量分中位 <b class="text-foreground">{{ (index.medianGrowthScore ?? 0).toFixed(0) }} / {{ (index.medianQualityScore ?? 0).toFixed(0) }}</b></span>
        </div>
        <p class="text-muted-foreground/80 mt-2 text-[10px] leading-relaxed">{{ index.concentrationNote }}</p>
        <p class="text-muted-foreground/80 mt-0.5 text-[10px] leading-relaxed">{{ index.marginNote }}</p>
      </div>

      <!-- 行业权重 -->
      <div v-if="index.industryWeights?.length" class="rounded-md border px-3 py-2.5">
        <p class="text-foreground mb-2 text-xs font-medium">行业权重结构（前 12）</p>
        <div class="space-y-1">
          <div v-for="industry in index.industryWeights.slice(0, 12)" :key="industry.name" class="flex items-center gap-2 text-[11px]">
            <span class="w-32 shrink-0 truncate">{{ industry.name }}</span>
            <div class="bg-muted h-2 flex-1 overflow-hidden rounded-sm">
              <div class="bg-primary/70 h-full" :style="{ width: `${Math.min(100, (industry.weight ?? 0) * 100)}%` }" />
            </div>
            <span class="tabular text-muted-foreground w-12 text-right">{{ formatPct(industry.weight, 1, false) }}</span>
          </div>
        </div>
      </div>

      <!-- 五档评分 -->
      <div v-if="index.scores?.length" class="rounded-md border px-3 py-2.5">
        <p class="text-foreground mb-2 text-xs font-medium">五档期限评分（独立计算）</p>
        <div class="overflow-hidden rounded border">
          <table class="w-full text-[11px]">
            <thead class="bg-muted/40 text-muted-foreground">
              <tr>
                <th class="px-2 py-1.5 text-left font-medium">期限</th>
                <th class="px-2 py-1.5 text-right font-medium">综合分</th>
                <th class="px-2 py-1.5 text-left font-medium">评级</th>
                <th class="px-2 py-1.5 text-right font-medium">目标空间</th>
                <th class="px-2 py-1.5 text-right font-medium">预期收益</th>
                <th class="px-2 py-1.5 text-right font-medium">悲观损失</th>
                <th class="px-2 py-1.5 text-right font-medium">收敛比例</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="score in index.scores" :key="score.horizon.key" class="border-t">
                <td class="px-2 py-1.5">{{ HORIZON_LABELS[score.horizon.key] ?? score.horizon.label }}</td>
                <td class="tabular px-2 py-1.5 text-right" :class="scoreClass(score.finalScore)">{{ score.finalScore.toFixed(1) }}</td>
                <td class="px-2 py-1.5">
                  <span class="rounded border px-1.5 py-0.5 text-[10px]" :class="ratingClass(score.rating)">
                    {{ ratingLabel(score.rating) }}
                  </span>
                </td>
                <td class="tabular px-2 py-1.5 text-right">{{ formatPct(outlookUpside(score)) }}</td>
                <td class="tabular px-2 py-1.5 text-right">{{ formatPct(score.outlook?.expectedReturn ?? 0) }}</td>
                <td class="tabular px-2 py-1.5 text-right text-rose-400/80">{{ formatPct(score.outlook?.pessimisticLoss ?? 0, 1, false) }}</td>
                <td class="tabular px-2 py-1.5 text-right">{{ formatPct(score.outlook?.convergence ?? 0, 0, false) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 维度 -->
      <div class="rounded-md border px-3 py-2.5">
        <p class="text-foreground mb-2 text-xs font-medium">指数评估的九个维度（12 个月口径）</p>
        <div class="grid gap-x-6 gap-y-1 text-[11px] sm:grid-cols-2 lg:grid-cols-3">
          <div v-for="dimension in dimensionRows(index)" :key="dimension.key" class="flex items-center gap-2">
            <span class="text-muted-foreground w-28 shrink-0">{{ dimension.label }}</span>
            <span class="tabular" :class="scoreClass(dimension.score)">{{ dimension.score.toFixed(1) }}</span>
            <span class="text-muted-foreground/70 text-[10px]">权重 {{ dimension.weight.toFixed(0) }}%</span>
          </div>
        </div>
        <p class="text-muted-foreground/80 mt-2 text-[10px] leading-relaxed">
          指数模型的维度含义与个股**不同**：自身估值 = 指数自己的历史分位；行业估值环境 = 成分估值分布的均衡程度；
          相对估值 = 加权 PE 与中位 PE 的背离度；安全边际 = 有安全边际的成分权重（指数层面的"便宜货含量"）。
        </p>
      </div>

      <!-- 风险与提示 -->
      <div v-if="index.warnings?.length" class="rounded-md border px-3 py-2.5">
        <p class="text-foreground mb-1.5 flex items-center gap-1.5 text-xs font-medium">
          <Info class="size-3.5" /> 口径提示
        </p>
        <ul class="text-muted-foreground list-disc space-y-1 pl-5 text-[10px] leading-relaxed">
          <li v-for="(warning, index2) in index.warnings" :key="index2">{{ warning }}</li>
        </ul>
      </div>
      <p class="text-muted-foreground text-[10px]">
        置信度 <b :class="scoreClass(index.overallConfidence?.score ?? 0)">{{ (index.overallConfidence?.score ?? 0).toFixed(0) }}</b> 分
        · {{ (index.overallConfidence?.reasons ?? []).join("；") }}
      </p>
    </div>
  </section>
</template>
