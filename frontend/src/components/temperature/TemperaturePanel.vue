<script setup lang="ts">
/**
 * 市场温度主面板：读数 + 等级 + 温度条 + 分项拆解。
 *
 * 三件事必须同屏给出，否则这个数字不可信：
 *   1. 温度（0~100）与它的含义（比历史上 X% 的交易日更热）；
 *   2. 四个分项各自的得分与权重 —— 用户要能看出"是量能拖的"还是"趋势推的"；
 *   3. 分位样本（哪段历史、多少个交易日），否则"历史"是无从判断的。
 */
import { computed } from "vue";
import { Flame, Snowflake, Thermometer } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import type { MarketTemperatureResult } from "@/lib/backend";
import { formatDate } from "@/lib/format";
import { FACTOR_HINTS, factorScoreColor, temperatureBadgeClass, temperatureColor } from "@/lib/temperature";

const props = defineProps<{ result: MarketTemperatureResult }>();

const temperature = computed(() => props.result.temperature ?? 0);
const markerLeft = computed(() => `${Math.max(0, Math.min(100, temperature.value))}%`);

/** 分项按后端返回的顺序（趋势 / 量能 / 位置 / 共识） */
const factors = computed(() => props.result.factors ?? []);
</script>

<template>
  <section class="bg-card flex h-full flex-col gap-4 rounded-lg border px-5 py-4">
    <!-- 读数 -->
    <div class="flex items-start gap-4">
      <div class="flex flex-col">
        <span class="text-muted-foreground flex items-center gap-1.5 text-xs">
          <Thermometer class="size-3.5" /> 市场温度
        </span>
        <div class="flex items-end gap-2">
          <span class="tabular text-5xl leading-none font-semibold" :style="{ color: temperatureColor(temperature) }">
            {{ temperature.toFixed(1) }}
          </span>
          <span class="text-muted-foreground pb-1 text-xs">/ 100</span>
        </div>
      </div>
      <div class="ml-auto flex flex-col items-end gap-1.5">
        <Badge :class="temperatureBadgeClass(result.level)">
          <Flame v-if="result.level === '过热' || result.level === '偏热'" />
          <Snowflake v-else-if="result.level === '偏冷' || result.level === '冰点'" />
          {{ result.level }}
        </Badge>
        <span class="text-muted-foreground text-[11px]">综合分 {{ result.composite.toFixed(1) }}</span>
      </div>
    </div>

    <!-- 温度条：0~100 渐变 + 指针 -->
    <div>
      <div class="relative h-3 w-full overflow-hidden rounded-full">
        <div
          class="h-full w-full"
          style="background: linear-gradient(90deg, rgb(59,130,246) 0%, rgb(113,113,122) 50%, rgb(220,38,38) 100%)"
        />
        <div
          class="absolute top-1/2 h-5 w-0.5 -translate-x-1/2 -translate-y-1/2 rounded-full bg-white shadow"
          :style="{ left: markerLeft }"
        />
      </div>
      <div class="text-muted-foreground mt-1 flex justify-between text-[10px]">
        <span>冰点 0</span>
        <span>中性 50</span>
        <span>过热 100</span>
      </div>
    </div>

    <p class="text-xs leading-relaxed">{{ result.levelText }}</p>

    <p class="text-muted-foreground text-[11px] leading-relaxed">
      温度 = 综合分在<b>全部样本</b>中的分位：
      当前比 {{ result.historyStart ? formatDate(result.historyStart) : "—" }} ~
      {{ formatDate(result.historyEnd) }} 这 {{ result.historyDays }} 个交易日里
      <b class="text-foreground">{{ temperature.toFixed(0) }}%</b> 的时间更热。
      周期：<b class="text-foreground">{{ result.periodLabel }}</b>。
    </p>

    <!-- 分项拆解 -->
    <div class="mt-auto space-y-2.5">
      <div v-for="factor in factors" :key="factor.key" class="space-y-1">
        <div class="flex items-baseline gap-2 text-xs">
          <span class="font-medium">{{ factor.name }}</span>
          <span class="text-muted-foreground text-[10px]">权重 {{ (factor.weight * 100).toFixed(0) }}%</span>
          <span class="tabular ml-auto" :style="{ color: factorScoreColor(factor.score) }">
            {{ factor.score.toFixed(0) }} 分
          </span>
          <span class="tabular text-muted-foreground w-20 text-right text-[11px]">{{ factor.valueText }}</span>
        </div>
        <div class="bg-muted h-1.5 w-full overflow-hidden rounded-full">
          <div
            class="h-full rounded-full transition-all"
            :style="{ width: `${Math.max(0, Math.min(100, factor.score))}%`, background: factorScoreColor(factor.score) }"
          />
        </div>
        <p class="text-muted-foreground text-[10px] leading-relaxed">
          {{ factor.detail || FACTOR_HINTS[factor.key] }}
        </p>
      </div>
    </div>
  </section>
</template>
