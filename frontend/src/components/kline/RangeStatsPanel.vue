<script setup lang="ts">
/**
 * 框选区间的涨跌信息面板。
 *
 * 只在用户框选了 ≥2 根 K 线时出现。每一项都带 tooltip 写明**口径** ——
 * 「区间涨跌幅」在一段行情里有好几种读法（首开→末收 / 前收→末收 / 首收→末收），
 * 差一个百分点就是误导，所以数字旁边必须能查到它是怎么算的
 *（文案来自 lib/rangeStats 的 RANGE_METRIC_NOTES，有单测钉着）。
 */
import { computed } from "vue";
import { Info, X } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatAmount, formatDate, trendClass } from "@/lib/format";
import { RANGE_METRIC_NOTES, type RangeStats } from "@/lib/rangeStats";

const props = defineProps<{
  stats: RangeStats;
  /** 指数没有成交额（数据源只给价格与成交量） */
  isIndex?: boolean;
}>();

const emit = defineEmits<{ clear: [] }>();

/** 口径说明的查表：key → note */
const notes = new Map(RANGE_METRIC_NOTES.map((item) => [item.key, item.note]));
function noteOf(key: string): string | undefined {
  return notes.get(key as keyof RangeStats);
}

function pct(value: number, digits = 2): string {
  const sign = value > 0 ? "+" : "";
  return `${sign}${value.toFixed(digits)}%`;
}

/** 回撤总给正数，但语义是"跌"，所以固定用跌色显示 */
function drawdownText(value: number): string {
  return value > 0 ? `−${value.toFixed(2)}%` : "0.00%";
}

const directionText = computed(() => {
  switch (props.stats.direction) {
    case "up":
      return "先低后高（上涨结构）";
    case "down":
      return "先高后低（下跌结构）";
    default:
      return "高低点同日（横盘）";
  }
});

/** 日历跨度只在真的比交易日多时才显示（否则是废话） */
const calendarHint = computed(() => {
  const { calendarDays, count } = props.stats;
  if (!calendarDays || calendarDays <= count) return "";
  return `自然日 ${calendarDays} 天（含停牌与非交易日）`;
});
</script>

<template>
  <section class="bg-card rounded-lg border">
    <div class="flex flex-wrap items-center gap-2 border-b px-4 py-2">
      <span class="text-sm font-medium">框选区间</span>
      <Badge variant="secondary" class="tabular text-[10px]">
        {{ formatDate(stats.startDate) }} → {{ formatDate(stats.endDate) }}
      </Badge>
      <span class="text-muted-foreground tabular text-[11px]">
        {{ stats.count }} 个交易日
        <template v-if="calendarHint">· {{ calendarHint }}</template>
      </span>
      <Badge variant="outline" class="text-[10px]">{{ directionText }}</Badge>

      <Button size="xs" variant="ghost" class="ml-auto" title="清除框选" @click="emit('clear')">
        <X /> 清除
      </Button>
    </div>

    <!-- 主指标 -->
    <div class="grid grid-cols-2 gap-x-4 gap-y-2 px-4 py-3 sm:grid-cols-3 lg:grid-cols-6">
      <div :title="noteOf('changePct')">
        <div class="text-muted-foreground text-[11px]">区间涨跌幅</div>
        <div class="tabular text-base font-semibold" :class="trendClass(stats.changePct)">
          {{ pct(stats.changePct) }}
        </div>
        <div class="text-muted-foreground tabular text-[10px]">
          首开 {{ stats.startPrice.toFixed(2) }} → 末收 {{ stats.endPrice.toFixed(2) }}
        </div>
      </div>

      <div :title="noteOf('maxDrawdownPct')">
        <div class="text-muted-foreground text-[11px]">最大回撤</div>
        <div class="tabular text-base font-semibold" :class="stats.maxDrawdownPct > 0 ? 'text-down' : ''">
          {{ drawdownText(stats.maxDrawdownPct) }}
        </div>
        <div class="text-muted-foreground tabular text-[10px]">
          <template v-if="stats.maxDrawdownPct > 0">
            谷底 {{ formatDate(stats.maxDrawdownDate) }}
          </template>
          <template v-else>区间内未回撤</template>
        </div>
      </div>

      <div :title="noteOf('amplitudePct')">
        <div class="text-muted-foreground text-[11px]">振幅</div>
        <div class="tabular text-base font-semibold">{{ stats.amplitudePct.toFixed(2) }}%</div>
        <div class="text-muted-foreground tabular text-[10px]">
          {{ stats.low.toFixed(2) }} ~ {{ stats.high.toFixed(2) }}
        </div>
      </div>

      <div :title="noteOf('volatilityPct')">
        <div class="text-muted-foreground text-[11px]">日均波动</div>
        <div class="tabular text-base font-semibold">{{ stats.volatilityPct.toFixed(2) }}%</div>
        <div class="text-muted-foreground tabular text-[10px]">
          {{ stats.upDays }} 涨 / {{ stats.downDays }} 跌
        </div>
      </div>

      <div>
        <div class="text-muted-foreground text-[11px]">区间最高</div>
        <div class="tabular text-base font-semibold">{{ stats.high.toFixed(2) }}</div>
        <div class="tabular text-[10px]" :class="trendClass(stats.highPct)">
          {{ formatDate(stats.highDate) }} · 距首开 {{ pct(stats.highPct) }}
        </div>
      </div>

      <div>
        <div class="text-muted-foreground text-[11px]">区间最低</div>
        <div class="tabular text-base font-semibold">{{ stats.low.toFixed(2) }}</div>
        <div class="tabular text-[10px]" :class="trendClass(stats.lowPct)">
          {{ formatDate(stats.lowDate) }} · 距首开 {{ pct(stats.lowPct) }}
        </div>
      </div>
    </div>

    <!-- 成交额：指数没有，隐藏而不是显示 0 -->
    <div
      v-if="!isIndex && stats.totalAmount > 0"
      class="text-muted-foreground border-t px-4 py-1.5 text-[11px]"
    >
      区间成交额 <b class="text-foreground tabular">{{ formatAmount(stats.totalAmount) }}</b>
      · 成交量 <span class="tabular">{{ formatAmount(stats.totalVolume) }}</span>
    </div>

    <div class="text-muted-foreground flex items-start gap-1.5 border-t px-4 py-2 text-[11px]">
      <Info class="mt-0.5 size-3 shrink-0" />
      <span>
        区间涨跌幅是<b class="text-foreground">首根开盘价 → 末根收盘价</b>，
        与行情软件「区间统计」同口径；把鼠标停在上面几项可以看到各自的口径。
        框选只统计 K 线本身，<b class="text-foreground">不包含区间外的价格跳空</b>。
      </span>
    </div>
  </section>
</template>
