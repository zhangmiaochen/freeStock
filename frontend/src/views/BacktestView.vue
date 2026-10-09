<script setup lang="ts">
/**
 * 组合回测页面。
 *
 * 流程（与原项目一致）：
 *   ① **历史回填**：给区间内每个交易日打分落库 —— 这是回测的前提，没有评分跑不了
 *   ② **跑回测**：引擎逐日重放决策，信号在 T 日收盘生成、T+1 开盘执行
 *   ③ **看结果**：净值曲线 + 成交明细 + 绩效指标
 */
import { computed, onMounted, ref } from "vue";
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
const backfillLimit = ref(1000);

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

async function loadPortfolios() {
  const list = (await api.portfolio.list()) as unknown as PortfolioLite[];
  portfolios.value = list ?? [];
  if (!portfolioId.value && portfolios.value.length > 0) {
    portfolioId.value = portfolios.value[0]!.id;
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

/** ① 历史回填 */
async function doBackfill() {
  const item = selected.value;
  if (!item) return;
  busy.value = "backfill";
  message.value = "";
  error.value = "";
  try {
    const result = (await api.portfolio.refreshBackfill(
      item.strategyId,
      Number(startDate.value),
      Number(endDate.value),
      backfillLimit.value,
    )) as unknown as { days: number; scored: number; skipped: string[]; elapsed: string };
    message.value = `历史回填完成：区间 ${result.days} 个交易日，成功 ${result.scored} 天，跳过 ${result.skipped?.length ?? 0} 天，耗时 ${result.elapsed}`;
  } catch (e) {
    error.value = String(e);
  } finally {
    busy.value = "";
  }
}

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

onMounted(async () => {
  defaultRange();
  await loadPortfolios();
  await loadRuns();
});
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
          <span class="text-muted-foreground">回填股票数</span>
          <input v-model.number="backfillLimit" type="number" class="border-input bg-background h-9 w-32 rounded-md border px-2" />
        </label>
        <Button :disabled="!!busy || !selected" variant="outline" @click="doBackfill">
          {{ busy === "backfill" ? "回填中…" : "① 历史回填" }}
        </Button>
        <Button :disabled="!!busy || !selected" @click="doRun">
          {{ busy === "run" ? "回测中…" : "② 跑回测" }}
        </Button>
      </CardContent>
    </Card>

    <p v-if="message" class="text-sm text-emerald-600 dark:text-emerald-400">{{ message }}</p>
    <p v-if="error" class="text-destructive text-sm">{{ error }}</p>

    <p class="text-muted-foreground text-xs">
      提示：回测前必须先做「历史回填」；回填股票数建议 ≥1000，否则候选池太小，结果与实盘建议不是一回事。
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
      </CardContent>
    </Card>

    <!-- 净值曲线 -->
    <Card v-if="chart">
      <CardHeader><CardTitle class="text-base">净值曲线</CardTitle></CardHeader>
      <CardContent>
        <svg :viewBox="`0 0 ${chart.width} ${chart.height}`" class="h-48 w-full">
          <path :d="chart.path" fill="none" stroke="currentColor" stroke-width="2" class="text-primary" />
        </svg>
        <div class="text-muted-foreground flex justify-between text-xs">
          <span>{{ daily[0]?.tradeDate }}</span>
          <span>最低 {{ chart.min.toFixed(0) }} / 最高 {{ chart.max.toFixed(0) }}</span>
          <span>{{ daily[daily.length - 1]?.tradeDate }}</span>
        </div>
      </CardContent>
    </Card>

    <!-- 历史回测记录 -->
    <Card v-if="runs.length > 0">
      <CardHeader><CardTitle class="text-base">回测记录</CardTitle></CardHeader>
      <CardContent class="overflow-auto">
        <table class="w-full text-sm">
          <thead class="text-muted-foreground">
            <tr class="text-left">
              <th class="py-1">运行</th><th>组合</th><th>策略</th><th>区间</th>
              <th class="text-right">总收益</th><th class="text-right">回撤</th>
              <th class="text-right">夏普</th><th class="text-right">笔数</th><th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="run in runs" :key="run.runId" class="border-t">
              <td class="py-1 font-mono text-xs">{{ run.runId }}</td>
              <td>{{ run.portfolioName }}</td>
              <td>{{ run.strategyLabel }}</td>
              <td class="font-mono text-xs">{{ run.startDate }}~{{ run.endDate }}</td>
              <td class="text-right">{{ run.totalReturn?.toFixed?.(2) ?? run.totalReturn }}%</td>
              <td class="text-right">{{ run.maxDrawdown?.toFixed?.(2) ?? run.maxDrawdown }}%</td>
              <td class="text-right">{{ run.sharpeRatio?.toFixed?.(2) ?? run.sharpeRatio }}</td>
              <td class="text-right">{{ run.totalTrades }}</td>
              <td class="text-right">
                <Button size="sm" variant="ghost" @click="openRun(run.runId)">查看</Button>
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
        <table class="w-full text-sm">
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
              <td class="py-1 font-mono text-xs">{{ t.tradeDate }}</td>
              <td class="font-mono text-xs">{{ t.tsCode }}</td>
              <td>{{ t.name }}</td>
              <td :class="t.action === 'buy' ? 'text-rose-600' : 'text-emerald-600'">
                {{ t.action === "buy" ? "买入" : "卖出" }}
              </td>
              <td class="text-right">{{ t.price?.toFixed?.(2) }}</td>
              <td class="text-right">{{ t.shares }}</td>
              <td class="text-right">{{ t.commission?.toFixed?.(2) }}</td>
              <td class="text-right">{{ t.tax?.toFixed?.(2) }}</td>
              <td class="text-right" :class="(t.profitLoss ?? 0) >= 0 ? 'text-emerald-600' : 'text-rose-600'">
                {{ t.action === "sell" ? `${(t.profitLoss ?? 0).toFixed(2)} (${(t.profitLossPct ?? 0).toFixed(2)}%)` : "—" }}
              </td>
              <td class="text-muted-foreground max-w-xs truncate text-xs" :title="t.reason">{{ t.reason }}</td>
            </tr>
          </tbody>
        </table>
      </CardContent>
    </Card>
  </div>
</template>
