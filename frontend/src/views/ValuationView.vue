<script setup lang="ts">
/**
 * A 股多期限性价比评估页（/valuation）。
 *
 * 结构（方案 §10）：
 *   工具栏（只数范围 / 期限 / 刷新）
 *   → 溯源条（评估日、数据截止、模型版本、耗时）
 *   → 该期限排名 + 分布
 *   → 行业分布
 *   → 行业估值环境
 *   → 指数性价比
 *   → 个股详情（点榜上任意一行）
 *   → 口径说明（底部固定）
 *
 * 三条与其他页面一致的约定：
 *  · 加载中 → 骨架屏；真错误 → destructive 提示 + 重试；
 *  · **可解释的空状态**（后端 `available=false` + `message`）独立分支，不显示一屏 0；
 *  · 口径说明固定放页面底部，并把后端 `notes` 逐条列出。
 */
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { Info, LoaderCircle, RefreshCw, Scale, TriangleAlert } from "@lucide/vue";
import { api, errorMessage, type IndexValuationResult, type IndustryValuationDetail, type RankRow, type ValuationIndexOption, type ValuationParamsInfo, type ValuationReport } from "@/lib/backend";
import { formatDate } from "@/lib/format";
import { HORIZON_LABELS, LIMIT_OPTIONS, limitLabel, DIMENSION_LABELS, DIMENSION_ORDER } from "@/lib/valuation";
import ValuationRankTable from "@/components/valuation/ValuationRankTable.vue";
import StockValuationPanel from "@/components/valuation/StockValuationPanel.vue";
import IndustryValuationPanel from "@/components/valuation/IndustryValuationPanel.vue";
import IndexValuationPanel from "@/components/valuation/IndexValuationPanel.vue";

const router = useRouter();

const report = ref<ValuationReport | null>(null);
const loading = ref(false);
const error = ref("");
/**
 * 评估范围。默认**全市场**（0）而不是前 150 只。
 *
 * 为什么默认全市场：这个页面的用途是"找出值得买的标的"，前 150 只只覆盖
 * 市值最大的那一批 —— 小盘里的机会与"看起来便宜其实是价值陷阱"的样本
 * 都会被直接排除在外，用户看到的排名是**有偏**的。
 *
 * 代价是可接受的全市场评估（实测 6.7~8.3 秒，靠派生快照 + 8 路并发做到），
 * 而且数据集缓存之后改范围 / 切回页面都是亚秒级，所以默认给最完整的那一档。
 */
const limit = ref(0);
const activeHorizon = ref("12m");

const selectedCode = ref("");
const industryDetail = ref<IndustryValuationDetail | null>(null);
const industryLoading = ref(false);
const industryError = ref("");
const activeIndustry = ref("");

const indexOptions = ref<ValuationIndexOption[]>([]);
const indexDetail = ref<IndexValuationResult | null>(null);
const indexLoading = ref(false);
const indexError = ref("");
const activeIndex = ref("000300.SH");

const params = ref<ValuationParamsInfo | null>(null);
const showParams = ref(false);

const ranking = computed(() => {
  const list = report.value?.rankings ?? [];
  return list.find((item) => item.horizon.key === activeHorizon.value) ?? list[0] ?? null;
});

const horizonLabel = computed(() => HORIZON_LABELS[activeHorizon.value] ?? activeHorizon.value);

async function load(refresh = false) {
  loading.value = true;
  error.value = "";
  try {
    report.value = await api.getValuationReport(limit.value, refresh);
    if (report.value?.available) {
      await loadIndices();
    }
  } catch (err) {
    error.value = errorMessage(err);
    report.value = null;
  } finally {
    loading.value = false;
  }
}

async function loadIndices() {
  try {
    indexOptions.value = await api.listValuationIndices();
    if (!indexOptions.value.some((item) => item.symbol === activeIndex.value)) {
      const first = indexOptions.value.find((item) => item.hasConstituents) ?? indexOptions.value[0];
      if (first) activeIndex.value = first.symbol;
    }
    await selectIndex(activeIndex.value);
  } catch {
    // 指数清单拿不到不影响主流程（个股/行业仍可用），静默降级
    indexOptions.value = [];
  }
}

async function selectIndex(symbol: string) {
  activeIndex.value = symbol;
  indexLoading.value = true;
  indexError.value = "";
  try {
    indexDetail.value = await api.getIndexValuation(symbol);
  } catch (err) {
    indexError.value = errorMessage(err);
    indexDetail.value = null;
  } finally {
    indexLoading.value = false;
  }
}

async function selectIndustry(code: string) {
  activeIndustry.value = code;
  industryLoading.value = true;
  industryError.value = "";
  try {
    industryDetail.value = await api.getIndustryValuation(code);
  } catch (err) {
    industryError.value = errorMessage(err);
    industryDetail.value = null;
  } finally {
    industryLoading.value = false;
  }
}

function selectRow(row: RankRow) {
  selectedCode.value = row.code;
  // 详情面板挂在页面下方，选中后滚过去，避免用户以为"点了没反应"
  requestAnimationFrame(() => {
    document.getElementById("valuation-stock-detail")?.scrollIntoView({ behavior: "smooth", block: "start" });
  });
}

async function toggleParams() {
  showParams.value = !showParams.value;
  if (showParams.value && !params.value) {
    try {
      params.value = await api.getValuationParams();
    } catch (err) {
      error.value = errorMessage(err);
    }
  }
}

function openKline(code: string) {
  router.push({ path: "/kline", query: { code } });
}

onMounted(() => load(false));
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <!-- 工具栏 -->
    <div class="flex shrink-0 flex-wrap items-center gap-3 border-b px-5 py-3">
      <Scale class="text-muted-foreground size-4" />
      <span class="text-sm font-medium">多期限性价比</span>
      <span class="text-muted-foreground text-[11px]">
        1 / 3 / 6 / 12 / 36 个月独立评估 · 不以涨跌幅为依据
      </span>

      <div class="ml-auto flex flex-wrap items-center gap-2">
        <span class="text-muted-foreground text-[11px]">评估范围</span>
        <button
          v-for="option in LIMIT_OPTIONS"
          :key="option.value"
          class="rounded-md border px-2 py-1 text-[11px] transition"
          :class="limit === option.value ? 'border-primary bg-primary/10 text-foreground' : 'border-border text-muted-foreground hover:text-foreground'"
          :disabled="loading"
          @click="limit = option.value; load(true)"
        >
          {{ option.label }}
        </button>
        <button
          class="text-muted-foreground hover:text-foreground flex items-center gap-1 rounded-md border px-2 py-1 text-[11px]"
          :disabled="loading"
          @click="load(true)"
        >
          <RefreshCw class="size-3" :class="loading ? 'animate-spin' : ''" /> 重新评估
        </button>
      </div>
    </div>

    <!-- 骨架屏 -->
    <div v-if="loading && !report" class="space-y-4 px-5 py-4">
      <div class="bg-muted/40 h-8 animate-pulse rounded-md" />
      <div class="text-muted-foreground flex items-center gap-2 text-xs">
        <LoaderCircle class="size-3.5 animate-spin" />
        正在读取数据湖的行情、财报、股本与复权因子并逐只评估…
      </div>
      <p class="text-muted-foreground max-w-3xl text-[11px] leading-relaxed">
        默认评估<b>全市场（约 4400 只）</b>：先读入 5 年的行情与财报
        （约 700 万行 + 650 万行，来自数据湖目录下的
        <code>valuation_cache/</code> 本地快照），再逐只算五档评分。
        <b>首次约 12 秒</b>（读快照 5 秒 + 评估 7 秒）；
        之后切换评估范围、切回本页、点重新评估都是<b>亚秒级</b>。
      </p>
      <p class="text-muted-foreground max-w-3xl text-[11px] leading-relaxed">
        如果只想先看大市值，切到「前 150 只」可以直接复用已加载的数据，<b>不到 1 秒</b>。
        每天第一次（数据湖更新后）需要重建快照，那一次约 15~20 秒，
        它在后台完成、<b>不阻塞本次评估</b>；删除 <code>valuation_cache/</code>
        不影响正确性，只会让下一次慢一次。
      </p>
      <div class="bg-muted/30 h-64 animate-pulse rounded-lg" />
    </div>

    <!-- 错误 -->
    <div v-else-if="error" class="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
      <p class="flex items-center gap-1.5 text-sm font-medium text-rose-400">
        <TriangleAlert class="size-4" /> 性价比评估失败
      </p>
      <p class="text-muted-foreground max-w-xl text-xs leading-relaxed">{{ error }}</p>
      <button class="rounded-md border px-3 py-1.5 text-xs" @click="load(false)">重试</button>
    </div>

    <!-- 可解释的空状态 -->
    <div
      v-else-if="report && !report.available"
      class="text-muted-foreground flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center"
    >
      <Scale class="size-6" />
      <p class="text-foreground text-sm font-medium">多期限性价比暂时算不出来</p>
      <p class="max-w-2xl text-xs leading-relaxed">{{ report.message }}</p>
      <p class="max-w-2xl text-[11px] leading-relaxed">
        本功能需要数据湖的 <b>daily_bars</b>（行情）、<b>financial_statement_items</b>（财报）、
        <b>share_structure</b>（股本）三个数据集，以及 <b>meta/adj_factors_cache</b>（复权因子）。
        入库情况可以在「数据源设置」页查看。
      </p>
      <button class="rounded-md border px-3 py-1.5 text-xs" @click="load(true)">重试</button>
    </div>

    <!-- 内容 -->
    <div v-else-if="report" class="min-h-0 flex-1 overflow-auto px-5 py-4">
      <div class="space-y-4">
        <!-- 溯源条 -->
        <section class="bg-card flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border px-4 py-3 text-xs">
          <span class="text-muted-foreground">
            评估日 <b class="text-foreground">{{ formatDate(report.snapshot.evalDate) }}</b>
          </span>
          <span class="text-muted-foreground">
            模型版本 <b class="text-foreground">{{ report.snapshot.modelVersion }}</b>
          </span>
          <span class="text-muted-foreground">
            标的 <b class="text-foreground">{{ report.snapshot.universe }}</b>
            · 给出评级 <b class="text-foreground">{{ report.snapshot.evaluated }}</b>
            · 暂无法评估 <b class="text-foreground">{{ report.snapshot.unavailable }}</b>
          </span>
          <span class="text-muted-foreground">范围 <b class="text-foreground">{{ limitLabel(report.requested) }}</b></span>
          <span class="text-muted-foreground ml-auto">耗时 {{ (report.elapsedMs / 1000).toFixed(1) }} 秒</span>
          <p class="text-muted-foreground/80 w-full text-[11px]">{{ report.snapshot.dataCutoff }}</p>
        </section>

        <!-- 期限切换 + 排名 -->
        <section class="space-y-3">
          <div class="flex flex-wrap items-center gap-1.5">
            <span class="text-muted-foreground mr-1 text-[11px]">期限（各自独立计算，权重与收敛速度不同）</span>
            <button
              v-for="item in report.rankings"
              :key="item.horizon.key"
              class="rounded-md border px-2.5 py-1 text-[11px] transition"
              :class="activeHorizon === item.horizon.key ? 'border-primary bg-primary/10 text-foreground' : 'border-border text-muted-foreground hover:text-foreground'"
              @click="activeHorizon = item.horizon.key"
            >
              {{ HORIZON_LABELS[item.horizon.key] ?? item.horizon.label }}
              <span class="text-muted-foreground ml-1">均分 {{ item.distribution.mean.toFixed(1) }}</span>
            </button>
          </div>
          <ValuationRankTable :ranking="ranking" :horizon-label="horizonLabel" @select="selectRow" />
        </section>

        <!-- 个股详情 -->
        <div id="valuation-stock-detail">
          <StockValuationPanel v-if="selectedCode" :code="selectedCode" @close="selectedCode = ''" />
          <p v-else class="text-muted-foreground rounded-lg border border-dashed px-4 py-3 text-[11px]">
            点榜上任意一行查看该标的的完整评估：四层估值、成长能力与成长质量、三情景合理价值、
            安全边际与潜在收益、五档收益风险、九维度得分与逐条依据。
          </p>
        </div>

        <!-- 行业分布 -->
        <section v-if="report.industryDistribution?.length" class="bg-card overflow-hidden rounded-lg border">
          <header class="border-b px-4 py-2.5">
            <h2 class="text-sm font-medium">行业分布：各行业有多少标的进入全市场前 20%</h2>
            <p class="text-muted-foreground mt-0.5 text-[11px]">
              各期限独立统计；同一行业在不同期限的表现差异本身就是有用的信息
            </p>
          </header>
          <div class="max-h-80 overflow-auto">
            <table class="w-full text-[11px]">
              <thead class="bg-muted/40 text-muted-foreground sticky top-0 backdrop-blur">
                <tr>
                  <th class="px-3 py-1.5 text-left font-medium">行业</th>
                  <th class="px-3 py-1.5 text-right font-medium">标的数</th>
                  <th v-for="item in report.rankings" :key="item.horizon.key" class="px-3 py-1.5 text-right font-medium">
                    {{ HORIZON_LABELS[item.horizon.key] }}<br />
                    <span class="font-normal">前20% / 均分</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in report.industryDistribution.slice(0, 40)" :key="row.code" class="border-t">
                  <td class="px-3 py-1.5">{{ row.name }}</td>
                  <td class="tabular text-muted-foreground px-3 py-1.5 text-right">{{ row.members }}</td>
                  <td v-for="item in report.rankings" :key="item.horizon.key" class="tabular px-3 py-1.5 text-right">
                    {{ row.topCounts?.[item.horizon.key] ?? 0 }}
                    <span class="text-muted-foreground/70">/ {{ (row.averageScore?.[item.horizon.key] ?? 0).toFixed(1) }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <!-- 行业估值环境 -->
        <IndustryValuationPanel
          :industries="report.industries ?? []"
          :detail="industryDetail"
          :loading="industryLoading"
          :error="industryError"
          :active-code="activeIndustry"
          @select="selectIndustry"
        />

        <!-- 指数评估 -->
        <IndexValuationPanel
          :options="indexOptions"
          :detail="indexDetail"
          :loading="indexLoading"
          :error="indexError"
          :active-symbol="activeIndex"
          @select="selectIndex"
        />

        <!-- 模型参数 -->
        <section class="bg-card rounded-lg border">
          <button class="flex w-full items-center gap-1.5 px-4 py-2.5 text-sm font-medium" @click="toggleParams">
            <Info class="size-3.5" /> 模型参数（五档权重、情景概率、评级分界）
          </button>
          <div v-if="showParams && params" class="space-y-3 border-t px-4 py-3 text-[11px]">
            <p class="text-muted-foreground leading-relaxed">
              方案 §7.2 要求"权重、评分区间、行业调整规则和风险阈值均应可配置，并通过历史验证校准，
              <b class="text-foreground">不将初始权重视为最优参数</b>"。下面就是当前生效的一组参数 ——
              它们是起点，不是最优解。
            </p>
            <div class="overflow-x-auto">
              <table class="w-full text-[11px]">
                <thead class="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th class="px-2 py-1.5 text-left font-medium">维度</th>
                    <th v-for="item in params.horizons" :key="item.key" class="px-2 py-1.5 text-right font-medium">
                      {{ item.label }}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="dimension in DIMENSION_ORDER" :key="dimension" class="border-t">
                    <td class="px-2 py-1.5">{{ DIMENSION_LABELS[dimension] ?? dimension }}</td>
                    <td
                      v-for="item in params.horizons"
                      :key="item.key"
                      class="tabular px-2 py-1.5 text-right"
                      :class="(params.horizonWeights?.[item.key]?.[dimension] ?? 0) >= 15 ? 'text-emerald-400' : ''"
                    >
                      {{ (params.horizonWeights?.[item.key]?.[dimension] ?? 0).toFixed(1) }}%
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div class="text-muted-foreground flex flex-wrap gap-x-6 gap-y-1">
              <span>WACC <b class="text-foreground">{{ (params.wacc * 100).toFixed(1) }}%</b></span>
              <span>稳态增长 <b class="text-foreground">{{ (params.terminalGrowth * 100).toFixed(1) }}%</b></span>
              <span>估值重建起点 <b class="text-foreground">{{ formatDate(params.valueStartDate) }}</b></span>
              <span>
                评级分界 优秀 ≥<b class="text-foreground">{{ params.ratingThresholds?.excellent }}</b>
                / 良好 ≥<b class="text-foreground">{{ params.ratingThresholds?.good }}</b>
                / 中性 ≥<b class="text-foreground">{{ params.ratingThresholds?.neutral }}</b>
                / 偏低 ≥<b class="text-foreground">{{ params.ratingThresholds?.poor }}</b>
              </span>
            </div>
            <div class="text-muted-foreground flex flex-wrap gap-x-6 gap-y-1">
              <span v-for="item in params.horizons" :key="item.key">
                {{ item.label }} 情景概率（悲观/基准/乐观）
                <b class="text-foreground">
                  {{ ((params.scenarioProb?.[item.key]?.[0] ?? 0) * 100).toFixed(0) }}% /
                  {{ ((params.scenarioProb?.[item.key]?.[1] ?? 0) * 100).toFixed(0) }}% /
                  {{ ((params.scenarioProb?.[item.key]?.[2] ?? 0) * 100).toFixed(0) }}%
                </b>
              </span>
            </div>
          </div>
        </section>

        <!-- 口径说明 -->
        <section class="text-muted-foreground space-y-1.5 rounded-lg border px-4 py-3 text-[11px] leading-relaxed">
          <p class="text-foreground flex items-center gap-1.5 text-xs font-medium">
            <Info class="size-3.5" /> 这个分数是怎么算出来的
          </p>
          <p>
            <b class="text-foreground">评分流程</b>：基础指标 → 九个维度各自评分 → 按期限各自加权
            → 乘数据质量乘子 → 施加风险约束 → 最终分与评级。原始分与最终分**分开保留**，
            风险约束不会静默改写原始估值。
          </p>
          <p>
            <b class="text-foreground">九个维度</b>：个股自身估值 / 行业估值环境 / 相对同行估值 /
            成长能力 / 成长与盈利质量 / 资本回报效率 / 安全边际 / 预期收益与风险 / 数据质量与置信度。
            同一份信息只在一个维度计分（有去重校验，跨维度重复的因子会被直接拒绝）。
          </p>
          <p>
            <b class="text-foreground">为什么五档不会给出同一个分数</b>：三重机制 ——
            ① 每档的维度权重不同（长期期限的成长权重显著更高）；
            ② 向合理价值收敛的比例不同（1 个月 12% → 36 个月 100%）；
            ③ 增速衰减强度与情景概率随期限变化。
          </p>
          <ul class="list-disc space-y-1 pl-5">
            <li v-for="(note, index) in report.notes ?? []" :key="index">{{ note }}</li>
          </ul>
        </section>
      </div>
    </div>
  </div>
</template>
