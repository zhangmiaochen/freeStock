<script setup lang="ts">
/**
 * 行业详情面板（方案 §10.4）。
 *
 * 必须体现方案 §5.2 的硬要求：**行业平均估值、行业中位数和行业历史分位
 * 需要分别保留，不能将三者混为一个指标** —— 所以这里用三列并列展示，
 * 每一列都标出自己的口径。
 */
import { computed } from "vue";
import { Info } from "@lucide/vue";
import type { IndustryValuation, IndustryValuationDetail } from "@/lib/backend";
import { formatMultiple, formatPct, formatPctValue, percentileClass, ratingClass, ratingLabel, scoreClass, stageLabel } from "@/lib/valuation";

const props = defineProps<{
  industries: IndustryValuation[];
  detail: IndustryValuationDetail | null;
  loading: boolean;
  error: string;
  activeCode: string;
}>();

const emit = defineEmits<{ (e: "select", code: string): void }>();

const industry = computed(() => props.detail?.industry ?? null);
</script>

<template>
  <section class="bg-card rounded-lg border">
    <header class="flex items-center gap-2 border-b px-4 py-2.5">
      <h2 class="text-sm font-medium">行业估值环境</h2>
      <span class="text-muted-foreground text-[11px]">
        分类体系为数据湖的 eastmoney（不是申万）
      </span>
    </header>

    <!-- 行业列表：按成分数排序，点选看详情 -->
    <div class="max-h-56 overflow-auto border-b">
      <table class="w-full text-[11px]">
        <thead class="bg-muted/40 text-muted-foreground sticky top-0 backdrop-blur">
          <tr>
            <th class="px-3 py-1.5 text-left font-medium">行业</th>
            <th class="px-3 py-1.5 text-right font-medium">成分</th>
            <th class="px-3 py-1.5 text-right font-medium" title="自由流通市值加权的调和口径（总市值 ÷ 总盈利）">加权 PE</th>
            <th class="px-3 py-1.5 text-right font-medium" title="成分股 PE 的中位数（抗极端值）">中位 PE</th>
            <th class="px-3 py-1.5 text-right font-medium" title="行业加权 PE 在自身历史中的分位">历史分位</th>
            <th class="px-3 py-1.5 text-right font-medium">亏损占比</th>
            <th class="px-3 py-1.5 text-right font-medium">前3权重</th>
            <th class="px-3 py-1.5 text-left font-medium">阶段</th>
            <th class="px-3 py-1.5 text-right font-medium">环境分</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="item in industries"
            :key="item.code"
            class="hover:bg-muted/30 cursor-pointer border-t"
            :class="activeCode === item.code ? 'bg-primary/5' : ''"
            @click="emit('select', item.code)"
          >
            <td class="px-3 py-1.5">{{ item.name }}</td>
            <td class="tabular text-muted-foreground px-3 py-1.5 text-right">{{ item.members }}</td>
            <td class="tabular px-3 py-1.5 text-right">{{ formatMultiple(item.weightedPE) }}</td>
            <td class="tabular px-3 py-1.5 text-right">{{ formatMultiple(item.medianPE) }}</td>
            <td class="tabular px-3 py-1.5 text-right" :class="percentileClass(item.pePercentile)">
              {{ formatPctValue(item.pePercentile) }}
            </td>
            <td class="tabular px-3 py-1.5 text-right">{{ formatPct(item.lossRatio, 0, false) }}</td>
            <td class="tabular px-3 py-1.5 text-right">{{ formatPct(item.top3Weight, 0, false) }}</td>
            <td class="px-3 py-1.5">{{ stageLabel(item.stage) }}</td>
            <td class="tabular px-3 py-1.5 text-right" :class="scoreClass(item.score)">{{ item.score.toFixed(0) }}</td>
          </tr>
          <tr v-if="industries.length === 0">
            <td colspan="9" class="text-muted-foreground px-3 py-8 text-center">暂无行业聚合结果</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="loading" class="text-muted-foreground px-4 py-6 text-xs">正在读取行业详情…</div>
    <div v-else-if="error" class="px-4 py-4 text-xs text-rose-400">{{ error }}</div>
    <div v-else-if="detail && !detail.available" class="text-muted-foreground px-4 py-4 text-xs">
      {{ detail.message }}
    </div>

    <div v-else-if="industry" class="space-y-3 px-4 py-4">
      <div>
        <p class="text-foreground text-sm font-medium">
          {{ industry.name }}
          <span class="text-muted-foreground ml-1 text-[11px] font-normal">
            {{ industry.code }} · 成分 {{ industry.members }} 只 · 阶段 {{ stageLabel(industry.stage) }}
          </span>
        </p>
        <p class="text-muted-foreground/80 mt-0.5 text-[10px] leading-relaxed">{{ industry.stageNote }}</p>
      </div>

      <!-- 三者分列：加权 / 中位 / 分位 -->
      <div class="grid gap-2 sm:grid-cols-3">
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">① 加权（调和口径）</p>
          <p class="tabular text-sm font-medium">
            PE {{ formatMultiple(industry.weightedPE) }}
          </p>
          <p class="text-muted-foreground/70 text-[10px]">
            PB {{ formatMultiple(industry.weightedPB) }} · PS {{ formatMultiple(industry.weightedPS) }}
          </p>
          <p class="text-muted-foreground/70 mt-0.5 text-[10px] leading-relaxed">
            总市值 ÷ 总盈利（自由流通市值加权），近似行业指数口径。
            **不是**成分股 PE 的算术平均。
          </p>
        </div>
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">② 中位数（抗极端值）</p>
          <p class="tabular text-sm font-medium">
            PE {{ formatMultiple(industry.medianPE) }}
          </p>
          <p class="text-muted-foreground/70 text-[10px]">
            PB {{ formatMultiple(industry.medianPB) }} · PS {{ formatMultiple(industry.medianPS) }}
          </p>
          <p class="text-muted-foreground/70 mt-0.5 text-[10px] leading-relaxed">
            成分股倍数的中位数（已缩尾 2%/98%）。
          </p>
        </div>
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">③ 历史分位</p>
          <p class="tabular text-sm font-medium" :class="percentileClass(industry.pePercentile)">
            {{ formatPctValue(industry.pePercentile) }}
          </p>
          <p class="text-muted-foreground/70 text-[10px]">
            区间 P10 {{ formatMultiple(industry.peBand?.p10) }} ~ P90 {{ formatMultiple(industry.peBand?.p90) }}
            · 中位 {{ formatMultiple(industry.peBand?.p50) }}
          </p>
          <p class="text-muted-foreground/70 mt-0.5 text-[10px] leading-relaxed">
            行业加权 PE 在自身历史中的位置。样本不足时留空而不是填 0。
          </p>
        </div>
      </div>

      <!-- 盈利结构 -->
      <div class="rounded-md border px-3 py-2.5">
        <p class="text-foreground mb-2 text-xs font-medium">盈利结构与集中度</p>
        <div class="text-muted-foreground grid gap-x-6 gap-y-1 text-[11px] sm:grid-cols-2 lg:grid-cols-4">
          <span>ROE 中位 <b class="text-foreground">{{ formatPct(industry.roeMedian, 1, false) }}</b></span>
          <span>营收增速中位 <b class="text-foreground">{{ formatPct(industry.revenueYoYMedian) }}</b></span>
          <span>净利润增速中位 <b class="text-foreground">{{ formatPct(industry.profitYoYMedian) }}</b></span>
          <span>亏损成分占比 <b class="text-foreground">{{ formatPct(industry.lossRatio, 0, false) }}</b></span>
          <span>前 3 大权重合计 <b class="text-foreground">{{ formatPct(industry.top3Weight, 0, false) }}</b></span>
          <span>剔除前 3 后中位 PE <b class="text-foreground">{{ formatMultiple(industry.peExTop3) }}</b></span>
        </div>
      </div>

      <div
        v-if="industry.cyclicalWarning"
        class="rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-[11px] leading-relaxed text-amber-400"
      >
        <p class="font-medium">周期顶部预警</p>
        <p class="mt-0.5">{{ industry.cyclicalReason }}</p>
      </div>

      <p v-if="industry.note" class="text-muted-foreground/80 rounded border border-dashed px-3 py-2 text-[10px] leading-relaxed">
        {{ industry.note }}
      </p>

      <!-- 行业内排名 -->
      <div v-if="detail?.members?.length" class="rounded-md border">
        <p class="border-b px-3 py-2 text-xs font-medium">
          行业内性价比排名（12 个月口径，前 {{ detail.members.length }} 只）
        </p>
        <div class="max-h-72 overflow-auto">
          <table class="w-full text-[11px]">
            <thead class="bg-muted/40 text-muted-foreground sticky top-0 backdrop-blur">
              <tr>
                <th class="px-2.5 py-1.5 text-right font-medium">行业名次</th>
                <th class="px-2.5 py-1.5 text-left font-medium">代码</th>
                <th class="px-2.5 py-1.5 text-left font-medium">名称</th>
                <th class="px-2.5 py-1.5 text-right font-medium">综合分</th>
                <th class="px-2.5 py-1.5 text-right font-medium">自身分位</th>
                <th class="px-2.5 py-1.5 text-right font-medium">成长</th>
                <th class="px-2.5 py-1.5 text-right font-medium">质量</th>
                <th class="px-2.5 py-1.5 text-right font-medium">安全边际</th>
                <th class="px-2.5 py-1.5 text-left font-medium">评级</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in detail.members" :key="row.code" class="border-t">
                <td class="tabular px-2.5 py-1.5 text-right">{{ row.industryRank }} / {{ row.industrySize }}</td>
                <td class="tabular px-2.5 py-1.5 font-mono">{{ row.code }}</td>
                <td class="px-2.5 py-1.5">{{ row.name }}</td>
                <td class="tabular px-2.5 py-1.5 text-right" :class="scoreClass(row.score)">{{ row.score.toFixed(1) }}</td>
                <td class="tabular px-2.5 py-1.5 text-right" :class="percentileClass(row.selfPercentile)">
                  {{ formatPctValue(row.selfPercentile) }}
                </td>
                <td class="tabular px-2.5 py-1.5 text-right">{{ row.growthScore.toFixed(0) }}</td>
                <td class="tabular px-2.5 py-1.5 text-right">{{ row.qualityScore.toFixed(0) }}</td>
                <td class="tabular px-2.5 py-1.5 text-right">{{ formatPct(row.safetyMargin) }}</td>
                <td class="px-2.5 py-1.5">
                  <span class="rounded border px-1.5 py-0.5 text-[10px]" :class="ratingClass(row.rating)">
                    {{ ratingLabel(row.rating) }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <section class="text-muted-foreground space-y-1.5 rounded-lg border px-4 py-3 text-[11px] leading-relaxed">
        <p class="text-foreground flex items-center gap-1.5 text-xs font-medium">
          <Info class="size-3.5" /> 行业估值怎么算的
        </p>
        <p>
          <b class="text-foreground">加权用调和口径</b>（总市值 ÷ 总盈利），而不是成分股 PE 的算术平均 ——
          后者在一家 PE 200 倍的小公司上会被"平均"掉，而它对行业整体盈利的贡献其实极小。
          亏损公司没有 PE，会退出分母（它们的市值仍算在分子里），所以加权 PE 天然**偏乐观**，
          这一点由"亏损成分占比"单独标出。
        </p>
        <p>
          <b class="text-foreground">中位数与分位必须与加权并列看</b>（方案 §5.2 的硬要求）：
          三者背离越大，说明行业内部结构越不均衡，任何单一数字都不可靠。
          本机 <b class="text-foreground">industry_members 只有 5 个快照</b>，
          历史归属无法时点正确重建，所以历史分位在多数行业上是空的 —— 不是 0，是"算不出来"。
        </p>
        <ul class="list-disc space-y-1 pl-5">
          <li v-for="(note, index) in detail?.notes ?? []" :key="index">{{ note }}</li>
        </ul>
      </section>
    </div>
  </section>
</template>
