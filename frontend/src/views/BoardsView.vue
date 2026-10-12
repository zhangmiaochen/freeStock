<script setup lang="ts">
/**
 * 板块行情页面。
 *
 * 数据来源：板块成分名单（后端已缓存）+ 成分股行情 → 后端聚合成板块行情。
 * 之所以聚合放后端：口径（加权方式、覆盖率、重叠、区间基准日）属于领域逻辑，
 * 而且股票列表的行情在后端本来就有缓存，一次聚合就能出全部板块。
 *
 * 页面结构：分类切换 + 周期切换 → 统计条 → 热力图 → 表格 →（选中后）成分股面板。
 *
 * 周期分两种口径，别混：
 *   - **当日**：成分股当日行情聚合（成交额 / 换手率 / 涨跌家数 / 领涨股都是当日口径）；
 *   - **区间**：最近 N 个交易日的区间涨跌幅（可选 5/10/15/30/60 或自定义）。
 * 两者一起显示：表格里当日一列 + 各区间一列，热力图颜色跟着选中的周期走。
 */
import { computed, onMounted, ref, watch } from "vue";
import { CalendarRange, Info, LayoutGrid, LoaderCircle, RefreshCw } from "@lucide/vue";

import BoardHeatmap from "@/components/boards/BoardHeatmap.vue";
import BoardMembersPanel from "@/components/boards/BoardMembersPanel.vue";
import BoardTable from "@/components/boards/BoardTable.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  api,
  errorMessage,
  type BoardCategoryInfo,
  type BoardQuote,
  type BoardQuoteResult,
} from "@/lib/backend";
import {
  BOARD_PERIOD_OPTIONS,
  DEFAULT_BOARD_PERIOD,
  MAX_BOARD_PERIOD,
  MIN_BOARD_PERIOD,
  boardPeriodRequests,
  clampBoardPeriod,
  periodLabel,
  periodPct,
  periodWindow,
  periodWindowText,
} from "@/lib/boardPeriod";
import { formatAmount, formatDate } from "@/lib/format";

const categories = ref<BoardCategoryInfo[]>([]);
const categoryKey = ref("");
const result = ref<BoardQuoteResult | null>(null);
const selected = ref<BoardQuote | null>(null);
const loading = ref(false);
const error = ref("");
const panel = ref<HTMLElement | null>(null);

/** 当前选中的周期（交易日）：决定热力图着色、表格高亮列与默认排序 */
const periodDays = ref(DEFAULT_BOARD_PERIOD);
/** 已应用的自定义周期（null 表示没有） */
const customDays = ref<number | null>(null);
const customOpen = ref(false);
/**
 * 自定义周期的输入值。
 *
 * ⚠ 类型是 `string | number` 而不是 `string`：Input 组件上的 `v-model` 挂在
 * `type="number"` 上，Vue 会自动套用 `.number` 修饰符，于是拿到的是**数字**——
 * 按字符串处理会在 `.trim()` 上直接抛错。
 */
const customInput = ref<string | number>("");
const customError = ref("");

/** 要向后端请求的周期集合：默认 5/10/15/30/60 + 自定义 */
const requestPeriods = computed(() => boardPeriodRequests(customDays.value));

/** 表格里的区间列：以后端实际算出来的为准（它还会被限幅、去重、升序） */
const periodColumns = computed(() => result.value?.periods ?? []);

/** 热力图容量：板块少就全画，近千个概念板块只画成交额靠前的 */
const heatmapLimit = computed(() => {
  const total = result.value?.boards.length ?? 0;
  return total <= 80 ? total : 60;
});

/** 选中周期的日历窗口（基准日/起止日） */
const selectedWindow = computed(() => periodWindow(result.value?.windows, periodDays.value));

/** 选中周期下的板块涨跌家数（后端只统计当日，区间在这里数） */
const periodSummary = computed(() => {
  const boards = result.value?.boards ?? [];
  let up = 0;
  let down = 0;
  let flat = 0;
  let missing = 0;
  for (const board of boards) {
    const value = periodPct(board, periodDays.value);
    if (value === undefined) missing++;
    else if (value > 0) up++;
    else if (value < 0) down++;
    else flat++;
  }
  return { up, down, flat, missing };
});

async function load() {
  const key = categoryKey.value;
  if (!key) return;

  const periods = requestPeriods.value;
  loading.value = true;
  error.value = "";
  try {
    const data = await api.getBoardQuotes(key, periods, 0);
    // 切分类是异步的，回来时可能已经不是当前分类了
    if (categoryKey.value !== key) return;
    result.value = data;
  } catch (err) {
    error.value = errorMessage(err);
    result.value = null;
  } finally {
    loading.value = false;
  }
}

async function selectCategory(key: string) {
  if (categoryKey.value === key) return;
  categoryKey.value = key;
  selected.value = null;
  await load();
}

/** 切周期**不重新取数**：后端一次就把所有周期算好了，这里只是换个显示口径 */
function selectPeriod(days: number) {
  periodDays.value = days;
}

function openCustom() {
  customInput.value = customDays.value ? String(customDays.value) : "";
  customError.value = "";
  customOpen.value = true;
}

/** 应用自定义周期；这个周期没算过的话要补一次请求（后端只取缺的那个基准日） */
async function applyCustom() {
  const text = String(customInput.value ?? "").trim();
  const raw = Number(text);
  if (!text || !Number.isFinite(raw) || raw < MIN_BOARD_PERIOD) {
    customError.value = `请填 ${MIN_BOARD_PERIOD} ~ ${MAX_BOARD_PERIOD} 之间的交易日数（1 日就是「当日」，已在表格里）`;
    return;
  }

  const days = clampBoardPeriod(raw);
  customError.value = "";
  customOpen.value = false;
  customDays.value = days;
  periodDays.value = days;

  if (!periodColumns.value.includes(days)) await load();
}

/** 去掉自定义列，回到默认周期 */
async function clearCustom() {
  customOpen.value = false;
  customError.value = "";
  if (customDays.value === null) return;
  customDays.value = null;
  if (!BOARD_PERIOD_OPTIONS.includes(periodDays.value)) periodDays.value = DEFAULT_BOARD_PERIOD;
  await load();
}

function selectBoard(board: BoardQuote) {
  selected.value = selected.value?.code === board.code ? null : board;
  if (selected.value) {
    // 面板在表格下方，选中后滚过去，否则用户以为没反应
    void Promise.resolve().then(() => panel.value?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }
}

onMounted(async () => {
  try {
    categories.value = await api.getBoardCategories();
    if (categories.value.length > 0) {
      const preferred = categories.value.find((item) => item.key === "申万一级") ?? categories.value[0];
      categoryKey.value = preferred.key;
      await load();
    }
  } catch (err) {
    error.value = errorMessage(err);
  }
});

// 支持的分类有限，不做缓存失效；切回来重新拉一次保证是最新的行情
watch(categoryKey, () => {
  if (result.value && result.value.category !== categoryKey.value) result.value = null;
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <!-- 工具栏 -->
    <div class="flex shrink-0 flex-wrap items-center gap-3 border-b px-5 py-3">
      <LayoutGrid class="text-muted-foreground size-4" />
      <span class="text-sm font-medium">板块分类</span>
      <div class="flex flex-wrap items-center gap-1.5">
        <Button
          v-for="category in categories"
          :key="category.key"
          size="xs"
          :variant="categoryKey === category.key ? 'default' : 'outline'"
          @click="selectCategory(category.key)"
        >
          {{ category.name }}
          <Badge variant="secondary" class="ml-1 px-1 py-0 text-[10px]">{{ category.count }}</Badge>
        </Button>
      </div>

      <span class="bg-border hidden h-4 w-px sm:block" />

      <CalendarRange class="text-muted-foreground size-4" />
      <span class="text-sm font-medium">区间周期</span>
      <div class="flex flex-wrap items-center gap-1.5">
        <Button
          v-for="days in BOARD_PERIOD_OPTIONS"
          :key="days"
          size="xs"
          :variant="periodDays === days ? 'default' : 'outline'"
          @click="selectPeriod(days)"
        >
          {{ periodLabel(days) }}
        </Button>
        <Button
          size="xs"
          :variant="customDays !== null && periodDays === customDays ? 'default' : 'outline'"
          @click="openCustom"
        >
          {{ customDays !== null ? `自定义 ${periodLabel(customDays)}` : "自定义" }}
        </Button>
      </div>

      <div v-if="customOpen" class="flex flex-wrap items-center gap-1.5">
        <Input
          v-model="customInput"
          type="number"
          class="h-7 w-24 text-xs"
          :min="MIN_BOARD_PERIOD"
          :max="MAX_BOARD_PERIOD"
          placeholder="交易日数"
          @keyup.enter="applyCustom"
        />
        <Button size="xs" @click="applyCustom">应用</Button>
        <Button v-if="customDays !== null" size="xs" variant="ghost" @click="clearCustom">清除</Button>
        <Button size="xs" variant="ghost" @click="customOpen = false">取消</Button>
      </div>
      <span v-if="customError" class="text-down text-[11px]">{{ customError }}</span>

      <Button size="xs" variant="outline" class="ml-auto" :disabled="loading || !categoryKey" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" /> 刷新
      </Button>
    </div>

    <!-- 加载中 -->
    <div v-if="loading && !result" class="flex flex-1 flex-col gap-3 px-5 py-4">
      <Skeleton class="h-10 rounded-lg" />
      <Skeleton class="h-[300px] rounded-lg" />
      <Skeleton class="h-[200px] rounded-lg" />
      <p class="text-muted-foreground flex items-center justify-center gap-2 text-xs">
        <LoaderCircle class="size-3.5 animate-spin" /> 正在聚合板块行情…
      </p>
      <p class="text-muted-foreground text-center text-[11px]">
        首次进入要回看历史行情算区间涨跌幅（全市场约需十余秒），之后切分类、切周期都很快
      </p>
    </div>

    <!-- 出错 -->
    <div
      v-else-if="error"
      class="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center"
    >
      <Badge variant="destructive">板块行情读取失败</Badge>
      <p class="max-w-xl text-sm">{{ error }}</p>
      <Button size="sm" variant="outline" @click="load"><RefreshCw /> 重试</Button>
    </div>

    <!-- 内容 -->
    <div v-else-if="result" class="min-h-0 flex-1 overflow-auto px-5 py-4">
      <div class="space-y-4">
        <!-- 统计条 -->
        <section class="bg-card flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border px-4 py-3 text-xs">
          <span class="text-muted-foreground">
            交易日 <b class="text-foreground">{{ formatDate(result.tradeDate) }}</b>
          </span>
          <span class="text-muted-foreground">
            当日上涨板块 <b class="text-up">{{ result.upBoards }}</b>
            <span class="mx-1">/</span>
            下跌 <b class="text-down">{{ result.downBoards }}</b>
            <span v-if="result.flatBoards" class="ml-1">/ 平盘 {{ result.flatBoards }}</span>
          </span>
          <span class="text-muted-foreground" :title="periodWindowText(selectedWindow)">
            <template v-if="periodColumns.length === 0">
              {{ periodLabel(periodDays) }}区间 <b class="text-foreground">不可用</b>
            </template>
            <template v-else>
              {{ periodLabel(periodDays) }}上涨板块 <b class="text-up">{{ periodSummary.up }}</b>
              <span class="mx-1">/</span>
              下跌 <b class="text-down">{{ periodSummary.down }}</b>
              <span v-if="periodSummary.flat" class="ml-1">/ 平盘 {{ periodSummary.flat }}</span>
              <span v-if="periodSummary.missing" class="ml-1">
                / 无区间行情 {{ periodSummary.missing }}
              </span>
            </template>
          </span>
          <span class="text-muted-foreground">
            <template v-if="result.overlapping">
              <span
                :title="`这个分类下板块互相重叠（成分总数是标的数的 ${result.overlapRatio} 倍），求和会重复计算，不是市场总成交额`"
              >
                成分成交额合计（有重叠）<b class="text-foreground">{{ formatAmount(result.totalAmount) }}</b>
              </span>
            </template>
            <template v-else>
              总成交额 <b class="text-foreground">{{ formatAmount(result.totalAmount) }}</b>
            </template>
          </span>
          <span class="text-muted-foreground">板块数 <b class="text-foreground">{{ result.boardCount }}</b></span>
          <span class="text-muted-foreground ml-auto">聚合耗时 {{ result.elapsed }}</span>
        </section>

        <!-- 区间口径说明：不复权收盘价 + 最新市值加权，这两点不写清楚会被误读 -->
        <p
          v-if="periodColumns.length > 0"
          class="text-muted-foreground flex items-start gap-1.5 text-[11px]"
        >
          <Info class="mt-0.5 size-3 shrink-0" />
          <span>
            <b class="text-foreground">{{ periodLabel(periodDays) }}</b>
            <template v-if="selectedWindow?.available">
              ：{{ formatDate(selectedWindow.startDate) }} ~ {{ formatDate(selectedWindow.endDate) }}
              （基准 {{ formatDate(selectedWindow.baseDate) }} 收盘）
            </template>
            。区间涨跌幅按<b>流通市值加权</b>（权重用最新市值），价格用<b>不复权</b>收盘价 ——
            除权除息日会自然回落，个股层面有百分之几的偏差；
            成分名单只有当前这一份。区间起点还没有行情的成分（次新股等）不参与统计，
            表格里显示为「—」而不是 0%。
          </span>
        </p>

        <!-- 区间行情整体不可用（例如历史日线没同步）：日行情仍然有效，说清楚为什么没有 -->
        <p
          v-else-if="result.periodNotice"
          class="text-muted-foreground flex items-start gap-1.5 text-[11px]"
        >
          <Info class="mt-0.5 size-3 shrink-0" />
          <span>区间行情暂时算不出来：{{ result.periodNotice }}（当日行情不受影响）</span>
        </p>

        <!-- 重叠提示：这是最容易误读的地方，单独说一次 -->
        <p
          v-if="result.overlapping"
          class="text-muted-foreground flex items-start gap-1.5 text-[11px]"
        >
          <Info class="mt-0.5 size-3 shrink-0" />
          <span>
            「{{ result.category }}」是多对多分类：一只股票同时属于多个板块，
            成分总数是全部标的的 <b>{{ result.overlapRatio }}</b> 倍。
            因此各板块的成交额会重复计入，合计数字只用于横向比较热度，<b>不是市场总成交额</b>。
            涨跌幅不受影响（每个板块各自加权）。
          </span>
        </p>

        <!-- 热力图 -->
        <section class="h-[360px]">
          <BoardHeatmap
            :boards="result.boards"
            :limit="heatmapLimit"
            :period-days="periodDays"
            :windows="result.windows"
            @select="selectBoard"
          />
        </section>

        <!-- 板块表格 -->
        <BoardTable
          :boards="result.boards"
          :selected-code="selected?.code"
          :periods="periodColumns"
          :period-days="periodDays"
          :windows="result.windows"
          @select="selectBoard"
        />

        <!-- 成分股面板 -->
        <div ref="panel">
          <BoardMembersPanel
            v-if="selected"
            :board="selected"
            @close="selected = null"
          />
        </div>
      </div>
    </div>
  </div>
</template>
