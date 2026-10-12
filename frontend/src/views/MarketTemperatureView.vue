<script setup lang="ts">
/**
 * 市场温度页面。
 *
 * 组成：顶部工具栏（周期）→ 温度面板 + 走势对比图 → 各指数卡片 → 口径说明。
 *
 * 数据来源与限制（页面里也会写）：
 *   指数行情只在**本地数据湖**的 `index_bars` 里（free-stockdb 没有指数表），
 *   本机湖里是 8 个指数、2023-10-09 起。后端会把"数据湖不可用/未入库"
 *   作为可解释的空状态返回（Available=false + Message），这里原样展示。
 *
 * 周期只影响**计算口径**（多长周期的走势参与打分），不影响取数：
 * 指数历史在后端缓存着，切周期是纯内存重算（实测几毫秒）。
 */
import { computed, onMounted, ref } from "vue";
import { CalendarRange, Info, LoaderCircle, RefreshCw, Thermometer } from "@lucide/vue";

import IndexCompareChart from "@/components/temperature/IndexCompareChart.vue";
import IndexTrendCard from "@/components/temperature/IndexTrendCard.vue";
import TemperaturePanel from "@/components/temperature/TemperaturePanel.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { api, errorMessage, type MarketTemperatureResult } from "@/lib/backend";
import {
  DEFAULT_TEMPERATURE_PERIOD,
  MAX_TEMPERATURE_PERIOD,
  MIN_TEMPERATURE_PERIOD,
  TEMPERATURE_PERIOD_OPTIONS,
  clampTemperaturePeriod,
  indexColor,
  temperaturePeriodLabel,
} from "@/lib/temperature";
import { formatDate } from "@/lib/format";

const result = ref<MarketTemperatureResult | null>(null);
const loading = ref(false);
const error = ref("");

/** 当前周期（交易日）：1 = 当日 */
const periodDays = ref(DEFAULT_TEMPERATURE_PERIOD);
const customOpen = ref(false);
/** ⚠ 有可能是 number：Input 的 v-model 挂在 type="number" 上时 Vue 会自动套 .number */
const customInput = ref<string | number>("");
const customError = ref("");

/** 周期按钮：默认选项 + 自定义（不在默认选项里时单独显示） */
const periodButtons = computed(() => {
  const list = [...TEMPERATURE_PERIOD_OPTIONS];
  if (!list.includes(periodDays.value)) list.push(periodDays.value);
  return list.sort((a, b) => a - b);
});

async function load() {
  const period = periodDays.value;
  loading.value = true;
  error.value = "";
  try {
    const data = await api.getMarketTemperature(period);
    // 切周期是异步的，回来时可能已经不是当前周期了
    if (periodDays.value !== period) return;
    result.value = data;
  } catch (err) {
    error.value = errorMessage(err);
    result.value = null;
  } finally {
    loading.value = false;
  }
}

function selectPeriod(days: number) {
  if (periodDays.value === days) return;
  periodDays.value = days;
  customOpen.value = false;
  void load();
}

function openCustom() {
  customInput.value = periodDays.value > 1 ? String(periodDays.value) : "";
  customError.value = "";
  customOpen.value = true;
}

function applyCustom() {
  const text = String(customInput.value ?? "").trim();
  const raw = Number(text);
  if (!text || !Number.isFinite(raw) || raw < MIN_TEMPERATURE_PERIOD) {
    customError.value = `请填 ${MIN_TEMPERATURE_PERIOD} ~ ${MAX_TEMPERATURE_PERIOD} 之间的交易日数（1 就是「当日」）`;
    return;
  }
  customError.value = "";
  selectPeriod(clampTemperaturePeriod(raw));
}

onMounted(load);
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <!-- 工具栏 -->
    <div class="flex shrink-0 flex-wrap items-center gap-3 border-b px-5 py-3">
      <Thermometer class="text-muted-foreground size-4" />
      <span class="text-sm font-medium">市场温度</span>

      <span class="bg-border hidden h-4 w-px sm:block" />

      <CalendarRange class="text-muted-foreground size-4" />
      <span class="text-sm font-medium">周期</span>
      <div class="flex flex-wrap items-center gap-1.5">
        <Button
          v-for="days in periodButtons"
          :key="days"
          size="xs"
          :variant="periodDays === days ? 'default' : 'outline'"
          @click="selectPeriod(days)"
        >
          {{ temperaturePeriodLabel(days) }}
        </Button>
        <Button size="xs" variant="outline" @click="openCustom">自定义</Button>
      </div>

      <div v-if="customOpen" class="flex flex-wrap items-center gap-1.5">
        <Input
          v-model="customInput"
          type="number"
          class="h-7 w-24 text-xs"
          :min="MIN_TEMPERATURE_PERIOD"
          :max="MAX_TEMPERATURE_PERIOD"
          placeholder="交易日数"
          @keyup.enter="applyCustom"
        />
        <Button size="xs" @click="applyCustom">应用</Button>
        <Button size="xs" variant="ghost" @click="customOpen = false">取消</Button>
      </div>
      <span v-if="customError" class="text-down text-[11px]">{{ customError }}</span>

      <Button size="xs" variant="outline" class="ml-auto" :disabled="loading" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" /> 刷新
      </Button>
    </div>

    <!-- 加载中 -->
    <div v-if="loading && !result" class="flex flex-1 flex-col gap-3 px-5 py-4">
      <div class="grid gap-3 lg:grid-cols-2">
        <Skeleton class="h-[320px] rounded-lg" />
        <Skeleton class="h-[320px] rounded-lg" />
      </div>
      <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
        <Skeleton v-for="i in 8" :key="i" class="h-[220px] rounded-lg" />
      </div>
      <p class="text-muted-foreground flex items-center justify-center gap-2 text-xs">
        <LoaderCircle class="size-3.5 animate-spin" /> 正在计算市场温度…
      </p>
    </div>

    <!-- 出错 -->
    <div v-else-if="error" class="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
      <Badge variant="destructive">市场温度读取失败</Badge>
      <p class="max-w-xl text-sm">{{ error }}</p>
      <Button size="sm" variant="outline" @click="load"><RefreshCw /> 重试</Button>
    </div>

    <!-- 数据不可用：说清楚为什么、怎么办，而不是显示一屏 0 -->
    <div
      v-else-if="result && !result.available"
      class="text-muted-foreground flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center"
    >
      <Thermometer class="size-6" />
      <p class="text-sm font-medium">市场温度暂时算不出来</p>
      <p class="max-w-2xl text-xs leading-relaxed">{{ result.message }}</p>
      <p class="max-w-2xl text-[11px] leading-relaxed">
        指数行情只在本地数据湖里（free-stockdb 没有指数表），
        数据湖状态可以在「数据源设置」页查看。
      </p>
      <Button size="sm" variant="outline" @click="load"><RefreshCw /> 重试</Button>
    </div>

    <!-- 内容 -->
    <div v-else-if="result" class="min-h-0 flex-1 overflow-auto px-5 py-4">
      <div class="space-y-4">
        <!-- 温度 + 走势对比 -->
        <div class="grid gap-3 lg:grid-cols-2">
          <TemperaturePanel :result="result" />
          <div class="flex flex-col gap-3">
            <IndexCompareChart
              :dates="result.dates"
              :indices="result.indices"
              :window="result.window"
              :period-label="result.periodLabel"
            />
            <section class="bg-card flex flex-wrap items-center gap-x-5 gap-y-2 rounded-lg border px-4 py-2.5 text-[11px]">
              <span class="text-muted-foreground">
                交易日 <b class="text-foreground">{{ formatDate(result.tradeDate) }}</b>
              </span>
              <span class="text-muted-foreground">
                {{ result.indexCount }} 大指数
              </span>
              <span class="text-muted-foreground">
                分位样本 <b class="text-foreground">{{ result.historyDays }}</b> 个交易日
                （{{ formatDate(result.historyStart) }} 起）
              </span>
              <span class="text-muted-foreground ml-auto">计算耗时 {{ result.elapsed }}</span>
            </section>
          </div>
        </div>

        <!-- 指数卡片 -->
        <div>
          <h2 class="mb-2 text-sm font-medium">{{ result.indexCount }} 大指数最新点数与趋势</h2>
          <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
            <IndexTrendCard
              v-for="(item, i) in result.indices"
              :key="item.code"
              :index="item"
              :period-label="result.periodLabel"
              :color="indexColor(i)"
            />
          </div>
        </div>

        <!-- 口径说明 -->
        <section class="text-muted-foreground space-y-1.5 rounded-lg border px-4 py-3 text-[11px] leading-relaxed">
          <p class="text-foreground flex items-center gap-1.5 text-xs font-medium">
            <Info class="size-3.5" /> 这个数字是怎么算出来的
          </p>
          <p>
            <b class="text-foreground">温度</b> = 四个分项（趋势 40% / 量能 25% / 位置 20% / 共识 15%）
            加权得到综合分，再取综合分在<b>全部样本</b>中的分位。所以温度可以直接读成
            「比历史上 X% 的交易日更热」。
          </p>
          <p>
            <b class="text-foreground">每个分项也先在全部样本里取分位</b>：这样"涨 2%"在 5 日周期里算高、
            在 60 日周期里算低，量能倍数与均线偏离也都能用同一套 0~100 表达。
          </p>
          <p>
            <b class="text-foreground">位置分不用点数高低</b>：指数长期向上，点数天然越来越高，
            直接比"点位在历史区间的位置"会把任何时点都判成高位。这里比的是
            「点位相对自身 250 日均线的偏离」在历史中的位置。
          </p>
          <ul class="list-disc space-y-1 pl-5">
            <li v-for="(note, i) in result.notes" :key="i">{{ note }}</li>
          </ul>
          <p v-if="result.source" class="truncate">指数数据源：{{ result.source }}</p>
        </section>
      </div>
    </div>
  </div>
</template>
