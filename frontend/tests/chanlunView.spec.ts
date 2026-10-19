import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, nextTick } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

import type { ChanlunBundle, ChanlunReplay, KlineBundle } from "@/lib/backend";

// ---- 假的 backend（只保留 K 线页用到的入口） ----
const mocks = vi.hoisted(() => ({
  getKline: vi.fn(),
  getKlinePrefs: vi.fn(),
  saveKlinePrefs: vi.fn(),
  searchStocks: vi.fn(),
  analyze: vi.fn(),
  replay: vi.fn(),
  getConfig: vi.fn(),
  saveConfig: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    getKline: mocks.getKline,
    getKlinePrefs: mocks.getKlinePrefs,
    saveKlinePrefs: mocks.saveKlinePrefs,
    searchStocks: mocks.searchStocks,
    chanlun: {
      analyze: mocks.analyze,
      replay: mocks.replay,
      getConfig: mocks.getConfig,
      saveConfig: mocks.saveConfig,
    },
    getStockUniverse: vi.fn(async () => null),
    getBoardCategories: vi.fn(async () => []),
    getBoards: vi.fn(async () => []),
    getDataSources: vi.fn(async () => []),
    getCurrentDataSource: vi.fn(async () => null),
    testDataSource: vi.fn(),
    setDataSource: vi.fn(),
    clearCache: vi.fn(async () => undefined),
  },
  errorMessage: (err: unknown) => String(err),
  onScanProgress: () => () => {},
}));

import KlineView from "@/views/KlineView.vue";

/** 图表桩：只暴露我们关心的 chanlun 标注 */
const KlineChartStub = defineComponent({
  name: "KlineChart",
  props: {
    candles: { type: Array, default: () => [] },
    chanlun: { type: Object, default: null },
  },
  template: '<div class="kline-chart-stub" />',
});

// ---- 测试数据 ----
function businessDays(count: number): number[] {
  const out: number[] = [];
  const cursor = new Date(Date.UTC(2026, 8, 30));
  while (out.length < count) {
    const day = cursor.getUTCDay();
    if (day !== 0 && day !== 6) {
      const y = cursor.getUTCFullYear();
      const m = String(cursor.getUTCMonth() + 1).padStart(2, "0");
      const d = String(cursor.getUTCDate()).padStart(2, "0");
      out.unshift(Number(`${y}${m}${d}`));
    }
    cursor.setUTCDate(cursor.getUTCDate() - 1);
  }
  return out;
}

const DATES = businessDays(120);
const CANDLES = DATES.map((date, i) => {
  const base = 100 + Math.sin(i / 8) * 6;
  return {
    date,
    open: Number((base - 0.3).toFixed(2)),
    high: Number((base + 1.1).toFixed(2)),
    low: Number((base - 1.2).toFixed(2)),
    close: Number((base + 0.4).toFixed(2)),
    volume: 1_000_000,
    amount: 1e8,
  };
});

const KLINE_BUNDLE = {
  code: "600519",
  name: "贵州茅台",
  adjust: "qfq",
  profile: {
    code: "600519",
    name: "贵州茅台",
    market: "sh-main",
    marketCN: "沪市主板",
    tradeDate: DATES[DATES.length - 1],
    close: 100,
    peTTM: 20,
    pb: 6,
    totalMV: 1e12,
    floatMV: 1e12,
  },
  candles: CANDLES,
  dividends: [],
  moneyFlows: [],
  notes: ["说明"],
} as unknown as KlineBundle;

const ANALYSIS = {
  code: "600519",
  name: "贵州茅台",
  level: "day",
  levelCN: "日线",
  adjust: "qfq",
  tradeDate: DATES[DATES.length - 1],
  data: {
    bars: 120,
    startDate: DATES[0],
    endDate: DATES[DATES.length - 1],
    mergedBars: 100,
    hasIntraday: false,
    precisionNote: "只有日 K：区间套只能在日线内部做，精度有限",
  },
  mergedBars: [],
  fractals: [],
  strokes: [],
  segments: [],
  pivots: [],
  trend: {
    type: "down",
    typeCN: "下跌趋势",
    level: "day",
    startDate: DATES[0],
    endDate: DATES[20],
    pivotIndex: [],
    status: "extending",
    statusCN: "延伸中",
    completedDate: 0,
    completionReason: "",
    completedBy: "",
    outlook: [
      {
        key: "continuation",
        label: "下跌趋势延续",
        condition: "不升破最后一个中枢上沿 ZG=11.14",
        status: "active",
        statusCN: "进行中",
        date: 0,
        note: "",
      },
      {
        key: "consolidation",
        label: "转为盘整",
        condition: "回到中枢 10.67 ~ 11.14 区间震荡",
        status: "pending",
        statusCN: "待发生",
        date: 0,
        note: "",
      },
      {
        key: "reversal",
        label: "转为上涨趋势",
        condition: "升破 ZG=11.14 后回抽不回，并形成反向中枢",
        status: "pending",
        statusCN: "待发生",
        date: 0,
        note: "",
      },
    ],
    note: "",
  },
  position: { phase: "pivot-inside", phaseCN: "中枢中", pivotIndex: -1, barsFromPivot: 0, note: "价格仍在中枢重叠区内" },
  divergences: [
    {
      index: 0,
      date: DATES[30],
      kind: "trend",
      kindCN: "趋势背驰",
      direction: "bottom",
      level: "day",
      pivotIndex: 0,
      legAStart: 10,
      legAEnd: 20,
      legBStart: 25,
      legBEnd: 30,
      legAStartDate: DATES[10],
      legAEndDate: DATES[20],
      legBStartDate: DATES[25],
      legBEndDate: DATES[30],
      strengthA: 1,
      strengthB: 0.72,
      ratio: 0.72,
      dims: [],
      newExtreme: true,
      confirmed: true,
      outcome: "failure",
      outcomeCN: "背驰失败",
      outcomes: [
        {
          key: "reversal",
          label: "反转",
          condition: "形成二买/二卖确认，或反向走势破坏原趋势最后一个中枢",
          status: "pending",
          statusCN: "待发生",
          date: 0,
        },
        {
          key: "consolidation",
          label: "盘整",
          condition: "回到最后一个中枢，并形成与原中枢重叠的新中枢",
          status: "pending",
          statusCN: "待发生",
          date: 0,
        },
        {
          key: "failure",
          label: "背驰失败",
          condition: "继续原趋势并创出新低/新高（超过失效容差）",
          status: "confirmed",
          statusCN: "已确认",
          date: DATES[40],
        },
      ],
      note: "背驰只保证至少回到最后一个中枢，不保证反转",
    },
  ],
  signals: [],
  state: {
    name: "reversal-candidate-buy",
    nameCN: "下跌趋势背驰（反转候选买）",
    note: "下跌趋势出现底背驰",
    sinceDate: DATES[0],
    watchSignals: ["buy1"],
  },
  adaptability: {
    score: 72,
    grade: "B",
    liquidity: 80,
    amount: 1e9,
    history: 90,
    completeness: 60,
    stability: 70,
    penalty: 0,
    reasons: ["流动性、历史长度与数据完整性均满足要求"],
  },
  notes: ["只有日 K：区间套只能在日线内部做，精度有限"],
  disclaimer: "仅作参考，不构成投资建议",
};

const BUNDLE = {
  code: "600519",
  name: "贵州茅台",
  adjust: "qfq",
  tradeDate: DATES[DATES.length - 1],
  analysis: ANALYSIS,
  multiLevel: { levels: [], resonance: false, nested: false, note: "只有一个级别参与印证", notes: [] },
  notes: ["只有一个级别参与印证"],
  disclaimer: "仅作参考，不构成投资建议",
} as unknown as ChanlunBundle;

const REPLAY = {
  code: "600519",
  level: "day",
  levelCN: "日线",
  step: 5,
  steps: [
    {
      index: 60,
      date: DATES[60],
      state: { name: "consolidation", nameCN: "盘整", note: "", sinceDate: DATES[0], watchSignals: [] },
      trend: { type: "consolidation", typeCN: "盘整", level: "day", startDate: DATES[0], endDate: DATES[60], pivotIndex: [], note: "" },
      position: { phase: "pivot-inside", phaseCN: "中枢中", pivotIndex: 0, barsFromPivot: 1, note: "" },
      pivots: 1,
      strokes: 10,
      divergences: 0,
      signals: [],
      note: "当时只有 61 根 K 线",
    },
  ],
  notes: [],
  disclaimer: "仅作参考，不构成投资建议",
} as unknown as ChanlunReplay;

async function mountPage() {
  const pinia = createPinia();
  setActivePinia(pinia);

  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", component: { template: "<div />" } },
      { path: "/kline", component: KlineView },
    ],
  });
  await router.push({ path: "/kline", query: { code: "600519" } });
  await router.isReady();

  const wrapper = mount(KlineView, {
    global: { plugins: [pinia, router], stubs: { KlineChart: KlineChartStub } },
  });
  await vi.waitFor(() => expect(mocks.getKline).toHaveBeenCalled());
  await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));
  await nextTick();
  return { wrapper };
}

/** 按文字找工具栏按钮（开关都是 Button） */
function buttonByText(wrapper: ReturnType<typeof mount>, text: string) {
  const button = wrapper.findAll("button").find((item) => item.text().includes(text));
  if (!button) throw new Error(`没找到按钮：${text}`);
  return button;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getKline.mockResolvedValue(KLINE_BUNDLE);
  mocks.getKlinePrefs.mockResolvedValue({ prefs: null, saved: false, configPath: "" });
  mocks.saveKlinePrefs.mockImplementation(async (prefs: unknown) => ({ prefs, saved: true, configPath: "" }));
  mocks.searchStocks.mockResolvedValue([]);
  mocks.analyze.mockResolvedValue(BUNDLE);
  mocks.replay.mockResolvedValue(REPLAY);
  mocks.getConfig.mockResolvedValue({
    config: {
      volumeAux: true,
      showCandidate: true,
      showConflict: true,
      invalidTolerance: 0.005,
      scoreThreshold: 45,
      replayStep: 1,
      divergence: {
        threshold: 0.9,
        structureWeight: 0.3,
        macdWeight: 0.4,
        slopeWeight: 0.2,
        volumeWeight: 0.1,
        minLegBars: 3,
      },
      levelWeights: [],
      typeWeights: [],
    },
    saved: false,
    configPath: "C:\\Users\\x\\config.json",
  });
  mocks.saveConfig.mockImplementation(async (config: unknown) => ({
    config,
    saved: true,
    configPath: "C:\\Users\\x\\config.json",
  }));
});

describe("K 线页的缠论叠加", () => {
  it("默认关闭：不请求缠论，也不给图表传标注", async () => {
    const { wrapper } = await mountPage();
    expect(mocks.analyze).not.toHaveBeenCalled();
    const chart = wrapper.findComponent(KlineChartStub);
    expect(chart.props("chanlun")).toBeNull();
    // 关闭时没有右侧面板
    expect(wrapper.findComponent({ name: "ChanlunPanel" }).exists()).toBe(false);
  });

  it("打开后按当前级别请求，并把标注交给图表与面板", async () => {
    const { wrapper } = await mountPage();

    await buttonByText(wrapper, "缠论").trigger("click");
    await vi.waitFor(() => expect(mocks.analyze).toHaveBeenCalledTimes(1));

    const request = mocks.analyze.mock.calls[0][0] as { code: string; level: string; adjust: string };
    expect(request.code).toBe("600519");
    expect(request.level).toBe("day");
    expect(request.adjust).toBe("qfq");

    await vi.waitFor(() =>
      expect(wrapper.findComponent(KlineChartStub).props("chanlun")).not.toBeNull(),
    );
    const overlay = wrapper.findComponent(KlineChartStub).props("chanlun") as {
      levelCN: string;
    };
    expect(overlay.levelCN).toBe("日线");

    const text = wrapper.text();
    expect(text).toContain("下跌趋势背驰（反转候选买）");
    expect(text).toContain("仅作参考，不构成投资建议");
    expect(text).toContain("只有日 K");
    // 走势终完美：生命周期 + 完全分类
    expect(text).toContain("延伸中");
    expect(text).toContain("后续可能（完全分类）");
    expect(text).toContain("下跌趋势延续");
    // 背驰 ≠ 反转：三种可能的状态
    expect(text).toContain("当前判定");
    expect(text).toContain("背驰失败");
  });

  it("切换级别会按新级别重新分析（级别是独立结构，不是缩放）", async () => {
    const { wrapper } = await mountPage();
    await buttonByText(wrapper, "缠论").trigger("click");
    await vi.waitFor(() => expect(mocks.analyze).toHaveBeenCalledTimes(1));

    await buttonByText(wrapper, "周线").trigger("click");
    await vi.waitFor(() => expect(mocks.analyze).toHaveBeenCalledTimes(2));
    const request = mocks.analyze.mock.calls[1][0] as { level: string };
    expect(request.level).toBe("week");
  });

  it("回放：请求快照并把图表裁到该时点", async () => {
    const { wrapper } = await mountPage();
    await buttonByText(wrapper, "缠论").trigger("click");
    await vi.waitFor(() => expect(mocks.analyze).toHaveBeenCalledTimes(1));

    await buttonByText(wrapper, "回放").trigger("click");
    await vi.waitFor(() => expect(mocks.replay).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(wrapper.text()).toContain("退出回放"));

    // 图表拿到的蜡烛只到快照那一天
    const chart = wrapper.findComponent(KlineChartStub);
    const candles = chart.props("candles") as { date: number }[];
    expect(candles.length).toBeGreaterThan(0);
    expect(candles[candles.length - 1].date).toBeLessThanOrEqual(REPLAY.steps[0].date);

    // 面板展示的是"当时的状态"
    expect(wrapper.text()).toContain("当时只有 61 根 K 线");
  });

  it("关闭缠论会清空标注与面板", async () => {
    const { wrapper } = await mountPage();
    const toggle = () => buttonByText(wrapper, "缠论");

    await toggle().trigger("click");
    await vi.waitFor(() =>
      expect(wrapper.findComponent(KlineChartStub).props("chanlun")).not.toBeNull(),
    );

    await toggle().trigger("click");
    await nextTick();
    expect(wrapper.findComponent(KlineChartStub).props("chanlun")).toBeNull();
    expect(wrapper.findComponent({ name: "ChanlunPanel" }).exists()).toBe(false);
  });

  it("缠论设置（背驰阈值）会写回配置，并触发重新分析", async () => {
    const { wrapper } = await mountPage();
    await buttonByText(wrapper, "缠论").trigger("click");
    await vi.waitFor(() => expect(mocks.analyze).toHaveBeenCalledTimes(1));

    // 打开「指标设置」才看得到缠论设置块
    await buttonByText(wrapper, "指标设置").trigger("click");
    await vi.waitFor(() => expect(mocks.getConfig).toHaveBeenCalled());

    const threshold = wrapper.find('input[title^="后段/前段力度比"]');
    expect(threshold.exists(), "没有找到背驰阈值输入框").toBe(true);
    await threshold.setValue("0.85");

    await vi.waitFor(() => expect(mocks.saveConfig).toHaveBeenCalled(), { timeout: 2000 });
    const saved = mocks.saveConfig.mock.calls[0][0] as { divergence: { threshold: number } };
    expect(saved.divergence.threshold).toBe(0.85);

    // 阈值变了，当前图上的结构要按新口径重算
    await vi.waitFor(() => expect(mocks.analyze).toHaveBeenCalledTimes(2));
  });
});
