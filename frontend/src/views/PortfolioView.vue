<script setup lang="ts">
/**
 * 组合管理页面（阶段一：组合 + 持仓管理）。
 *
 * 逻辑移植自 tushare-stock-tonyz 的组合管理页，字段与计算口径逐条对齐。
 * 排版重新设计（原项目是 Qt 的一堆分组框）。
 *
 * 几点与原项目一致、但容易觉得"奇怪"的地方，界面上都做了说明：
 *   - 数量口径是**层数**，不是股数。1 层 = 总资金的 1/总层数
 *   - 权重是**计划权重**（层数/总层数），不是市值权重
 *   - 组合收益率是**持仓收益的加权和**，不含现金
 *   - 手动新增持仓时层数上限是 2（原项目如此）
 */
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import {
  Briefcase,
  ChartCandlestick,
  Check,
  CalendarDays,
  CircleAlert,
  Funnel,
  LoaderCircle,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from "@lucide/vue";

import PortfolioForm from "@/components/portfolio/PortfolioForm.vue";
import PlanPanel from "@/components/portfolio/PlanPanel.vue";
import EmailScheduleCard from "@/components/portfolio/EmailScheduleCard.vue";
import StockSearchBox from "@/components/kline/StockSearchBox.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  api,
  errorMessage,
  type PlanResult,
  type Portfolio,
  type PortfolioDetail,
  type PortfolioInput,
  type PortfolioOptions,
  type PortfolioOverview,
  type PositionInput,
  type StrengthSummary,
} from "@/lib/backend";
import { formatPercent, trendClass } from "@/lib/format";
import { confirmDialog, promptDialog } from "@/lib/dialog";
import { cn } from "@/lib/utils";

const router = useRouter();

const options = ref<PortfolioOptions | null>(null);
const overview = ref<PortfolioOverview | null>(null);
const list = ref<Portfolio[]>([]);
const current = ref<PortfolioDetail | null>(null);
const currentId = ref("");

const loadingList = ref(false);
const loadingDetail = ref(false);
const saving = ref(false);
const error = ref("");
const databasePath = ref("");

/** 左侧表单状态：idle=不显示，create=新建，edit=编辑 */
const formMode = ref<"idle" | "create" | "edit">("idle");
const editing = ref<Portfolio | null>(null);

/** 新增持仓的表单状态 */
const showPositionForm = ref(false);
const positionError = ref("");
const positionInput = ref<PositionInput>({
  code: "",
  name: "",
  entryPrice: 0,
  entryDate: new Date().toISOString().slice(0, 10).replace(/-/g, ""),
  layers: 1,
  weight: 0,
  holdingType: "",
  strategyTag: "",
} as PositionInput);
const pickedName = ref("");

// ---- 阶段二：评分与调仓建议 ----
const plan = ref<PlanResult | null>(null);
const planLoading = ref(false);
const planError = ref("");
const applying = ref(false);
const refreshing = ref(false);
const strength = ref<StrengthSummary | null>(null);
const money = ref<import("@/lib/backend").MoneyView | null>(null);
/** 刷新评分时评多少只。**默认 0 = 全市场**（原版没有这个参数，广度闸门需要全市场样本） */
const refreshLimit = ref(0);

/**
 * 「刷新评分 / 全量重算 / 指定区间」都是**统一刷新所有活跃策略**
 *（原版 refresh_latest / refresh_backfill 也是全局的：取所有**非归档**组合
 * 用到的策略去重后一次刷完）。这里把范围显式讲出来，避免用户以为
 * 只刷了当前组合、或者以为没生效。
 */
/** 策略 ID → 中文名（options 里带 label，取不到就退回 ID） */
function strategyLabelOf(id: string): string {
  const found = options.value?.strategies?.find(
    (item) =>
      item.key === id ||
      (item as { value?: string }).value === id ||
      (item as { id?: string }).id === id,
  );
  return found?.label ?? id;
}

/**
 * 刷新范围提示。
 *
 * 直接从组合列表推导，避免"新建组合后提示还停留在旧策略数"：
 * 之前用的是挂载时拉一次的 activeStrategyIds，新建组合不会刷新，
 * 于是出现"3 个组合共用这 1 个策略"的错觉。
 *
 * 质量成长·趋势回调不写 stock_strength（Scored=false），不参与"刷新评分"，
 * 单独提示，避免用户以为漏刷。
 */
const refreshScopeHint = computed(() => {
  const scope = refreshLimit.value > 0 ? `前 ${refreshLimit.value} 只` : "全市场";
  const active = (list.value ?? []).filter((item) => item.status !== "archived");
  const ids = [
    ...new Set(active.map((item) => item.strategyId).filter((v): v is string => !!v)),
  ];
  if (ids.length === 0) return `统一刷新全部活跃组合用到的策略（${scope}）`;
  const isScored = (id: string) => {
    const opt = (options.value?.strategies ?? []).find((item) => item.key === id) as
      | { scored?: boolean }
      | undefined;
    return opt?.scored !== false;
  };
  const scored = ids.filter(isScored);
  const skipped = ids.filter((id) => !isScored(id));
  const labels = scored.length ? scored.map(strategyLabelOf).join("、") : "（无）";
  const notes: string[] = [];
  if (active.length > ids.length) notes.push(`${active.length} 个组合共用这 ${ids.length} 个策略`);
  if (skipped.length) notes.push(`${skipped.map(strategyLabelOf).join("、")} 不参与评分`);
  const suffix = notes.length ? `（${notes.join("；")}）` : "";
  return `统一刷新 ${labels}${suffix}（${scope}）`;
});

// 刷新范围直接从组合列表（list）推导，不再额外缓存活跃策略列表 ——
// 缓存在新建/编辑组合后不会自动更新，曾导致"3 个组合共用 1 个策略"的错觉。


// ---- 指定区间刷新 ----
//
// 「刷新评分」只算**最新交易日**；回测需要的是**历史每一天**的评分。
// 之前那个入口只在回测页有，组合页要翻过去才能用，所以这里补一个。
const useRange = ref(false);
const rangeStart = ref("");
const rangeEnd = ref("");
const rangeRefreshing = ref(false);

/** 把 YYYYMMDD 往前挪若干自然日（用 Date 算，避免非法日期）。 */
function shiftDate(yyyymmdd: string, days: number): string {
  const m = /^(\d{4})(\d{2})(\d{2})$/.exec(yyyymmdd);
  if (!m) return "";
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  d.setDate(d.getDate() + days);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}`;
}

/** 切到区间模式时，用当前评分日兜一个默认区间（近 90 天）。 */
function toggleRange(on: boolean) {
  useRange.value = on;
  if (on && !rangeEnd.value) {
    const latest = String(strength.value?.latestDate ?? "");
    if (/^\d{8}$/.test(latest)) {
      rangeEnd.value = latest;
      rangeStart.value = shiftDate(latest, -90);
    }
  }
}

/** 区间刷新：给区间内每个交易日打分（增量，已算过的会跳过）。 */
async function refreshRange(force = false) {
  if (!current.value) return;
  const start = Number(rangeStart.value);
  const end = Number(rangeEnd.value);
  if (!start || !end || start > end) {
    planError.value = "请填写合法的区间（YYYYMMDD，开始日不能晚于结束日）";
    return;
  }
  rangeRefreshing.value = true;
  planError.value = "";
  applyResult.value = "";
  try {
    // **刷所有活跃策略**（原版 `refresh_backfill(start, end)` 也是全局的）。
    // 只刷当前组合的策略会让其它组合的区间评分悄悄过期。
    const result = (await api.portfolio.refreshBackfillAll(
      start,
      end,
      refreshLimit.value,
      force,
    )) as unknown as {
      days: number; scored: number; reused: number; total: number; elapsed: string;
      strategies: { strategyLabel: string; scored: number; reused: number; error?: string }[];
      skippedStrategies?: string[];
    };
    await loadStrength();
    await loadPlan();
    const scope = refreshLimit.value > 0 ? `前 ${refreshLimit.value} 只` : "全市场";
    const failed = (result.strategies ?? []).filter((s) => s.error);
    const skipped = (result.skippedStrategies ?? []).join("、");
    const perStrategy = (result.strategies ?? [])
      .map((s) =>
        s.error ? `${s.strategyLabel}✗` : `${s.strategyLabel} ${s.scored} 天`,
      )
      .join("，");
    applyResult.value =
      `区间评分完成（${scope}）：${result.days} 个交易日、` +
      `${result.strategies?.length ?? 0} 个活跃策略` +
      (skipped ? `（${skipped} 不参与评分）` : "") +
      `（实算 ${result.scored} 天、复用 ${result.reused} 条，耗时 ${result.elapsed}）` +
      (perStrategy ? `：${perStrategy}` : "");
    if (failed.length > 0) {
      planError.value = `以下策略区间回填失败：${failed
        .map((s) => `${s.strategyLabel}（${s.error}）`)
        .join("；")}`;
    }
  } catch (err) {
    planError.value = errorMessage(err);
  } finally {
    rangeRefreshing.value = false;
  }
}
const applyResult = ref<string>("");

const summary = computed(() => current.value?.summary ?? null);
const positions = computed(() => current.value?.positions ?? []);

/** 手工把日期 YYYYMMDD 显示成 YYYY-MM-DD */
function displayDate(value: string | number | undefined): string {
  const text = String(value ?? "");
  if (text.length !== 8) return text || "—";
  return `${text.slice(0, 4)}-${text.slice(4, 6)}-${text.slice(6, 8)}`;
}

async function loadOptions() {
  try {
    options.value = await api.portfolio.getOptions();
    databasePath.value = await api.portfolio.getDatabasePath();
  } catch (err) {
    error.value = errorMessage(err);
  }
}

async function loadList() {
  loadingList.value = true;
  try {
    list.value = await api.portfolio.list();
    overview.value = await api.portfolio.overview();
    // 没有选中组合时自动选第一个
    if (!currentId.value && list.value.length > 0) {
      await selectPortfolio(list.value[0].id);
    }
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    loadingList.value = false;
  }
}

async function loadDetail(id: string) {
  if (!id) {
    current.value = null;
    money.value = null;
    return;
  }
  loadingDetail.value = true;
  try {
    current.value = await api.portfolio.detail(id);
    money.value = await api.portfolio.getMoney(id);
  } catch (err) {
    error.value = errorMessage(err);
    current.value = null;
    money.value = null;
  } finally {
    loadingDetail.value = false;
  }
}

/** 修改本金：本地保存，并按新本金重置现金/重算股数。 */
async function editCapital() {
  if (!current.value) return;
  const input = await promptDialog({
    title: "设置本金",
    message: "本金用于把推荐权重换算成股数（100 股整手），保存在本地。",
    defaultValue: String(Math.round(money.value?.initialCapital ?? 100000)),
    placeholder: "例如 100000",
    inputSuffix: "元",
  });
  if (input === null) return;
  const capital = Number(input);
  if (!Number.isFinite(capital) || capital < 0) {
    error.value = "本金必须是非负数字";
    return;
  }
  try {
    money.value = await api.portfolio.setCapital(current.value.portfolio.id, capital);
    applyResult.value = "本金已更新";
  } catch (err) {
    error.value = errorMessage(err);
  }
}

/** 金额格式：1234567 → 1,234,567 元 */
function formatMoney(value: number | undefined | null): string {
  if (value === undefined || value === null || !Number.isFinite(value)) return "—";
  return value.toLocaleString("zh-CN", { maximumFractionDigits: 0 }) + " 元";
}

async function selectPortfolio(id: string) {
  currentId.value = id;
  showPositionForm.value = false;
  plan.value = null;
  planError.value = "";
  applyResult.value = "";
  await loadDetail(id);
  // 详情拿到后就知道策略了，顺手读一下评分状态
  if (current.value) {
    await loadStrength();
  }
}

/** 读评分物化状态（判断能不能出建议） */
async function loadStrength() {
  if (!current.value) return;
  try {
    strength.value = await api.portfolio.getStrengthSummary(current.value.portfolio.strategyId);
  } catch {
    strength.value = null;
  }
}

/** 生成调仓建议 */
async function loadPlan() {
  if (!currentId.value) return;
  planLoading.value = true;
  planError.value = "";
  applyResult.value = "";
  try {
    plan.value = await api.portfolio.buildPlan(currentId.value);
  } catch (err) {
    planError.value = errorMessage(err);
    plan.value = null;
  } finally {
    planLoading.value = false;
  }
}

/** 刷新全市场评分并物化 */
async function refreshStrength(force = false) {
  if (!current.value) return;
  refreshing.value = true;
  planError.value = "";
  applyResult.value = "";
  try {
    // **刷新所有活跃策略**（原版 `refresh_latest` 的语义）——
    // 原版取所有非归档组合用到的策略一次刷完。只刷当前组合的话，
    // 其它组合的评分会悄悄过期，之后点「生成建议」就是基于旧数据。
    const result = (await api.portfolio.refreshStrengthAll(
      refreshLimit.value,
      force,
    )) as unknown as {
      tradeDate: number;
      saved: number;
      reused: number;
      total: number;
      elapsed: string;
      strategies: { strategyLabel: string; saved: number; reused: number; total: number; fromCache: boolean; error?: string }[];
    };
    await loadStrength();
    await loadPlan();
    const failed = (result.strategies ?? []).filter((s) => s.error);
    if (result.saved === 0 && failed.length === 0) {
      applyResult.value =
        `${result.tradeDate} 的评分已是最新（共 ${result.total} 条），本次未重算。` +
        `数据有修订需要覆盖时，点「全量重算」。`;
    } else {
      const perStrategy = (result.strategies ?? [])
        .map((s) =>
          s.error
            ? `${s.strategyLabel}✗`
            : `${s.strategyLabel} 新算 ${s.saved}${s.reused > 0 ? `/复用 ${s.reused}` : ""}`,
        )
        .join("，");
      const skipped = ((result as unknown as { skippedStrategies?: string[] }).skippedStrategies ?? []).join("、");
      applyResult.value =
        `评分完成：${result.tradeDate}，共 ${result.strategies?.length ?? 0} 个活跃策略` +
        (skipped ? `（${skipped} 不参与评分）` : "") +
        `（新算 ${result.saved} 条、复用 ${result.reused} 条、合计 ${result.total} 条，耗时 ${result.elapsed}）` +
        (perStrategy ? `：${perStrategy}` : "");
    }
    if (failed.length > 0) {
      planError.value = `以下策略刷新失败：${failed.map((s) => `${s.strategyLabel}（${s.error}）`).join("；")}`;
    }
  } catch (err) {
    planError.value = errorMessage(err);
  } finally {
    refreshing.value = false;
  }
}

/** 一键应用建议 */
async function applyPlan() {
  if (!currentId.value || !plan.value) return;
  const changed = plan.value.plan.rows.filter((row) => row.isExecutable).length;
  const confirmed = await confirmDialog({
    title: "应用调仓建议",
    message: `会影响 ${changed} 只股票，持仓会被重写（流水保留审计记录）。`,
    confirmText: "应用",
  });
  if (!confirmed) return;

  applying.value = true;
  planError.value = "";
  try {
    const result = await api.portfolio.applyRotation(currentId.value);
    await refresh();
    await loadPlan();
    // 顺序很重要：loadPlan 会清空提示，结果消息必须放在它之后
    applyResult.value =
      `已应用：持仓 ${result.applied.positionCount} 只 ` +
      `（清仓 ${result.applied.cleared}、新增 ${result.applied.added}、` +
      `减仓 ${result.applied.reduced}、不变 ${result.applied.unchanged}）`;
  } catch (err) {
    planError.value = errorMessage(err);
  } finally {
    applying.value = false;
  }
}

async function refresh() {
  error.value = "";
  await loadList();
  if (currentId.value) await loadDetail(currentId.value);
}
function openCreate() {
  editing.value = null;
  formMode.value = "create";
}

function openEdit(item: Portfolio) {
  editing.value = item;
  formMode.value = "edit";
}

async function submitPortfolio(input: PortfolioInput) {
  saving.value = true;
  error.value = "";
  try {
    if (formMode.value === "edit" && editing.value) {
      await api.portfolio.update(editing.value.id, input);
    } else {
      const created = await api.portfolio.create(input);
      currentId.value = created.id;
    }
    formMode.value = "idle";
    editing.value = null;
    await refresh();
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    saving.value = false;
  }
}

async function removePortfolio(item: Portfolio) {
  const ok = await confirmDialog({
    title: `删除组合「${item.name}」`,
    message: "持仓与流水会一起删除，且不可恢复。",
    confirmText: "删除",
    danger: true,
  });
  if (!ok) return;
  error.value = "";
  try {
    await api.portfolio.remove(item.id);
    if (currentId.value === item.id) {
      currentId.value = "";
      current.value = null;
    }
    await refresh();
  } catch (err) {
    error.value = errorMessage(err);
  }
}

// ---- 持仓 ----

function openPositionForm() {
  showPositionForm.value = true;
  positionError.value = "";
  pickedName.value = "";
  positionInput.value = {
    code: "",
    name: "",
    entryPrice: 0,
    entryDate: new Date().toISOString().slice(0, 10).replace(/-/g, ""),
    layers: 1,
    weight: 0,
    holdingType: "",
    strategyTag: "",
  } as PositionInput;
}

/** 股票搜索框选中后回填代码与名称（select 的签名是 (code, item)） */
function onPickStock(code: string, item: { name?: string }) {
  positionInput.value.code = code;
  positionInput.value.name = item?.name ?? "";
  pickedName.value = item?.name ?? code;
}

async function submitPosition() {
  positionError.value = "";
  if (!positionInput.value.code) {
    positionError.value = "请先选择股票";
    return;
  }
  if (!positionInput.value.entryPrice || positionInput.value.entryPrice <= 0) {
    positionError.value = "建仓价格必须大于 0";
    return;
  }
  saving.value = true;
  try {
    await api.portfolio.addPosition(currentId.value, positionInput.value);
    showPositionForm.value = false;
    await refresh();
  } catch (err) {
    positionError.value = errorMessage(err);
  } finally {
    saving.value = false;
  }
}

async function removePosition(code: string, name: string) {
  const ok = await confirmDialog({
    title: `删除持仓「${name || code}」`,
    message: "只删除这条持仓，不会影响其它持仓。",
    confirmText: "删除",
    danger: true,
  });
  if (!ok) return;
  try {
    await api.portfolio.removePosition(currentId.value, code);
    await refresh();
  } catch (err) {
    error.value = errorMessage(err);
  }
}

function openKline(code: string) {
  void router.push({ path: "/kline", query: { code } });
}

onMounted(async () => {
  await loadOptions();
  await loadList();
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <!-- 工具栏 -->
    <div class="flex shrink-0 flex-wrap items-center gap-3 border-b px-5 py-3">
      <Briefcase class="text-muted-foreground size-4" />
      <span class="text-sm font-medium">组合管理</span>
      <Badge v-if="overview" variant="secondary" class="text-[10px]">
        {{ overview.portfolioCount }} 个组合 / {{ overview.positionCount }} 个持仓
      </Badge>
      <Badge v-if="overview" variant="outline" class="text-[10px]">
        层数占用 {{ overview.usedLayers }} / {{ overview.totalLayers }}
      </Badge>
      <span v-if="databasePath" class="text-muted-foreground hidden text-[10px] lg:inline">
        {{ databasePath }}
      </span>
      <div class="ml-auto flex items-center gap-2">
        <Button size="xs" variant="outline" :disabled="loadingList" @click="refresh">
          <RefreshCw :class="cn(loadingList && 'animate-spin')" /> 刷新
        </Button>
        <Button size="xs" @click="openCreate"><Plus /> 新建组合</Button>
      </div>
    </div>

    <p v-if="error" class="text-destructive shrink-0 border-b px-5 py-2 text-xs">{{ error }}</p>

    <div class="flex min-h-0 flex-1">
      <!-- ============ 左侧：组合列表 ============ -->
      <aside class="flex w-[300px] shrink-0 flex-col border-r">
        <div class="min-h-0 flex-1 space-y-2 overflow-auto p-3">
          <PortfolioForm
            v-if="formMode !== 'idle'"
            :options="options"
            :editing="editing"
            :saving="saving"
            @submit="submitPortfolio"
            @cancel="formMode = 'idle'"
          />

          <p v-if="loadingList && list.length === 0" class="text-muted-foreground flex items-center gap-2 p-2 text-xs">
            <LoaderCircle class="size-3.5 animate-spin" /> 正在读取组合…
          </p>

          <p v-else-if="list.length === 0" class="text-muted-foreground p-3 text-center text-xs">
            还没有组合，点右上角「新建组合」开始
          </p>

          <button
            v-for="item in list"
            :key="item.id"
            type="button"
            class="hover:bg-muted/40 w-full rounded-lg border p-2.5 text-left transition-colors"
            :class="item.id === currentId ? 'border-primary bg-muted/50' : ''"
            @click="selectPortfolio(item.id)"
          >
            <div class="flex items-center gap-1.5">
              <span class="truncate text-sm font-medium">{{ item.name }}</span>
              <Badge
                :variant="item.status === 'archived' ? 'outline' : 'secondary'"
                class="ml-auto shrink-0 px-1 py-0 text-[10px]"
              >
                {{ item.status === "active" ? "运行中" : item.status === "paused" ? "暂停" : "归档" }}
              </Badge>
            </div>
            <div class="text-muted-foreground mt-1 flex flex-wrap items-center gap-x-2 text-[11px]">
              <span>{{ item.typeLabel }}</span>
              <span>·</span>
              <span>{{ item.strategyLabel }}</span>
            </div>
            <div class="text-muted-foreground tabular mt-0.5 flex flex-wrap gap-x-2 text-[11px]">
              <span>持仓 {{ item.positionCount }}</span>
              <span>层数 {{ item.usedLayers }}/{{ item.totalLayers }}</span>
              <span>{{ item.positionRatio.toFixed(1) }}%</span>
            </div>
          </button>
        </div>
      </aside>

      <!-- ============ 右侧：详情 ============ -->
      <div class="min-h-0 flex-1 overflow-auto p-4">
        <!-- 邮件订阅是**全局**设置（一套 SMTP、一套收件人、一个发送时间），
             所以放在"还没选组合"也能看到的位置，而不是塞进某个组合的详情里 -->
        <EmailScheduleCard
          class="mb-4"
          :portfolios="list"
          :current-id="currentId"
        />

        <p v-if="!current" class="text-muted-foreground py-20 text-center text-sm">
          从左侧选择一个组合，或新建一个
        </p>

        <div v-else class="space-y-4">
          <!-- 组合头 -->
          <section class="bg-card rounded-lg border p-4">
            <div class="flex flex-wrap items-center gap-2">
              <h2 class="text-base font-semibold">{{ current.portfolio.name }}</h2>
              <Badge variant="secondary" class="text-[11px]">{{ current.portfolio.typeLabel }}</Badge>
              <Badge variant="outline" class="text-[11px]">{{ current.portfolio.strategyLabel }}</Badge>
              <Badge variant="outline" class="text-[11px]">{{ current.portfolio.scopeLabel }}</Badge>
              <div class="ml-auto flex gap-2">
                <Button size="xs" variant="outline" @click="openEdit(current.portfolio)">
                  <Pencil /> 编辑
                </Button>
                <Button size="xs" variant="outline" @click="removePortfolio(current.portfolio)">
                  <Trash2 /> 删除
                </Button>
              </div>
            </div>

            <p v-if="current.portfolio.notes" class="text-muted-foreground mt-1 text-[11px]">
              {{ current.portfolio.notes }}
            </p>

            <!-- 资金：本金（当前权益 = 现金 + 持仓市值） -->
            <div v-if="money" class="border-border/60 mt-3 rounded-lg border p-3">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <div class="text-muted-foreground text-[11px]">当前本金（现金 + 持仓市值）</div>
                  <div class="tabular text-lg font-semibold">{{ formatMoney(money.equity) }}</div>
                </div>
                <div class="flex items-center gap-3 text-[11px]">
                  <span :class="trendClass(money.returnPct)">收益 {{ formatPercent(money.returnPct) }}</span>
                  <Button variant="outline" size="sm" class="h-6 px-2 text-[11px]" @click="editCapital">
                    修改本金
                  </Button>
                </div>
              </div>
              <div class="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-[11px] sm:grid-cols-4">
                <div>
                  <span class="text-muted-foreground">初始本金</span>
                  <span class="tabular ml-1">{{ formatMoney(money.initialCapital) }}</span>
                </div>
                <div>
                  <span class="text-muted-foreground">持仓市值</span>
                  <span class="tabular ml-1">{{ formatMoney(money.marketValue) }}</span>
                </div>
                <div>
                  <span class="text-muted-foreground">现金</span>
                  <span class="tabular ml-1">{{ formatMoney(money.cash) }}</span>
                </div>
                <div>
                  <span class="text-muted-foreground">口径</span>
                  <span class="ml-1">100 股整手</span>
                </div>
              </div>
            </div>

            <!-- 汇总指标 -->
            <div v-if="summary" class="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 sm:grid-cols-3 lg:grid-cols-6">
              <div>
                <div class="text-muted-foreground text-[11px]">组合收益率</div>
                <div class="tabular text-base font-semibold" :class="trendClass(summary.totalReturnPct)">
                  {{ formatPercent(summary.totalReturnPct) }}
                </div>
              </div>
              <div>
                <div class="text-muted-foreground text-[11px]">持仓数</div>
                <div class="tabular text-base font-semibold">{{ summary.positionCount }}</div>
              </div>
              <div>
                <div class="text-muted-foreground text-[11px]">已用层数</div>
                <div class="tabular text-base font-semibold">
                  {{ summary.usedLayers }} / {{ current.portfolio.totalLayers }}
                </div>
              </div>
              <div>
                <div class="text-muted-foreground text-[11px]">可用层数</div>
                <div class="tabular text-base font-semibold">{{ summary.availableLayers }}</div>
              </div>
              <div>
                <div class="text-muted-foreground text-[11px]">仓位占比</div>
                <div class="tabular text-base font-semibold">{{ summary.positionRatio.toFixed(1) }}%</div>
              </div>
              <div>
                <div class="text-muted-foreground text-[11px]">现价交易日</div>
                <div class="tabular text-base font-semibold">
                  {{ summary.priceDate ? displayDate(summary.priceDate) : "—" }}
                </div>
              </div>
            </div>

            <!-- 口径说明：这些地方最容易误解 -->
            <div class="text-muted-foreground mt-3 space-y-0.5 border-t pt-2 text-[11px]">
              <p>· 数量口径是<b>层数</b>：1 层 = 总资金的 1/{{ current.portfolio.totalLayers }}；质量成长会按本金折算成 100 股整手（见上方资金卡片）</p>
              <p>· 权重是<b>计划权重</b>（层数 ÷ 总层数），不是按市值算的</p>
              <p>· 组合收益率是<b>各持仓收益按权重加权求和</b>，不含未投入的现金</p>
              <p v-if="summary && summary.missingPrices > 0" class="text-amber-500">
                · 有 {{ summary.missingPrices }} 只持仓没取到现价（停牌/退市/未同步），这些的盈亏按 0 计
              </p>
            </div>
          </section>

          <!-- 持仓 -->
          <section class="bg-card overflow-hidden rounded-lg border">
            <div class="flex items-center gap-2 border-b px-4 py-2">
              <span class="text-sm font-medium">当前持仓</span>
              <Badge variant="secondary" class="text-[10px]">{{ positions.length }}</Badge>
              <Button
                size="xs"
                variant="outline"
                class="ml-auto"
                :disabled="showPositionForm"
                @click="openPositionForm"
              >
                <Plus /> 新增持仓
              </Button>
            </div>

            <!-- 新增持仓表单 -->
            <div v-if="showPositionForm" class="bg-muted/20 space-y-2 border-b px-4 py-3">
              <div class="flex flex-wrap items-center gap-2">
                <StockSearchBox
                  mode="filter"
                  placeholder="搜索股票代码或名称"
                  width-class="w-64"
                  autofocus
                  @select="onPickStock"
                />
                <span v-if="pickedName" class="text-xs">
                  已选 <b>{{ pickedName }}</b>（{{ positionInput.code }}）
                </span>
                <Button size="xs" variant="ghost" class="ml-auto" @click="showPositionForm = false">
                  <X />
                </Button>
              </div>

              <div class="flex flex-wrap items-end gap-3">
                <label class="block">
                  <span class="text-muted-foreground text-[11px]">建仓价格</span>
                  <input
                    v-model.number="positionInput.entryPrice"
                    type="number"
                    step="0.0001"
                    min="0"
                    class="tabular bg-background mt-0.5 h-7 w-24 rounded border px-2 text-xs outline-none"
                  />
                </label>
                <label class="block">
                  <span class="text-muted-foreground text-[11px]">建仓日期</span>
                  <input
                    v-model="positionInput.entryDate"
                    class="tabular bg-background mt-0.5 h-7 w-28 rounded border px-2 text-xs outline-none"
                    placeholder="YYYYMMDD"
                  />
                </label>
                <label class="block" title="1 层 = 总资金的 1/总层数；手动新增时上限为 2">
                  <span class="text-muted-foreground text-[11px]">层数</span>
                  <input
                    v-model.number="positionInput.layers"
                    type="number"
                    min="1"
                    max="2"
                    class="tabular bg-background mt-0.5 h-7 w-16 rounded border px-2 text-xs outline-none"
                  />
                </label>
                <label class="block" title="留 0 表示按层数自动换算">
                  <span class="text-muted-foreground text-[11px]">权重 %</span>
                  <input
                    v-model.number="positionInput.weight"
                    type="number"
                    step="0.01"
                    min="0"
                    class="tabular bg-background mt-0.5 h-7 w-20 rounded border px-2 text-xs outline-none"
                  />
                </label>
                <label class="block">
                  <span class="text-muted-foreground text-[11px]">仓位性质</span>
                  <select
                    v-model="positionInput.holdingType"
                    class="bg-background mt-0.5 h-7 w-24 rounded border px-1.5 text-xs outline-none"
                  >
                    <option v-for="item in options?.holdingTypes ?? []" :key="item.key" :value="item.key">
                      {{ item.label }}
                    </option>
                  </select>
                </label>
                <label class="block">
                  <span class="text-muted-foreground text-[11px]">策略标签</span>
                  <input
                    v-model="positionInput.strategyTag"
                    class="bg-background mt-0.5 h-7 w-28 rounded border px-2 text-xs outline-none"
                    placeholder="底仓/波段/做T"
                  />
                </label>
                <Button size="sm" :disabled="saving" @click="submitPosition">
                  <Plus /> 添加
                </Button>
              </div>
              <p v-if="positionError" class="text-destructive text-[11px]">{{ positionError }}</p>
            </div>

            <div v-if="loadingDetail" class="text-muted-foreground flex items-center justify-center gap-2 py-10 text-xs">
              <LoaderCircle class="size-4 animate-spin" /> 正在读取持仓…
            </div>

            <div v-else-if="positions.length === 0" class="text-muted-foreground px-4 py-10 text-center text-xs">
              这个组合还没有持仓
            </div>

            <div v-else class="overflow-auto">
              <table class="w-full text-xs">
                <thead class="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th class="px-3 py-2 text-left font-medium">代码</th>
                    <th class="px-3 py-2 text-left font-medium">名称</th>
                    <th class="px-3 py-2 text-right font-medium">层数</th>
                    <th class="px-3 py-2 text-right font-medium">权重</th>
                    <th class="px-3 py-2 text-right font-medium">成本价</th>
                    <th class="px-3 py-2 text-right font-medium">现价</th>
                    <th class="px-3 py-2 text-right font-medium">浮动盈亏</th>
                    <th class="px-3 py-2 text-left font-medium">建仓日</th>
                    <th class="px-3 py-2 text-left font-medium">性质</th>
                    <th class="px-3 py-2 text-right font-medium"></th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="position in positions" :key="position.code" class="hover:bg-muted/30 border-t">
                    <td class="tabular px-3 py-1.5 font-mono">{{ position.code }}</td>
                    <td class="px-3 py-1.5">{{ position.name || "—" }}</td>
                    <td class="tabular px-3 py-1.5 text-right">{{ position.positionLayers }}</td>
                    <td class="tabular px-3 py-1.5 text-right">{{ position.positionWeight.toFixed(2) }}%</td>
                    <td class="tabular px-3 py-1.5 text-right">{{ position.entryPrice.toFixed(2) }}</td>
                    <td class="tabular px-3 py-1.5 text-right">
                      {{ position.currentPrice ? position.currentPrice.toFixed(2) : "—" }}
                    </td>
                    <td
                      class="tabular px-3 py-1.5 text-right font-medium"
                      :class="trendClass(position.profitLossPct)"
                    >
                      {{ position.currentPrice ? formatPercent(position.profitLossPct) : "—" }}
                    </td>
                    <td class="tabular text-muted-foreground px-3 py-1.5">{{ displayDate(position.entryDate) }}</td>
                    <td class="px-3 py-1.5">
                      <Badge v-if="position.holdingType" variant="outline" class="px-1 py-0 text-[10px]">
                        {{ position.holdingType === "base" ? "底仓" : "波段仓" }}
                      </Badge>
                      <span v-else class="text-muted-foreground">—</span>
                    </td>
                    <td class="px-3 py-1.5 text-right">
                      <button
                        type="button"
                        class="text-muted-foreground hover:text-foreground mr-2"
                        title="查看 K 线"
                        @click="openKline(position.code)"
                      >
                        <ChartCandlestick class="size-3.5" />
                      </button>
                      <button
                        type="button"
                        class="text-muted-foreground hover:text-destructive"
                        title="删除持仓"
                        @click="removePosition(position.code, position.name)"
                      >
                        <Trash2 class="size-3.5" />
                      </button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>

          <!-- ============ 策略建议 ============ -->
          <section class="bg-card rounded-lg border">
            <div class="flex flex-wrap items-center gap-2 border-b px-4 py-2">
              <span class="text-sm font-medium">策略建议</span>
              <Badge v-if="strength && strength.latestDate" variant="secondary" class="text-[10px]">
                评分日 {{ strength.latestDate }} · {{ strength.count }} 只
              </Badge>
              <Badge v-else variant="outline" class="text-[10px]">尚未评分</Badge>

              <div class="ml-auto flex flex-wrap items-center gap-2">
                <label class="text-muted-foreground flex items-center gap-1 text-[11px]">
                  评分只数
                  <select
                    v-model.number="refreshLimit"
                    class="bg-background h-6 rounded border px-1 text-[11px] outline-none"
                    title="广度闸门按**全市场**统计「评分≥70 的只数」。只评一部分会让它被系统性低估，判成「广度不足」并删光买入信号 —— 回测就会一笔都不成交。"
                  >
                    <option :value="0">全市场</option>
                    <option :value="3000">3000</option>
                    <option :value="1500">1500</option>
                    <option :value="800">800</option>
                    <option :value="300">300</option>
                  </select>
                </label>
                <!-- ⚠ 这几个按钮都是**统一刷新所有活跃策略**（原版 refresh_latest /
                     refresh_backfill 也是全局的，取所有非归档组合用到的策略去重）。
                     只刷当前组合会让其它组合的评分悄悄过期。 -->
                <Button
                  size="xs"
                  variant="outline"
                  :disabled="refreshing"
                  :title="refreshScopeHint"
                  @click="refreshStrength(false)"
                >
                  <LoaderCircle v-if="refreshing" class="animate-spin" />
                  <RefreshCw v-else />
                  {{ refreshing ? "评分中…" : "刷新评分（全部策略）" }}
                </Button>
                <Button
                  size="xs"
                  variant="ghost"
                  :disabled="refreshing"
                  :title="`忽略已有评分全部重算 —— ${refreshScopeHint}`"
                  @click="refreshStrength(true)"
                >
                  <RefreshCw v-if="!refreshing" />
                  全量重算
                </Button>
                <Button
                  size="xs"
                  variant="outline"
                  :disabled="rangeRefreshing"
                  :title="`给指定区间内每个交易日打分（全部活跃策略）—— 回测需要的就是这个`"
                  @click="toggleRange(!useRange)"
                >
                  <CalendarDays v-if="!rangeRefreshing" />
                  {{ useRange ? "收起区间" : "指定区间" }}
                </Button>
                <Button size="xs" variant="outline" :disabled="planLoading" @click="loadPlan">
                  <LoaderCircle v-if="planLoading" class="animate-spin" />
                  <Funnel v-else />
                  生成建议
                </Button>
                <Button
                  size="xs"
                  :disabled="applying || !plan || !current.portfolio.allowApplyRotation"
                  :title="current.portfolio.allowApplyRotation
                    ? '把建议应用到持仓（会重写持仓，流水保留）'
                    : '该组合类型仅生成分析提醒，不支持一键应用'"
                  @click="applyPlan"
                >
                  <LoaderCircle v-if="applying" class="animate-spin" />
                  <Check v-else />
                  一键应用建议
                </Button>
              </div>

              <!-- 指定区间刷新：回测需要的是**历史每一天**的评分，
                   「刷新评分」只算最新交易日 -->
              <div
                v-if="useRange"
                class="bg-muted/40 mt-2 flex flex-wrap items-center gap-2 rounded-md px-2 py-2 text-[11px]"
              >
                <span class="text-muted-foreground">区间（含首尾）</span>
                <input
                  v-model="rangeStart"
                  class="bg-background h-6 w-24 rounded border px-1 font-mono outline-none"
                  placeholder="YYYYMMDD"
                />
                <span class="text-muted-foreground">→</span>
                <input
                  v-model="rangeEnd"
                  class="bg-background h-6 w-24 rounded border px-1 font-mono outline-none"
                  placeholder="YYYYMMDD"
                />
                <Button
                  size="xs"
                  variant="outline"
                  :disabled="rangeRefreshing"
                  @click="refreshRange(false)"
                >
                  <LoaderCircle v-if="rangeRefreshing" class="animate-spin" />
                  <RefreshCw v-else />
                  {{ rangeRefreshing ? "区间评分中…" : "刷新区间评分" }}
                </Button>
                <Button
                  size="xs"
                  variant="ghost"
                  :disabled="rangeRefreshing"
                  title="忽略已有评分，区间内全部重算"
                  @click="refreshRange(true)"
                >
                  全量重算
                </Button>
                <span class="text-muted-foreground">
                  增量进行：已算过的交易日会跳过；股票数用的就是左边的「评分只数」（0 = 全市场）。
                </span>
              </div>
            </div>

            <p v-if="planError" class="text-destructive border-b px-4 py-2 text-xs">{{ planError }}</p>
            <p v-if="applyResult" class="text-up border-b px-4 py-2 text-xs">{{ applyResult }}</p>

            <!-- 广度闸门明细 —— 「为什么没有建议」最直接的答案。
                 回填范围小于全市场时，这个数会被系统性低估，
                 闸门就会删光所有买入信号（表现为"没有任何建议"）。 -->
            <div v-if="plan && plan.plan.breadth" class="border-b px-4 py-2 text-xs">
              <span class="text-muted-foreground">市场广度 </span>
              <span
                :class="
                  plan.plan.breadth.enabled && !plan.plan.entryAllowed
                    ? 'text-destructive font-medium'
                    : 'text-muted-foreground'
                "
              >
                {{ plan.plan.breadth.readyCount }} / {{ plan.plan.breadth.minReadyCount }}
                （评分≥{{ plan.plan.breadth.readyScore }} 分，全市场 {{ plan.plan.breadth.universeCount }} 只）
              </span>
              <span v-if="!plan.plan.breadth.enabled" class="text-muted-foreground">
                · 样本不足 100 只，闸门未启用
              </span>
              <span v-else-if="!plan.plan.entryAllowed" class="text-destructive">
                · 广度不足，已暂停开新仓 —— 扩大「评分只数」或回填范围后重试
              </span>
              <span v-else class="text-up">· 广度充足</span>
            </div>

            <!-- 「为什么没有调仓动作」—— 原实现 summary.no_action_reasons -->
            <div
              v-if="plan && plan.plan.noActionReasons && plan.plan.noActionReasons.length"
              class="border-b px-4 py-2 text-xs"
            >
              <div class="text-muted-foreground mb-1">为什么没有调仓动作：</div>
              <ul class="space-y-0.5 pl-4">
                <li v-for="(reason, i) in plan.plan.noActionReasons" :key="i">· {{ reason }}</li>
              </ul>
            </div>

            <p
              v-if="!plan && !planError && !planLoading"
              class="text-muted-foreground px-4 py-6 text-center text-xs"
            >
              <template v-if="!strength || !strength.latestDate">
                还没有 {{ current.portfolio.strategyLabel }} 的评分数据。
                点「刷新评分（全部策略）」—— {{ refreshLimit > 0 ? `只评前 ${refreshLimit} 只` : "评全市场" }}，
                全市场约 30 秒。
              </template>
              <template v-else>
                点「生成建议」查看候选池与调仓动作
              </template>
            </p>

            <p v-if="planLoading" class="text-muted-foreground flex items-center justify-center gap-2 py-6 text-xs">
              <LoaderCircle class="size-4 animate-spin" /> 正在生成调仓建议…
            </p>
          </section>

          <PlanPanel v-if="plan" :plan="plan" @open-kline="openKline" />

          <!-- 流水 -->
          <section v-if="current.transactions.length" class="bg-card overflow-hidden rounded-lg border">
            <div class="flex items-center gap-2 border-b px-4 py-2">
              <span class="text-sm font-medium">持仓流水</span>
              <Badge variant="secondary" class="text-[10px]">{{ current.transactions.length }}</Badge>
              <span class="text-muted-foreground ml-auto flex items-center gap-1 text-[10px]">
                <CircleAlert class="size-3" /> 只增不改，作为审计记录
              </span>
            </div>
            <div class="max-h-64 overflow-auto">
              <table class="w-full text-xs">
                <thead class="bg-muted/40 text-muted-foreground sticky top-0">
                  <tr>
                    <th class="px-3 py-2 text-left font-medium">日期</th>
                    <th class="px-3 py-2 text-left font-medium">代码</th>
                    <th class="px-3 py-2 text-left font-medium">动作</th>
                    <th class="px-3 py-2 text-right font-medium">价格</th>
                    <th class="px-3 py-2 text-right font-medium">层数变化</th>
                    <th class="px-3 py-2 text-right font-medium">权重变化</th>
                    <th class="px-3 py-2 text-left font-medium">原因</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="txn in current.transactions" :key="txn.id" class="border-t">
                    <td class="tabular px-3 py-1.5">{{ displayDate(txn.tradeDate) }}</td>
                    <td class="tabular px-3 py-1.5 font-mono">{{ txn.code }}</td>
                    <td class="px-3 py-1.5">
                      <Badge
                        :variant="txn.action === 'buy' ? 'default' : 'outline'"
                        class="px-1 py-0 text-[10px]"
                      >
                        {{ txn.action }}
                      </Badge>
                    </td>
                    <td class="tabular px-3 py-1.5 text-right">{{ txn.price.toFixed(2) }}</td>
                    <td class="tabular px-3 py-1.5 text-right" :class="trendClass(txn.layersDelta)">
                      {{ txn.layersDelta > 0 ? "+" : "" }}{{ txn.layersDelta }}
                    </td>
                    <td class="tabular px-3 py-1.5 text-right">
                      {{ txn.weightDelta > 0 ? "+" : "" }}{{ txn.weightDelta.toFixed(2) }}%
                    </td>
                    <td class="text-muted-foreground px-3 py-1.5">{{ txn.reason }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>
        </div>
      </div>
    </div>
  </div>
</template>
