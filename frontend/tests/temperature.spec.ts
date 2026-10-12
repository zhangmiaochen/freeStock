import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, nextTick } from "vue";

import type { MarketIndexStat, MarketTemperatureResult, TemperatureFactor } from "@/lib/backend";

const mocks = vi.hoisted(() => ({
  getMarketTemperature: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: { getMarketTemperature: mocks.getMarketTemperature },
  errorMessage: (err: unknown) => String(err),
}));

import MarketTemperatureView from "@/views/MarketTemperatureView.vue";

function factor(key: string, name: string, weight: number, score: number, valueText: string): TemperatureFactor {
  return {
    key,
    name,
    weight,
    score,
    value: 1,
    valueText,
    detail: `${name}的口径说明`,
  } as TemperatureFactor;
}

function indexStat(overrides: Partial<MarketIndexStat> & { code: string; name: string }): MarketIndexStat {
  return {
    group: "宽基",
    date: 20261009,
    close: 3813.79,
    pctChg: 0.05,
    periodPctChg: 0.05,
    amount: 3.86e11,
    periodHigh: 3850,
    periodLow: 3700,
    volumeRatio: 0.83,
    ma20: 3900,
    ma60: 3950,
    ma250: 3990,
    maDeviation: -4.44,
    levelScore: 1,
    levelText: "点位低于 250 日均线 4.44%，这个偏离在 2023-10-09 以来的历史里处于 1% 分位。",
    drawdownFromHigh: -10.11,
    highDate: 20260513,
    highClose: 4242,
    newHighDays: 0,
    newLowDays: 0,
    momentumScore: 50,
    volumeScore: 12,
    trendScore: 24,
    trendLabel: "弱势下行",
    trendNote: "跌破 20 / 60 / 250 日均线；当日 +0.05%（历史分位 50%）。",
    recent: [3900, 3880, 3850, 3813.79],
    norm: [100, 99.5, 98.7, 97.8],
    historyDays: 728,
    historyStart: 20231009,
    ...overrides,
  } as MarketIndexStat;
}

const INDEX_NAMES = [
  ["000001.SH", "上证指数"],
  ["399001.SZ", "深证成指"],
  ["399006.SZ", "创业板指"],
  ["000688.SH", "科创50"],
  ["000300.SH", "沪深300"],
  ["000016.SH", "上证50"],
  ["000905.SH", "中证500"],
  ["000852.SH", "中证1000"],
] as const;

function result(overrides: Partial<MarketTemperatureResult> = {}): MarketTemperatureResult {
  return {
    available: true,
    message: "",
    tradeDate: 20261009,
    periodDays: 1,
    periodLabel: "当日",
    temperature: 41.6,
    level: "偏冷",
    levelText: "市场热度低于历史多数时间：趋势偏弱、量能不足，适合等待信号。",
    composite: 37.2,
    historyDays: 728,
    historyStart: 20231009,
    historyEnd: 20261009,
    factors: [
      factor("trend", "趋势", 0.4, 54.6, "+0.13%"),
      factor("volume", "量能", 0.25, 9.6, "0.80 倍"),
      factor("position", "位置", 0.2, 0.4, "-7.79%"),
      factor("breadth", "共识", 0.15, 85.8, "100%"),
    ],
    indices: INDEX_NAMES.map(([code, name]) => indexStat({ code, name })),
    indexCount: 8,
    window: 20,
    dates: [20260910, 20260911, 20260912, 20261009],
    notes: ["指数历史来自本地数据湖的 index_bars：2023-10-09 ~ 2026-10-09 共 728 个交易日（8 个指数）。"],
    source: "D:\\workspace\\stockTool\\CNEquity\\data\\cnequity",
    elapsed: "16ms",
    ...overrides,
  } as MarketTemperatureResult;
}

const IndexCompareChartStub = defineComponent({
  name: "IndexCompareChart",
  props: {
    dates: { type: Array, default: () => [] },
    indices: { type: Array, default: () => [] },
    window: { type: Number, default: 20 },
    periodLabel: { type: String, default: "当日" },
  },
  template: '<div class="compare-stub" />',
});

async function mountPage() {
  const wrapper = mount(MarketTemperatureView, {
    global: { stubs: { IndexCompareChart: IndexCompareChartStub } },
  });
  await vi.waitFor(() => expect(mocks.getMarketTemperature).toHaveBeenCalled());
  await nextTick();
  await nextTick();
  return { wrapper };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getMarketTemperature.mockResolvedValue(result());
});

describe("市场温度页面", () => {
  it("默认按当日加载，显示温度、等级与分项拆解", async () => {
    const { wrapper } = await mountPage();

    expect(mocks.getMarketTemperature).toHaveBeenCalledWith(1);

    const text = wrapper.text();
    expect(text).toContain("市场温度");
    expect(text).toContain("41.6");
    expect(text).toContain("偏冷");
    expect(text).toContain("综合分 37.2");
    expect(text).toContain("市场热度低于历史多数时间");

    // 四个分项 + 权重必须都在（否则用户没法核对这个数字怎么来的）
    for (const [name, weight] of [["趋势", "40%"], ["量能", "25%"], ["位置", "20%"], ["共识", "15%"]] as const) {
      expect(text).toContain(name);
      expect(text).toContain(`权重 ${weight}`);
    }
    expect(text).toContain("+0.13%");
    expect(text).toContain("0.80 倍");

    // 分位样本要写清楚
    expect(text).toContain("728");
    expect(text).toContain("2023-10-09");
  });

  it("列出 8 大指数的最新点数、走势与趋势说明", async () => {
    const { wrapper } = await mountPage();

    const text = wrapper.text();
    expect(text).toContain("8 大指数");
    for (const [, name] of INDEX_NAMES) expect(text).toContain(name);

    // 点数与涨跌
    expect(text).toContain("3813.79");
    expect(text).toContain("+0.05%");
    // 位置水平用分位表达，而不是点数高低
    expect(text).toContain("位置水平");
    expect(text).toContain("1 分位");
    expect(text).toContain("点位低于 250 日均线");
    // 趋势结论
    expect(text).toContain("弱势下行");
    expect(text).toContain("跌破 20 / 60 / 250 日均线");

    // 每个指数一张卡（+ 顶部对比图是 stub）
    expect(wrapper.findAll("article")).toHaveLength(8);
    // 每张卡都有一个迷你走势图
    expect(wrapper.findAll("article svg")).toHaveLength(8);
  });

  it("对比图拿到归一化序列与日期轴", async () => {
    const { wrapper } = await mountPage();

    const chart = wrapper.findComponent(IndexCompareChartStub);
    expect(chart.exists()).toBe(true);
    expect(chart.props("indices")).toHaveLength(8);
    expect(chart.props("dates")).toHaveLength(4);
    expect(chart.props("window")).toBe(20);
    expect(chart.props("periodLabel")).toBe("当日");
  });

  it("切周期会带上新周期重新请求", async () => {
    const { wrapper } = await mountPage();
    mocks.getMarketTemperature.mockClear();
    mocks.getMarketTemperature.mockResolvedValue(result({ periodDays: 60, periodLabel: "60日" }));

    const button = wrapper.findAll("button").find((b) => b.text().trim() === "60日");
    expect(button).toBeTruthy();
    await button!.trigger("click");
    await vi.waitFor(() => expect(mocks.getMarketTemperature).toHaveBeenCalledWith(60));
    await nextTick();

    // 卡片上的周期标签跟着变
    expect(wrapper.text()).toContain("60日");
  });

  it("自定义周期：合法值会请求，非法值给提示且不请求", async () => {
    const { wrapper } = await mountPage();
    mocks.getMarketTemperature.mockClear();

    const custom = wrapper.findAll("button").find((b) => b.text().trim() === "自定义");
    await custom!.trigger("click");
    await nextTick();

    // 非法值：0
    await wrapper.find('input[type="number"]').setValue("0");
    const apply = wrapper.findAll("button").find((b) => b.text().trim() === "应用");
    await apply!.trigger("click");
    await nextTick();
    expect(mocks.getMarketTemperature).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("1 就是「当日」");

    // 合法值：20
    await wrapper.find('input[type="number"]').setValue("20");
    await apply!.trigger("click");
    await vi.waitFor(() => expect(mocks.getMarketTemperature).toHaveBeenCalledWith(20));
  });

  it("数据不可用时说明原因与下一步，而不是显示一屏 0", async () => {
    mocks.getMarketTemperature.mockResolvedValue(
      result({
        available: false,
        message: "本地数据湖里没有 index_bars（指数日线）。free-stockdb 不提供指数行情，所以市场温度只能在数据湖模式下使用。",
        indices: [],
        indexCount: 0,
        factors: [],
        temperature: 0,
        composite: 0,
        level: "",
        levelText: "",
        notes: [],
      }),
    );

    const { wrapper } = await mountPage();
    await nextTick();

    const text = wrapper.text();
    expect(text).toContain("市场温度暂时算不出来");
    expect(text).toContain("没有 index_bars");
    expect(text).toContain("数据源设置");
    // 不该出现指数卡片
    expect(wrapper.findAll("article")).toHaveLength(0);
  });

  it("读取失败时给出错误与重试", async () => {
    mocks.getMarketTemperature.mockRejectedValue(new Error("数据湖不可用"));
    const { wrapper } = await mountPage();

    await vi.waitFor(() => expect(wrapper.text()).toContain("市场温度读取失败"));
    expect(wrapper.text()).toContain("数据湖不可用");
    expect(wrapper.findAll("button").some((b) => b.text().includes("重试"))).toBe(true);
  });

  it("页面上必须写清「位置不用点数高低」的口径", async () => {
    const { wrapper } = await mountPage();
    const text = wrapper.text();
    expect(text).toContain("位置分不用点数高低");
    expect(text).toContain("250 日均线");
    expect(text).toContain("全部样本");
    expect(text).toContain("比历史上");
  });
});
