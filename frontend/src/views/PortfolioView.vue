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
/** 刷新评分时评多少只（保护数据源，全市场要几分钟） */
const refreshLimit = ref(800);
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
    return;
  }
  loadingDetail.value = true;
  try {
    current.value = await api.portfolio.detail(id);
  } catch (err) {
    error.value = errorMessage(err);
    current.value = null;
  } finally {
    loadingDetail.value = false;
  }
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
    // 第三个参数 force=false → **增量刷新**：已算过的代码不再重算
    //（评分只取决于 代码 + 交易日 + 策略，重算值完全相同，
    //  但每只都会省掉一次 320 个交易日的行情拉取）
    const result = await api.portfolio.refreshStrength(
      current.value.portfolio.strategyId,
      refreshLimit.value,
      force,
    );
    // 注意顺序：loadPlan 会清空提示，所以结果消息必须**在它之后**设置，
    // 否则用户看不到"评分完成"的反馈
    await loadStrength();
    await loadPlan();
    if (result.skippedFromCache) {
      applyResult.value =
        `${result.tradeDate} 的评分已是最新（${result.total} 只），本次未重算。` +
        `数据有修订需要覆盖时，点「全量重算」。`;
    } else {
      applyResult.value =
        `评分完成：${result.tradeDate} 新算 ${result.saved} 只` +
        (result.reused > 0 ? `、复用已有 ${result.reused} 只` : "") +
        `，共 ${result.total} 只，` +
        `市场状态 ${result.marketState.marketStateLabel}` +
        (result.skipped > 0 ? `（跳过 ${result.skipped} 只数据不足）` : "");
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
  if (!window.confirm(
    `确定应用建议？会影响 ${changed} 只股票，持仓会被重写（流水会保留审计记录）。`,
  )) return;

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
  if (!window.confirm(`确定删除组合「${item.name}」？持仓与流水会一起删除。`)) return;
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
  if (!window.confirm(`确定删除持仓「${name || code}」？`)) return;
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
              <p>· 数量口径是<b>层数</b>：1 层 = 总资金的 1/{{ current.portfolio.totalLayers }}，本项目组合不记股数</p>
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
                  >
                    <option :value="300">300</option>
                    <option :value="800">800</option>
                    <option :value="1500">1500</option>
                    <option :value="0">全市场（很慢）</option>
                  </select>
                </label>
                <Button size="xs" variant="outline" :disabled="refreshing" @click="refreshStrength(false)">
                  <LoaderCircle v-if="refreshing" class="animate-spin" />
                  <RefreshCw v-else />
                  {{ refreshing ? "评分中…" : "刷新评分" }}
                </Button>
                <Button
                  size="xs"
                  variant="ghost"
                  :disabled="refreshing"
                  title="忽略已有评分，全部重算（数据修订后需要覆盖时用）"
                  @click="refreshStrength(true)"
                >
                  <RefreshCw v-if="!refreshing" />
                  全量重算
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
            </div>

            <p v-if="planError" class="text-destructive border-b px-4 py-2 text-xs">{{ planError }}</p>
            <p v-if="applyResult" class="text-up border-b px-4 py-2 text-xs">{{ applyResult }}</p>

            <p
              v-if="!plan && !planError && !planLoading"
              class="text-muted-foreground px-4 py-6 text-center text-xs"
            >
              <template v-if="!strength || !strength.latestDate">
                还没有 {{ current.portfolio.strategyLabel }} 的评分数据。
                评分需要对全市场逐股拉一年日线，先点「刷新评分」（约 {{ refreshLimit || "几" }} 只需要几分钟）。
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
