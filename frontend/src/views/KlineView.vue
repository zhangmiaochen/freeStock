<script setup lang="ts">
/**
 * K 线图表页面。
 *
 * 页面只做编排：取数、周期/复权切换、指标开关，具体渲染交给子组件：
 *   - StockSearchBox   顶部搜索（联想 + 拼音首字母）
 *   - StockSummaryBar  股票简要信息
 *   - MaSettings       均线增删改
 *   - IndicatorParams  布林线 / MACD 参数
 *   - KlineChart       ECharts 图表
 *
 * 指标参数与显示开关会写入后端配置文件（%AppData%\FreeStock\config.json），
 * 不是浏览器存储 —— 这样换机器、清缓存都不会丢，也便于备份。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import {
  ChartCandlestick,
  Info,
  LoaderCircle,
  RefreshCw,
  Save,
  SlidersHorizontal,
} from "@lucide/vue";

import KlineChart from "@/components/kline/KlineChart.vue";
import IndicatorParams from "@/components/kline/IndicatorParams.vue";
import MaSettings from "@/components/kline/MaSettings.vue";
import StockSearchBox from "@/components/kline/StockSearchBox.vue";
import StockSummaryBar from "@/components/kline/StockSummaryBar.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { api, errorMessage, type KlineBundle, type KlinePrefs } from "@/lib/backend";
import {
  DEFAULT_BOLL,
  DEFAULT_MACD,
  PERIODS,
  aggregate,
  defaultMaConfigs,
  normalizeBoll,
  normalizeMacd,
  type BollConfig,
  type Candle,
  type MaConfig,
  type MacdConfig,
  type Period,
} from "@/lib/indicators";
import { formatDate } from "@/lib/format";
import { cn } from "@/lib/utils";

const route = useRoute();
const router = useRouter();

type AdjustMode = "none" | "qfq" | "hfq";

const ADJUST_OPTIONS: { key: AdjustMode; label: string }[] = [
  { key: "qfq", label: "前复权" },
  { key: "hfq", label: "后复权" },
  { key: "none", label: "不复权" },
];

// ---- 偏好设置：存在后端配置文件里（%AppData%\FreeStock\config.json） ----
// 早期版本存在 localStorage，那样换台机器/清缓存就丢了，也不便于用户备份。

const code = ref("");
// 先用默认值渲染，读到配置后再覆盖
const period = ref<Period>("day");
const adjust = ref<AdjustMode>("qfq");

const bundle = ref<KlineBundle | null>(null);
const loading = ref(false);
const error = ref("");

const showSettings = ref(false);
const showBoll = ref(false);
const showMacd = ref(true);
const showVolume = ref(true);
const showFlow = ref(true);
/** 斐波那契黄金分割辅助线（区间随缩放/平移动态重算，见 lib/fibonacci） */
const showFibonacci = ref(false);
const bollConfig = ref<BollConfig>({ ...DEFAULT_BOLL });
const macdConfig = ref<MacdConfig>({ ...DEFAULT_MACD });
const showSummary = ref(true);
const maConfigs = ref<MaConfig[]>(defaultMaConfigs());

const configPath = ref("");
const prefsSaved = ref(false);
/** 读配置期间不要触发保存，否则会用默认值把已存的配置覆盖掉 */
const prefsReady = ref(false);

/** 该股是否有资金流数据（没有就不显示这个开关，免得点了没反应） */
const hasMoneyFlow = computed(() => (bundle.value?.moneyFlows?.length ?? 0) > 0);

/**
 * 当前看的是不是**大盘指数**。
 *
 * 指数与股票共用这个 K 线页，但有两处必须区别对待：
 *   - 复权：指数不存在分红送转，前/后复权与不复权完全一样，所以把复权按钮藏起来
 *     （留着会让人以为"点了没变化是坏了"）；
 *   - 股票信息栏：指数没有 PE/PB/市值/股本/资金流，那些行也藏起来。
 */
const isIndex = computed(() => bundle.value?.isIndex === true);

/** 把当前设置快照成后端需要的结构 */
function currentPrefs(): KlinePrefs {
  return {
    period: period.value,
    adjust: adjust.value,
    showBoll: showBoll.value,
    showMacd: showMacd.value,
    showVolume: showVolume.value,
    showFlow: showFlow.value,
    showFibonacci: showFibonacci.value,
    boll: { period: bollConfig.value.period, mult: bollConfig.value.mult },
    macd: {
      fast: macdConfig.value.fast,
      slow: macdConfig.value.slow,
      signal: macdConfig.value.signal,
    },
    maConfigs: maConfigs.value.map((item) => ({
      period: item.period,
      color: item.color,
      visible: item.visible,
    })),
  } as KlinePrefs;
}

function applyPrefs(stored: KlinePrefs) {
  period.value = (stored.period as Period) ?? "day";
  adjust.value = (stored.adjust as AdjustMode) ?? "qfq";
  showBoll.value = stored.showBoll ?? false;
  showMacd.value = stored.showMacd ?? true;
  showVolume.value = stored.showVolume ?? true;
  showFlow.value = stored.showFlow ?? true;
  showFibonacci.value = stored.showFibonacci ?? false;
  bollConfig.value = normalizeBoll(stored.boll ?? DEFAULT_BOLL);
  macdConfig.value = normalizeMacd(stored.macd ?? DEFAULT_MACD);

  const mas = stored.maConfigs ?? [];
  maConfigs.value =
    mas.length > 0
      ? mas.map((item, index) => ({
          period: Number(item.period) || 5,
          color: item.color || defaultMaConfigs()[index % 5].color,
          visible: item.visible !== false,
        }))
      : defaultMaConfigs();
}

/** 从配置文件读取偏好；失败时保持默认值，不影响页面使用 */
async function loadPrefs(): Promise<void> {
  try {
    const info = await api.getKlinePrefs();
    if (info) {
      configPath.value = info.configPath ?? "";
      prefsSaved.value = info.saved === true;
      if (info.prefs) applyPrefs(info.prefs);
    }
  } catch {
    /* 读不到就用默认值 */
  } finally {
    prefsReady.value = true;
  }
}

// 改动后延迟落盘：调周期、拖均线周期都会连续触发，没必要每次都写文件
let saveTimer: number | undefined;
let saveSeq = 0;

async function flushPrefs() {
  if (!prefsReady.value) return;
  const seq = ++saveSeq;
  try {
    const info = await api.saveKlinePrefs(currentPrefs());
    // 只认最后一次的结果，避免乱序回包把状态写错
    if (seq === saveSeq && info) {
      prefsSaved.value = true;
      configPath.value = info.configPath ?? configPath.value;
    }
  } catch {
    /* 写失败不打断使用，界面上有提示 */
  }
}

function scheduleSave() {
  if (!prefsReady.value) return;
  window.clearTimeout(saveTimer);
  saveTimer = window.setTimeout(() => void flushPrefs(), 400);
}

watch(
  [period, adjust, showBoll, showMacd, showVolume, showFlow, showFibonacci, bollConfig, macdConfig, maConfigs],
  () => scheduleSave(),
  { deep: true },
);

// 关页面前把还没落盘的改动补上
onBeforeUnmount(() => {
  window.clearTimeout(saveTimer);
  if (prefsReady.value) void flushPrefs();
});

/** 按当前周期聚合后的蜡烛（纯前端计算，切周期是瞬时的） */
const displayCandles = computed<Candle[]>(() => {
  const raw = (bundle.value?.candles ?? []) as unknown as Candle[];
  return aggregate(raw, period.value);
});

const visibleMaCount = computed(() => maConfigs.value.filter((item) => item.visible).length);

const periodTitle = computed(
  () => PERIODS.find((item) => item.key === period.value)?.title ?? "日线",
);

async function load(targetCode?: string, force = false) {
  const next = (targetCode ?? code.value).trim();
  if (!next) return;
  code.value = next;

  loading.value = true;
  error.value = "";
  if (force) bundle.value = null;

  try {
    const data = await api.getKline(next, adjust.value, 1990);
    if (!data || !data.candles || data.candles.length === 0) {
      throw new Error(`没有取到 ${next} 的 K 线数据`);
    }
    bundle.value = data;
  } catch (err) {
    error.value = errorMessage(err);
    bundle.value = null;
  } finally {
    loading.value = false;
  }
}

function onSelect(selected: string) {
  code.value = selected;
  // 同步到地址栏，刷新/前进后退都能还原
  void router.replace({ path: "/kline", query: { code: selected } });
  void load(selected);
}

function reload() {
  if (code.value) void load(code.value, true);
}

onMounted(async () => {
  // 先读配置再取数：复权方式是配置里的，读晚了会先用默认值请求一次
  await loadPrefs();

  const initial = (route.query.code as string | undefined) ?? "";
  if (initial) {
    code.value = initial;
    await load(initial);
  }
});

watch(
  () => route.query.code,
  (value) => {
    const next = (value as string | undefined) ?? "";
    if (next && next !== code.value) {
      code.value = next;
      void load(next);
    }
  },
);

watch(adjust, () => {
  if (code.value) void load();
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <!-- ============ 顶部工具栏 ============ -->
    <div class="shrink-0 space-y-3 border-b px-5 py-3">
      <!-- 搜索 + 周期 + 复权 -->
      <div class="flex flex-wrap items-center gap-3">
        <StockSearchBox class="min-w-[280px] flex-1" @select="onSelect" />

        <div class="flex items-center gap-1.5">
          <span class="text-muted-foreground text-xs">周期</span>
          <Button
            v-for="item in PERIODS"
            :key="item.key"
            size="xs"
            :variant="period === item.key ? 'default' : 'outline'"
            @click="period = item.key"
          >
            {{ item.label }}
          </Button>
        </div>

        <div v-if="!isIndex" class="flex items-center gap-1.5">
          <span class="text-muted-foreground text-xs">复权</span>
          <Button
            v-for="item in ADJUST_OPTIONS"
            :key="item.key"
            size="xs"
            :variant="adjust === item.key ? 'default' : 'outline'"
            :disabled="loading"
            @click="adjust = item.key"
          >
            {{ item.label }}
          </Button>
        </div>
        <Badge v-else variant="secondary" class="text-[11px]" title="指数不存在分红送转，没有复权概念">
          指数 · 无复权
        </Badge>

        <Button size="xs" variant="outline" :disabled="loading || !code" @click="reload">
          <RefreshCw :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </div>

      <Separator />

      <!-- 指标开关 -->
      <div class="flex flex-wrap items-center gap-2">
        <span class="text-muted-foreground text-xs">指标</span>
        <Button
          size="xs"
          :variant="showSettings ? 'default' : 'outline'"
          @click="showSettings = !showSettings"
        >
          <SlidersHorizontal /> 指标设置
          <Badge variant="secondary" class="ml-1 px-1 py-0 text-[10px]">
            MA {{ visibleMaCount }}/{{ maConfigs.length }}
          </Badge>
        </Button>
        <Button
          size="xs"
          :variant="showBoll ? 'default' : 'outline'"
          @click="showBoll = !showBoll"
        >
          布林线 BOLL
        </Button>
        <Button
          size="xs"
          :variant="showMacd ? 'default' : 'outline'"
          @click="showMacd = !showMacd"
        >
          MACD
        </Button>
        <Button
          size="xs"
          :variant="showFibonacci ? 'default' : 'outline'"
          title="黄金分割辅助线：区间取当前可见窗口的最高/最低价，缩放或平移时自动重算"
          @click="showFibonacci = !showFibonacci"
        >
          斐波那契
        </Button>
        <Button
          size="xs"
          :variant="showVolume ? 'default' : 'outline'"
          @click="showVolume = !showVolume"
        >
          能量辅助线
        </Button>
        <Button
          v-if="hasMoneyFlow"
          size="xs"
          :variant="showFlow ? 'default' : 'outline'"
          title="主力净流入柱状图 + 5 期均线"
          @click="showFlow = !showFlow"
        >
          资金流
        </Button>

        <div class="ml-auto flex items-center gap-2">
          <span v-if="bundle" class="text-muted-foreground tabular text-xs">
            {{ periodTitle }} · {{ displayCandles.length }} 根 · 数据到
            {{ formatDate(bundle.profile.tradeDate) }}
          </span>
          <Button
            v-if="bundle"
            size="xs"
            variant="ghost"
            @click="showSummary = !showSummary"
          >
            {{ showSummary ? "收起信息" : "展开信息" }}
          </Button>
        </div>
      </div>

      <!-- 指标设置：均线 + 布林线/MACD 参数 -->
      <div v-if="showSettings" class="space-y-2">
        <MaSettings :configs="maConfigs" @change="maConfigs = $event" />
        <IndicatorParams
          :boll="bollConfig"
          :macd="macdConfig"
          :boll-enabled="showBoll"
          :macd-enabled="showMacd"
          @update:boll="bollConfig = $event"
          @update:macd="macdConfig = $event"
        />
        <p
          v-if="configPath"
          class="text-muted-foreground flex items-center gap-1.5 text-[11px]"
          title="配置写在这个文件里，换机器或清缓存都不会丢"
        >
          <Save class="size-3 shrink-0" />
          <span>
            以上设置自动保存到
            <code class="font-mono">{{ configPath }}</code>
            <span v-if="prefsSaved">（已保存）</span>
          </span>
        </p>
      </div>
    </div>

    <!-- ============ 未选择股票 ============ -->
    <div
      v-if="!code"
      class="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center"
    >
      <div class="bg-muted text-muted-foreground flex size-12 items-center justify-center rounded-xl">
        <ChartCandlestick class="size-5" />
      </div>
      <h2 class="text-base font-semibold">搜索一只股票或指数开始看 K 线</h2>
      <p class="text-muted-foreground max-w-md text-sm">
        支持代码、名称、拼音全拼与拼音首字母（例如 <code>600519</code>、<code>茅台</code>、
        <code>gzmt</code>）。<b class="text-foreground">指数</b>也在这里搜：
        <code>上证指数</code>、<code>szzs</code>、<code>000001.SH</code>（带交易所后缀）。
        也可以从「股票列表」里直接点某一行跳转过来。
      </p>
      <Badge variant="outline">默认显示日线，可切换 日 / 周 / 月 / 年</Badge>
    </div>

    <!-- ============ 加载中 ============ -->
    <div v-else-if="loading && !bundle" class="flex flex-1 flex-col gap-3 px-5 py-4">
      <Skeleton class="h-28 rounded-lg" />
      <Skeleton class="flex-1 rounded-lg" />
      <p class="text-muted-foreground flex items-center justify-center gap-2 text-xs">
        <LoaderCircle class="size-3.5 animate-spin" /> 正在读取 {{ code }} 的 K 线数据…
      </p>
    </div>

    <!-- ============ 出错 ============ -->
    <div
      v-else-if="error && !bundle"
      class="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center"
    >
      <Badge variant="destructive">数据读取失败</Badge>
      <p class="max-w-xl text-sm">{{ error }}</p>
      <p class="text-muted-foreground max-w-xl text-xs">
        请确认 free-stockdb 已启动，或在「数据源设置」里切换数据源。
      </p>
      <Button size="sm" variant="outline" @click="reload"><RefreshCw /> 重试</Button>
    </div>

    <!-- ============ 图表 ============ -->
    <template v-else-if="bundle">
      <div v-if="showSummary" class="shrink-0 px-5 pt-3">
        <StockSummaryBar :profile="bundle.profile" :is-index="isIndex" />

        <p class="text-muted-foreground mt-2 flex items-start gap-1.5 text-[11px]">
          <Info class="mt-0.5 size-3 shrink-0" />
          <span>{{ bundle.notes.join(" ") }}</span>
        </p>

        <!-- 斐波那契口径：区间是"可见窗口"，两个方向是同一价位的两种读法 -->
        <p
          v-if="showFibonacci"
          class="text-muted-foreground mt-2 flex items-start gap-1.5 text-[11px]"
        >
          <Info class="mt-0.5 size-3 shrink-0" />
          <span>
            斐波那契黄金分割：区间取<b class="text-foreground">当前可见窗口</b>的最高价与最低价，
            <b class="text-foreground">缩放或平移时自动重算</b>。
            每条线标两个方向 —— <b class="text-foreground">↓</b> 是从高点回落的回撤比例（涨势回撤，该价位是支撑参考），
            <b class="text-foreground">↑</b> 是从低点反弹的比例（跌势反弹，该价位是压力参考）；
            两者互为镜像，所以一条线标两个数就够，画两遍只会得到重叠的线。
          </span>
        </p>
      </div>

      <div class="min-h-0 flex-1 px-5 py-3">
        <div
          :class="cn('bg-card h-full w-full rounded-lg border p-2', loading && 'opacity-60')"
        >
          <KlineChart
            :candles="displayCandles"
            :ma-configs="maConfigs"
            :period="period"
            :show-boll="showBoll"
            :show-macd="showMacd"
            :show-volume="showVolume"
            :show-flow="showFlow"
            :show-fibonacci="showFibonacci"
            :money-flows="bundle.moneyFlows"
            :boll-config="bollConfig"
            :macd-config="macdConfig"
            :reset-key="`${code}-${period}-${adjust}`"
          />
        </div>
      </div>
    </template>
  </div>
</template>
