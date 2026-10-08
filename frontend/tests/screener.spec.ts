import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

type ProgressPayload = { phase: string; done: number; total: number };

const mocks = vi.hoisted(() => ({
  getScreenOptions: vi.fn(),
  getScreenCondition: vi.fn(),
  saveScreenCondition: vi.fn(),
  screenStocks: vi.fn(),
  getBoards: vi.fn(),
  // 签名必须带回调参数，否则 mockImplementation 传 1 参函数会类型不匹配
  onScreenProgress: vi.fn((_cb?: (payload: ProgressPayload) => void) => () => {}),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    getScreenOptions: mocks.getScreenOptions,
    getScreenCondition: mocks.getScreenCondition,
    saveScreenCondition: mocks.saveScreenCondition,
    screenStocks: mocks.screenStocks,
    getBoards: mocks.getBoards,
  },
  onScreenProgress: mocks.onScreenProgress,
  errorMessage: (err: unknown) => String(err),
}));

import ScreenerView from "@/views/ScreenerView.vue";
import ConditionPanel from "@/components/screener/ConditionPanel.vue";
import RangeField from "@/components/screener/RangeField.vue";

function defaultCondition() {
  return {
    minTotalMV: 0, maxTotalMV: 0, minFloatMV: 0, maxFloatMV: 0,
    minPE: 0, maxPE: 0, minPB: 0, maxPB: 0, minROE: 0, maxROE: 0,
    minTurnover: 0, maxTurnover: 0, minPctChg: 0, maxPctChg: 0,
    minVolumeRatio: 0, maxVolumeRatio: 0, minAmount: 0, maxAmount: 0,
    excludeST: true, markets: [], hasQuoteOnly: true,
    boardFilters: [], maConditions: [],
    enableDrawdown: false, drawdownDays: 30, minDrawdown: 0, maxDrawdown: 0,
    limit: 0,
  };
}

function options() {
  return {
    categories: [
      { key: "申万一级", name: "申万一级", count: 28 },
      { key: "概念", name: "概念", count: 978 },
    ],
    markets: [
      { key: "sh-main", name: "沪市主板", group: "主板", count: 0 },
      { key: "chinext", name: "创业板", group: "创业板", count: 0 },
    ],
    unsupported: [
      "市销率 PS：free-stockdb 的日K没有市销率字段",
      "季报增长：本地数据集不含财务报表科目",
    ],
    defaults: defaultCondition(),
  };
}

function screenResult(overrides: Record<string, unknown> = {}) {
  return {
    tradeDate: 20260930,
    scanned: 7563,
    snapshotPassed: 550,
    boardPassed: 38,
    techPassed: 38,
    matched: 2,
    historyEvaluated: false,
    elapsed: "20ms",
    notes: [],
    items: [
      {
        code: "000423", name: "东阿阿胶", market: "sz-main", marketCN: "深市主板",
        close: 45.95, pctChg: 1.57, amount: 1e8, turnover: 1.2, volumeRatio: 1.1,
        peTTM: 16.47, pb: 3.16, roe: 19.19, totalMV: 294e8, floatMV: 290e8,
        isST: false, boards: ["医药生物"], drawdownPct: 0, maValues: [],
      },
      {
        code: "600276", name: "恒瑞医药", market: "sh-main", marketCN: "沪市主板",
        close: 50.1, pctChg: -0.9, amount: 2e8, turnover: 0.8, volumeRatio: 0.9,
        peTTM: 60.2, pb: 7.1, roe: 11.8, totalMV: 3200e8, floatMV: 3100e8,
        isST: false, boards: ["医药生物", "创新药"], drawdownPct: 0, maValues: [],
      },
    ],
    ...overrides,
  };
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", component: { template: "<div />" } },
      { path: "/screener", component: ScreenerView },
      { path: "/kline", component: { template: "<div />" } },
    ],
  });
  await router.push("/screener");
  await router.isReady();

  const wrapper = mount(ScreenerView, { global: { plugins: [router] } });
  await vi.waitFor(() => expect(mocks.getScreenOptions).toHaveBeenCalled());
  await nextTick();
  await nextTick();
  return { wrapper, router };
}

/** 找到写着指定文案的按钮 */
function button(wrapper: ReturnType<typeof mount>, text: string) {
  return wrapper.findAll("button").find((b) => b.text().includes(text));
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getScreenOptions.mockResolvedValue(options());
  mocks.getScreenCondition.mockResolvedValue({ condition: defaultCondition(), saved: false });
  mocks.saveScreenCondition.mockResolvedValue(undefined);
  mocks.screenStocks.mockResolvedValue(screenResult());
  mocks.getBoards.mockResolvedValue([
    { code: "801150.SL", name: "医药生物", category: "申万一级", type: "sw_1", source: "sw", count: 477, symbols: [] },
    { code: "801780.SL", name: "银行", category: "申万一级", type: "sw_1", source: "sw", count: 42, symbols: [] },
  ]);
  mocks.onScreenProgress.mockReturnValue(() => {});
});

describe("条件选股页面", () => {
  it("加载元信息并默认排除 ST", async () => {
    const { wrapper } = await mountPage();
    expect(mocks.getScreenOptions).toHaveBeenCalled();

    const text = wrapper.text();
    expect(text).toContain("筛选条件");
    expect(text).toContain("基础条件");
    expect(text).toContain("行业 / 概念板块");
    expect(text).toContain("技术形态");

    // 默认勾选「排除 ST」与「仅有行情」：按标签文字定位，不依赖 DOM 顺序
    for (const label of ["排除 ST", "仅保留有行情的标的"]) {
      const row = wrapper.findAll("label").find((l) => l.text().includes(label));
      expect(row, `找不到「${label}」开关`).toBeTruthy();
      expect((row!.find('input[type="checkbox"]').element as HTMLInputElement).checked).toBe(true);
    }
  });

  it("不支持的条件要如实列出，不能静默隐藏", async () => {
    const { wrapper } = await mountPage();

    // 折叠状态也要能看到有这一节
    expect(wrapper.text()).toContain("暂不支持的条件");
    expect(wrapper.text()).toContain("2 项");

    // 展开后列出原因
    await button(wrapper, "暂不支持的条件")!.trigger("click");
    await nextTick();
    expect(wrapper.text()).toContain("市销率 PS");
    expect(wrapper.text()).toContain("季报增长");
  });

  it("没有结果时给出引导，执行后展示统计与表格", async () => {
    const { wrapper } = await mountPage();
    expect(wrapper.text()).toContain("设置条件后开始筛选");

    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(mocks.screenStocks).toHaveBeenCalled());
    await nextTick();
    await nextTick();

    const text = wrapper.text();
    // 分层统计要让用户看到条件是怎么一层层收窄的
    expect(text).toContain("扫描");
    expect(text).toContain("7563");
    expect(text).toContain("基础条件后");
    expect(text).toContain("550");
    expect(text).toContain("板块条件后");
    expect(text).toContain("38");
    expect(text).toContain("东阿阿胶");
    expect(text).toContain("恒瑞医药");
  });

  it("提交给后端的是当前界面上的条件", async () => {
    const { wrapper } = await mountPage();

    // 勾选「沪市主板」
    await button(wrapper, "沪市主板")!.trigger("click");
    await nextTick();
    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(mocks.screenStocks).toHaveBeenCalled());

    const submitted = mocks.screenStocks.mock.calls[0][0];
    expect(submitted.markets).toEqual(["sh-main"]);
    expect(submitted.excludeST).toBe(true);
  });

  it("区间条件勾选后才提交数值", async () => {
    const { wrapper } = await mountPage();

    // 直接按组件的 label 属性定位，比遍历 DOM 稳
    const peField = wrapper
      .findAllComponents(RangeField)
      .find((f) => f.props("label") === "市盈率");
    expect(peField, "找不到市盈率区间输入").toBeTruthy();

    // 未勾选时不该有限制值
    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(mocks.screenStocks).toHaveBeenCalled());
    expect(mocks.screenStocks.mock.calls[0][0].maxPE).toBe(0);

    mocks.screenStocks.mockClear();

    // 勾选并填上限（RangeField 监听的是 change，不是 input）
    await peField!.find('input[type="checkbox"]').setValue(true);
    await nextTick();
    const numberInputs = peField!.findAll('input[inputmode="decimal"]');
    const maxInput = numberInputs[1].element as HTMLInputElement;
    maxInput.value = "30";
    await numberInputs[1].trigger("change");
    await nextTick();

    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(mocks.screenStocks).toHaveBeenCalled());
    expect(mocks.screenStocks.mock.calls[0][0].maxPE).toBe(30);
  });

  it("板块条件可以添加、选板块、删除", async () => {
    const { wrapper } = await mountPage();

    // 初始只有 0 条
    expect(wrapper.text()).toContain("添加板块条件");
    await button(wrapper, "添加板块条件")!.trigger("click");
    await nextTick();

    // 出现了板块选择器，展开后拉到板块列表
    await vi.waitFor(() => expect(mocks.getBoards).toHaveBeenCalledWith("申万一级"));
    await button(wrapper, "选择板块")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("医药生物"));
    await nextTick();

    // 选中「医药生物」
    const boardButton = wrapper
      .findAll("button")
      .find((b) => b.text().includes("医药生物") && b.text().includes("477"));
    expect(boardButton).toBeTruthy();
    await boardButton!.trigger("click");
    await nextTick();

    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(mocks.screenStocks).toHaveBeenCalled());

    const submitted = mocks.screenStocks.mock.calls[0][0];
    expect(submitted.boardFilters).toHaveLength(1);
    expect(submitted.boardFilters[0].category).toBe("申万一级");
    expect(submitted.boardFilters[0].names).toEqual(["医药生物"]);
    expect(submitted.boardFilters[0].mode).toBe("any");
  });

  it("板块条件未选板块时不参与筛选", async () => {
    const { wrapper } = await mountPage();

    await button(wrapper, "添加板块条件")!.trigger("click");
    await nextTick();
    expect(wrapper.text()).toContain("未选择时这条条件不生效");

    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(mocks.screenStocks).toHaveBeenCalled());
    // 空的条件仍然提交，但后端会因为 names 为空而忽略它
    expect(mocks.screenStocks.mock.calls[0][0].boardFilters[0].names).toEqual([]);
  });

  it("结果可以按列排序，默认按代码升序", async () => {
    const { wrapper } = await mountPage();
    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("东阿阿胶"));
    await nextTick();

    const codes = () =>
      wrapper.findAll("tbody tr").map((r) => r.findAll("td")[0].text());
    expect(codes()[0]).toBe("000423"); // 代码升序

    // 点「ROE」按数值降序
    const roeHeader = wrapper.findAll("th").find((th) => th.text().includes("ROE"));
    await roeHeader!.trigger("click");
    await nextTick();
    expect(codes()[0]).toBe("000423"); // ROE 19.19 > 11.8

    await roeHeader!.trigger("click");
    await nextTick();
    expect(codes()[0]).toBe("600276"); // 升序后恒瑞在前
  });

  it("点击结果行跳转到 K 线页面", async () => {
    const { wrapper, router } = await mountPage();
    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("恒瑞医药"));
    await nextTick();

    const row = wrapper.findAll("tbody tr").find((r) => r.text().includes("恒瑞医药"));
    await row!.trigger("click");
    await vi.waitFor(() => expect(router.currentRoute.value.path).toBe("/kline"));
    expect(router.currentRoute.value.query.code).toBe("600276");
  });

  it("显示后端返回的提示（例如板块名写错）", async () => {
    mocks.screenStocks.mockResolvedValue(
      screenResult({ notes: ["以下板块没有找到，已忽略：申万一级 / 这个不存在"], matched: 0, items: [] }),
    );

    const { wrapper } = await mountPage();
    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("没有找到"));
    await nextTick();

    expect(wrapper.text()).toContain("这个不存在");
    expect(wrapper.text()).toContain("没有符合条件的股票");
  });

  it("条件改动会自动保存到配置文件", async () => {
    vi.useFakeTimers();
    try {
      const { wrapper } = await mountPage();

      await button(wrapper, "沪市主板")!.trigger("click");
      await nextTick();

      // 防抖 500ms
      await vi.advanceTimersByTimeAsync(600);
      expect(mocks.saveScreenCondition).toHaveBeenCalled();

      const saved = mocks.saveScreenCondition.mock.calls.at(-1)![0];
      expect(saved.markets).toEqual(["sh-main"]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("启动时读回上次保存的条件", async () => {
    mocks.getScreenCondition.mockResolvedValue({
      condition: {
        ...defaultCondition(),
        minTotalMV: 100,
        maxPE: 30,
        markets: ["chinext"],
        boardFilters: [{ category: "概念", names: ["创新药"], mode: "any" }],
      },
      saved: true,
    });

    const { wrapper } = await mountPage();
    await nextTick();
    await nextTick();

    expect(wrapper.text()).toContain("已保存");
    expect(wrapper.text()).toContain("创新药");

    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(mocks.screenStocks).toHaveBeenCalled());
    const submitted = mocks.screenStocks.mock.calls[0][0];
    expect(submitted.minTotalMV).toBe(100);
    expect(submitted.maxPE).toBe(30);
    expect(submitted.markets).toEqual(["chinext"]);
  });

  it("重置会清空全部条件", async () => {
    const { wrapper } = await mountPage();

    await button(wrapper, "沪市主板")!.trigger("click");
    await nextTick();

    await button(wrapper, "重置")!.trigger("click");
    await nextTick();

    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(mocks.screenStocks).toHaveBeenCalled());
    expect(mocks.screenStocks.mock.calls[0][0].markets).toEqual([]);
  });

  it("技术层筛选时显示进度条，完成后消失", async () => {
    // 捕获后端进度事件的回调
    let pushProgress: ((payload: ProgressPayload) => void) | null = null;
    mocks.onScreenProgress.mockImplementation((cb?: (payload: ProgressPayload) => void) => {
      pushProgress = cb ?? null;
      return () => {};
    });

    // 让筛选一直挂着，好在中途推进度
    let finish: ((value: unknown) => void) | null = null;
    mocks.screenStocks.mockReturnValue(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );

    const { wrapper } = await mountPage();
    await button(wrapper, "开始筛选")!.trigger("click");
    await nextTick();
    expect(wrapper.text()).toContain("筛选中…");

    // 后端开始逐只拉日线
    pushProgress!({ phase: "history", done: 100, total: 457 });
    await nextTick();
    await nextTick();

    expect(wrapper.text()).toContain("正在拉取日线并计算技术条件");
    expect(wrapper.text()).toContain("100 / 457");

    // 进度条宽度应反映进度
    const bar = wrapper.find(".bg-primary.h-full");
    expect(bar.exists()).toBe(true);
    expect(bar.attributes("style")).toContain("21.8");

    // 筛选结束，进度条消失、结果出现
    finish!(screenResult());
    await vi.waitFor(() => expect(wrapper.text()).toContain("东阿阿胶"));
    await nextTick();
    expect(wrapper.text()).not.toContain("100 / 457");
  });

  it("筛选失败时给出错误与重试", async () => {
    mocks.screenStocks.mockRejectedValue(new Error("数据源不可用"));
    const { wrapper } = await mountPage();

    await button(wrapper, "开始筛选")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("筛选失败"));
    expect(wrapper.text()).toContain("数据源不可用");
    expect(button(wrapper, "重试")).toBeTruthy();
  });
});
