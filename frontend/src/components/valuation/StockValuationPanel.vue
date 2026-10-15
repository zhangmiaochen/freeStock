<script setup lang="ts">
/**
 * 个股详情面板（方案 §10.3）。
 *
 * 展示顺序刻意与"分析的四层估值"一致：
 *   当前价与估值 → 自身历史分位 → 行业估值 → 相对行业/同行
 *   → 成长能力/成长质量/盈利质量/资本回报 → 三情景价值区间 → 安全边际与潜在收益
 *   → 多期限收益风险 → 综合评分/评级/风险/置信度 → 评分依据
 *
 * 每个数字都带口径或可展开的依据（方案 §10.6：每个评分都能追到指标、数据时点与来源）。
 */
import { computed, ref, watch } from "vue";
import { ChevronDown, ChevronRight, Info, LoaderCircle, TriangleAlert, X } from "@lucide/vue";
import { api, errorMessage, type HorizonScore, type StockValuation } from "@/lib/backend";
import { formatCap, formatMultiple, formatPct, formatPctValue, formatRatio, confidenceClass, confidenceLabel, growthFlagLabel, methodLabel, percentileClass, percentileLabel, quadrantClass, quadrantLabel, qualityIssueLabel, ratingClass, ratingLabel, riskSeverityClass, riskSeverityLabel, scoreClass, stageLabel, DIMENSION_LABELS, DIMENSION_ORDER, HORIZON_LABELS } from "@/lib/valuation";

const props = defineProps<{ code: string }>();
const emit = defineEmits<{ (e: "close"): void }>();

const result = ref<StockValuation | null>(null);
const loading = ref(false);
const error = ref("");
const activeHorizon = ref("12m");
const expanded = ref<Record<string, boolean>>({});

/**
 * 请求序号，用来丢弃"过期响应"。
 *
 * 为什么需要：面板在榜上反复点不同标的（`watch(() => props.code)`）会连续发请求，
 * 而它们的返回顺序**不保证**与发出顺序一致（缓存命中时几乎瞬间返回、
 * 未命中时要算几十毫秒）。没有这个守卫的话，先发的慢请求可能后到，
 * 把新股票的数据覆盖成旧股票 —— 界面上的表现就是"点了 A 却显示 B 的数字"，
 * 而且是一个很难复现的时序问题。
 *
 * 规则很简单：只有序号等于最新一次的响应才允许写进 `result`。
 */
let requestSeq = 0;

async function load() {
  if (!props.code) return;
  const seq = ++requestSeq;
  loading.value = true;
  error.value = "";
  result.value = null;
  try {
    const payload = await api.getStockValuation(props.code);
    if (seq !== requestSeq) return; // 已经被更晚的点击取代，丢弃
    result.value = payload;
  } catch (err) {
    if (seq !== requestSeq) return;
    error.value = errorMessage(err);
  } finally {
    if (seq === requestSeq) loading.value = false;
  }
}

watch(() => props.code, load, { immediate: true });

const stock = computed(() => result.value?.stock ?? null);
const horizonScore = computed<HorizonScore | null>(() => {
  const list = stock.value?.scores ?? [];
  return list.find((item) => item.horizon.key === activeHorizon.value) ?? list[3] ?? null;
});
const fair = computed(() => {
  if (!stock.value) return null;
  return stock.value.fairByHorizon?.[activeHorizon.value] ?? null;
});

function toggle(key: string) {
  expanded.value[key] = !expanded.value[key];
}

/** 维度分与权重（界面要能核对"为什么长期更看重成长"）。 */
const dimensions = computed(() => {
  const item = horizonScore.value;
  if (!item) return [];
  return DIMENSION_ORDER.map((key) => ({
    key,
    label: DIMENSION_LABELS[key] ?? String(key),
    score: Number(item.dimensions?.[key] ?? 0),
    weight: Number(item.weights?.[key] ?? 0),
    contribution: Number(item.contributions?.[key] ?? 0),
  }));
});

function evidenceFor(dimension: string) {
  return (stock.value?.evidence ?? []).filter((item) => item.dimension === dimension);
}

/** 口径说明。用 computed 而不是模板里直接取 result.notes —— 模板里 result 是
 * 可空的，`?? []` 在 v-for 上会让 TS 无法收窄（实测报 possibly null）。 */
const notes = computed(() => result.value?.notes ?? []);
</script>

<template>
  <section class="bg-card rounded-lg border">
    <!-- 头部 -->
    <header class="flex items-center gap-2 border-b px-4 py-2.5">
      <h2 class="text-sm font-medium">
        个股详情
        <span v-if="stock" class="text-muted-foreground ml-1 font-normal">
          {{ stock.code }} {{ stock.name }}
          <template v-if="stock.industryName"> · {{ stock.industryName }}</template>
        </span>
      </h2>
      <span v-if="result" class="text-muted-foreground text-[11px]">
        评估日 {{ result.snapshot?.evalDate }} · 报告期 {{ result.snapshot ? stock?.latestPeriod : "—" }}
      </span>
      <button class="text-muted-foreground hover:text-foreground ml-auto" title="关闭" @click="emit('close')">
        <X class="size-4" />
      </button>
    </header>

    <div v-if="loading" class="text-muted-foreground flex items-center gap-2 px-4 py-10 text-xs">
      <LoaderCircle class="size-4 animate-spin" />
      正在按需评估（只取这一只的行情、财报、股本与复权因子，通常 20~40 秒）…
    </div>

    <div v-else-if="error" class="space-y-2 px-4 py-6">
      <p class="flex items-center gap-1.5 text-sm font-medium text-rose-400">
        <TriangleAlert class="size-4" /> 读取失败
      </p>
      <p class="text-muted-foreground max-w-xl text-xs">{{ error }}</p>
      <button class="text-xs underline" @click="load">重试</button>
    </div>

    <div v-else-if="result && !result.available" class="space-y-2 px-4 py-6">
      <p class="text-sm font-medium">该标的暂无法可靠评估</p>
      <p class="text-muted-foreground max-w-2xl text-xs leading-relaxed">{{ result.message }}</p>
      <p class="text-muted-foreground max-w-2xl text-[11px] leading-relaxed">
        方案验收标准 8 要求：数据不足、估值方法不适用或模型冲突严重时明确提示，**不强行给出确定性结论**。
      </p>
    </div>

    <div v-else-if="stock" class="space-y-4 px-4 py-4">
      <!-- 当前价与估值 -->
      <div class="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">当前价格 / 市值</p>
          <p class="tabular text-sm font-medium">
            {{ formatMultiple(stock.price) }} 元
            <span class="text-muted-foreground ml-1 text-[11px] font-normal">{{ formatCap(stock.marketCap) }}</span>
          </p>
        </div>
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">当前估值（不复权价 × 当期股本）</p>
          <p class="tabular text-sm font-medium">
            PE {{ formatMultiple(stock.self?.currentPE) }}
            <span class="text-muted-foreground text-[11px] font-normal">
              · PB {{ formatMultiple(stock.self?.currentPB) }} · PS {{ formatMultiple(stock.self?.currentPS) }}
            </span>
          </p>
        </div>
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">自身历史估值分位</p>
          <p class="tabular text-sm font-medium" :class="percentileClass(stock.self?.pePercentile5Y ?? 0)">
            {{ formatPctValue(stock.self?.pePercentile5Y) }}
            <span class="text-muted-foreground text-[11px] font-normal">{{ percentileLabel(stock.self?.pePercentile5Y) }}</span>
          </p>
          <p class="text-muted-foreground/70 text-[10px]">
            3 年分位 {{ formatPctValue(stock.self?.pePercentile3Y) }} · 样本 {{ stock.self?.sampleDays5Y }} 天
          </p>
        </div>
        <div class="rounded-md border px-3 py-2">
          <p class="text-muted-foreground text-[10px]">数据置信度</p>
          <p class="text-sm font-medium" :class="confidenceClass(stock.overallConfidence?.level)">
            {{ (stock.overallConfidence?.score ?? 0).toFixed(0) }} 分
            <span class="text-[11px] font-normal">{{ confidenceLabel(stock.overallConfidence?.level) }}</span>
          </p>
          <p class="text-muted-foreground/70 text-[10px]">区间宽度 / 方法一致性 / 数据覆盖</p>
        </div>
      </div>

      <!-- 结构性变化提示 -->
      <div
        v-if="stock.self?.structuralShift"
        class="rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-[11px] leading-relaxed"
      >
        <p class="flex items-center gap-1.5 font-medium text-amber-400">
          <Info class="size-3.5" /> 已检测到结构性变化，历史估值分位的参照权重已下调
        </p>
        <ul class="text-muted-foreground mt-1 list-disc space-y-0.5 pl-5">
          <li v-for="(reason, index) in stock.self?.structuralReasons ?? []" :key="index">{{ reason }}</li>
        </ul>
      </div>

      <!-- 行业估值 -->
      <div class="rounded-md border px-3 py-2">
        <p class="text-foreground mb-1.5 text-xs font-medium">
          行业整体估值
          <span class="text-muted-foreground ml-1 font-normal">
            {{ stock.industry?.name }} · 成分 {{ stock.industry?.members }} 只 · 阶段 {{ stageLabel(stock.industry?.stage) }}
          </span>
        </p>
        <div class="text-muted-foreground grid gap-x-6 gap-y-1 text-[11px] sm:grid-cols-2 lg:grid-cols-4">
          <span>加权 PE <b class="text-foreground">{{ formatMultiple(stock.industry?.weightedPE) }}</b></span>
          <span>中位 PE <b class="text-foreground">{{ formatMultiple(stock.industry?.medianPE) }}</b></span>
          <span>
            历史分位
            <b :class="percentileClass(stock.industry?.pePercentile ?? 0)">
              {{ formatPctValue(stock.industry?.pePercentile) }}
            </b>
          </span>
          <span>亏损占比 <b class="text-foreground">{{ formatPct(stock.industry?.lossRatio, 0, false) }}</b></span>
          <span>前 3 大权重 <b class="text-foreground">{{ formatPct(stock.industry?.top3Weight, 0, false) }}</b></span>
          <span>ROE 中位 <b class="text-foreground">{{ formatPct(stock.industry?.roeMedian, 1, false) }}</b></span>
          <span>营收增速中位 <b class="text-foreground">{{ formatPct(stock.industry?.revenueYoYMedian) }}</b></span>
          <span v-if="stock.industry?.peExTop3 > 0">
            剔除前 3 大后中位 PE <b class="text-foreground">{{ formatMultiple(stock.industry?.peExTop3) }}</b>
          </span>
        </div>
        <p v-if="stock.industry?.note" class="text-muted-foreground/80 mt-1.5 text-[10px] leading-relaxed">
          {{ stock.industry.note }}
        </p>
        <p v-if="stock.industry?.cyclicalWarning" class="mt-1.5 text-[10px] leading-relaxed text-amber-400">
          {{ stock.industry.cyclicalReason }}
        </p>
      </div>

      <!-- 相对行业与同行 -->
      <div class="rounded-md border px-3 py-2">
        <p class="text-foreground mb-1.5 text-xs font-medium">相对行业与同行</p>
        <div class="text-muted-foreground grid gap-x-6 gap-y-1 text-[11px] sm:grid-cols-2 lg:grid-cols-4">
          <span>对行业加权 <b :class="(stock.relative?.premiumVsWeighted ?? 0) < 0 ? 'text-emerald-400' : 'text-amber-400'">{{ formatPct(stock.relative?.premiumVsWeighted) }}</b></span>
          <span>对行业中位 <b :class="(stock.relative?.premiumVsMedian ?? 0) < 0 ? 'text-emerald-400' : 'text-amber-400'">{{ formatPct(stock.relative?.premiumVsMedian) }}</b></span>
          <span>行业内 PE 分位 <b class="text-foreground">{{ formatPctValue(stock.relative?.pePercentileInIndustry) }}</b></span>
          <span>基本面行业分位 <b class="text-foreground">{{ formatPctValue(stock.relative?.growthPercentileInIndustry) }}</b></span>
        </div>
        <p class="mt-1.5 text-[11px]">
          <b :class="quadrantClass(stock.relative?.quadrant)">{{ quadrantLabel(stock.relative?.quadrant) }}</b>
        </p>
        <p class="text-muted-foreground mt-0.5 text-[10px] leading-relaxed">{{ stock.relative?.quadrantReason }}</p>
        <p class="text-muted-foreground/80 mt-0.5 text-[10px] leading-relaxed">{{ stock.relative?.comparableNote }}</p>
        <p
          v-if="stock.relative?.bothCheap"
          class="mt-1.5 rounded border border-amber-500/30 bg-amber-500/5 px-2 py-1.5 text-[10px] leading-relaxed text-amber-400"
        >
          {{ stock.relative.bothCheapNote }}
        </p>
      </div>

      <!-- 成长能力 / 成长质量（分开） -->
      <div class="grid gap-3 lg:grid-cols-2">
        <div class="rounded-md border px-3 py-2">
          <p class="text-foreground mb-1.5 flex items-center gap-2 text-xs font-medium">
            成长能力
            <span class="tabular" :class="scoreClass(stock.growth?.score ?? 0)">{{ (stock.growth?.score ?? 0).toFixed(0) }}</span>
          </p>
          <div class="text-muted-foreground grid grid-cols-2 gap-x-4 gap-y-1 text-[11px]">
            <span>营收单季同比 <b class="text-foreground">{{ formatPct(stock.growth?.revenueYoY) }}</b></span>
            <span>净利单季同比 <b class="text-foreground">{{ formatPct(stock.growth?.profitYoY) }}</b></span>
            <span>扣非单季同比 <b class="text-foreground">{{ formatPct(stock.growth?.deductedYoY) }}</b></span>
            <span>经营现金流同比 <b class="text-foreground">{{ formatPct(stock.growth?.ocfGrowth) }}</b></span>
            <span>营收 3 年 CAGR <b class="text-foreground">{{ formatPct(stock.growth?.revenueCAGR3) }}</b></span>
            <span>营收 5 年 CAGR <b class="text-foreground">{{ formatPct(stock.growth?.revenueCAGR5) }}</b></span>
            <span>成长持续性 <b class="text-foreground">{{ formatPctValue((stock.growth?.consistency ?? 0) * 100) }}</b></span>
            <span>增速波动 <b class="text-foreground">{{ formatPct(stock.growth?.volatilityOfGrowth, 1, false) }}</b></span>
            <span>ROE <b class="text-foreground">{{ formatPct(stock.growth?.roe, 1, false) }}</b></span>
            <span>增量投入资本回报 <b class="text-foreground">{{ formatPct(stock.growth?.incrementalROIC) }}</b></span>
          </div>
          <ul v-if="stock.growth?.flags?.length" class="mt-2 space-y-1">
            <li v-for="flag in stock.growth.flags" :key="flag.code" class="text-[10px] leading-relaxed">
              <b class="text-amber-400">{{ growthFlagLabel(flag.code) }}</b>
              <span class="text-muted-foreground">：{{ flag.detail }}</span>
            </li>
          </ul>
        </div>

        <div class="rounded-md border px-3 py-2">
          <p class="text-foreground mb-1.5 flex items-center gap-2 text-xs font-medium">
            成长质量与盈利质量
            <span class="tabular" :class="scoreClass(stock.quality?.score ?? 0)">{{ (stock.quality?.score ?? 0).toFixed(0) }}</span>
          </p>
          <div class="text-muted-foreground grid grid-cols-2 gap-x-4 gap-y-1 text-[11px]">
            <span>扣非占比 <b class="text-foreground">{{ formatRatio(stock.quality?.deductedRatio) }}</b></span>
            <span>利润现金含量 <b class="text-foreground">{{ formatRatio(stock.quality?.cashContent) }}</b></span>
            <span>资产负债率 <b class="text-foreground">{{ formatPct(stock.quality?.debtRatio, 1, false) }}</b></span>
            <span>负债率同比变化 <b class="text-foreground">{{ formatPct(stock.quality?.debtChange) }}</b></span>
            <span>筹资依赖度 <b class="text-foreground">{{ formatPct(stock.quality?.financingDependence) }}</b></span>
            <span>主营利润占比 <b class="text-foreground">{{ formatRatio(stock.quality?.mainBusinessRatio) }}</b></span>
            <span>毛利率 <b class="text-foreground">{{ formatPct(stock.quality?.grossMargin, 1, false) }}</b></span>
            <span>毛利率同比变化 <b class="text-foreground">{{ formatPct(stock.quality?.grossMarginChange) }}</b></span>
          </div>
          <p v-if="!stock.quality?.issues?.length" class="text-muted-foreground/80 mt-2 text-[10px]">
            未发现成长质量或盈利质量问题。质量分是**独立维度**，与成长速度分开评价（方案 §4.2）。
          </p>
          <ul v-else class="mt-2 space-y-1">
            <li v-for="issue in stock.quality.issues" :key="issue.code" class="text-[10px] leading-relaxed">
              <b class="text-amber-400">{{ qualityIssueLabel(issue.code) }}</b>
              <span class="text-muted-foreground">：{{ issue.detail }}</span>
            </li>
          </ul>
        </div>
      </div>

      <!-- 多期限评分 -->
      <div>
        <div class="mb-2 flex flex-wrap items-center gap-1.5">
          <span class="text-muted-foreground mr-1 text-[11px]">期限</span>
          <button
            v-for="item in stock.scores"
            :key="item.horizon.key"
            class="rounded-md border px-2 py-1 text-[11px] transition"
            :class="activeHorizon === item.horizon.key ? 'border-primary bg-primary/10 text-foreground' : 'border-border text-muted-foreground hover:text-foreground'"
            @click="activeHorizon = item.horizon.key"
          >
            {{ HORIZON_LABELS[item.horizon.key] ?? item.horizon.label }}
            <b class="tabular ml-1" :class="scoreClass(item.finalScore)">{{ item.finalScore.toFixed(1) }}</b>
          </button>
        </div>

        <div v-if="horizonScore" class="space-y-3">
          <!-- 综合评分与评级 -->
          <div class="bg-muted/20 rounded-md border px-3 py-2.5">
            <div class="flex flex-wrap items-center gap-x-5 gap-y-2 text-[11px]">
              <span class="text-muted-foreground">
                综合分
                <b class="tabular text-base" :class="scoreClass(horizonScore.finalScore)">{{ horizonScore.finalScore.toFixed(1) }}</b>
              </span>
              <span class="rounded border px-1.5 py-0.5" :class="ratingClass(horizonScore.rating)">
                {{ ratingLabel(horizonScore.rating) }}
              </span>
              <span v-if="horizonScore.ratingCapped" class="text-amber-400">评级已被风险约束封顶</span>
              <span class="text-muted-foreground">
                风险约束前 <b class="text-foreground">{{ horizonScore.rawScore.toFixed(1) }}</b>
              </span>
              <span class="text-muted-foreground">
                数据质量乘子 <b class="text-foreground">{{ horizonScore.dataMultiplier.toFixed(2) }}</b>
              </span>
            </div>
            <p class="text-muted-foreground/80 mt-1.5 text-[10px] leading-relaxed">
              方案 §7.2 的流程：基础指标 → 各维度评分 → 按期限加权 → 数据质量乘子 → 风险约束 → 最终分与评级。
              原始分与最终分**分开保留**（方案 §7.3 要求风险约束不得静默修改原始估值结果）。
            </p>
          </div>

          <!-- 三情景合理价值 -->
          <div v-if="fair" class="rounded-md border px-3 py-2.5">
            <p class="text-foreground mb-2 text-xs font-medium">
              合理价值与安全边际（{{ HORIZON_LABELS[activeHorizon] }}）
            </p>
            <div class="text-muted-foreground grid gap-x-6 gap-y-1 text-[11px] sm:grid-cols-2 lg:grid-cols-4">
              <span>悲观 <b class="text-rose-400">{{ formatMultiple(fair.pessimistic) }}</b> 元</span>
              <span>基准 <b class="text-foreground">{{ formatMultiple(fair.base) }}</b> 元</span>
              <span>乐观 <b class="text-emerald-400">{{ formatMultiple(fair.optimistic) }}</b> 元</span>
              <span>当前价 <b class="text-foreground">{{ formatMultiple(fair.price) }}</b> 元</span>
              <span>安全边际 <b class="text-foreground">{{ formatPct(fair.safetyMargin) }}</b></span>
              <span>潜在收益 <b class="text-foreground">{{ formatPct(fair.upside) }}</b></span>
              <span>隐含成长 <b class="text-foreground">{{ formatPct(fair.impliedGrowth) }}</b></span>
              <span>模型成长假设 <b class="text-foreground">{{ formatPct(fair.modelGrowth) }}</b></span>
            </div>
            <p class="text-muted-foreground mt-1 text-[10px] leading-relaxed">
              <b class="text-foreground">安全边际</b> =（基准合理价值 − 当前价格）÷ 基准合理价值；
              <b class="text-foreground">潜在收益</b> =（乐观价值 − 当前价格）÷ 当前价格。
              两者口径不同，**不能混用**（方案 §5.6）。
            </p>
            <p class="mt-1 text-[10px] leading-relaxed">
              <b class="text-foreground">市场预期：</b>{{ fair.growthExpectation === "high" ? "偏高" : fair.growthExpectation === "low" ? "偏低" : fair.growthExpectation === "fair" ? "与历史匹配" : "无法判断" }}
              <span class="text-muted-foreground"> · {{ fair.growthNote }}</span>
            </p>

            <!-- 估值方法逐条 -->
            <div class="mt-2 space-y-1">
              <p class="text-muted-foreground text-[10px]">
                多模型交叉验证（按行业与公司特征自动选择；不适用时输出原因而不是硬算）
              </p>
              <div
                v-for="method in fair.methods"
                :key="method.method"
                class="rounded border px-2 py-1.5 text-[10px] leading-relaxed"
                :class="method.applicable ? 'border-border' : 'border-dashed border-border/60 opacity-70'"
              >
                <span class="text-foreground font-medium">{{ methodLabel(method.method) }}</span>
                <span v-if="method.applicable" class="text-muted-foreground">
                  · 价值 <b class="text-foreground">{{ formatMultiple(method.fairValue) }}</b> 元
                  · 区间 {{ formatMultiple(method.low) }} ~ {{ formatMultiple(method.high) }}
                  · 目标倍数 <b class="text-foreground">{{ formatMultiple(method.targetMultiple) }}</b>
                  （当前 {{ formatMultiple(method.currentMultiple) }}）
                </span>
                <span class="text-muted-foreground ml-1">{{ method.applicable ? method.basis : method.reason }}</span>
              </div>
            </div>

            <p
              v-if="fair.conflict"
              class="mt-2 rounded border border-amber-500/30 bg-amber-500/5 px-2 py-1.5 text-[10px] leading-relaxed text-amber-400"
            >
              {{ fair.conflictNote }}
            </p>
            <ul v-if="fair.warnings?.length" class="text-muted-foreground/80 mt-2 list-disc space-y-0.5 pl-5 text-[10px]">
              <li v-for="(warning, index) in fair.warnings" :key="index">{{ warning }}</li>
            </ul>
          </div>

          <!-- 该期限的收益情景 -->
          <div v-if="horizonScore.outlook?.available" class="rounded-md border px-3 py-2.5">
            <p class="text-foreground mb-2 text-xs font-medium">
              该期限的收益情景（{{ HORIZON_LABELS[activeHorizon] }}）
            </p>
            <div class="overflow-hidden rounded border">
              <table class="w-full text-[11px]">
                <thead class="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th class="px-2 py-1.5 text-left font-medium">情景</th>
                    <th class="px-2 py-1.5 text-right font-medium">概率</th>
                    <th class="px-2 py-1.5 text-right font-medium">目标价</th>
                    <th class="px-2 py-1.5 text-right font-medium">收益</th>
                    <th class="px-2 py-1.5 text-left font-medium">关键假设</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="scenario in horizonScore.outlook.scenarios" :key="scenario.key" class="border-t">
                    <td class="px-2 py-1.5">{{ scenario.label }}</td>
                    <td class="tabular px-2 py-1.5 text-right">{{ (scenario.probability * 100).toFixed(0) }}%</td>
                    <td class="tabular px-2 py-1.5 text-right">{{ formatMultiple(scenario.targetPrice) }}</td>
                    <td class="tabular px-2 py-1.5 text-right" :class="scenario.return >= 0 ? 'text-emerald-400' : 'text-rose-400'">
                      {{ formatPct(scenario.return) }}
                    </td>
                    <td class="text-muted-foreground px-2 py-1.5">{{ scenario.assumption }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div class="text-muted-foreground mt-2 flex flex-wrap gap-x-5 gap-y-1 text-[11px]">
              <span>预期收益 <b :class="horizonScore.outlook.expectedReturn >= 0 ? 'text-emerald-400' : 'text-rose-400'">{{ formatPct(horizonScore.outlook.expectedReturn) }}</b></span>
              <span>悲观损失 <b class="text-rose-400">{{ formatPct(horizonScore.outlook.pessimisticLoss, 1, false) }}</b></span>
              <span>风险收益比 <b class="text-foreground">{{ horizonScore.outlook.returnRiskRatio.toFixed(2) }}</b></span>
              <span>期内分红 <b class="text-foreground">{{ formatPct(horizonScore.outlook.dividendYield) }}</b></span>
              <span>向合理价值收敛 <b class="text-foreground">{{ formatPct(horizonScore.outlook.convergence, 0, false) }}</b></span>
              <span>该期限波动 <b class="text-foreground">{{ formatPct(horizonScore.outlook.expectedVolatility, 1, false) }}</b></span>
              <span>乐观依赖度 <b class="text-foreground">{{ formatPct(horizonScore.outlook.optimismDependence, 0, false) }}</b></span>
            </div>
            <p class="text-muted-foreground/80 mt-1.5 text-[10px] leading-relaxed">
              {{ horizonScore.outlook.focusNote }}
            </p>
          </div>

          <!-- 风险约束 -->
          <div v-if="horizonScore.riskFlags?.length" class="rounded-md border px-3 py-2.5">
            <p class="text-foreground mb-1.5 flex items-center gap-1.5 text-xs font-medium">
              <TriangleAlert class="size-3.5 text-amber-400" /> 风险约束（{{ horizonScore.riskFlags.length }} 条）
            </p>
            <ul class="space-y-1.5">
              <li v-for="flag in horizonScore.riskFlags" :key="flag.code + flag.title" class="text-[11px] leading-relaxed">
                <span class="rounded border px-1.5 py-0.5 text-[10px]" :class="riskSeverityClass(flag.severity)">
                  {{ riskSeverityLabel(flag.severity) }}
                </span>
                <b class="text-foreground ml-1.5">{{ flag.title }}</b>
                <span v-if="flag.capRating" class="text-muted-foreground">（评级上限：{{ ratingLabel(flag.capRating) }}）</span>
                <p class="text-muted-foreground mt-0.5">{{ flag.reason }}</p>
              </li>
            </ul>
          </div>

          <!-- 维度分与依据 -->
          <div class="rounded-md border">
            <button class="flex w-full items-center gap-1.5 px-3 py-2 text-xs font-medium" @click="toggle('dims')">
              <component :is="expanded.dims ? ChevronDown : ChevronRight" class="size-3.5" />
              九维度得分与依据（点击展开可追溯的指标、时点与来源）
            </button>
            <div v-if="expanded.dims" class="space-y-2 border-t px-3 py-2.5">
              <div
                v-for="dimension in dimensions"
                :key="dimension.key"
                class="rounded border px-2 py-1.5 text-[11px]"
              >
                <div class="flex items-center gap-2">
                  <span class="text-foreground w-32 shrink-0">{{ dimension.label }}</span>
                  <span class="tabular" :class="scoreClass(dimension.score)">{{ dimension.score.toFixed(1) }}</span>
                  <span class="text-muted-foreground">权重 {{ dimension.weight.toFixed(1) }}% → 贡献 {{ dimension.contribution.toFixed(2) }}</span>
                </div>
                <ul class="text-muted-foreground mt-1 space-y-0.5">
                  <li v-for="item in evidenceFor(dimension.key)" :key="item.metric" class="leading-relaxed">
                    <b class="text-foreground">{{ item.metric }}</b>
                    <template v-if="item.value">：{{ item.value }}</template>
                    <span class="text-muted-foreground/70"> · 时点 {{ item.asOf }} · 来源 {{ item.source }}</span>
                    <span v-if="item.note" class="text-muted-foreground/70"> · {{ item.note }}</span>
                  </li>
                </ul>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 口径说明 -->
      <section class="text-muted-foreground space-y-1.5 rounded-lg border px-4 py-3 text-[11px] leading-relaxed">
        <p class="text-foreground flex items-center gap-1.5 text-xs font-medium">
          <Info class="size-3.5" /> 这个分数是怎么算出来的
        </p>
        <ul class="list-disc space-y-1 pl-5">
          <li v-for="(note, index) in notes" :key="index">{{ note }}</li>
        </ul>
      </section>
    </div>
  </section>
</template>
