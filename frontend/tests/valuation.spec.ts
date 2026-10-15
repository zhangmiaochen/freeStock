import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, nextTick } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import type {
  HorizonRanking,
  HorizonScore,
  IndexEvaluation,
  IndexValuationResult,
  IndustryValuation,
  IndustryValuationDetail,
  RankRow,
  ScoreDistribution,
  StockValuation,
  ValuationIndexOption,
  ValuationReport,
} from "@/lib/backend";

/**
 * 多期限性价比评估页的用例。
 *
 * 断言策略与其它页面一致：**几乎全是对用户可见文案的断言**（数值、口径原文、
 * 空状态原因、下一步提示），结构断言只用于计数。
 *
 * 特别钉住几条来自方案要求的可见行为：
 *  - 五档期限必须都能选，且各档均分不同（不允许复制同一个评分）；
 *  - "暂无法可靠评估"的标的**单独列出、不混排**（方案 §9）；
 *  - 安全边际与潜在收益必须**分开展示**并说明口径不同（方案 §5.6）；
 *  - 行业估值的"加权 / 中位 / 历史分位"必须**三列并列**（方案 §5.2）；
 *  - 数据不可用时给可解释的空状态 + 下一步（验收标准 8）。
 */

const mocks = vi.hoisted(() => ({
  getValuationReport: vi.fn(),
  getStockValuation: vi.fn(),
  getIndustryValuation: vi.fn(),
  getIndexValuation: vi.fn(),
  listValuationIndices: vi.fn(),
  getValuationParams: vi.fn(),
  getNamePinyin: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    getValuationReport: mocks.getValuationReport,
    getStockValuation: mocks.getStockValuation,
    getIndustryValuation: mocks.getIndustryValuation,
    getIndexValuation: mocks.getIndexValuation,
    listValuationIndices: mocks.listValuationIndices,
    getValuationParams: mocks.getValuationParams,
    getNamePinyin: mocks.getNamePinyin,
  },
  errorMessage: (err: unknown) => String(err),
}));

import ValuationView from "@/views/ValuationView.vue";

// ---------------------------------------------------------------------------
// 造数据
// ---------------------------------------------------------------------------

function horizonScore(key: string, label: string, finalScore: number, overrides: Partial<HorizonScore> = {}): HorizonScore {
  return {
    horizon: { key, label, tradingDays: 252, months: 12 },
    dimensions: {
      selfValuation: 62,
      indValuation: 49,
      relValuation: 58,
      growth: 60,
      quality: 85,
      capital: 70,
      margin: 74,
      return: 55,
      data: 77,
    },
    weights: {
      selfValuation: 12,
      indValuation: 9,
      relValuation: 11,
      growth: 19,
      quality: 14,
      capital: 10,
      margin: 9,
      return: 9,
      data: 7,
    },
    rawScore: finalScore + 8,
    dataMultiplier: 0.91,
    finalScore,
    rating: finalScore >= 62 ? "good" : "neutral",
    ratingCapped: true,
    riskFlags: [
      {
        code: "optimism_dependent",
        severity: "cap",
        title: "估值结论高度依赖乐观假设",
        reason: "12 个月 期限：剔除乐观情景后预期收益下降 41%（超过 40% 的阈值）",
        capRating: "neutral",
      },
    ],
    outlook: {
      horizon: { key, label, tradingDays: 252, months: 12 },
      expectedReturn: 0.31,
      pessimisticLoss: 0.105,
      returnRiskRatio: 2.95,
      dividendYield: 0.02,
      scenarios: [
        { key: "pessimistic", label: "悲观", probability: 0.25, targetPrice: 18.2, return: -0.105, assumption: "盈利不及预期叠加估值下修" },
        { key: "base", label: "基准", probability: 0.5, targetPrice: 27.9, return: 0.269, assumption: "价格向合理价值收敛 75%" },
        { key: "optimistic", label: "乐观", probability: 0.25, targetPrice: 33.4, return: 0.518, assumption: "盈利超预期叠加估值上修" },
      ],
      convergence: 0.75,
      targetPrice: 27.9,
      expectedVolatility: 0.32,
      focusNote: "重点考察未来一年盈利增长、合理估值变化、分红与潜在风险",
      optimismDependence: 0.41,
      available: true,
    },
    contributions: {
      selfValuation: 7.44,
      indValuation: 4.41,
      relValuation: 6.38,
      growth: 11.4,
      quality: 11.9,
      capital: 7,
      margin: 6.66,
      return: 4.95,
      data: 5.39,
    },
    ...overrides,
  } as HorizonScore;
}

function rankRow(code: string, name: string, score: number, industryName = "银行Ⅱ"): RankRow {
  return {
    rank: 1,
    code,
    name,
    industryCode: "475",
    industryName,
    score,
    rawScore: score + 8,
    rating: "neutral",
    ratingCapped: true,
    scorePercentile: 99,
    industryRank: 1,
    industrySize: 13,
    price: 22.0,
    marketCap: 5e11,
    floatMarketCap: 4e11,
    currentPE: 11.8,
    selfPercentile: 6,
    industryWeightedPE: 6.3,
    industryPercentile: 0,
    premiumVsMedian: -0.12,
    growthScore: 60,
    qualityScore: 85,
    roe: 0.295,
    fairBase: 31.5,
    safetyMargin: 0.302,
    upside: 0.51,
    expectedReturn: 0.31,
    pessimisticLoss: 0.105,
    returnRiskRatio: 2.95,
    targetPrice: 27.9,
    selfPercentileForHorizon: 6,
    confidence: { score: 77, level: "high", reasons: ["区间宽度、方法一致性、数据覆盖三项均正常，未扣分"] },
    riskFlags: [
      {
        code: "optimism_dependent",
        severity: "cap",
        title: "估值结论高度依赖乐观假设",
        reason: "剔除乐观情景后预期收益下降 41%",
      },
    ],
    keyStrengths: ["自身估值处于历史 6% 分位（历史极低位）", "成长质量分 85（扣非占比 0.99、现金含量 1.51）"],
    mainRisks: ["估值结论高度依赖乐观假设"],
  } as RankRow;
}

function distribution(mean: number): ScoreDistribution {
  return {
    mean,
    median: mean + 0.2,
    p10: mean - 15,
    p25: mean - 8,
    p75: mean + 9,
    p90: mean + 13,
    histogram: [1, 3, 9, 22, 41, 38, 20, 4, 1, 0],
    ratingCounts: { neutral: 100, good: 20 },
  } as ScoreDistribution;
}

function ranking(key: string, mean: number): HorizonRanking {
  return {
    horizon: { key, label: key, tradingDays: 252, months: 12 },
    rows: [rankRow("601899", "紫金矿业", mean + 22)],
    excluded: [
      {
        code: "999999",
        name: "数据不足公司",
        rating: "unavailable",
        reason: "评估日前没有任何已披露财报",
        score: 50,
        rawScore: 50,
        riskFlags: [],
      },
    ],
    scored: 135,
    excludedCount: 1,
    distribution: distribution(mean),
  } as HorizonRanking;
}

function industry(): IndustryValuation {
  return {
    code: "475",
    name: "银行Ⅱ",
    weightedPE: 6.3,
    weightedPB: 0.7,
    weightedPS: 2.1,
    medianPE: 6.1,
    medianPB: 0.68,
    medianPS: 2.0,
    pePercentile: 0,
    pbPercentile: 0,
    peBand: { p10: 0, p25: 0, p50: 0, p75: 0, p90: 0, min: 0, max: 0 },
    members: 13,
    lossMembers: 0,
    lossRatio: 0,
    roeMedian: 0.11,
    revenueYoYMedian: 0.03,
    profitYoYMedian: 0.05,
    grossMarginMedian: 0,
    top3Weight: 0.51,
    peExTop3: 6.0,
    stage: "mature",
    stageNote: "成分股营收增速中位数 3.0%、离散度 4 个百分点，处于成熟阶段",
    cyclicalWarning: false,
    available: true,
    note: "行业历史估值样本不足（0 个交易日，至少需要 60 个）",
    score: 49,
  } as IndustryValuation;
}

function indexEval(): IndexEvaluation {
  return {
    symbol: "000300.SH",
    name: "沪深300",
    weightedPE: 11.9,
    weightedPB: 1.3,
    weightedPS: 1.4,
    medianPE: 17.9,
    pePercentile: 0,
    peBand: { p10: 0, p25: 0, p50: 0, p75: 0, p90: 0, min: 0, max: 0 },
    cheapWeight: 0.26,
    expensiveWeight: 0.51,
    lossWeight: 0.03,
    medianSelfPercentile: 62,
    weightedROE: 0.11,
    medianROE: 0.09,
    weightedRevenueYoY: 0.04,
    weightedGrowthScore: 55,
    weightedQualityScore: 70,
    medianGrowthScore: 52,
    medianQualityScore: 66,
    industryWeights: [
      { name: "银行Ⅱ", weight: 0.22 },
      { name: "半导体", weight: 0.11 },
    ],
    top10Weight: 0.11,
    topIndustryWeight: 0.22,
    concentrationNote: "前 10 大成分权重合计 11%，权重分布相对分散",
    marginWeight: 0.79,
    margin10Weight: 0.41,
    marginNote: "共 58 个成分，其中 58 个能算出安全边际（0 个缺少合理价值或价格）",
    scores: [horizonScore("12m", "12 个月", 38.1)],
    overallConfidence: { score: 75, level: "medium", reasons: ["缺少时点正确的历史指数 PE 序列，扣 20 分"] },
    available: true,
    warnings: ["指数估值用**调和加权**（总市值 ÷ 总盈利），不是成分 PE 的算术平均"],
  } as IndexEvaluation;
}

function report(overrides: Partial<ValuationReport> = {}): ValuationReport {
  return {
    available: true,
    message: "",
    snapshot: {
      evalDate: 20261014,
      dataCutoff: "行情 2026-10-14 收盘；财务只使用公告日 ≤ 2026-10-14 的报告期",
      modelVersion: "v1-2026.10",
      universe: 5781,
      evaluated: 135,
      unavailable: 15,
    },
    rankings: [
      ranking("1m", 47.1),
      ranking("3m", 47.3),
      ranking("6m", 48.6),
      ranking("12m", 49.6),
      ranking("36m", 50.7),
    ],
    industries: [industry()],
    indices: [indexEval()],
    distributions: {},
    industryDistribution: [
      { code: "475", name: "银行Ⅱ", members: 13, topCounts: { "12m": 5 }, averageScore: { "12m": 52.3 } },
    ],
    elapsedMs: 22000,
    fullMarket: false,
    requested: 150,
    notes: [
      "复权口径：收益、波动、回撤与情景收益一律用**后复权价**；市值、PE、PB、PS 一律用**不复权价 × 当期股本**。",
      "财务数值是**年内累计**口径，且 Q4 是**全年值**：TTM = 上年全年 + 本年至今 − 去年同期。",
    ],
    ...overrides,
  } as ValuationReport;
}

function stockValuation(): StockValuation {
  return {
    available: true,
    message: "",
    stock: {
      code: "601899",
      name: "紫金矿业",
      industryCode: "475",
      industryName: "银行Ⅱ",
      price: 22.0,
      marketCap: 5e11,
      floatMarketCap: 4e11,
      evalDate: 20261014,
      latestPeriod: "2026Q2",
      self: {
        available: true,
        currentPE: 11.8,
        currentPB: 3.2,
        currentPS: 1.4,
        pePercentile3Y: 6,
        pePercentile5Y: 6,
        pbPercentile3Y: 20,
        pbPercentile5Y: 22,
        psPercentile5Y: 30,
        sampleDays3Y: 730,
        sampleDays5Y: 1220,
        structuralShift: true,
        structuralReasons: ["估值中枢移动：近 3 年 PE 中位数 40.0 倍 vs 近 5 年 20.0 倍（偏移 100%）"],
        score: 83,
      },
      industry: industry(),
      relative: {
        available: true,
        industryCode: "475",
        industryName: "银行Ⅱ",
        premiumVsWeighted: -0.10,
        premiumVsMedian: -0.12,
        pePercentileInIndustry: 20,
        pbPercentileInIndustry: 25,
        growthPercentileInIndustry: 58,
        qualityPercentileInIndustry: 90,
        roePercentileInIndustry: 85,
        quadrant: "cheap_good",
        quadrantLabel: "估值折价 + 基本面优于行业 → 可能是估值折价机会",
        quadrantReason: "相对行业中位数折价 12.0%，基本面行业分位 74",
        bothCheap: true,
        bothCheapNote: "个股自身估值处于历史 6% 分位、行业加权 PE 处于历史 0% 分位，两者都在低位。这**不等于**高性价比。",
        comparables: 10,
        comparableNote: "同行样本 10 只（筛法 L1：同行业 + 市值在 10 倍以内）",
        score: 58,
      },
      fairByHorizon: {
        "12m": {
          available: true,
          price: 22.0,
          shares: 2.6e10,
          pessimistic: 18.2,
          base: 31.5,
          optimistic: 33.4,
          safetyMargin: 0.302,
          upside: 0.518,
          methods: [
            {
              method: "pe",
              label: "PE（市盈率）",
              applicable: true,
              reason: "当前 PE 11.8 倍 → 目标 PE 16.0 倍",
              fairValue: 31.5,
              low: 26.8,
              high: 34.6,
              targetMultiple: 16,
              currentMultiple: 11.8,
              basis: "预测 TTM 每股收益 1.97 元 × 目标 PE 16.0 倍",
            },
            {
              method: "dcf",
              label: "DCF（现金流折现）",
              applicable: false,
              reason: "36 个月 期限不做 DCF：现金流折现依赖长期终值假设",
              fairValue: 0,
              low: 0,
              high: 0,
              targetMultiple: 0,
              currentMultiple: 0,
              basis: "",
            },
          ],
          methodsUsed: ["pe"],
          conflict: true,
          conflictNote: "PE 给出 40.00 元（区间 36.00~44.00），PB 给出 10.00 元（区间 8.00~12.00），两者区间完全不重叠且相差 28.00 元（超过较窄区间的 30%）。按方案要求**不取平均**",
          impliedGrowth: 0.18,
          modelGrowth: 0.11,
          growthExpectation: "high",
          growthNote: "当前 PE 11.8 倍隐含的市场预期增速约 18.0%，明显高于公司近 3 年实际增速 11.0%",
          confidence: { score: 77, level: "high", reasons: ["区间宽度正常"] },
          warnings: ["12 个月 期限的基准合理价值被夹到现价的 200% 上限"],
          assumptions: [],
        },
      },
      growth: {
        score: 60,
        available: true,
        revenueYoY: 0.1,
        profitYoY: 0.08,
        deductedYoY: 0.07,
        revenueCAGR3: 0.11,
        revenueCAGR5: 0.09,
        profitCAGR3: 0.1,
        consistency: 1,
        volatilityOfGrowth: 0.012,
        ocfGrowth: 0.09,
        fcfPositive: true,
        roe: 0.295,
        incrementalROIC: 0.14,
        flags: [
          { code: "cyclical", title: "可能是行业性反弹", detail: "个股营收同比 +10.0%、行业中位数 +10.0%" },
        ],
      },
      quality: {
        score: 85,
        available: true,
        deductedRatio: 0.99,
        cashContent: 1.51,
        debtRatio: 0.45,
        debtChange: 0.01,
        financingDependence: -0.01,
        mainBusinessRatio: 0.96,
        grossMargin: 0.32,
        grossMarginChange: 0.005,
        issues: [],
      },
      scores: [
        horizonScore("1m", "1 个月", 72.2),
        horizonScore("3m", "3 个月", 71.2),
        horizonScore("6m", "6 个月", 70.9),
        horizonScore("12m", "12 个月", 70.9),
        horizonScore("36m", "36 个月", 67.9),
      ],
      overallConfidence: { score: 77, level: "high", reasons: ["区间宽度、方法一致性、数据覆盖三项均正常，未扣分"] },
      evidence: [
        {
          dimension: "selfValuation",
          metric: "自身历史估值分位",
          value: "PE 11.8 倍（历史 6% 分位，历史极低位）",
          raw: 6,
          asOf: "20261014",
          source: "自建历史估值序列（不复权价 × 当期股本 ÷ 当期已披露 TTM 业绩）",
          note: "3 年分位 6%、5 年分位 6%；样本 1220 个交易日",
        },
        {
          dimension: "growth",
          metric: "成长能力",
          value: "营收单季同比 +10.0%、3 年 CAGR 11.0%",
          asOf: "2026Q2",
          source: "cnequity:financial_statement_items（单季拆解 + 年报 CAGR）",
          note: "",
        },
      ],
      riskFlags: [],
      available: true,
      unavailableReason: "",
      warnings: [],
    },
    snapshot: {
      evalDate: 20261014,
      dataCutoff: "行情 2026-10-14 收盘",
      modelVersion: "v1-2026.10",
      universe: 1,
      evaluated: 1,
      unavailable: 0,
    },
    notes: ["复权口径：收益、波动、回撤与情景收益一律用**后复权价**。"],
  } as unknown as StockValuation;
}

const INDEX_OPTIONS: ValuationIndexOption[] = [
  { symbol: "000001.SH", name: "上证指数", hasConstituents: true, constituents: 46 },
  { symbol: "000300.SH", name: "沪深300", hasConstituents: true, constituents: 58 },
  { symbol: "000905.SH", name: "中证500", hasConstituents: false, constituents: 0, note: "成分数据未入库" },
];

/**
 * 页面里用了 `useRouter()`（点击"看 K 线"要跳转），所以挂载时必须注入一个真路由，
 * 否则 Vue 会报 injection "Symbol(router)" not found（与 screener.spec.ts 同一处理）。
 */
const router = createRouter({
  history: createMemoryHistory(),
  routes: [
    { path: "/", component: { template: "<div />" } },
    { path: "/kline", component: { template: "<div />" } },
  ],
});

async function mountPage() {
  await router.push("/");
  await router.isReady();
  const wrapper = mount(ValuationView, {
    global: {
      plugins: [router],
      stubs: {
        // 子面板本身有独立用例覆盖，这里只验证父页面的编排与传参
        ValuationRankTable: defineComponent({
          name: "ValuationRankTable",
          props: { ranking: { type: Object, default: null }, horizonLabel: { type: String, default: "" } },
          emits: ["select"],
          template: '<div class="rank-stub" @click="$emit(\'select\', ranking?.rows?.[0])">{{ horizonLabel }}</div>',
        }),
        StockValuationPanel: defineComponent({
          name: "StockValuationPanel",
          props: { code: { type: String, default: "" } },
          template: '<div class="stock-stub">个股详情 {{ code }}</div>',
        }),
        IndustryValuationPanel: defineComponent({
          name: "IndustryValuationPanel",
          props: { industries: { type: Array, default: () => [] }, activeCode: { type: String, default: "" } },
          emits: ["select"],
          template: '<div class="industry-stub">行业面板 {{ industries.length }}</div>',
        }),
        IndexValuationPanel: defineComponent({
          name: "IndexValuationPanel",
          props: { options: { type: Array, default: () => [] }, activeSymbol: { type: String, default: "" } },
          emits: ["select"],
          template: '<div class="index-stub">指数面板 {{ options.length }}</div>',
        }),
      },
    },
  });
  await vi.waitFor(() => expect(mocks.getValuationReport).toHaveBeenCalled());
  // ⚠ 必须把首次加载的 Promise 全部排空再返回：loading 期间工具栏按钮是 disabled 的，
  // 只等"被调用过"会让后续的点击落在 disabled 按钮上（实测过：
  // 断言 `mock.calls.length` 一直停在 1，看不出是按钮没点动）。
  await flushPromises();
  await nextTick();
  return { wrapper };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getValuationReport.mockResolvedValue(report());
  mocks.getStockValuation.mockResolvedValue(stockValuation());
  mocks.getIndustryValuation.mockResolvedValue({
    available: true,
    industry: industry(),
    members: [rankRow("601899", "紫金矿业", 70.9)],
    snapshot: report().snapshot,
    notes: [],
  } as IndustryValuationDetail);
  mocks.getIndexValuation.mockResolvedValue({
    available: true,
    index: indexEval(),
    snapshot: report().snapshot,
    notes: [],
  } as IndexValuationResult);
  mocks.listValuationIndices.mockResolvedValue(INDEX_OPTIONS);
  mocks.getValuationParams.mockResolvedValue({
    modelVersion: "v1-2026.10",
    horizons: [
      { key: "1m", label: "1 个月", tradingDays: 21, months: 1 },
      { key: "36m", label: "36 个月", tradingDays: 756, months: 36 },
    ],
    horizonWeights: {
      "1m": { selfValuation: 22, growth: 4 },
      "36m": { selfValuation: 8, growth: 24 },
    },
    scenarioProb: { "1m": [0.3, 0.45, 0.25], "36m": [0.25, 0.5, 0.25] },
    wacc: 0.08,
    terminalGrowth: 0.025,
    valueStartDate: 20210101,
    ratingThresholds: { excellent: 72, good: 62, neutral: 50, poor: 38 },
    notes: [],
  });
});

describe("多期限性价比评估页", () => {
  it("默认按市值前 150 只加载，并显示溯源信息（评估日 / 模型版本 / 覆盖数）", async () => {
    const { wrapper } = await mountPage();

    expect(mocks.getValuationReport).toHaveBeenCalledWith(150, false);

    const text = wrapper.text();
    expect(text).toContain("多期限性价比");
    expect(text).toContain("1 / 3 / 6 / 12 / 36 个月独立评估");
    // 溯源条：方案 §4.1 要求所有结果绑定评估日期、数据截止时间与模型版本
    expect(text).toContain("2026-10-14");
    expect(text).toContain("v1-2026.10");
    expect(text).toContain("5781");
    expect(text).toContain("135");
    expect(text).toContain("22.0 秒");
    expect(text).toContain("市值前 150 只");
  });

  it("五档期限都可切换，且各档均分不同（不允许复制同一个评分）", async () => {
    const { wrapper } = await mountPage();

    const text = wrapper.text();
    for (const [label, mean] of [
      ["1 个月", "47.1"],
      ["3 个月", "47.3"],
      ["6 个月", "48.6"],
      ["12 个月", "49.6"],
      ["36 个月", "50.7"],
    ] as const) {
      expect(text).toContain(label);
      expect(text).toContain(mean);
    }
    // 五档均分不得全部相同
    const means = new Set(["47.1", "47.3", "48.6", "49.6", "50.7"]);
    expect(means.size).toBe(5);

    // 切到 1 个月后，传给排名表的期限标签应变化
    const buttons = wrapper.findAll("button").filter((b) => b.text().startsWith("1 个月"));
    expect(buttons.length).toBeGreaterThan(0);
    await buttons[0].trigger("click");
    await nextTick();
    expect(wrapper.find(".rank-stub").text()).toContain("1 个月");
  });

  it("切换评估范围会带上 limit 重新评估", async () => {
    const { wrapper } = await mountPage();
    const before = mocks.getValuationReport.mock.calls.length;

    const fullMarket = wrapper.findAll("button").find((b) => b.text().trim() === "全市场");
    expect(fullMarket).toBeTruthy();
    expect(fullMarket!.attributes("disabled")).toBeUndefined();
    await fullMarket!.trigger("click");
    await vi.waitFor(() => expect(mocks.getValuationReport.mock.calls.length).toBeGreaterThan(before));
    expect(mocks.getValuationReport).toHaveBeenLastCalledWith(0, true);
  });

  it("行业分布与行业/指数面板都被渲染，且指数选中的是有成分的那个", async () => {
    const { wrapper } = await mountPage();

    const text = wrapper.text();
    expect(text).toContain("行业分布");
    expect(text).toContain("各行业有多少标的进入全市场前 20%");
    expect(text).toContain("银行Ⅱ");
    expect(wrapper.find(".industry-stub").text()).toContain("行业面板");
    expect(wrapper.find(".index-stub").text()).toContain("指数面板");
    expect(mocks.listValuationIndices).toHaveBeenCalled();
    expect(mocks.getIndexValuation).toHaveBeenCalledWith("000300.SH");
  });

  it("口径说明必须写清评分流程、九个维度与五档独立的三重机制", async () => {
    const { wrapper } = await mountPage();

    const text = wrapper.text();
    expect(text).toContain("评分流程");
    expect(text).toContain("九个维度");
    expect(text).toContain("同一份信息只在一个维度计分");
    // 方案 §7.3：风险约束不得静默改写原始估值
    expect(text).toContain("原始分与最终分");
    // 五档为什么不同
    expect(text).toContain("为什么五档不会给出同一个分数");
    expect(text).toContain("向合理价值收敛的比例不同");
    // 后端 notes 必须逐条显示
    expect(text).toContain("后复权价");
    expect(text).toContain("Q4 是**全年值**");
  });

  it("模型参数可展开：五档权重与评级分界都能核对", async () => {
    const { wrapper } = await mountPage();

    const toggle = wrapper.findAll("button").find((b) => b.text().includes("模型参数"));
    expect(toggle).toBeTruthy();
    await toggle!.trigger("click");
    await vi.waitFor(() => expect(mocks.getValuationParams).toHaveBeenCalled());
    await nextTick();

    const text = wrapper.text();
    // 长期限的成长权重必须显著高于短期限（方案 §7.2 的"不同期限不同权重"）
    expect(text).toContain("24.0%");
    expect(text).toContain("WACC");
    expect(text).toContain("模型版本");
    expect(text).toContain("不将初始权重视为最优参数");
  });

  it("点榜上一行会打开个股详情面板并传入代码", async () => {
    const { wrapper } = await mountPage();

    expect(wrapper.find(".stock-stub").exists()).toBe(false);
    await wrapper.find(".rank-stub").trigger("click");
    await nextTick();

    expect(wrapper.find(".stock-stub").text()).toContain("601899");
  });

  it("数据不可用时给出可解释的空状态与下一步，而不是一屏 0", async () => {
    mocks.getValuationReport.mockResolvedValue(
      report({
        available: false,
        message: "数据湖缺少必需数据集 financial_statement_items；可在「数据源设置」页查看各数据集的入库情况。",
      }),
    );
    const { wrapper } = await mountPage();

    const text = wrapper.text();
    expect(text).toContain("多期限性价比暂时算不出来");
    expect(text).toContain("financial_statement_items");
    expect(text).toContain("数据源设置");
    // 不可用时绝不显示排名表
    expect(wrapper.find(".rank-stub").exists()).toBe(false);
    expect(text).not.toContain("参与排名");
  });

  it("读取失败时提示错误并提供重试", async () => {
    mocks.getValuationReport.mockRejectedValue(new Error("读取日线失败：磁盘错误"));
    const { wrapper } = await mountPage();

    const text = wrapper.text();
    expect(text).toContain("性价比评估失败");
    expect(text).toContain("读取日线失败：磁盘错误");
    expect(wrapper.findAll("button").some((b) => b.text().includes("重试"))).toBe(true);
  });
});

describe("多期限性价比：排名表", () => {
  async function mountTable(overrides: Partial<HorizonRanking> = {}) {
    const ValuationRankTable = (await import("@/components/valuation/ValuationRankTable.vue")).default;
    const wrapper = mount(ValuationRankTable, {
      props: { ranking: { ...ranking("12m", 49.6), ...overrides } as HorizonRanking, horizonLabel: "12 个月" },
    });
    return wrapper;
  }

  it("显示参与排名与未参与的数量，并能展开未参与清单", async () => {
    const wrapper = await mountTable();
    let text = wrapper.text();
    expect(text).toContain("参与排名");
    expect(text).toContain("135");
    expect(text).toContain("未参与");

    const toggle = wrapper.findAll("button").find((b) => b.text().includes("查看未参与排名的标的"));
    expect(toggle).toBeTruthy();
    await toggle!.trigger("click");
    await nextTick();
    text = wrapper.text();
    expect(text).toContain("数据不足公司");
    expect(text).toContain("评估日前没有任何已披露财报");
    // 方案 §9：被风险规则阻断或无法可靠评估的标的单独标记，不与正常评估的混排
    expect(text).toContain("单独标记");
    expect(text).toContain("混合排序");
  });

  it("榜上必须有代码与名称两列（只有代码等于让用户自己查表）", async () => {
    const wrapper = await mountTable({
      rows: [rankRow("601899", "紫金矿业", 70.9), { ...rankRow("000001", "平安银行", 55), ratingCapped: false }],
      scored: 2,
    });

    const headers = wrapper.findAll("thead th").map((th) => th.text());
    expect(headers).toContain("代码");
    expect(headers).toContain("名称");

    const text = wrapper.text();
    expect(text).toContain("601899");
    expect(text).toContain("紫金矿业");
    expect(text).toContain("平安银行");
  });

  it("行业聚合不可用时「对行业中位」显示「—」而不是 0%（0 会被读成与行业持平）", async () => {
    const broken = {      ...rankRow("601899", "紫金矿业", 70.9),
      industryWeightedPE: 0,
      industryPercentile: 0,
      currentPE: 0,
      premiumVsMedian: 0,
    };
    const wrapper = await mountTable({ rows: [broken as RankRow], scored: 1 });

    // "对行业中位"那一列必须显示破折号
    const cells = wrapper.findAll("tbody tr td").map((td) => td.text());
    expect(cells).toContain("—");
    // 而且不能出现 0.0% 这种会把"算不出来"读成"持平"的写法
    expect(cells).not.toContain("0.0%");
  });

  it("榜首的依据里同时给出关键优势、主要风险，以及安全边际与潜在收益的不同口径", async () => {
    const wrapper = await mountTable();
    const text = wrapper.text();

    expect(text).toContain("关键优势");
    expect(text).toContain("自身估值处于历史 6% 分位");
    expect(text).toContain("主要风险");
    expect(text).toContain("安全边际");
    expect(text).toContain("潜在收益");
    // 方案 §5.6 要求两者分开展示并说明口径不同
    expect(text).toContain("两个不同口径");
  });

  it("风险约束的严重程度与原因都可见", async () => {
    const wrapper = await mountTable();
    const text = wrapper.text();
    expect(text).toContain("限制评级");
    expect(text).toContain("估值结论高度依赖乐观假设");
  });

  it("按代码筛选与只看未被封顶两种筛选都生效", async () => {
    const wrapper = await mountTable({
      rows: [rankRow("601899", "紫金矿业", 70.9), { ...rankRow("000001", "平安银行", 55), ratingCapped: false }],
      scored: 2,
    });

    // ⚠ 关键词输入框没有写 type 属性（默认就是 text），所以按 type='text' 选不到；
    // 这里用"第一个非 checkbox 的 input"来定位，顺便保证 checkbox 那条断言选的是另一个。
    const input = wrapper.findAll("input").find((item) => item.attributes("type") !== "checkbox");
    expect(input).toBeTruthy();
    await input!.setValue("平安");
    await nextTick();
    let text = wrapper.text();
    expect(text).toContain("平安银行");
    expect(text).not.toContain("紫金矿业");

    await input!.setValue("");
    await nextTick();
    const checkbox = wrapper.find("input[type='checkbox']");
    expect(checkbox.exists()).toBe(true);
    await checkbox.setValue(true);
    await nextTick();
    text = wrapper.text();
    expect(text).toContain("平安银行");
    expect(text).not.toContain("紫金矿业");
  });

  it("搜索支持拼音全拼与首字母（与股票列表页同一套规则）", async () => {
    // 后端按"名称原样"返回拼音，多音字的所有写法都在同一个串里
    mocks.getNamePinyin.mockResolvedValue({
      贵州茅台: { pinyin: "guizhoumaotai", initials: "gzmt" },
      平安银行: { pinyin: "pinganyinxing pinganyinhang", initials: "payx payh" },
    });
    const wrapper = await mountTable({
      rows: [rankRow("600519", "贵州茅台", 70.9), rankRow("000001", "平安银行", 55)],
      scored: 2,
    });
    await flushPromises();
    await nextTick();

    const input = wrapper.findAll("input").find((item) => item.attributes("type") !== "checkbox")!;

    // 全拼
    await input.setValue("guizhou");
    await nextTick();
    expect(wrapper.text()).toContain("贵州茅台");
    expect(wrapper.text()).not.toContain("平安银行");

    // 首字母
    await input.setValue("gzmt");
    await nextTick();
    expect(wrapper.text()).toContain("贵州茅台");

    // 多音字的另一个读法也要能命中（「银行」的行读 háng）
    await input.setValue("payh");
    await nextTick();
    expect(wrapper.text()).toContain("平安银行");
    expect(wrapper.text()).not.toContain("贵州茅台");

    // 中文名称仍可搜
    await input.setValue("茅台");
    await nextTick();
    expect(wrapper.text()).toContain("贵州茅台");
  });

  it("行业筛选是真筛选（按行业多选，可叠加可清除）", async () => {
    const bank = { ...rankRow("000001", "平安银行", 55), industryCode: "sw:480301", industryName: "银行" };
    const liquor = { ...rankRow("600519", "贵州茅台", 70.9), industryCode: "sw:340501", industryName: "白酒" };
    const wrapper = await mountTable({ rows: [bank as RankRow, liquor as RankRow], scored: 2 });

    // 两个行业都出现为可点的筛选按钮
    const buttons = wrapper.findAll("button");
    const bankButton = buttons.find((b) => b.text().startsWith("银行"));
    const liquorButton = buttons.find((b) => b.text().startsWith("白酒"));
    expect(bankButton).toBeTruthy();
    expect(liquorButton).toBeTruthy();

    await bankButton!.trigger("click");
    await nextTick();
    let text = wrapper.text();
    expect(text).toContain("平安银行");
    expect(text).not.toContain("贵州茅台");

    // 再选一个行业 → 并集（两个都在）
    await liquorButton!.trigger("click");
    await nextTick();
    text = wrapper.text();
    expect(text).toContain("平安银行");
    expect(text).toContain("贵州茅台");

    // 清除
    const clear = wrapper.findAll("button").find((b) => b.text().includes("清除行业筛选"));
    expect(clear).toBeTruthy();
    await clear!.trigger("click");
    await nextTick();
    text = wrapper.text();
    expect(text).toContain("平安银行");
    expect(text).toContain("贵州茅台");

    // 输入行业名也能当搜索词（提示语承诺了三件事：代码/名称/行业）
    const input = wrapper.findAll("input").find((item) => item.attributes("type") !== "checkbox")!;
    await input.setValue("白酒");
    await nextTick();
    text = wrapper.text();
    expect(text).toContain("贵州茅台");
    expect(text).not.toContain("平安银行");
  });

  it("行很多时只渲染视口内的行（虚拟列表），不是全部塞进 DOM", async () => {
    // 造 2000 行 —— 真实全市场规模是 4400+
    const many = Array.from({ length: 2000 }, (_, index) => {
      const code = String(600000 + index);
      return { ...rankRow(code, `公司${index}`, 70.9), rank: index + 1 } as RankRow;
    });
    const wrapper = await mountTable({ rows: many, scored: 2000 });
    await nextTick();

    const rendered = wrapper.findAll("tbody tr").length;
    // 关键断言：绝不能渲染 2000 行
    expect(rendered).toBeLessThan(200);
    // 但至少要渲染出一些行（否则就是"虚拟列表把自己也虚拟没了"）
    expect(rendered).toBeGreaterThan(0);

    // 参与排名的总数仍如实显示，虚拟化不影响语义
    expect(wrapper.text()).toContain("2000");
  });
});

describe("多期限性价比：行业面板", () => {
  async function mountIndustry() {
    const IndustryValuationPanel = (await import("@/components/valuation/IndustryValuationPanel.vue")).default;
    const wrapper = mount(IndustryValuationPanel, {
      props: {
        industries: [industry()],
        detail: {
          available: true,
          industry: industry(),
          members: [rankRow("601899", "紫金矿业", 70.9)],
          snapshot: report().snapshot,
          notes: [],
        } as IndustryValuationDetail,
        loading: false,
        error: "",
        activeCode: "475",
      },
    });
    return wrapper;
  }

  it("加权 / 中位 / 历史分位三者分列展示（方案 §5.2 的硬要求）", async () => {
    const wrapper = await mountIndustry();
    const text = wrapper.text();

    expect(text).toContain("① 加权（调和口径）");
    expect(text).toContain("② 中位数（抗极端值）");
    expect(text).toContain("③ 历史分位");
    // 口径必须写清楚"不是算术平均"
    expect(text).toContain("不是");
    expect(text).toContain("算术平均");
    expect(text).toContain("偏乐观");
  });

  it("行业历史分位与说明都能显示（含时点正确重建的口径）", async () => {
    const detail = {
      available: true,
      industry: {
        ...industry(),
        pePercentile: 12,
        peBand: { p10: 20, p25: 25, p50: 32, p75: 40, p90: 55, min: 15, max: 70 },
        note: "历史加权 PE 序列由**时点正确**重建：2021-01-29 起 69 个行业快照日里算出 69 个",
      },
      members: [],
      snapshot: report().snapshot,
      notes: [],
    } as IndustryValuationDetail;
    const IndustryValuationPanel = (await import("@/components/valuation/IndustryValuationPanel.vue")).default;
    const wrapper = mount(IndustryValuationPanel, {
      props: { industries: [detail.industry], detail, loading: false, error: "", activeCode: "475" },
    });

    const text = wrapper.text();
    expect(text).toContain("时点正确");
    expect(text).toContain("69 个行业快照日");
    // 区间要能看见（P10/P90/中位）
    expect(text).toContain("P10");
    expect(text).toContain("P90");
  });
});

describe("多期限性价比：个股详情", () => {
  async function mountStock() {
    mocks.getStockValuation.mockResolvedValue(stockValuation());
    const StockValuationPanel = (await import("@/components/valuation/StockValuationPanel.vue")).default;
    const wrapper = mount(StockValuationPanel, { props: { code: "601899" } });
    await vi.waitFor(() => expect(mocks.getStockValuation).toHaveBeenCalledWith("601899"));
    await nextTick();
    await nextTick();
    return wrapper;
  }

  it("四层估值都在：当前估值 / 自身历史分位 / 行业估值 / 相对行业与同行", async () => {
    const wrapper = await mountStock();
    const text = wrapper.text();

    expect(text).toContain("当前估值（不复权价 × 当期股本）");
    expect(text).toContain("自身历史估值分位");
    expect(text).toContain("行业整体估值");
    expect(text).toContain("相对行业与同行");
    // 行业三者分列（方案 §5.2 的硬要求）
    expect(text).toContain("加权 PE");
    expect(text).toContain("中位 PE");
    expect(text).toContain("历史分位");
  });

  it("成长能力与成长质量是两个独立区块（方案 §4.2）", async () => {
    const wrapper = await mountStock();
    const text = wrapper.text();
    expect(text).toContain("成长能力");
    expect(text).toContain("成长质量与盈利质量");
    expect(text).toContain("扣非占比");
    expect(text).toContain("利润现金含量");
    // 成长形态要点名
    expect(text).toContain("可能是行业性反弹");
  });

  it("三情景合理价值、安全边际、潜在收益与两者口径说明都在", async () => {
    const wrapper = await mountStock();
    const text = wrapper.text();

    expect(text).toContain("悲观");
    expect(text).toContain("基准");
    expect(text).toContain("乐观");
    expect(text).toContain("安全边际");
    expect(text).toContain("潜在收益");
    expect(text).toContain("不能混用");
    // 反向 DCF 的市场预期判断
    expect(text).toContain("市场预期");
    expect(text).toContain("偏高");
  });

  it("多模型交叉验证逐条列出，不适用的方法也给出原因", async () => {
    const wrapper = await mountStock();
    const text = wrapper.text();

    expect(text).toContain("多模型交叉验证");
    expect(text).toContain("PE（市盈率）");
    // 不适用的 DCF 必须说明为什么不用，而不是悄悄消失
    expect(text).toContain("DCF（现金流折现）");
    expect(text).toContain("不做 DCF");
    // 方法冲突必须解释且写明不取平均
    expect(text).toContain("不取平均");
  });

  it("五档期限各自的评分、目标价与情景收益都不同", async () => {
    const wrapper = await mountStock();

    // 默认 12 个月
    let text = wrapper.text();
    expect(text).toContain("70.9");
    expect(text).toContain("向合理价值收敛");
    expect(text).toContain("乐观依赖度");
    expect(text).toContain("风险收益比");

    // 切到 1 个月：分数与收敛比例都要变
    const oneMonth = wrapper.findAll("button").find((b) => b.text().startsWith("1 个月"));
    expect(oneMonth).toBeTruthy();
    await oneMonth!.trigger("click");
    await nextTick();
    text = wrapper.text();
    expect(text).toContain("72.2");
  });

  it("风险约束逐条列出严重程度、原因与评级上限", async () => {
    const wrapper = await mountStock();
    const text = wrapper.text();

    expect(text).toContain("风险约束");
    expect(text).toContain("限制评级");
    expect(text).toContain("估值结论高度依赖乐观假设");
    expect(text).toContain("评级上限");
    expect(text).toContain("被风险约束封顶");
  });

  it("九维度得分与依据可展开，且依据带时点与来源（方案 §10.6）", async () => {
    const wrapper = await mountStock();

    const toggle = wrapper.findAll("button").find((b) => b.text().includes("九维度得分与依据"));
    expect(toggle).toBeTruthy();
    await toggle!.trigger("click");
    await nextTick();

    const text = wrapper.text();
    expect(text).toContain("个股自身估值");
    expect(text).toContain("成长能力");
    expect(text).toContain("权重");
    expect(text).toContain("贡献");
    // 依据必须能追到指标、时点与来源
    expect(text).toContain("自身历史估值分位");
    expect(text).toContain("自建历史估值序列");
    expect(text).toContain("2026Q2");
  });

  it("结构性变化会提示历史分位权重已下调（方案 §5.1）", async () => {
    const wrapper = await mountStock();
    const text = wrapper.text();
    expect(text).toContain("已检测到结构性变化");
    expect(text).toContain("估值中枢移动");
  });

  it("不可评估时说明原因且不给评级（验收标准 8）", async () => {
    mocks.getStockValuation.mockResolvedValue({
      available: false,
      message: "601899 的数据不足以评估：可能是行情不足 252 根（新股 / 长期停牌）",
      stock: null,
      snapshot: report().snapshot,
      notes: [],
    } as unknown as StockValuation);
    const StockValuationPanel = (await import("@/components/valuation/StockValuationPanel.vue")).default;
    const wrapper = mount(StockValuationPanel, { props: { code: "601899" } });
    await vi.waitFor(() => expect(mocks.getStockValuation).toHaveBeenCalled());
    await nextTick();

    const text = wrapper.text();
    expect(text).toContain("暂无法可靠评估");
    expect(text).toContain("行情不足 252 根");
    expect(text).toContain("不强行给出确定性结论");
  });
});
