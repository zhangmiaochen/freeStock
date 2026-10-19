<script setup lang="ts">
/**
 * 条件选股页面。
 *
 * 条件逻辑移植自 tushare-stock-tonyz 的 StockSelector.filter_stocks，
 * 按 FreeStock 的数据可得性做了裁剪；板块/行业条件换成了
 * free-stockdb 的 申万一级/二级/三级 + 概念。
 *
 * 页面结构：左侧条件面板（独立滚动）+ 右侧结果。
 * 条件变动会自动存进配置文件，下次打开接着用。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { Funnel, LoaderCircle, RefreshCw, RotateCcw } from "@lucide/vue";

import ConditionPanel from "@/components/screener/ConditionPanel.vue";
import ScreenResultTable from "@/components/screener/ScreenResultTable.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  api,
  errorMessage,
  onScreenProgress,
  type ChanlunScreenCondition,
  type ScanProgress,
  type ScreenCondition,
  type ScreenItem,
  type ScreenOptions,
  type ScreenResult,
} from "@/lib/backend";

const router = useRouter();

const options = ref<ScreenOptions | null>(null);
const condition = ref<ScreenCondition>(emptyCondition());
const result = ref<ScreenResult | null>(null);
const loading = ref(false);
const error = ref("");
const progress = ref<ScanProgress | null>(null);
const conditionSaved = ref(false);

function emptyChanlunCondition(): ChanlunScreenCondition {
  // 开关默认关闭：缠论是最后一层额外计算，不能悄悄改变原有筛选结果
  return {
    enable: false,
    operLevel: "day",
    minScore: 0,
    types: [],
    states: [],
    trends: [],
    requirePivot: false,
    requireDivergence: false,
    allowCandidate: true,
    confirmedOnly: false,
    excludeConflict: false,
    multiResonance: false,
    minAdaptability: 0,
    lookbackDays: 500,
  } as ChanlunScreenCondition;
}

function emptyCondition(): ScreenCondition {
  return {
    minTotalMV: 0, maxTotalMV: 0,
    minFloatMV: 0, maxFloatMV: 0,
    minPE: 0, maxPE: 0,
    minPB: 0, maxPB: 0,
    minROE: 0, maxROE: 0,
    minTurnover: 0, maxTurnover: 0,
    minPctChg: 0, maxPctChg: 0,
    minVolumeRatio: 0, maxVolumeRatio: 0,
    minAmount: 0, maxAmount: 0,
    excludeST: true,
    markets: [],
    hasQuoteOnly: true,
    boardFilters: [],
    maConditions: [],
    enableDrawdown: false,
    drawdownDays: 30,
    minDrawdown: 0, maxDrawdown: 0,
    // 数据湖条件
    minPS: 0, maxPS: 0,
    minInstitution: 0, maxInstitution: 0,
    minQuarterGrowth: 0,
    enableRelativeStrength: false,
    relativeStrengthDays: 20,
    minRelativeStrength: 0,
    limit: 0,
    // 0 = 用最新交易日
    tradeDate: 0,
    // 缠论筛选（最后一层）
    chanlun: emptyChanlunCondition(),
  } as ScreenCondition;
}

/** 已启用的条件数量，用于按钮上的提示 */
const activeCount = computed(() => {
  const c = condition.value;
  let count = 0;
  const pairs: [number, number][] = [
    [c.minTotalMV, c.maxTotalMV], [c.minFloatMV, c.maxFloatMV],
    [c.minPE, c.maxPE], [c.minPB, c.maxPB], [c.minROE, c.maxROE],
    [c.minTurnover, c.maxTurnover], [c.minPctChg, c.maxPctChg],
    [c.minVolumeRatio, c.maxVolumeRatio], [c.minAmount, c.maxAmount],
  ];
  for (const [min, max] of pairs) if (min !== 0 || max !== 0) count++;
  if (c.excludeST) count++;
  if (c.hasQuoteOnly) count++;
  if (c.markets.length) count++;
  count += c.boardFilters.filter((f) => f.names.length > 0).length;
  count += c.maConditions.filter((m) => m.fast > 0 && m.slow > 0).length;
  if (c.enableDrawdown && c.drawdownDays >= 2) count++;
  // 数据湖条件
  if (c.minPS !== 0 || c.maxPS !== 0) count++;
  if (c.minInstitution !== 0 || c.maxInstitution !== 0) count++;
  if (c.minQuarterGrowth !== 0) count++;
  if (c.enableRelativeStrength) count++;
  // 缠论是最后一层，启用时也算一项条件
  if (c.chanlun?.enable) count++;
  return count;
});

let unsubscribe: (() => void) | null = null;

async function run() {
  loading.value = true;
  error.value = "";
  progress.value = null;
  try {
    result.value = await api.screenStocks(condition.value);
  } catch (err) {
    error.value = errorMessage(err);
    result.value = null;
  } finally {
    loading.value = false;
    progress.value = null;
  }
}

function reset() {
  condition.value = emptyCondition();
  result.value = null;
}

function openKline(item: ScreenItem) {
  void router.push({ path: "/kline", query: { code: item.code } });
}

/** 后端用 YYYYMMDD 整数传交易日，展示时补上分隔符 */
function formatTradeDate(value: number): string {
  const text = String(value ?? "");
  if (text.length !== 8) return text || "—";
  return `${text.slice(0, 4)}-${text.slice(4, 6)}-${text.slice(6, 8)}`;
}

// ---- 条件自动保存（防抖，避免每敲一个数字就写盘） ----
let saveTimer: number | undefined;
let saveSeq = 0;
let ready = false;

async function flushCondition() {
  if (!ready) return;
  const seq = ++saveSeq;
  try {
    await api.saveScreenCondition(condition.value);
    if (seq === saveSeq) conditionSaved.value = true;
  } catch {
    /* 写失败不打断使用 */
  }
}

watch(
  condition,
  () => {
    if (!ready) return;
    window.clearTimeout(saveTimer);
    saveTimer = window.setTimeout(() => void flushCondition(), 500);
  },
  { deep: true },
);

onMounted(async () => {
  unsubscribe = onScreenProgress((event) => {
    progress.value = event;
  });

  try {
    options.value = await api.getScreenOptions();
  } catch (err) {
    error.value = errorMessage(err);
  }
  try {
    const saved = await api.getScreenCondition();
    if (saved) {
      condition.value = { ...emptyCondition(), ...saved.condition };
      conditionSaved.value = saved.saved;
    }
  } catch {
    /* 读不到就用默认值 */
  }
  ready = true;
});

onBeforeUnmount(() => {
  unsubscribe?.();
  window.clearTimeout(saveTimer);
  if (ready) void flushCondition();
});
</script>

<template>
  <div class="flex h-full min-h-0">
    <!-- ============ 左侧：条件 ============ -->
    <aside class="flex w-[380px] shrink-0 flex-col border-r">
      <div class="flex shrink-0 items-center gap-2 border-b px-3 py-2">
        <Funnel class="text-muted-foreground size-4" />
        <span class="text-sm font-medium">筛选条件</span>
        <Badge variant="secondary" class="text-[10px]">{{ activeCount }} 项</Badge>
        <span v-if="conditionSaved" class="text-muted-foreground text-[10px]">已保存</span>
        <Button size="xs" variant="ghost" class="ml-auto" title="清空全部条件" @click="reset">
          <RotateCcw /> 重置
        </Button>
      </div>

      <div class="min-h-0 flex-1 overflow-auto p-3">
        <ConditionPanel
          :condition="condition"
          :options="options"
          @update:condition="condition = $event"
        />
      </div>

      <div class="shrink-0 border-t p-3">
        <Button class="w-full" :disabled="loading" @click="run">
          <LoaderCircle v-if="loading" class="animate-spin" />
          <Funnel v-else />
          {{ loading ? "筛选中…" : "开始筛选" }}
        </Button>
      </div>
    </aside>

    <!-- ============ 右侧：结果 ============ -->
    <div class="flex min-h-0 flex-1 flex-col">
      <!-- 进度 -->
      <div
        v-if="loading && progress"
        class="shrink-0 border-b bg-muted/20 px-4 py-2 text-xs"
      >
        <div class="flex items-center gap-2">
          <LoaderCircle class="size-3.5 animate-spin" />
          <span>
            {{
              progress.phase === "history"
                ? "正在拉取日线并计算技术条件"
                : progress.phase === "chanlun"
                  ? "正在计算缠论结构（最后一层）"
                  : "筛选中"
            }}
          </span>
          <span class="tabular text-muted-foreground">
            {{ progress.done }} / {{ progress.total }}
          </span>
        </div>
        <div class="bg-muted mt-1.5 h-1 w-full overflow-hidden rounded">
          <div
            class="bg-primary h-full transition-all"
            :style="{ width: `${progress.total ? (progress.done / progress.total) * 100 : 0}%` }"
          />
        </div>
      </div>

      <!-- 统计条 -->
      <div
        v-if="result"
        data-testid="screen-stats"
        class="flex shrink-0 flex-wrap items-center gap-x-5 gap-y-1 border-b px-4 py-2 text-xs"
      >
        <span class="text-muted-foreground">
          交易日
          <b class="text-foreground">{{ formatTradeDate(result.tradeDate) }}</b>
          <Badge v-if="result.historical" variant="default" class="ml-1 text-[10px]">历史</Badge>
          <Badge v-else variant="secondary" class="ml-1 text-[10px]">最新</Badge>
        </span>
        <span class="text-muted-foreground">
          扫描 <b class="text-foreground">{{ result.scanned }}</b>
        </span>
        <span class="text-muted-foreground">
          基础条件后 <b class="text-foreground">{{ result.snapshotPassed }}</b>
        </span>
        <span v-if="result.boardPassed !== result.snapshotPassed" class="text-muted-foreground">
          板块条件后 <b class="text-foreground">{{ result.boardPassed }}</b>
        </span>
        <span
          v-if="result.lakePassed !== undefined && result.lakePassed !== result.boardPassed"
          class="text-muted-foreground"
        >
          数据湖条件后 <b class="text-foreground">{{ result.lakePassed }}</b>
        </span>
        <span v-if="result.historyEvaluated" class="text-muted-foreground">
          技术条件后 <b class="text-foreground">{{ result.techPassed }}</b>
        </span>
        <span v-if="result.chanlunEvaluated" class="text-muted-foreground">
          缠论筛选后 <b class="text-foreground">{{ result.chanlunPassed }}</b>
        </span>
        <span>
          命中
          <b class="text-base text-foreground">{{ result.matched }}</b>
          只
        </span>
        <span class="text-muted-foreground ml-auto">耗时 {{ result.elapsed }}</span>
      </div>

      <!-- 提示 -->
      <div v-if="result?.notes?.length" class="shrink-0 border-b bg-amber-500/5 px-4 py-2">
        <p v-for="note in result.notes" :key="note" class="text-muted-foreground text-[11px]">
          · {{ note }}
        </p>
      </div>

      <!-- 内容 -->
      <div class="min-h-0 flex-1 overflow-auto p-4">
        <!-- 未执行过 -->
        <div
          v-if="!result && !loading && !error"
          class="flex h-full flex-col items-center justify-center gap-3 text-center"
        >
          <div class="bg-muted text-muted-foreground flex size-12 items-center justify-center rounded-xl">
            <Funnel class="size-5" />
          </div>
          <h2 class="text-base font-semibold">设置条件后开始筛选</h2>
          <p class="text-muted-foreground max-w-md text-sm">
            左侧可组合基础条件、行业/概念板块与均线形态。
            条件逻辑移植自 tushare-stock-tonyz 的选股模块，
            板块条件已替换为本项目可用的申万行业与概念数据。
          </p>
        </div>

        <div v-else-if="error" class="flex h-full flex-col items-center justify-center gap-3">
          <Badge variant="destructive">筛选失败</Badge>
          <p class="max-w-xl text-sm">{{ error }}</p>
          <Button size="sm" variant="outline" @click="run"><RefreshCw /> 重试</Button>
        </div>

        <div v-else-if="loading && !result" class="flex h-full items-center justify-center gap-2 text-sm">
          <LoaderCircle class="size-4 animate-spin" />
          <span class="text-muted-foreground">正在筛选…</span>
        </div>

        <ScreenResultTable v-else-if="result" :items="result.items" @select="openKline" />
      </div>
    </div>
  </div>
</template>
