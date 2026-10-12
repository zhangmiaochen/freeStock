import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, nextTick } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

import type { BoardPeriodStat, BoardPeriodWindow, BoardQuote, BoardQuoteResult } from "@/lib/backend";

const mocks = vi.hoisted(() => ({
  getBoardCategories: vi.fn(),
  getBoardQuotes: vi.fn(),
  getBoardMembers: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    getBoardCategories: mocks.getBoardCategories,
    getBoardQuotes: mocks.getBoardQuotes,
    getBoardMembers: mocks.getBoardMembers,
  },
  errorMessage: (err: unknown) => String(err),
}));

import BoardsView from "@/views/BoardsView.vue";

/** 界面默认请求的周期集合（与后端 DefaultBoardPeriods 一致） */
const PERIODS = [5, 10, 15, 30, 60];

function member(code: string, name: string, pctChg: number) {
  return { code, name, close: 10, pctChg, amount: 1e8, floatMV: 1e10 };
}

function periodStat(days: number, pctChg: number, quotedCount = 100): BoardPeriodStat {
  return {
    days,
    pctChg,
    pctChgEqual: pctChg - 0.5,
    upCount: pctChg >= 0 ? 70 : 30,
    downCount: pctChg >= 0 ? 30 : 70,
    flatCount: 0,
    quotedCount,
    memberCount: 100,
    leader: member("600000", "浦发银行", pctChg),
    laggard: member("600001", "邯郸钢铁", -pctChg),
  } as BoardPeriodStat;
}

function windows(): BoardPeriodWindow[] {
  return PERIODS.map((days, index) => ({
    days,
    label: `${days}日`,
    baseDate: 20260901 - index,
    startDate: 20260902 - index,
    endDate: 20260930,
    available: true,
    message: "",
  })) as BoardPeriodWindow[];
}

function quote(overrides: Partial<BoardQuote> & { code: string; name: string }): BoardQuote {
  return {
    category: "申万一级",
    type: "sw_1",
    memberCount: 100,
    quotedCount: 100,
    pctChg: 1,
    pctChgEqual: 1,
    amount: 1e10,
    turnover: 1,
    floatMV: 1e12,
    upCount: 50,
    downCount: 50,
    flatCount: 0,
    leader: member("600000", "浦发银行", 5),
    laggard: member("600001", "邯郸钢铁", -5),
    periods: PERIODS.map((days) => periodStat(days, 1)),
    ...overrides,
  } as BoardQuote;
}

function result(overrides: Partial<BoardQuoteResult> = {}): BoardQuoteResult {
  return {
    category: "申万一级",
    tradeDate: 20260930,
    boardCount: 3,
    boards: [
      quote({
        code: "BK1",
        name: "医药生物",
        pctChg: 2.73,
        pctChgEqual: 2.02,
        amount: 1369.6e8,
        upCount: 401,
        downCount: 66,
        periods: [periodStat(5, 3.5), periodStat(10, 5.2), periodStat(15, 6.1), periodStat(30, 9.4), periodStat(60, 12.8)],
      }),
      quote({
        code: "BK2",
        name: "银行",
        pctChg: 1.28,
        pctChgEqual: 1.46,
        amount: 265.1e8,
        upCount: 42,
        downCount: 0,
        periods: [periodStat(5, 0.4), periodStat(10, -0.2), periodStat(15, 0.9), periodStat(30, 2.1), periodStat(60, 3.3)],
      }),
      quote({
        code: "BK3",
        name: "电子",
        pctChg: -2.18,
        pctChgEqual: -1.9,
        amount: 2000e8,
        upCount: 100,
        downCount: 380,
        periods: [periodStat(5, -1.1), periodStat(10, -3.4), periodStat(15, -4.2), periodStat(30, -6.5), periodStat(60, -8.9)],
      }),
    ],
    periods: PERIODS,
    windows: windows(),
    periodNotice: "",
    upBoards: 2,
    downBoards: 1,
    flatBoards: 0,
    totalAmount: 3634.7e8,
    overlapping: false,
    overlapRatio: 0.67,
    elapsed: "830ms",
    ...overrides,
  } as BoardQuoteResult;
}

const BoardHeatmapStub = defineComponent({
  name: "BoardHeatmap",
  props: {
    boards: { type: Array, default: () => [] },
    limit: { type: Number, default: 60 },
    periodDays: { type: Number, default: 5 },
    windows: { type: Array, default: () => [] },
  },
  emits: ["select"],
  template: '<div class="heatmap-stub" />',
});

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", component: { template: "<div />" } },
      { path: "/boards", component: BoardsView },
      { path: "/kline", component: { template: "<div />" } },
    ],
  });
  await router.push("/boards");
  await router.isReady();

  const wrapper = mount(BoardsView, {
    global: { plugins: [router], stubs: { BoardHeatmap: BoardHeatmapStub } },
  });
  await vi.waitFor(() => expect(mocks.getBoardQuotes).toHaveBeenCalled());
  await nextTick();
  await nextTick();
  return { wrapper, router };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getBoardCategories.mockResolvedValue([
    { key: "申万一级", name: "申万一级", count: 28 },
    { key: "概念", name: "概念", count: 978 },
  ]);
  mocks.getBoardQuotes.mockResolvedValue(result());
  mocks.getBoardMembers.mockResolvedValue([
    { code: "688185", name: "康希诺", close: 102.64, pctChg: 20, amount: 10.65e8, floatMV: 1e10 },
    { code: "300981", name: "中红医疗", close: 20.3, pctChg: 13.41, amount: 7.39e8, floatMV: 1e10 },
    { code: "600001", name: "某下跌股", close: 5, pctChg: -3.2, amount: 1e8, floatMV: 1e10 },
  ]);
});

describe("板块行情页面", () => {
  it("加载分类并默认选中申万一级，展示统计与表格", async () => {
    const { wrapper } = await mountPage();

    // 默认分类是申万一级（不是第一个碰巧的），默认周期是 5/10/15/30/60
    expect(mocks.getBoardQuotes).toHaveBeenCalledWith("申万一级", PERIODS, 0);

    const text = wrapper.text();
    expect(text).toContain("申万一级");
    expect(text).toContain("概念");
    expect(text).toContain("978"); // 概念板块数徽标
    // 统计条
    expect(text).toContain("上涨板块");
    expect(text).toContain("总成交额");
    expect(text).toContain("2026-09-30");
    // 表格内容
    expect(text).toContain("医药生物");
    expect(text).toContain("电子");
    expect(text).toContain("+2.73%");
    expect(text).toContain("-2.18%");
    // 领涨股
    expect(text).toContain("浦发银行");
  });

  it("热力图拿到的是全量板块，由组件自己按成交额裁剪", async () => {
    const { wrapper } = await mountPage();

    const heatmap = wrapper.findComponent(BoardHeatmapStub);
    expect(heatmap.exists()).toBe(true);
    expect(heatmap.props("boards")).toHaveLength(3);
    // 板块数不多时全画
    expect(heatmap.props("limit")).toBe(3);
  });

  it("板块数很多时热力图只显示前 60 个", async () => {
    const many = Array.from({ length: 200 }, (_, i) =>
      quote({ code: `BK${i}`, name: `板块${i}`, amount: (200 - i) * 1e8 }),
    );
    mocks.getBoardQuotes.mockResolvedValue(result({ boards: many, boardCount: 200 }));

    const { wrapper } = await mountPage();
    expect(wrapper.findComponent(BoardHeatmapStub).props("limit")).toBe(60);
  });

  it("默认按选中周期（5 日）从高到低排序，点表头可切换", async () => {
    const { wrapper } = await mountPage();

    const rows = () => wrapper.findAll("tbody tr").map((r) => r.text());
    expect(rows()[0]).toContain("医药生物");
    expect(rows()[2]).toContain("电子");

    // 点当前排序列「5日」的表头切换为升序
    const header = wrapper.findAll("th").find((th) => th.text().includes("5日"));
    expect(header).toBeTruthy();
    await header!.trigger("click");
    await nextTick();
    expect(rows()[0]).toContain("电子");

    // 再点一次回到降序
    await header!.trigger("click");
    await nextTick();
    expect(rows()[0]).toContain("医药生物");

    // 点「当日」是按另一个口径重排：默认降序（点新列不会直接变升序）
    const today = wrapper.findAll("th").find((th) => th.text().includes("当日"));
    await today!.trigger("click");
    await nextTick();
    expect(rows()[0]).toContain("医药生物");
    expect(rows()[2]).toContain("电子");
  });

  it("表格并列显示各周期区间涨跌幅，默认高亮 5 日", async () => {
    const { wrapper } = await mountPage();

    const text = wrapper.text();
    for (const days of PERIODS) expect(text).toContain(`${days}日`);
    // 区间涨跌幅按月列出来（医药生物的 5/10/60 日）
    expect(text).toContain("+3.50%");
    expect(text).toContain("+5.20%");
    expect(text).toContain("+12.80%");
    // 当日与区间是两套口径，两个数都要在
    expect(text).toContain("+2.73%");

    // 热力图按选中的周期着色
    expect(wrapper.findComponent(BoardHeatmapStub).props("periodDays")).toBe(5);
    // 默认排序也跟着选中的周期：5 日涨幅最高的排最前
    expect(wrapper.findAll("tbody tr")[0].text()).toContain("医药生物");
  });

  it("切换周期只换显示口径，不重新请求后端", async () => {
    const { wrapper } = await mountPage();
    mocks.getBoardQuotes.mockClear();

    const button = wrapper.findAll("button").find((b) => b.text().trim() === "60日");
    expect(button).toBeTruthy();
    await button!.trigger("click");
    await nextTick();

    // 后端一次就把所有周期算好了，切周期不该再请求（否则每点一下都要等几秒）
    expect(mocks.getBoardQuotes).not.toHaveBeenCalled();
    expect(wrapper.findComponent(BoardHeatmapStub).props("periodDays")).toBe(60);
  });

  it("自定义周期会带上这个周期重新请求", async () => {
    const { wrapper } = await mountPage();
    mocks.getBoardQuotes.mockClear();

    const custom = wrapper.findAll("button").find((b) => b.text().trim() === "自定义");
    expect(custom).toBeTruthy();
    await custom!.trigger("click");
    await nextTick();

    // 输入 7 个交易日并应用
    await wrapper.find('input[type="number"]').setValue("7");
    const apply = wrapper.findAll("button").find((b) => b.text().trim() === "应用");
    await apply!.trigger("click");
    await vi.waitFor(() => expect(mocks.getBoardQuotes).toHaveBeenCalled());

    expect(mocks.getBoardQuotes).toHaveBeenCalledWith("申万一级", [5, 7, 10, 15, 30, 60], 0);
    expect(wrapper.text()).toContain("自定义 7日");
  });

  it("自定义周期填了当日或非法值时给出提示且不请求", async () => {
    const { wrapper } = await mountPage();
    mocks.getBoardQuotes.mockClear();

    const custom = wrapper.findAll("button").find((b) => b.text().trim() === "自定义");
    await custom!.trigger("click");
    await nextTick();

    await wrapper.find('input[type="number"]').setValue("1");
    const apply = wrapper.findAll("button").find((b) => b.text().trim() === "应用");
    await apply!.trigger("click");
    await nextTick();

    expect(mocks.getBoardQuotes).not.toHaveBeenCalled();
    // 1 日就是「当日」，必须说清楚，否则用户以为界面坏了
    expect(wrapper.text()).toContain("1 日就是「当日」");
  });

  it("某个周期算不出来时显示「—」而不是 0%", async () => {
    mocks.getBoardQuotes.mockResolvedValue(
      result({
        boards: [
          quote({
            code: "BK1",
            name: "次新板块",
            pctChg: 1.5,
            // 成分股在区间起点都还没有行情 → 区间算不出来
            periods: [],
          }),
        ],
      }),
    );

    const { wrapper } = await mountPage();
    const row = wrapper.findAll("tbody tr").find((r) => r.text().includes("次新板块"));
    expect(row).toBeTruthy();

    const cells = row!.findAll("td").map((c) => c.text());
    // 当日列有数，区间列全是「—」
    expect(cells).toContain("+1.50%");
    expect(cells.filter((text) => text === "—").length).toBeGreaterThanOrEqual(PERIODS.length);
    // 0% 会被读成"没涨没跌"，不能出现
    expect(row!.text()).not.toContain("+0.00%");
  });

  it("区间行情整体不可用时说明原因，当日行情照常显示", async () => {
    mocks.getBoardQuotes.mockResolvedValue(
      result({ periods: [], windows: [], periodNotice: "历史日线还没同步" }),
    );

    const { wrapper } = await mountPage();
    const text = wrapper.text();

    expect(text).toContain("区间行情暂时算不出来");
    expect(text).toContain("历史日线还没同步");
    // 当日行情不受影响
    expect(text).toContain("+2.73%");
    // 表格里没有区间列（周期选择器上的按钮不算）
    const headers = wrapper.findAll("th").map((th) => th.text()).join(" ");
    expect(headers).not.toContain("5日");
    expect(headers).not.toContain("60日");
  });

  it("统计条给出所选周期的板块涨跌家数", async () => {
    const { wrapper } = await mountPage();
    // 5 日：医药生物 +3.5、银行 +0.4、电子 -1.1 → 2 涨 1 跌
    const text = wrapper.text();
    expect(text).toContain("5日上涨板块");
    expect(text).toContain("区间涨跌幅按");
    // 不复权与最新市值加权这两点必须写出来
    expect(text).toContain("不复权");
  });

  it("点板块行展开成分股，可按涨跌过滤并跳到 K 线", async () => {
    const { wrapper } = await mountPage();

    const row = wrapper.findAll("tbody tr").find((r) => r.text().includes("医药生物"));
    expect(row).toBeTruthy();
    await row!.trigger("click");

    await vi.waitFor(() => expect(mocks.getBoardMembers).toHaveBeenCalledWith("BK1"));
    await nextTick();
    await nextTick();

    const text = wrapper.text();
    expect(text).toContain("康希诺");
    expect(text).toContain("中红医疗");
    expect(text).toContain("某下跌股");
    // 成分统计
    expect(text).toContain("成分 3 只");

    // 只看上涨
    const upButton = wrapper.findAll("button").find((b) => b.text().trim() === "仅上涨");
    await upButton!.trigger("click");
    await nextTick();
    const afterFilter = wrapper.text();
    expect(afterFilter).toContain("康希诺");
    expect(afterFilter).not.toContain("某下跌股");
  });

  it("点热力图里的板块也会展开成分股", async () => {
    const { wrapper } = await mountPage();

    const heatmap = wrapper.findComponent(BoardHeatmapStub);
    heatmap.vm.$emit("select", quote({ code: "BK2", name: "银行" }));
    await vi.waitFor(() => expect(mocks.getBoardMembers).toHaveBeenCalledWith("BK2"));
    await nextTick();

    expect(wrapper.text()).toContain("银行");
  });

  it("重叠分类会明确说明成交额不能当市场总量", async () => {
    mocks.getBoardQuotes.mockResolvedValue(
      result({
        category: "概念",
        overlapping: true,
        overlapRatio: 10.57,
        totalAmount: 252350.2e8,
      }),
    );

    const { wrapper } = await mountPage();
    const text = wrapper.text();

    // 措辞必须变，否则 25 万亿会被当成市场总成交额
    expect(text).toContain("成分成交额合计（有重叠）");
    expect(text).not.toContain("总成交额 25");
    expect(text).toContain("多对多分类");
    expect(text).toContain("10.57");
    expect(text).toContain("不是市场总成交额");
  });

  it("成分覆盖不全时标出实际有行情的数量", async () => {
    mocks.getBoardQuotes.mockResolvedValue(
      result({
        boards: [
          quote({ code: "BK1", name: "某板块", memberCount: 488, quotedCount: 486, pctChg: 1.5 }),
        ],
      }),
    );

    const { wrapper } = await mountPage();
    // 表格里要能看到 486/488，否则用户会奇怪涨跌家数为什么加起来不到 488
    expect(wrapper.text()).toContain("486/488");
  });

  it("切换分类会重新拉取行情", async () => {
    const { wrapper } = await mountPage();
    mocks.getBoardQuotes.mockClear();

    const concept = wrapper.findAll("button").find((b) => b.text().includes("概念"));
    await concept!.trigger("click");
    await vi.waitFor(() => expect(mocks.getBoardQuotes).toHaveBeenCalledWith("概念", PERIODS, 0));
  });

  it("读取失败时给出错误与重试", async () => {
    mocks.getBoardQuotes.mockRejectedValue(new Error("数据源不可用"));
    const { wrapper } = await mountPage();

    await vi.waitFor(() => expect(wrapper.text()).toContain("板块行情读取失败"));
    expect(wrapper.text()).toContain("数据源不可用");
    expect(wrapper.findAll("button").some((b) => b.text().includes("重试"))).toBe(true);
  });
});
