<script setup lang="ts">
/**
 * 缠论分析面板：把后端标注里的"状态 / 依据 / 买卖点 / 多级别"展示出来。
 *
 * 两个数据来源：
 *   - `bundle`：完整分析（走势、中枢、背驰、买卖点、多级别印证、适配性）；
 *   - `step`：回放快照（只有状态、走势、位置与该时点的信号 —— 未来数据没参与计算）。
 *
 * 所有文案都是**纯文本**渲染（后端提示里没有 Markdown 标记），
 * 而且底部常驻免责声明：无论有没有信号，都必须显示。
 */
import { computed } from "vue";
import { AlertTriangle, Info, ShieldAlert } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import type { ChanlunBundle, ChanlunReplayStep } from "@/lib/backend";
import { formatDate } from "@/lib/format";

const props = defineProps<{
  bundle: ChanlunBundle | null;
  step?: ChanlunReplayStep | null;
  loading?: boolean;
  error?: string;
}>();

const analysis = computed(() => props.bundle?.analysis ?? null);
const isReplay = computed(() => props.step !== null && props.step !== undefined);

/** 状态 / 走势 / 位置：回放时取快照，否则取完整分析 */
const stateName = computed(() => props.step?.state.nameCN ?? analysis.value?.state.nameCN ?? "—");
const stateNote = computed(() => props.step?.state.note ?? analysis.value?.state.note ?? "");
const trendCN = computed(() => props.step?.trend.typeCN ?? analysis.value?.trend.typeCN ?? "—");
const trendNote = computed(() => props.step?.trend.note ?? analysis.value?.trend.note ?? "");
const positionCN = computed(
  () => props.step?.position.phaseCN ?? analysis.value?.position.phaseCN ?? "—",
);
const positionNote = computed(
  () => props.step?.position.note ?? analysis.value?.position.note ?? "",
);

/** 回放时只有该时点的信号；完整分析时是当前级别的全部信号 */
const signals = computed(() => props.step?.signals ?? analysis.value?.signals ?? []);

// ---- 走势终完美：生命周期 + 完全分类 ----
const trendStatusCN = computed(
  () => props.step?.trend.statusCN ?? analysis.value?.trend.statusCN ?? "",
);
const trendCompletedDate = computed(
  () => props.step?.trend.completedDate ?? analysis.value?.trend.completedDate ?? 0,
);
const trendCompletionReason = computed(
  () => props.step?.trend.completionReason ?? analysis.value?.trend.completionReason ?? "",
);
const outlook = computed(() => props.step?.trend.outlook ?? analysis.value?.trend.outlook ?? []);

const latestPivot = computed(() => {
  const pivots = analysis.value?.pivots ?? [];
  return pivots.length ? pivots[pivots.length - 1] : null;
});

const divergences = computed(() => analysis.value?.divergences ?? []);
const multi = computed(() => props.bundle?.multiLevel ?? null);
const notes = computed(() => props.bundle?.notes ?? []);
const adaptability = computed(() => analysis.value?.adaptability ?? null);
const precision = computed(() => analysis.value?.data.precisionNote ?? "");

/** 结构矛盾：同级别反向信号，后端已降权 */
const hasConflict = computed(() => signals.value.some((signal) => signal.conflict));

/** 「完全分类 / 背驰三种可能」的状态配色：已确认加粗、进行中用琥珀、已排除置灰划线 */
function scenarioTone(status: string | undefined): string {
  switch (status) {
    case "confirmed":
      return "text-foreground font-medium";
    case "active":
      return "text-amber-400";
    case "paused":
      return "text-muted-foreground";
    case "invalidated":
      return "text-muted-foreground line-through";
    default:
      return "text-muted-foreground";
  }
}

/** 中枢状态的中文名（后端给的是枚举值） */
const PIVOT_STATUS: Record<string, string> = {
  forming: "形成",
  extension: "延伸",
  expanded: "扩展",
  new: "新生",
};

function pivotStatusText(status: string | undefined): string {
  return status ? PIVOT_STATUS[status] ?? status : "";
}

const STATE_TONE: Record<string, string> = {
  "down-trend": "text-down",
  "up-trend": "text-up",
  "reversal-candidate-buy": "text-up",
  "reversal-candidate-sell": "text-down",
  "reversal-confirmed-up": "text-up",
  "reversal-confirmed-down": "text-down",
  invalid: "text-muted-foreground",
  insufficient: "text-muted-foreground",
};

function stateClass(): string {
  const name = props.step?.state.name ?? analysis.value?.state.name ?? "";
  return STATE_TONE[name] ?? "";
}

function signalClass(direction: string): string {
  return direction === "buy" ? "text-up" : "text-down";
}

function scoreText(value: number): string {
  return value > 0 ? value.toFixed(1) : "—";
}

/** 买卖点的时间线：只显示已有时间，避免一堆「—」 */
function timeline(signal: {
  formDate: number;
  triggerDate: number;
  confirmDate: number;
  invalidDate: number;
}): string {
  const items: string[] = [];
  if (signal.formDate) items.push(`形成 ${formatDate(signal.formDate)}`);
  if (signal.triggerDate) items.push(`触发 ${formatDate(signal.triggerDate)}`);
  if (signal.confirmDate) items.push(`确认 ${formatDate(signal.confirmDate)}`);
  if (signal.invalidDate) items.push(`失效 ${formatDate(signal.invalidDate)}`);
  return items.join(" · ");
}
</script>

<template>
  <aside class="bg-card flex min-h-0 w-[350px] shrink-0 flex-col rounded-lg border">
    <!-- 头部 -->
    <div class="flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2">
      <span class="text-sm font-medium">缠论</span>
      <Badge variant="secondary" class="text-[10px]">
        {{ analysis?.levelCN ?? "—" }}
      </Badge>
      <Badge v-if="isReplay" variant="outline" class="text-[10px]">回放</Badge>
      <Badge v-if="hasConflict" variant="destructive" class="text-[10px]">结构矛盾</Badge>
      <span class="text-muted-foreground ml-auto text-[10px]">
        {{ bundle?.tradeDate ? formatDate(bundle.tradeDate) : "" }}
      </span>
    </div>

    <div class="min-h-0 flex-1 space-y-3 overflow-auto px-3 py-3 text-xs">
      <!-- 加载 / 错误 -->
      <p v-if="loading" class="text-muted-foreground">正在计算结构…</p>
      <p v-else-if="error" class="text-destructive">{{ error }}</p>

      <template v-else>
        <!-- 状态 -->
        <section>
          <p class="text-muted-foreground mb-1 text-[11px]">当前状态</p>
          <p class="text-sm font-medium" :class="stateClass()">{{ stateName }}</p>
          <p v-if="stateNote" class="text-muted-foreground mt-1 leading-relaxed">
            {{ stateNote }}
          </p>
        </section>

        <!-- 走势与位置 -->
        <section class="grid grid-cols-2 gap-2">
          <div class="bg-muted/30 rounded border px-2 py-1.5">
            <p class="text-muted-foreground text-[10px]">走势类型</p>
            <p class="flex items-center gap-1 font-medium">
              {{ trendCN }}
              <Badge v-if="trendStatusCN" variant="outline" class="px-1 py-0 text-[9px]">
                {{ trendStatusCN }}
              </Badge>
            </p>
          </div>
          <div class="bg-muted/30 rounded border px-2 py-1.5">
            <p class="text-muted-foreground text-[10px]">当前位置</p>
            <p class="font-medium">{{ positionCN }}</p>
          </div>
        </section>

        <!-- 走势终完美：完成时间与原因 -->
        <p v-if="trendCompletedDate" class="text-muted-foreground flex items-start gap-1 text-[11px]">
          <Info class="mt-0.5 size-3 shrink-0" />
          <span>
            走势已完成（{{ formatDate(trendCompletedDate) }}）：{{ trendCompletionReason }}
          </span>
        </p>

        <p v-if="positionNote" class="text-muted-foreground flex items-start gap-1 text-[11px]">
          <Info class="mt-0.5 size-3 shrink-0" />
          <span>{{ positionNote }}</span>
        </p>

        <!-- 完全分类：不预测，只把可能的走向与成立条件列清楚 -->
        <section v-if="outlook.length">
          <p class="text-muted-foreground mb-1 text-[11px]">后续可能（完全分类）</p>
          <div
            v-for="scenario in outlook"
            :key="scenario.key"
            class="bg-muted/20 mb-1 rounded border px-2 py-1"
          >
            <p class="flex items-center gap-1.5 text-[11px]" :class="scenarioTone(scenario.status)">
              <span class="font-medium">{{ scenario.label }}</span>
              <Badge variant="outline" class="px-1 py-0 text-[9px]">{{ scenario.statusCN }}</Badge>
              <span v-if="scenario.date" class="ml-auto text-[10px]">
                {{ formatDate(scenario.date) }}
              </span>
            </p>
            <p class="text-muted-foreground mt-0.5 text-[10px] leading-relaxed">
              {{ scenario.condition }}
            </p>
          </div>
        </section>

        <!-- 回放：说明这个快照"当时看到了多少数据" -->
        <section v-if="isReplay && step?.note" class="bg-muted/20 rounded border px-2 py-1.5">
          <p class="text-muted-foreground text-[11px]">{{ step.note }}</p>
        </section>

        <!-- 数据与精度 -->
        <section v-if="!isReplay && analysis" class="bg-muted/20 rounded border px-2 py-1.5">
          <p class="text-muted-foreground text-[11px]">
            数据：{{ analysis.data.bars }} 根（合并后 {{ analysis.data.mergedBars }} 根）
          </p>
          <p class="text-muted-foreground mt-0.5 flex items-start gap-1 text-[11px]">
            <AlertTriangle class="mt-0.5 size-3 shrink-0 text-amber-400" />
            <span>{{ precision }}</span>
          </p>
        </section>

        <!-- 中枢 / 背驰 -->
        <section v-if="!isReplay && analysis">
          <p class="text-muted-foreground mb-1 text-[11px]">
            中枢（{{ analysis.pivots.length }} 个）
          </p>
          <div v-if="latestPivot" class="bg-muted/30 rounded border px-2 py-1.5">
            <p class="tabular font-medium">
              {{ latestPivot.zd.toFixed(2) }} ~ {{ latestPivot.zg.toFixed(2) }}
            </p>
            <p class="text-muted-foreground mt-0.5 text-[10px]">
              最近一个中枢 · {{ latestPivot.strokeCount }} 笔 ·
              {{ pivotStatusText(latestPivot.status) }}
              <span v-if="!latestPivot.confirmed"> · 未确认</span>
            </p>
          </div>
          <p v-else class="text-muted-foreground">尚未形成中枢（走势未完成）</p>

          <template v-if="divergences.length">
            <p class="text-muted-foreground mt-2 mb-1 text-[11px]">背驰</p>
            <div
              v-for="div in divergences"
              :key="div.index"
              class="bg-muted/30 mb-1 rounded border px-2 py-1.5"
            >
              <p class="font-medium">
                {{ div.kindCN }}
                <span :class="div.direction === 'bottom' ? 'text-up' : 'text-down'">
                  {{ div.direction === "bottom" ? "底背驰" : "顶背驰" }}
                </span>
                <span class="text-muted-foreground">力度比 {{ div.ratio.toFixed(2) }}</span>
              </p>
              <div class="mt-1 grid grid-cols-4 gap-1">
                <span
                  v-for="dim in div.dims"
                  :key="dim.name"
                  class="text-muted-foreground text-[10px]"
                  :title="dim.note"
                >
                  {{ dim.name }} {{ dim.ratio.toFixed(2) }}
                </span>
              </div>

              <!-- 背驰 ≠ 反转：三种可能各自走到哪一步 -->
              <p class="mt-1.5 flex items-center gap-1 text-[10px]">
                <span class="text-muted-foreground">当前判定</span>
                <Badge variant="secondary" class="px-1 py-0 text-[9px]">{{ div.outcomeCN }}</Badge>
              </p>
              <div class="mt-1 space-y-0.5">
                <p
                  v-for="outcome in div.outcomes"
                  :key="outcome.key"
                  class="flex items-center gap-1.5 text-[10px]"
                  :class="scenarioTone(outcome.status)"
                  :title="outcome.condition"
                >
                  <span>{{ outcome.label }}</span>
                  <Badge variant="outline" class="px-1 py-0 text-[9px]">
                    {{ outcome.statusCN }}
                  </Badge>
                  <span v-if="outcome.date" class="ml-auto">{{ formatDate(outcome.date) }}</span>
                </p>
              </div>
              <p class="text-muted-foreground mt-1 text-[10px] leading-relaxed">{{ div.note }}</p>
            </div>
          </template>
        </section>

        <!-- 买卖点 -->
        <section>
          <p class="text-muted-foreground mb-1 text-[11px]">
            买卖点（{{ signals.length }}）
          </p>
          <p v-if="signals.length === 0" class="text-muted-foreground">
            当前级别没有可关注的买卖点
          </p>
          <div
            v-for="(signal, index) in signals"
            :key="`${signal.type}-${index}`"
            class="bg-muted/30 mb-1 rounded border px-2 py-1.5"
          >
            <div class="flex items-center gap-1.5">
              <span class="font-medium" :class="signalClass(signal.direction)">
                {{ signal.typeCN }}
              </span>
              <Badge variant="outline" class="px-1 py-0 text-[10px]">{{ signal.stateCN }}</Badge>
              <span class="text-muted-foreground ml-auto text-[10px]">
                分 {{ scoreText(signal.score) }}
              </span>
            </div>
            <p class="text-muted-foreground tabular mt-0.5 text-[10px]">
              {{ timeline(signal) }}
            </p>
            <ul class="text-muted-foreground mt-1 space-y-0.5 text-[10px] leading-relaxed">
              <li v-for="reason in signal.reasons" :key="reason">· {{ reason }}</li>
            </ul>
            <p v-if="signal.conflict && signal.conflictNote" class="text-destructive mt-1 text-[10px]">
              {{ signal.conflictNote }}
            </p>
            <p
              v-else-if="signal.scoreFactors?.length"
              class="text-muted-foreground mt-1 flex flex-wrap gap-x-2 text-[10px]"
            >
              <span v-for="factor in signal.scoreFactors" :key="factor.name">
                {{ factor.name }} {{ factor.value }}
              </span>
            </p>
          </div>
        </section>

        <!-- 多级别印证 -->
        <section v-if="!isReplay && multi">
          <p class="text-muted-foreground mb-1 flex items-center gap-1.5 text-[11px]">
            多级别印证
            <Badge v-if="multi.resonance" variant="default" class="px-1 py-0 text-[10px]">共振</Badge>
            <Badge v-if="multi.nested" variant="secondary" class="px-1 py-0 text-[10px]">区间套</Badge>
          </p>
          <div class="space-y-1">
            <div
              v-for="level in multi.levels"
              :key="level.level"
              class="bg-muted/20 flex items-center gap-2 rounded border px-2 py-1"
            >
              <span class="w-14 shrink-0">{{ level.levelCN }}</span>
              <span class="text-muted-foreground truncate">{{ level.stateCN }}</span>
              <span class="text-muted-foreground ml-auto shrink-0 text-[10px]">
                中枢 {{ level.pivots }}
              </span>
            </div>
          </div>
          <p v-if="multi.note" class="text-muted-foreground mt-1 text-[10px] leading-relaxed">
            {{ multi.note }}
          </p>
        </section>

        <!-- 适配性 -->
        <section v-if="!isReplay && adaptability">
          <p class="text-muted-foreground mb-1 text-[11px]">股票适配性</p>
          <p>
            <b>{{ adaptability.grade }}</b>
            <span class="text-muted-foreground"> · {{ adaptability.score.toFixed(1) }}</span>
            <span v-if="adaptability.liquidity < 40" class="text-amber-400"> · 流动性偏低</span>
          </p>
          <ul class="text-muted-foreground mt-1 space-y-0.5 text-[10px] leading-relaxed">
            <li v-for="reason in adaptability.reasons" :key="reason">· {{ reason }}</li>
          </ul>
        </section>

        <!-- 口径与提示 -->
        <section v-if="notes.length">
          <p class="text-muted-foreground mb-1 text-[11px]">口径与提示</p>
          <ul class="text-muted-foreground space-y-1 text-[10px] leading-relaxed">
            <li v-for="note in notes" :key="note">· {{ note }}</li>
          </ul>
        </section>
      </template>
    </div>

    <!-- 常驻免责声明 -->
    <div class="bg-muted/30 flex shrink-0 items-start gap-1.5 border-t px-3 py-2">
      <ShieldAlert class="mt-0.5 size-3.5 shrink-0 text-amber-400" />
      <p class="text-muted-foreground text-[10px] leading-relaxed">
        {{ bundle?.disclaimer ?? "仅作参考，不构成投资建议" }}。缠论是结构分析工具，
        不预测涨跌、不承诺收益；买卖点仅标注结构状态。
        <span class="mt-1 block">
          本页按全历史计算；条件选股按「分析窗口」（默认 500 根日 K）计算，
          同一只票的当前结构可能不同。
        </span>
      </p>
    </div>
  </aside>
</template>
