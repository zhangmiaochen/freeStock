<script setup lang="ts">
/**
 * 调仓建议面板：候选池 / 调仓动作 / 持仓·候选对比 三个子表。
 *
 * 数据来自后端 score.BuildRotationPlan，逻辑与参考实现 1:1。
 * 排版重新设计（参考项目是 Qt 的三个表格页签）。
 *
 * 界面上刻意说明了几个容易误解的口径：
 *   - `hold_for_rebound_exit` 是**状态**不是动作
 *   - `keep` 是兜底，不是"评分稳定"的判定
 *   - 只列出**真正配对**的持仓/候选对比（行数 ≤ 换股上限）
 */
import { computed, ref } from "vue";
import { ChartCandlestick, CircleAlert, Info } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { PlanResult, RotationRow, ScoreRow } from "@/lib/backend";
import { trendClass } from "@/lib/format";

const props = defineProps<{ plan: PlanResult }>();
const emit = defineEmits<{ openKline: [code: string] }>();

type Tab = "rotation" | "candidates" | "compare";
const active = ref<Tab>("rotation");

/** 后端已按动作 CASE + rank_no 排好序，这里不再重排 */
const rows = computed<RotationRow[]>(() => props.plan.plan?.rows ?? []);
const candidates = computed<ScoreRow[]>(() =>
  // 参考实现界面是硬编码 head(20)
  (props.plan.candidates ?? []).slice(0, 20),
);
const compare = computed(() => props.plan.plan?.compare ?? []);

/** 按动作统计，用于概览 */
const counts = computed(() => {
  const out: Record<string, number> = {};
  for (const row of rows.value) {
    out[row.action] = (out[row.action] ?? 0) + 1;
  }
  return out;
});

/** 可执行的动作数 */
const executableCount = computed(() => rows.value.filter((row) => row.isExecutable).length);

function actionVariant(action: string): "default" | "secondary" | "outline" | "destructive" {
  switch (action) {
    case "clear":
    case "replace_out":
      return "destructive";
    case "add":
    case "add_layer":
    case "replace_in":
      return "default";
    case "reduce":
      return "secondary";
    default:
      return "outline";
  }
}

function fmt(value: number | undefined, digits = 2): string {
  if (value === undefined || value === null || Number.isNaN(value)) return "—";
  return value.toFixed(digits);
}

function fmtSigned(value: number | undefined, digits = 2): string {
  if (value === undefined || value === null || Number.isNaN(value)) return "—";
  return `${value >= 0 ? "+" : ""}${value.toFixed(digits)}`;
}
</script>

<template>
  <section class="bg-card overflow-hidden rounded-lg border">
    <!-- 头部：市场状态 + 参数 + 页签 -->
    <div class="flex flex-wrap items-center gap-2 border-b px-4 py-2">
      <span class="text-sm font-medium">调仓建议</span>
      <Badge
        :variant="plan.marketState.marketState === 'bull' ? 'default'
          : plan.marketState.marketState === 'bear' ? 'destructive' : 'secondary'"
        class="text-[10px]"
      >
        {{ plan.marketState.marketStateLabel }}
      </Badge>
      <span class="text-muted-foreground tabular text-[11px]">
        评分日 {{ plan.strengthDate }} · 建仓线 {{ fmt(plan.config.readyScore, 0) }} ·
        预警 {{ fmt(plan.config.warningScore, 0) }} · 清仓 {{ fmt(plan.config.clearScore, 0) }}
      </span>
      <Badge v-if="executableCount > 0" variant="outline" class="text-[10px]">
        可执行 {{ executableCount }}
      </Badge>

      <div class="ml-auto flex gap-1">
        <Button
          v-for="tab in ([
            { key: 'rotation', label: '调仓动作' },
            { key: 'candidates', label: '候选池' },
            { key: 'compare', label: '持仓·候选对比' },
          ] as const)"
          :key="tab.key"
          size="xs"
          :variant="active === tab.key ? 'default' : 'outline'"
          @click="active = tab.key as Tab"
        >
          {{ tab.label }}
        </Button>
      </div>
    </div>

    <!-- 说明：几个容易误解的地方 -->
    <div v-if="plan.notes?.length" class="text-muted-foreground border-b px-4 py-1.5 text-[11px]">
      <span v-for="(note, index) in plan.notes" :key="index">
        <span v-if="index > 0"> · </span>{{ note }}
      </span>
    </div>

    <!-- ============ 调仓动作 ============ -->
    <div v-if="active === 'rotation'" class="overflow-auto">
      <p v-if="rows.length === 0" class="text-muted-foreground px-4 py-8 text-center text-xs">
        没有调仓动作
      </p>
      <table v-else class="w-full text-xs">
        <thead class="bg-muted/40 text-muted-foreground sticky top-0">
          <tr>
            <th class="px-3 py-2 text-left font-medium">动作</th>
            <th class="px-3 py-2 text-left font-medium">代码</th>
            <th class="px-3 py-2 text-left font-medium">名称</th>
            <th class="px-3 py-2 text-right font-medium">评分</th>
            <th class="px-3 py-2 text-right font-medium">分差</th>
            <th class="px-3 py-2 text-left font-medium">候选</th>
            <th class="px-3 py-2 text-right font-medium">预估提升</th>
            <th class="px-3 py-2 text-right font-medium">层数</th>
            <th class="px-3 py-2 text-left font-medium">执行</th>
            <th class="px-3 py-2 text-left font-medium">原因</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in rows"
            :key="`${row.action}-${row.tsCode}-${row.rankNo}`"
            class="hover:bg-muted/30 border-t"
          >
            <td class="px-3 py-1.5">
              <Badge :variant="actionVariant(row.action)" class="px-1 py-0 text-[10px]">
                {{ row.actionPrefix ? `${row.actionPrefix}·` : "" }}{{ row.actionLabel }}
              </Badge>
            </td>
            <td class="tabular px-3 py-1.5 font-mono">{{ row.tsCode }}</td>
            <td class="px-3 py-1.5">{{ row.name || "—" }}</td>
            <td class="tabular px-3 py-1.5 text-right">{{ fmt(row.score) }}</td>
            <td class="tabular px-3 py-1.5 text-right" :class="trendClass(row.scoreGap)">
              {{ row.scoreGap ? fmtSigned(row.scoreGap) : "—" }}
            </td>
            <td class="px-3 py-1.5">
              <span v-if="row.candidateTsCode" class="tabular font-mono">
                {{ row.candidateTsCode }}
              </span>
              <span v-else class="text-muted-foreground">—</span>
            </td>
            <td class="tabular px-3 py-1.5 text-right">
              {{ row.expectedImprovement ? fmtSigned(row.expectedImprovement) : "—" }}
            </td>
            <td class="tabular px-3 py-1.5 text-right">
              <span v-if="row.currentLayers !== row.targetLayers">
                {{ row.currentLayers }} → {{ row.targetLayers }}
              </span>
              <span v-else class="text-muted-foreground">{{ row.currentLayers }}</span>
            </td>
            <td class="px-3 py-1.5">
              <Badge v-if="row.isExecutable" variant="outline" class="px-1 py-0 text-[10px]">
                {{ row.requiresConfirmation ? "需确认" : "可应用" }}
              </Badge>
              <span v-else class="text-muted-foreground text-[10px]">仅提醒</span>
            </td>
            <td class="text-muted-foreground px-3 py-1.5">{{ row.reason }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- ============ 候选池 ============ -->
    <div v-else-if="active === 'candidates'" class="overflow-auto">
      <p v-if="candidates.length === 0" class="text-muted-foreground px-4 py-8 text-center text-xs">
        候选池为空（当前市场状态下没有达标的股票）
      </p>
      <table v-else class="w-full text-xs">
        <thead class="bg-muted/40 text-muted-foreground sticky top-0">
          <tr>
            <th class="px-3 py-2 text-left font-medium">代码</th>
            <th class="px-3 py-2 text-left font-medium">名称</th>
            <th class="px-3 py-2 text-right font-medium">总分</th>
            <th class="px-3 py-2 text-right font-medium">趋势</th>
            <th class="px-3 py-2 text-right font-medium">结构</th>
            <th class="px-3 py-2 text-right font-medium">RS</th>
            <th class="px-3 py-2 text-right font-medium">资金</th>
            <th class="px-3 py-2 text-right font-medium">修复</th>
            <th class="px-3 py-2 text-right font-medium">现价</th>
            <th class="px-3 py-2 text-right font-medium">RS20</th>
            <th class="px-3 py-2 text-right font-medium">120日回撤</th>
            <th class="px-3 py-2 text-right font-medium">量比</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in candidates" :key="item.tsCode" class="hover:bg-muted/30 border-t">
            <td class="tabular px-3 py-1.5 font-mono">
              <button
                type="button"
                class="hover:underline"
                @click="emit('openKline', item.tsCode)"
              >
                {{ item.tsCode }}
              </button>
            </td>
            <td class="px-3 py-1.5">{{ item.name || "—" }}</td>
            <td class="tabular px-3 py-1.5 text-right font-medium">{{ fmt(item.totalScore) }}</td>
            <td class="tabular px-3 py-1.5 text-right">{{ fmt(item.trendScore, 1) }}</td>
            <td class="tabular px-3 py-1.5 text-right">{{ fmt(item.structureScore, 1) }}</td>
            <td class="tabular px-3 py-1.5 text-right">{{ fmt(item.rsScore, 1) }}</td>
            <td class="tabular px-3 py-1.5 text-right">{{ fmt(item.capitalScore, 1) }}</td>
            <td class="tabular px-3 py-1.5 text-right">{{ fmt(item.repairScore, 1) }}</td>
            <td class="tabular px-3 py-1.5 text-right">{{ fmt(item.closePrice) }}</td>
            <td class="tabular px-3 py-1.5 text-right" :class="trendClass(item.relativeStrength20d)">
              {{ fmtSigned(item.relativeStrength20d) }}
            </td>
            <td class="tabular px-3 py-1.5 text-right" :class="trendClass(item.drawdownFromHigh120)">
              {{ fmt(item.drawdownFromHigh120) }}%
            </td>
            <td class="tabular px-3 py-1.5 text-right">{{ fmt(item.volumeRatio20) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- ============ 持仓·候选对比 ============ -->
    <div v-else class="overflow-auto">
      <p v-if="compare.length === 0" class="text-muted-foreground px-4 py-8 text-center text-xs">
        没有配对（只有真正配成对的持仓才会写对比，行数不超过换股上限）
      </p>
      <table v-else class="w-full text-xs">
        <thead class="bg-muted/40 text-muted-foreground sticky top-0">
          <tr>
            <th class="px-3 py-2 text-left font-medium">当前持仓</th>
            <th class="px-3 py-2 text-left font-medium">候选替代</th>
            <th class="px-3 py-2 text-right font-medium">总分差</th>
            <th class="px-3 py-2 text-right font-medium">RS 差</th>
            <th class="px-3 py-2 text-right font-medium">修复差</th>
            <th class="px-3 py-2 text-right font-medium">预估提升</th>
            <th class="px-3 py-2 text-left font-medium">主要原因</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="pair in compare"
            :key="pair.holdingTsCode"
            class="hover:bg-muted/30 border-t"
          >
            <td class="px-3 py-1.5">
              <span class="tabular font-mono">{{ pair.holdingTsCode }}</span>
              <span class="text-muted-foreground ml-1">{{ pair.holdingName }}</span>
              <span class="tabular ml-1">{{ fmt(pair.holdingScore) }}</span>
            </td>
            <td class="px-3 py-1.5">
              <span class="tabular font-mono">{{ pair.candidateTsCode }}</span>
              <span class="text-muted-foreground ml-1">{{ pair.candidateName }}</span>
              <span class="tabular ml-1">{{ fmt(pair.candidateScore) }}</span>
            </td>
            <td class="tabular px-3 py-1.5 text-right" :class="trendClass(pair.totalScoreGap)">
              {{ fmtSigned(pair.totalScoreGap) }}
            </td>
            <td class="tabular px-3 py-1.5 text-right" :class="trendClass(pair.rsScoreGap)">
              {{ fmtSigned(pair.rsScoreGap) }}
            </td>
            <td class="tabular px-3 py-1.5 text-right" :class="trendClass(pair.repairScoreGap)">
              {{ fmtSigned(pair.repairScoreGap) }}
            </td>
            <td class="tabular px-3 py-1.5 text-right">
              {{ fmtSigned(pair.expectedPortfolioUplift) }}
            </td>
            <td class="text-muted-foreground px-3 py-1.5">{{ pair.mainReason }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 口径说明 -->
    <div class="text-muted-foreground flex items-start gap-1.5 border-t px-4 py-2 text-[11px]">
      <Info class="mt-0.5 size-3 shrink-0" />
      <div class="space-y-0.5">
        <p>· <b>继续持有</b>是兜底分支：没命中任何风控条件就归到这里，不代表"评分稳定"</p>
        <p>· 「等待反弹后处理」是<b>状态</b>不是动作，它的动作仍然是「观察」</p>
        <p>· 「减仓」只会在<b>急涨兑现</b>和<b>换股减层</b>两种情形出现</p>
        <p v-if="plan.marketState.days" class="text-amber-500">
          · 市场状态由全市场宽度（80 个交易日）判定，当前取到 {{ plan.marketState.days }} 日
        </p>
      </div>
    </div>

    <div
      v-if="!plan.candidates?.length && counts.replace_in"
      class="text-muted-foreground flex items-center gap-1.5 border-t px-4 py-1.5 text-[11px]"
    >
      <CircleAlert class="size-3" /> 有换入动作但候选池为空，可能是评分刚刷新导致的数据不一致
    </div>
    <div
      v-else-if="!plan.candidates?.length"
      class="text-muted-foreground flex items-center gap-1.5 border-t px-4 py-1.5 text-[11px]"
    >
      <ChartCandlestick class="size-3" /> 候选池为空说明当前市场状态下没有股票达到建仓质量门槛
    </div>
  </section>
</template>
