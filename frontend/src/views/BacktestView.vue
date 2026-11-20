<script setup lang="ts">
/**
 * 组合回测页面。
 *
 * 流程（与原项目一致）：
 *   ① **历史回填**：给区间内每个交易日打分落库 —— 这是回测的前提，没有评分跑不了
 *   ② **跑回测**：引擎逐日重放决策，信号在 T 日收盘生成、T+1 开盘执行
 *   ③ **看结果**：净值曲线 + 成交明细 + 绩效指标
 */
import { computed, onActivated, onMounted, onUnmounted, ref } from "vue";
import { api } from "@/lib/backend";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

interface PortfolioLite {
  id: string;
  name: string;
  strategyId: string;
  strategyLabel: string;
}

interface BacktestRunLite {
  runId: string;
  portfolioId: string;
  portfolioName: string;
  strategyLabel: string;
  startDate: string;
  endDate: string;
  status: string;
  totalReturn: number;
  annualReturn: number;
  maxDrawdown: number;
  sharpeRatio: number;
  totalTrades: number;
  createdAt: string;
}

interface DailyLite {
  tradeDate: string;
  totalValue: number;
  cumulativeReturn: number;
  benchmarkReturn: number;
  positionCount: number;
}

interface TradeLite {
  tradeDate: string;
  tsCode: string;
  name: string;
  action: string;
  price: number;
  shares: number;
  commission: number;
  tax: number;
  profitLoss: number;
  profitLossPct: number;
  reason: string;
}

const portfolios = ref<PortfolioLite[]>([]);
const runs = ref<BacktestRunLite[]>([]);
const daily = ref<DailyLite[]>([]);
const trades = ref<TradeLite[]>([]);
const metrics = ref<Record<string, unknown>>({});

const portfolioId = ref("");
const startDate = ref("");
const endDate = ref("");
const initialCapital = ref(100000);
// 0 = 全市场。**回测就该用全市场** —— 只评前 1000 只的话候选池与实盘建议
// 不是一回事，回测出来的组合也不代表策略的真实表现。
const backfillLimit = ref(0);

const busy = ref("");
const message = ref("");
const error = ref("");
const activeRunId = ref("");

const selected = computed(() => portfolios.value.find((p) => p.id === portfolioId.value));

/** 从指标里安全取数值 */
function num(key: string): string {
  const value = metrics.value[key];
  if (typeof value !== "number") return "—";
  return value.toFixed(2);
}

function pct(key: string): string {
  const value = metrics.value[key];
  if (typeof value !== "number") return "—";
  return `${value > 0 ? "+" : ""}${value.toFixed(2)}%`;
}

const warning = computed(() => {
  const value = metrics.value["annual_return_warning"];
  return typeof value === "string" ? value : "";
});

/** 回填范围过小时的提示（广度闸门会误判 → 买入信号被删光 → 零成交） */
const scopeWarning = computed(() => {
  const value = metrics.value["scope_warning"];
  return typeof value === "string" ? value : "";
});

async function loadPortfolios() {
  const list = (await api.portfolio.list()) as unknown as PortfolioLite[];
  portfolios.value = list ?? [];
  // 保持用户的选择；但那只组合已被删除时回落到第一只 ——
  // 否则下拉框会停在一个不存在的 id 上，"跑回测"必然报错
  const stillExists = portfolios.value.some((p) => p.id === portfolioId.value);
  if (!stillExists) {
    portfolioId.value = portfolios.value[0]?.id ?? "";
  }
}

async function loadRuns() {
  runs.value = ((await api.portfolio.listBacktests("")) as unknown as BacktestRunLite[]) ?? [];
}

function defaultRange() {
  // 默认最近一年（结束日交给后端按已有评分取）
  const now = new Date();
  const end = now.toISOString().slice(0, 10).replace(/-/g, "");
  const from = new Date(now.getTime() - 365 * 24 * 3600 * 1000);
  startDate.value = from.toISOString().slice(0, 10).replace(/-/g, "");
  endDate.value = end;
}

/** ① 历史回填（后台任务 + 轮询；页面刷新/离开都不会丢进度） */
const backfillJob = ref<{ status: string; daysDone: number; daysTotal: number; currentDay: number } | null>(null);
let backfillTimer: number | undefined;

function stopBackfillPoll() {
  if (backfillTimer !== undefined) {
    window.clearInterval(backfillTimer);
    backfillTimer = undefined;
  }
}

function describeBackfill(job: any) {
  const scope = backfillLimit.value > 0 ? `前 ${backfillLimit.value} 只` : "全市场";
  const bad = job.skippedDays ?? 0;
  let text =
    `历史回填完成（${scope}）：区间 ${job.days} 个交易日 — ` +
    `新算 ${job.scored} 天` +
    ((job.scoredFromCache ?? 0) > 0 ? `，整天跳过 ${job.scoredFromCache} 天（该日已算全）` : "") +
    ((job.reused ?? 0) > 0 ? `，逐只复用 ${job.reused} 条` : "") +
    (bad > 0 ? `，${bad} 天无数据或失败` : "") +
    `，耗时 ${job.elapsed}`;
  if ((job.scoredFromCache ?? 0) === 0 && job.scored > 0) {
    text +=
      (job.reused ?? 0) > 0
        ? "。说明：切换了「回填股票数」口径，每天都有一部分股票是新补的，所以无法整天跳过。"
        : "。说明：区间内每一天都有未算过的股票（多为刚改过「回填股票数」口径），本次把它们补齐。";
  }
  return text;
}

function handleBackfillJob(job: any) {
  backfillJob.value = job;
  if (!job || job.status === "idle") return;
  if (job.status === "running") {
    busy.value = "backfill";
    const total = job.daysTotal || 0;
    message.value = total > 0
      ? job.daysDone >= total
        ? `数据已全部落库（${total}/${total} 天），正在收尾：重建评分索引 / 回收空闲页 —— 大表可能要几分钟，可以离开本页`
        : `回填中：${job.daysDone}/${total} 天（当前 ${job.currentDay || "—"}）— 可以离开本页，任务在后台继续`
      : "回填中：正在准备交易日列表…";
    return;
  }
  stopBackfillPoll();
  busy.value = "";
  if (job.status === "succeeded") {
    message.value = describeBackfill(job);
  } else if (job.status === "failed") {
    error.value = job.error || "回填失败";
  } else if (job.status === "interrupted") {
    error.value = "上次回填未正常结束（进程重启或被中断）。再点一次会把缺的部分补齐。";
  }
}

async function doBackfill(force = false) {
  const item = selected.value;
  if (!item) return;
  busy.value = "backfill";
  message.value = "";
  error.value = "";
  try {
    const started = (await api.portfolio.startBackfill(
      item.strategyId,
      Number(startDate.value),
      Number(endDate.value),
      backfillLimit.value,
      force,
    )) as unknown as { status: string };
    handleBackfillJob(started);
    stopBackfillPoll();
    backfillTimer = window.setInterval(async () => {
      try {
        handleBackfillJob(await api.portfolio.getBackfillJob());
      } catch {
        // 单次轮询失败不打断（进程重启时状态会被判 interrupted）
      }
    }, 2000);
  } catch (e) {
    busy.value = "";
    error.value = String(e);
  }
}

// 进页面时若已有任务在跑（刷新/重开页面），继续显示进度
async function resumeBackfillJob() {
  try {
    const job = await api.portfolio.getBackfillJob();
    if (job && job.status === "running") {
      handleBackfillJob(job);
      stopBackfillPoll();
      backfillTimer = window.setInterval(async () => {
        try {
          handleBackfillJob(await api.portfolio.getBackfillJob());
        } catch {
          /* 同上 */
        }
      }, 2000);
    }
  } catch {
    /* 忽略 */
  }
}

onMounted(resumeBackfillJob);
onUnmounted(stopBackfillPoll);
/** ② 跑回测 */
async function doRun() {
  const item = selected.value;
  if (!item) return;
  busy.value = "run";
  message.value = "";
  error.value = "";
  try {
    const result = (await api.portfolio.createBacktest({
      portfolioId: item.id,
      startDate: Number(startDate.value),
      endDate: Number(endDate.value),
      initialCapital: initialCapital.value,
      targetPositionCount: 0,
      maxStockCount: 0,
      benchmarkCode: "",
    } as never)) as unknown as { runId: string; metrics: Record<string, unknown> };
    metrics.value = result.metrics ?? {};
    message.value = `回测完成：${result.runId}`;
    await loadRuns();
    await openRun(result.runId);
  } catch (e) {
    error.value = String(e);
  } finally {
    busy.value = "";
  }
}

/** ③ 看某次回测的结果 */
async function openRun(runId: string) {
  activeRunId.value = runId;
  const payload = (await api.portfolio.getBacktest(runId)) as unknown as {
    run: { metrics: Record<string, unknown> };
    daily: DailyLite[];
    trades: TradeLite[];
  };
  metrics.value = payload.run?.metrics ?? {};
  daily.value = payload.daily ?? [];
  trades.value = payload.trades ?? [];
}

/** 净值曲线：用 SVG 画，避免为一张图引入图表库 */
const chart = computed(() => {
  const points = daily.value;
  if (points.length < 2) return null;
  const values = points.map((p) => p.totalValue);
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const width = 800;
  const height = 200;
  const path = points
    .map((p, i) => {
      const x = (i / (points.length - 1)) * width;
      const y = height - ((p.totalValue - min) / span) * height;
      return `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  return { path, width, height, min, max };
});

// ---- 净值曲线悬停：显示某一天的具体数据 ----
const hoverIndex = ref<number | null>(null);

const hoverPoint = computed(() => {
  const i = hoverIndex.value;
  const c = chart.value;
  if (i == null || !c || !daily.value[i]) return null;
  const p = daily.value[i];
  const span = c.max - c.min || 1;
  const x = (i / Math.max(1, daily.value.length - 1)) * c.width;
  const y = c.height - ((p.totalValue - c.min) / span) * c.height;
  return { i, x, y, p };
});

/** 悬停提示框位置：按 x 百分比定位，并向内收 12% 防止贴边溢出 */
const hoverTipStyle = computed(() => {
  const hp = hoverPoint.value;
  const c = chart.value;
  if (!hp || !c) return {};
  const pct = Math.min(88, Math.max(12, (hp.x / c.width) * 100));
  return { left: `${pct}%` };
});

function onChartMove(e: MouseEvent) {
  const c = chart.value;
  if (!c || daily.value.length < 2) return;
  const el = e.currentTarget as SVGSVGElement;
  const rect = el.getBoundingClientRect();
  if (rect.width <= 0) return;
  const ratio = (e.clientX - rect.left) / rect.width;
  const i = Math.round(ratio * (daily.value.length - 1));
  hoverIndex.value = Math.max(0, Math.min(daily.value.length - 1, i));
}

function dayReturn(i: number): number {
  const cur = daily.value[i];
  const prev = daily.value[i - 1];
  if (!cur || !prev || !prev.totalValue) return 0;
  return (cur.totalValue / prev.totalValue - 1) * 100;
}

function fmtPct(v: number | undefined | null): string {
  if (v == null || Number.isNaN(v)) return "—";
  return `${v >= 0 ? "+" : ""}${v.toFixed(2)}%`;
}

// ⚠ App.vue 用了 `<KeepAlive>`，页面切走再切回来**不会重新 mount** ——
// 只用 onMounted 的话，在组合管理页改了组合（新建/改名/删除）再切回来，
// 这里的组合列表还是旧的。
//
// onActivated 在 KeepAlive 下每次重新显示都会触发（首次挂载也触发），
// 所以真正的刷新放在这里。
let rangeInitialized = false;

async function refreshOnShow() {
  // 回测区间只在**第一次**进入时给默认值 ——
  // 否则用户填好的区间会在每次切回来时被重置
  if (!rangeInitialized) {
    defaultRange();
    rangeInitialized = true;
  }
  await loadPortfolios();
  await loadRuns();
}

onMounted(refreshOnShow); // 非 KeepAlive 场景（比如单独渲染/测试）
onActivated(refreshOnShow); // KeepAlive 下每次切回来

// ---- 回测记录删除（单个 / 批量）----
// 确认弹窗用 shadcn-vue 的 AlertDialog 原语（reka-ui）：项目 ui/ 下没有 dialog 组件，
// 只有一个 sheet 也是这么直接封装 reka-ui 的，所以这里不新增组件文件。
import {
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogOverlay,
  AlertDialogPortal,
  AlertDialogRoot,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "reka-ui";
// 注意：reka-ui 没有 AlertDialogHeader / AlertDialogFooter ——
// shadcn 里这两个只是布局 div，所以下面直接用 <div class="..."> 写。

// 选中集合用 runId；全选判断刻意写成**函数**而不是 computed，
// 避免依赖本文件是否已 import computed。
const selectedRuns = ref<string[]>([]);

function allRunsSelected(): boolean {
  return runs.value.length > 0 && selectedRuns.value.length === runs.value.length;
}

function toggleAllRuns(e: Event) {
  selectedRuns.value = (e.target as HTMLInputElement).checked
    ? runs.value.map((r) => r.runId)
    : [];
}

/** 删一条：走后端已有单删接口（级联清净值与成交） */
async function removeOneRun(runId: string) {
  error.value = "";
  message.value = "";
  try {
    await api.portfolio.deleteBacktest(runId);
    selectedRuns.value = selectedRuns.value.filter((id) => id !== runId);
    await loadRuns();
    message.value = `已删除回测记录 ${runId}`;
  } catch (e) {
    error.value = String(e);
  }
}

/** 批量删：Wails 把 Go 的 (int, error) 映射成"返回值=int、error→reject" */
async function removeSelectedRuns() {
  const ids = [...selectedRuns.value];
  if (!ids.length) return;
  error.value = "";
  message.value = "";
  try {
    await api.portfolio.deleteBacktests(ids);
    selectedRuns.value = [];
    await loadRuns();
    message.value = `已删除 ${ids.length} 条回测记录`;
  } catch (e) {
    error.value = String(e);
    await loadRuns(); // 可能已删掉一部分（后端单条失败不中断其余），刷新看实际结果
  }
}
</script>

<template>
  <div class="flex h-full flex-col gap-4 overflow-auto p-6">
    <div>
      <h1 class="text-xl font-semibold">组合回测</h1>
      <p class="text-muted-foreground mt-1 text-sm">
        用历史评分与日线复盘策略。信号在 T 日收盘生成、<b>T+1 日开盘</b>执行，含手续费、印花税与滑点。
      </p>
    </div>

    <!-- 参数区 -->
    <Card>
      <CardHeader><CardTitle class="text-base">回测参数</CardTitle></CardHeader>
      <CardContent class="flex flex-wrap items-end gap-4">
        <label class="flex flex-col gap-1 text-sm">
          <span class="text-muted-foreground">组合</span>
          <select v-model="portfolioId" class="border-input bg-background h-9 rounded-md border px-2">
            <option v-for="p in portfolios" :key="p.id" :value="p.id">
              {{ p.name }}（{{ p.strategyLabel }}）
            </option>
          </select>
        </label>
        <label class="flex flex-col gap-1 text-sm">
          <span class="text-muted-foreground">开始日</span>
          <input v-model="startDate" class="border-input bg-background h-9 rounded-md border px-2" placeholder="YYYYMMDD" />
        </label>
        <label class="flex flex-col gap-1 text-sm">
          <span class="text-muted-foreground">结束日</span>
          <input v-model="endDate" class="border-input bg-background h-9 rounded-md border px-2" placeholder="YYYYMMDD" />
        </label>
        <label class="flex flex-col gap-1 text-sm">
          <span class="text-muted-foreground">初始资金</span>
          <input v-model.number="initialCapital" type="number" class="border-input bg-background h-9 w-32 rounded-md border px-2" />
        </label>
        <label class="flex flex-col gap-1 text-sm">
          <span class="text-muted-foreground">回填股票数（0 = 全市场）</span>
          <input v-model.number="backfillLimit" type="number" class="border-input bg-background h-9 w-32 rounded-md border px-2" />
        </label>
        <Button :disabled="!!busy || !selected" variant="outline" @click="doBackfill(false)">
          {{ busy === "backfill" ? "回填中…" : "① 历史回填" }}
        </Button>
        <Button
          :disabled="!!busy || !selected"
          variant="ghost"
          title="忽略已有评分，区间内全部重算（数据修订后需要覆盖时用）"
          @click="doBackfill(true)"
        >
          全量重算区间
        </Button>
        <Button :disabled="!!busy || !selected" @click="doRun">
          {{ busy === "run" ? "回测中…" : "② 跑回测" }}
        </Button>
      </CardContent>
    </Card>

    <p v-if="message" class="text-sm text-emerald-600 dark:text-emerald-400">{{ message }}</p>
    <p v-if="error" class="text-destructive text-sm">{{ error }}</p>

    <p class="text-muted-foreground text-xs">
      提示：回测前必须先做「历史回填」；<strong>建议用全市场（股票数填 0）</strong> ——
      只回填一部分的话候选池太小，回测出来的组合与实盘建议不是一回事。
      回填**增量进行**：已经算过的交易日会直接跳过，重跑同一区间只需几秒。
      已回填的区间可以直接「② 跑回测」，回测会复用回填取到的日线，通常百毫秒级完成。
    </p>

    <!-- 指标 -->
    <Card v-if="Object.keys(metrics).length > 0">
      <CardHeader><CardTitle class="text-base">绩效指标</CardTitle></CardHeader>
      <CardContent>
        <div class="grid grid-cols-2 gap-4 text-sm md:grid-cols-4">
          <div><div class="text-muted-foreground">总收益</div><div class="text-lg font-semibold">{{ pct("total_return") }}</div></div>
          <div><div class="text-muted-foreground">年化收益</div><div class="text-lg font-semibold">{{ pct("annual_return") }}</div></div>
          <div><div class="text-muted-foreground">最大回撤</div><div class="text-lg font-semibold">{{ num("max_drawdown") }}%</div></div>
          <div><div class="text-muted-foreground">夏普比率</div><div class="text-lg font-semibold">{{ num("sharpe_ratio") }}</div></div>
          <div><div class="text-muted-foreground">卡玛比率</div><div class="text-lg font-semibold">{{ num("calmar_ratio") }}</div></div>
          <div><div class="text-muted-foreground">波动率</div><div class="text-lg font-semibold">{{ num("volatility") }}%</div></div>
          <div><div class="text-muted-foreground">成交笔数</div><div class="text-lg font-semibold">{{ metrics["total_trades"] ?? "—" }}</div></div>
          <div><div class="text-muted-foreground">胜率</div><div class="text-lg font-semibold">{{ num("win_rate") }}%</div></div>
        </div>
        <p v-if="warning" class="mt-3 text-xs text-amber-600 dark:text-amber-400">{{ warning }}</p>
        <!-- 回填范围远小于全市场时，广度闸门会误判并把买入信号全删掉 —— 这是
             "回测一笔都不成交"最常见的原因，必须显眼地讲出来 -->
        <p
          v-if="scopeWarning"
          class="mt-2 rounded-md border border-amber-500/40 bg-amber-500/5 p-2 text-xs text-amber-700 dark:text-amber-300"
        >
          ⚠ {{ scopeWarning }}
        </p>
      </CardContent>
    </Card>

    <!-- 净值曲线 -->
    <Card v-if="chart">
      <CardHeader><CardTitle class="text-base">净值曲线</CardTitle></CardHeader>
      <CardContent>
        <div class="relative" @mouseleave="hoverIndex = null">
          <svg
            :viewBox="`0 0 ${chart.width} ${chart.height}`"
            preserveAspectRatio="none"
            class="h-48 w-full cursor-crosshair"
            @mousemove="onChartMove"
            @mouseleave="hoverIndex = null"
          >
            <path :d="chart.path" fill="none" stroke="currentColor" stroke-width="2" class="text-primary" />
            <g v-if="hoverPoint">
              <line
                :x1="hoverPoint.x"
                :x2="hoverPoint.x"
                y1="0"
                :y2="chart.height"
                stroke="currentColor"
                stroke-opacity="0.3"
                stroke-dasharray="4 4"
              />
              <circle :cx="hoverPoint.x" :cy="hoverPoint.y" r="3.5" class="fill-primary" />
            </g>
          </svg>
          <div
            v-if="hoverPoint"
            class="bg-popover pointer-events-none absolute top-1 z-10 -translate-x-1/2 rounded-md border px-2 py-1.5 text-[11px] leading-5 shadow-md"
            :style="hoverTipStyle"
          >
            <div class="font-medium">{{ hoverPoint.p.tradeDate }}</div>
            <div class="text-muted-foreground">净值 {{ hoverPoint.p.totalValue.toFixed(0) }}</div>
            <div class="text-muted-foreground">累计收益 {{ fmtPct(hoverPoint.p.cumulativeReturn) }}</div>
            <div class="text-muted-foreground">当日收益 {{ fmtPct(dayReturn(hoverPoint.i)) }}</div>
            <div class="text-muted-foreground">基准累计 {{ fmtPct(hoverPoint.p.benchmarkReturn) }}</div>
            <div class="text-muted-foreground">持仓 {{ hoverPoint.p.positionCount }} 只</div>
          </div>
        </div>
        <div class="text-muted-foreground flex justify-between text-xs">
          <span>{{ daily[0]?.tradeDate }}</span>
          <span>最低 {{ chart.min.toFixed(0) }} / 最高 {{ chart.max.toFixed(0) }}</span>
          <span>{{ daily[daily.length - 1]?.tradeDate }}</span>
        </div>
      </CardContent>
    </Card>

    <!-- 历史回测记录 -->
    <Card v-if="runs.length > 0">
      <CardHeader class="flex flex-row items-center justify-between">
        <CardTitle class="text-base">回测记录（{{ runs.length }}）</CardTitle>
        <div class="flex items-center gap-2">
          <span v-if="selectedRuns.length" class="text-muted-foreground text-xs">已选 {{ selectedRuns.length }} 条</span>
          <AlertDialogRoot>
            <AlertDialogTrigger as-child>
              <Button size="sm" variant="ghost" class="text-rose-600" :disabled="!selectedRuns.length">
                删除选中
              </Button>
            </AlertDialogTrigger>
            <AlertDialogPortal>
              <AlertDialogOverlay class="fixed inset-0 z-50 bg-black/80" />
              <AlertDialogContent class="fixed left-1/2 top-1/2 z-50 w-full max-w-sm -translate-x-1/2 -translate-y-1/2 rounded-lg border bg-background p-6 shadow-lg">
                <div class="flex flex-col gap-2">
                  <AlertDialogTitle class="text-base font-semibold">
                    删除选中的 {{ selectedRuns.length }} 条回测记录？
                  </AlertDialogTitle>
                  <AlertDialogDescription class="text-muted-foreground text-sm">
                    这些记录会连同各自的净值与成交明细一起删除，不可撤销。
                  </AlertDialogDescription>
                </div>
                <div class="mt-4 flex justify-end gap-2">
                  <AlertDialogCancel as-child><Button size="sm" variant="ghost">取消</Button></AlertDialogCancel>
                  <AlertDialogAction as-child>
                    <Button size="sm" variant="ghost" class="text-rose-600" @click="removeSelectedRuns">确认删除</Button>
                  </AlertDialogAction>
                </div>
              </AlertDialogContent>
            </AlertDialogPortal>
          </AlertDialogRoot>
        </div>
      </CardHeader>
      <CardContent class="overflow-auto">
        <table class="bt-cards w-full text-sm">
          <thead class="text-muted-foreground">
            <tr class="text-left">
              <th class="py-1 w-8">
                <input type="checkbox" :checked="allRunsSelected()" @change="toggleAllRuns" />
              </th>
              <th class="py-1">运行</th><th>组合</th><th>策略</th><th>区间</th>
              <th class="text-right">总收益</th><th class="text-right">回撤</th>
              <th class="text-right">夏普</th><th class="text-right">笔数</th><th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="run in runs" :key="run.runId" class="border-t">
              <td class="py-1" data-label="选中">
                <input type="checkbox" :value="run.runId" v-model="selectedRuns" />
              </td>
              <td class="py-1 font-mono text-xs" data-label="运行" data-span="2">{{ run.runId }}</td>
              <td data-label="组合" data-span="2">{{ run.portfolioName }}</td>
              <td data-label="策略" data-span="2">{{ run.strategyLabel }}</td>
              <td class="font-mono text-xs" data-label="区间" data-span="2">{{ run.startDate }}~{{ run.endDate }}</td>
              <td class="text-right" data-label="总收益">{{ run.totalReturn?.toFixed?.(2) ?? run.totalReturn }}%</td>
              <td class="text-right" data-label="回撤">{{ run.maxDrawdown?.toFixed?.(2) ?? run.maxDrawdown }}%</td>
              <td class="text-right" data-label="夏普">{{ run.sharpeRatio?.toFixed?.(2) ?? run.sharpeRatio }}</td>
              <td class="text-right" data-label="笔数">{{ run.totalTrades }}</td>
              <td class="text-right whitespace-nowrap" data-label="操作" data-span="2">
                <Button size="sm" variant="ghost" @click="openRun(run.runId)">查看</Button>
                <AlertDialogRoot>
                  <AlertDialogTrigger as-child>
                    <Button size="sm" variant="ghost" class="text-rose-600">删除</Button>
                  </AlertDialogTrigger>
                  <AlertDialogPortal>
                    <AlertDialogOverlay class="fixed inset-0 z-50 bg-black/80" />
                    <AlertDialogContent class="fixed left-1/2 top-1/2 z-50 w-full max-w-sm -translate-x-1/2 -translate-y-1/2 rounded-lg border bg-background p-6 shadow-lg">
                      <div class="flex flex-col gap-2">
                        <AlertDialogTitle class="text-base font-semibold">删除这条回测记录？</AlertDialogTitle>
                        <AlertDialogDescription class="text-muted-foreground text-sm">
                          {{ run.runId }}（{{ run.startDate }}~{{ run.endDate }}）会连同净值与成交明细一起删除，不可撤销。
                        </AlertDialogDescription>
                      </div>
                      <div class="mt-4 flex justify-end gap-2">
                        <AlertDialogCancel as-child><Button size="sm" variant="ghost">取消</Button></AlertDialogCancel>
                        <AlertDialogAction as-child>
                          <Button size="sm" variant="ghost" class="text-rose-600" @click="removeOneRun(run.runId)">确认删除</Button>
                        </AlertDialogAction>
                      </div>
                    </AlertDialogContent>
                  </AlertDialogPortal>
                </AlertDialogRoot>
              </td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>

    <!-- 成交明细 -->
    <Card v-if="trades.length > 0">
      <CardHeader>
        <CardTitle class="text-base">成交明细（{{ trades.length }} 笔）<span class="text-muted-foreground ml-2 text-xs">{{ activeRunId }}</span></CardTitle>
      </CardHeader>
      <CardContent class="max-h-96 overflow-auto">
        <table class="bt-cards w-full text-sm">
          <thead class="text-muted-foreground sticky top-0 bg-background">
            <tr class="text-left">
              <th class="py-1">日期</th><th>代码</th><th>名称</th><th>方向</th>
              <th class="text-right">价格</th><th class="text-right">股数</th>
              <th class="text-right">手续费</th><th class="text-right">印花税</th>
              <th class="text-right">已实现盈亏</th><th>原因</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(t, i) in trades" :key="i" class="border-t">
              <td class="py-1 font-mono text-xs" data-label="日期">{{ t.tradeDate }}</td>
              <td class="font-mono text-xs" data-label="代码" data-span="2">{{ t.tsCode }}</td>
              <td data-label="名称" data-span="2">{{ t.name }}</td>
              <td :class="t.action === 'buy' ? 'text-rose-600' : 'text-emerald-600'" data-label="方向">
                {{ t.action === "buy" ? "买入" : "卖出" }}
              </td>
              <td class="text-right" data-label="价格">{{ t.price?.toFixed?.(2) }}</td>
              <td class="text-right" data-label="股数">{{ t.shares }}</td>
              <td class="text-right" data-label="手续费">{{ t.commission?.toFixed?.(2) }}</td>
              <td class="text-right" data-label="印花税">{{ t.tax?.toFixed?.(2) }}</td>
              <td class="text-right" data-label="已实现盈亏" data-span="2" :class="(t.profitLoss ?? 0) >= 0 ? 'text-emerald-600' : 'text-rose-600'">
                {{ t.action === "sell" ? `${(t.profitLoss ?? 0).toFixed(2)} (${(t.profitLossPct ?? 0).toFixed(2)}%)` : "—" }}
              </td>
              <td class="text-muted-foreground max-w-xs truncate text-xs" data-label="原因" data-span="2" :title="t.reason">{{ t.reason }}</td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>
  </div>
</template>

<style scoped>
/*
 * 移动端把两张宽表变成"卡片"。
 *
 * 问题：回测记录有 10 列、成交明细有 10 列，窄屏上表格被压到每列几十像素，
 * 组合名/策略名/股票名就变成"一个字一行"（实测：质量成长+趋势回调、上海能源），
 * 数字列也挤在一起看不出对应关系。
 *
 * 做法：<=768px 时隐藏表头，每个 <tr> 变成一个卡片，每个 <td> 用
 * data-label 显示字段名、数值右对齐。纯展示改动，不动任何数据/逻辑。
 */
@media (max-width: 768px) {
  .bt-cards thead {
    display: none;
  }
  .bt-cards,
  .bt-cards tbody {
    display: block;
    width: 100%;
  }
  .bt-cards tr {
    display: grid;
    /* minmax(0,…) 是关键：纯 1fr 的最小宽度是 min-content，
       长 runId / 区间串会把整张卡片撑出屏幕（实测右侧被裁掉） */
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 0.2rem 0.9rem;
    border: 1px solid var(--border);
    border-radius: 0.6rem;
    padding: 0.6rem 0.7rem;
    margin-bottom: 0.6rem;
    background: color-mix(in oklab, var(--muted-foreground) 6%, transparent);
  }
  .bt-cards tr.border-t {
    border-top-width: 1px;
  }
  .bt-cards td {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
    padding: 0 !important;
    min-width: 0;
    text-align: left !important;
    white-space: normal !important;
    overflow-wrap: anywhere;
    word-break: break-word;
  }
  .bt-cards td::before {
    content: attr(data-label);
    color: var(--muted-foreground);
    font-size: 0.72rem;
    flex: 0 0 auto;
    white-space: nowrap;
  }
  .bt-cards td[data-span="2"] {
    grid-column: span 2;
  }
  /* 选中/操作这类没有字段名的单元格不显示前缀 */
  .bt-cards td[data-label="选中"]::before {
    content: none;
  }
  .bt-cards td[data-label="操作"] {
    justify-content: flex-end;
    gap: 0.25rem;
    margin-top: 0.15rem;
  }
  /* 原因可能很长：卡片里换行显示，不再 truncate 成省略号 */
  .bt-cards td[data-label="原因"] {
    max-width: none !important;
    white-space: normal !important;
    overflow: visible !important;
    text-overflow: clip !important;
    display: block;
    line-height: 1.35;
  }
  .bt-cards td[data-label="原因"]::before {
    display: block;
    margin-bottom: 0.1rem;
  }
}
</style>