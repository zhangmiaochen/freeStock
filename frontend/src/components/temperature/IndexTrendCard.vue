<script setup lang="ts">
/**
 * 单个大盘指数的卡片。
 *
 * 一块卡片里要回答用户的两个问题：
 *   1. **现在什么位置**——最新点数、当日与所选周期的涨跌、最近走势；
 *   2. **这个位置算高还是低**——用「点位相对自身 250 日均线的偏离」在全部历史里的
 *      分位来表达（不是点数高低：指数长期向上，直接比点数永远显示高位）。
 *
 * 趋势说明（TrendNote）由后端按可核对的事实拼出来（均线关系、区间涨幅及分位、
 * 量能、距高点），这里只负责排版。
 */
import { computed } from "vue";

import { Badge } from "@/components/ui/badge";
import Sparkline from "@/components/temperature/Sparkline.vue";
import type { MarketIndexStat } from "@/lib/backend";
import { formatPercent } from "@/lib/format";
import { trendLabelClass } from "@/lib/temperature";

const props = defineProps<{
  index: MarketIndexStat;
  /** 所选周期标签（当日 / 5日 …） */
  periodLabel: string;
  /** 该指数在结果里的序号，决定走势图颜色 */
  color: string;
}>();

const pctClass = (value: number) => {
  if (!Number.isFinite(value) || value === 0) return "text-flat";
  return value > 0 ? "text-up" : "text-down";
};

/** 位置分位的进度条颜色：低=冷色、高=热色 */
const levelColor = computed(() => {
  const score = Math.max(0, Math.min(100, props.index.levelScore));
  if (score <= 50) return `rgb(59,130,246)`;
  return `rgb(220,38,38)`;
});

/** 均线关系：站上为红、跌破为绿（与行情配色一致） */
function maState(ma: number): { text: string; class: string } {
  if (!ma) return { text: "—", class: "text-muted-foreground" };
  const above = props.index.close >= ma;
  return { text: above ? "站上" : "跌破", class: above ? "text-up" : "text-down" };
}
</script>

<template>
  <article class="bg-card flex flex-col gap-3 rounded-lg border px-4 py-3">
    <!-- 标题 -->
    <header class="flex items-center gap-2">
      <span class="size-2 shrink-0 rounded-full" :style="{ background: color }" />
      <span class="font-medium">{{ index.name }}</span>
      <span class="text-muted-foreground text-[11px]">{{ index.code }}</span>
      <Badge variant="outline" class="ml-auto px-1.5 py-0 text-[10px]">{{ index.group }}</Badge>
    </header>

    <!-- 点数与涨跌 -->
    <div class="flex flex-wrap items-end gap-x-4 gap-y-1">
      <div>
        <div class="tabular text-xl leading-none font-semibold">{{ index.close.toFixed(2) }}</div>
        <div class="text-muted-foreground mt-1 text-[10px]">最新点数</div>
      </div>
      <div class="text-xs">
        <div class="tabular font-medium" :class="pctClass(index.pctChg)">
          {{ formatPercent(index.pctChg) }}
        </div>
        <div class="text-muted-foreground text-[10px]">当日</div>
      </div>
      <div class="text-xs">
        <div class="tabular font-medium" :class="pctClass(index.periodPctChg)">
          {{ formatPercent(index.periodPctChg) }}
        </div>
        <div class="text-muted-foreground text-[10px]">{{ periodLabel }}</div>
      </div>
    </div>

    <!-- 最近走势 -->
    <Sparkline :values="index.recent" :height="44" />

    <!-- 位置：分位进度条 + 说明 -->
    <div class="space-y-1">
      <div class="flex items-baseline gap-2 text-[11px]">
        <span class="font-medium">位置水平</span>
        <span class="tabular ml-auto" :style="{ color: levelColor }">
          {{ index.levelScore.toFixed(0) }} 分位
        </span>
      </div>
      <div class="bg-muted h-1.5 w-full overflow-hidden rounded-full">
        <div
          class="h-full rounded-full"
          :style="{ width: `${Math.max(0, Math.min(100, index.levelScore))}%`, background: levelColor }"
        />
      </div>
      <p class="text-muted-foreground text-[10px] leading-relaxed">{{ index.levelText }}</p>
    </div>

    <!-- 均线 -->
    <div class="text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-[10px]">
      <span v-for="ma in [{ label: 'MA20', value: index.ma20 }, { label: 'MA60', value: index.ma60 }, { label: 'MA250', value: index.ma250 }]" :key="ma.label">
        {{ ma.label }}
        <b class="text-foreground tabular">{{ ma.value ? ma.value.toFixed(2) : "—" }}</b>
        <span :class="maState(ma.value).class">{{ maState(ma.value).text }}</span>
      </span>
      <span v-if="index.newHighDays" class="text-up">创 {{ index.newHighDays }} 日新高</span>
      <span v-else-if="index.newLowDays" class="text-down">创 {{ index.newLowDays }} 日新低</span>
      <span v-else-if="index.drawdownFromHigh < 0">
        距高点 <b class="text-foreground tabular">{{ formatPercent(index.drawdownFromHigh) }}</b>
      </span>
    </div>

    <!-- 趋势结论 -->
    <div class="mt-auto space-y-1.5 border-t pt-2">
      <div class="flex items-center gap-2">
        <Badge :class="trendLabelClass(index.trendLabel)">{{ index.trendLabel }}</Badge>
        <span class="text-muted-foreground text-[10px]">动向分 {{ index.trendScore.toFixed(0) }}</span>
      </div>
      <p class="text-[11px] leading-relaxed">{{ index.trendNote }}</p>
    </div>
  </article>
</template>
