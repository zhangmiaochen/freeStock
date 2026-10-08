import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, nextTick } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

import type { KlineBundle } from "@/lib/backend";

// ---- 假的 backend --------------------------------------------------------
const mocks = vi.hoisted(() => ({
  searchStocks: vi.fn(),
  getKline: vi.fn(),
  getKlinePrefs: vi.fn(),
  saveKlinePrefs: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    searchStocks: mocks.searchStocks,
    getKline: mocks.getKline,
    getKlinePrefs: mocks.getKlinePrefs,
    saveKlinePrefs: mocks.saveKlinePrefs,
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

// ---- 测试数据 ------------------------------------------------------------
/** 生成 count 个工作日（倒推），返回 YYYYMMDD */
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

const DATES = businessDays(300);
const CANDLES = DATES.map((date, i) => {
  const base = 100 + Math.sin(i / 12) * 8 + i * 0.15;
  return {
    date,
    open: Number(base.toFixed(2)),
    high: Number((base + 1.2).toFixed(2)),
    low: Number((base - 1.1).toFixed(2)),
    close: Number((base + 0.4).toFixed(2)),
    volume: 1_000_000 + i * 1000,
    amount: 1e8 + i * 1000,
  };
});

const BUNDLE = {
  code: "600519",
  name: "贵州茅台",
  adjust: "qfq",
  profile: {
    code: "600519",
    name: "贵州茅台",
    market: "sh-main",
    marketCN: "沪市主板",
    tradeDate: 20260930,
    open: 1239.53,
    high: 1268,
    low: 1236.05,
    close: 1258.62,
    preClose: 1235.58,
    pctChg: 1.86,
    amplitude: 2.59,
    volume: 3833100,
    amount: 4797250000,
    turnover: 0.31,
    volRatio: 1.36,
    peTTM: 19.32,
    pb: 6.26,
    totalMV: 1573378000000,
    floatMV: 1573378000000,
    totalShare: 1250081601,
    floatShare: 1250081601,
    isST: false,
    dividendYield: 4.13,
    dividendTTM: 51.9812,
    latestDiv: 23.882,
    latestDivDate: 20260619,
    dividendCount: 31,
    // 由 PE/PB 推导的 TTM 财务指标
    roe: 32.4017,
    eps: 65.146,
    bps: 201.0575,
    netProfitTTM: 81440000000,
    netAssets: 251340000000,
    // 资金流
    mainNetToday: 539214462,
    mainNet5D: 121900000,
    mainNet20D: -1268864000,
    moneyFlowDay: 20260930,
  },
  candles: CANDLES,
  dividends: [{ date: 20260619, div: 23.882, give: 0, trans: 0, mult: 1.019, cum: 5.694 }],
  // 资金流只覆盖最后 120 天（真实数据里资金流通常比 K 线短）
  moneyFlows: CANDLES.slice(-120).map((c, i) => ({
    date: c.date,
    mainNet: (i % 2 === 0 ? 1 : -1) * 1e8,
    jumboNet: (i % 2 === 0 ? 1 : -1) * 6e7,
    bigNet: (i % 2 === 0 ? 1 : -1) * 4e7,
    midNet: 0,
    smallNet: 0,
  })),
  notes: ["本地数据集不含财务报表。"],
} as unknown as KlineBundle;

const KlineChartStub = defineComponent({
  name: "KlineChart",
  props: {
    candles: { type: Array, default: () => [] },
    maConfigs: { type: Array, default: () => [] },
    period: { type: String, default: "day" },
    showBoll: Boolean,
    showMacd: Boolean,
    showVolume: Boolean,
    showFlow: Boolean,
    moneyFlows: { type: Array, default: () => [] },
    bollConfig: { type: Object, default: () => ({}) },
    macdConfig: { type: Object, default: () => ({}) },
    resetKey: String,
  },
  template: '<div class="kline-chart-stub" />',
});

async function mountPage(query: Record<string, string> = {}) {
  const pinia = createPinia();
  setActivePinia(pinia);

  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", component: { template: "<div />" } },
      { path: "/kline", component: KlineView },
    ],
  });
  await router.push({ path: "/kline", query });
  await router.isReady();

  const wrapper = mount(KlineView, {
    global: { plugins: [pinia, router], stubs: { KlineChart: KlineChartStub } },
  });
  // 组件挂载后会先读配置再取数，等这个异步链走完
  await vi.waitFor(() => expect(mocks.getKlinePrefs).toHaveBeenCalled());
  await nextTick();
  return { wrapper, router };
}

/** 保存是防抖的，等它落盘 */
async function waitForSave() {
  await new Promise((resolve) => setTimeout(resolve, 550));
  await nextTick();
}

/** 取出最后一次保存的偏好 */
function lastSavedPrefs() {
  const calls = mocks.saveKlinePrefs.mock.calls;
  expect(calls.length, "没有调用保存").toBeGreaterThan(0);
  return calls[calls.length - 1][0];
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  mocks.getKline.mockResolvedValue(BUNDLE);
  mocks.getKlinePrefs.mockResolvedValue({
    prefs: null,
    saved: false,
    configPath: "C:\\Users\\x\\AppData\\Roaming\\FreeStock\\config.json",
  });
  mocks.saveKlinePrefs.mockImplementation(async (prefs: unknown) => ({
    prefs,
    saved: true,
    configPath: "C:\\Users\\x\\AppData\\Roaming\\FreeStock\\config.json",
  }));
  mocks.searchStocks.mockResolvedValue([
    {
      code: "600519",
      name: "贵州茅台",
      market: "sh-main",
      marketCN: "沪市主板",
      pinyin: "guizhoumaotai",
      initials: "gzmt",
      close: 1258.62,
      pctChg: 1.86,
      matchOn: "initials",
    },
  ]);
});

describe("K 线页面", () => {
  it("未选股票时给出引导提示", async () => {
    const { wrapper } = await mountPage();
    expect(wrapper.text()).toContain("搜索一只股票开始看 K 线");
    expect(wrapper.text()).toContain("gzmt");
    expect(mocks.getKline).not.toHaveBeenCalled();
  });

  // 页面被 KeepAlive 缓存后，onMounted 不会再执行 ——
  // 如果加载逻辑只写在 onMounted 里，"从选股页点股票跳 K 线"就会永远显示上一只。
  // 所以这里必须验证路由参数变化能触发重新加载。
  it("路由参数变化时会加载新股票", async () => {
    const { router } = await mountPage({ code: "600519" });
    await vi.waitFor(() => {
      expect(mocks.getKline).toHaveBeenCalledWith("600519", "qfq", 1990);
    });

    mocks.getKline.mockClear();

    // 模拟从别的页面跳到另一只股票
    await router.push({ path: "/kline", query: { code: "300750" } });
    await vi.waitFor(() => {
      expect(mocks.getKline).toHaveBeenCalledWith("300750", "qfq", 1990);
    });
  });

  it("路由参数与当前股票相同时不重复请求", async () => {
    const { router } = await mountPage({ code: "600519" });
    await vi.waitFor(() => {
      expect(mocks.getKline).toHaveBeenCalledWith("600519", "qfq", 1990);
    });

    mocks.getKline.mockClear();
    // 推同一个 code 不该再拉一次
    await router.push({ path: "/kline", query: { code: "600519" } });
    await nextTick();
    await nextTick();
    expect(mocks.getKline).not.toHaveBeenCalled();
  });

  it("带 code 进入时自动加载并显示股票信息", async () => {
    const { wrapper } = await mountPage({ code: "600519" });

    await vi.waitFor(() => {
      expect(mocks.getKline).toHaveBeenCalledWith("600519", "qfq", 1990);
    });
    await nextTick();

    const text = wrapper.text();
    expect(text).toContain("贵州茅台");
    expect(text).toContain("沪市主板");
    expect(text).toContain("1258.62"); // 最新价
    expect(text).toContain("1.86%"); // 涨跌幅
    expect(text).toContain("总市值");
    expect(text).toContain("市盈(TTM)");
    expect(text).toContain("股息率");
    // 由行情推导的财务指标（非财报口径）
    expect(text).toContain("ROE(TTM)");
    expect(text).toContain("32.40%");
    expect(text).toContain("每股净资产");
    expect(text).toContain("净利润TTM");
    // 资金流
    expect(text).toContain("主力净流入");
    expect(text).toContain("20日主力");
    expect(text).toContain("本地数据集不含财务报表"); // notes

    const chart = wrapper.findComponent(KlineChartStub);
    expect(chart.exists()).toBe(true);
    expect(chart.props("candles")).toHaveLength(300);
    expect(chart.props("period")).toBe("day");
  });

  it("默认显示 5 条均线：5/20/60/120/250", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    const chart = wrapper.findComponent(KlineChartStub);
    const periods = (chart.props("maConfigs") as { period: number }[]).map((m) => m.period);
    expect(periods).toEqual([5, 20, 60, 120, 250]);
  });

  it("切换周期后传给图表的蜡烛变少（周/月/年逐级聚合）", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    const daily = wrapper.findComponent(KlineChartStub).props("candles") as unknown[];
    expect(daily).toHaveLength(300);

    const clickPeriod = async (label: string) => {
      const button = wrapper.findAll("button").find((b) => b.text().trim() === label);
      expect(button, `找不到「${label}」按钮`).toBeTruthy();
      await button!.trigger("click");
      await nextTick();
      await nextTick();
    };

    await clickPeriod("周");
    const weekly = wrapper.findComponent(KlineChartStub).props("candles") as unknown[];
    expect(weekly.length).toBeGreaterThan(0);
    expect(weekly.length).toBeLessThan(daily.length);
    expect(wrapper.findComponent(KlineChartStub).props("period")).toBe("week");

    await clickPeriod("月");
    const monthly = wrapper.findComponent(KlineChartStub).props("candles") as unknown[];
    expect(monthly.length).toBeLessThan(weekly.length);

    await clickPeriod("年");
    const yearly = wrapper.findComponent(KlineChartStub).props("candles") as unknown[];
    expect(yearly.length).toBeLessThanOrEqual(monthly.length);
  });

  it("可以开关布林线 / MACD / 能量辅助线", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    const chart = wrapper.findComponent(KlineChartStub);
    expect(chart.props("showBoll")).toBe(false);
    expect(chart.props("showMacd")).toBe(true);
    expect(chart.props("showVolume")).toBe(true);

    const toggle = async (label: string) => {
      const button = wrapper.findAll("button").find((b) => b.text().includes(label));
      await button!.trigger("click");
      await nextTick();
    };

    await toggle("布林线");
    expect(wrapper.findComponent(KlineChartStub).props("showBoll")).toBe(true);

    await toggle("MACD");
    expect(wrapper.findComponent(KlineChartStub).props("showMacd")).toBe(false);

    await toggle("能量辅助线");
    expect(wrapper.findComponent(KlineChartStub).props("showVolume")).toBe(false);
  });

  it("均线可以删除和新增，并持久化到配置文件", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    // 展开指标设置
    const panelButton = wrapper.findAll("button").find((b) => b.text().includes("指标设置"));
    await panelButton!.trigger("click");
    await nextTick();

    const before = wrapper.findComponent(KlineChartStub).props("maConfigs") as unknown[];
    expect(before).toHaveLength(5);

    // 删掉第一条
    const removeButton = wrapper.find('button[title="删除这条均线"]');
    expect(removeButton.exists()).toBe(true);
    await removeButton.trigger("click");
    await nextTick();

    const afterRemove = wrapper.findComponent(KlineChartStub).props("maConfigs") as { period: number }[];
    expect(afterRemove).toHaveLength(4);
    expect(afterRemove.map((m) => m.period)).not.toContain(5);

    // 新增一条
    const addButton = wrapper.findAll("button").find((b) => b.text().includes("添加均线"));
    await addButton!.trigger("click");
    await nextTick();
    expect((wrapper.findComponent(KlineChartStub).props("maConfigs") as unknown[])).toHaveLength(5);

    // 持久化走配置文件（具体断言见「均线增删也会写进配置文件」）
    await waitForSave();
    expect(lastSavedPrefs().maConfigs).toHaveLength(5);
  });

  it("切换复权方式会重新向后端取数", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(mocks.getKline).toHaveBeenCalledTimes(1));

    const hfq = wrapper.findAll("button").find((b) => b.text().trim() === "后复权");
    await hfq!.trigger("click");
    await vi.waitFor(() => expect(mocks.getKline).toHaveBeenCalledWith("600519", "hfq", 1990));
  });

  it("三种复权方式都在，默认前复权", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(mocks.getKline).toHaveBeenCalled());

    // 默认就是前复权
    expect(mocks.getKline).toHaveBeenCalledWith("600519", "qfq", 1990);

    for (const label of ["前复权", "后复权", "不复权"]) {
      const button = wrapper.findAll("button").find((b) => b.text().trim() === label);
      expect(button, `缺少「${label}」按钮`).toBeTruthy();
    }

    const none = wrapper.findAll("button").find((b) => b.text().trim() === "不复权");
    await none!.trigger("click");
    await vi.waitFor(() => expect(mocks.getKline).toHaveBeenCalledWith("600519", "none", 1990));
  });

  it("布林线与 MACD 的参数可以自定义并传给图表", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    const chart = () => wrapper.findComponent(KlineChartStub);
    // 默认参数
    expect(chart().props("bollConfig")).toEqual({ period: 20, mult: 2 });
    expect(chart().props("macdConfig")).toEqual({ fast: 12, slow: 26, signal: 9 });

    // 打开指标设置
    const panelButton = wrapper.findAll("button").find((b) => b.text().includes("指标设置"));
    await panelButton!.trigger("click");
    await nextTick();

    const editParam = async (param: string, value: string) => {
      const input = wrapper.find(`input[data-param="${param}"]`);
      expect(input.exists(), `找不到参数输入框 ${param}`).toBe(true);
      await input.setValue(value);
      await input.trigger("change");
      await nextTick();
    };

    await editParam("boll-period", "26");
    expect(chart().props("bollConfig")).toEqual({ period: 26, mult: 2 });

    await editParam("boll-mult", "2.5");
    expect(chart().props("bollConfig")).toEqual({ period: 26, mult: 2.5 });

    await editParam("macd-fast", "10");
    await editParam("macd-slow", "22");
    await editParam("macd-signal", "7");
    expect(chart().props("macdConfig")).toEqual({ fast: 10, slow: 22, signal: 7 });
  });

  it("MACD 慢线不大于快线，非法输入会被纠正", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    const panelButton = wrapper.findAll("button").find((b) => b.text().includes("指标设置"));
    await panelButton!.trigger("click");
    await nextTick();

    const fast = wrapper.find('input[data-param="macd-fast"]');
    await fast.setValue("30");
    await fast.trigger("change");
    await nextTick();

    const slow = wrapper.find('input[data-param="macd-slow"]');
    await slow.setValue("10");
    await slow.trigger("change");
    await nextTick();

    const config = wrapper.findComponent(KlineChartStub).props("macdConfig") as {
      fast: number;
      slow: number;
    };
    expect(config.fast).toBe(30);
    expect(config.slow).toBeGreaterThan(config.fast);
  });

  it("有资金流数据时出现「资金流」开关，数据传给图表且可开关", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    const chart = wrapper.findComponent(KlineChartStub);
    // 资金流序列原样传给图表（长度与后端返回一致，不随周期聚合丢弃）
    expect(chart.props("moneyFlows")).toHaveLength(120);
    expect(chart.props("showFlow")).toBe(true);

    const button = wrapper.findAll("button").find((b) => b.text().trim() === "资金流");
    expect(button, "缺少「资金流」开关").toBeTruthy();
    await button!.trigger("click");
    await nextTick();
    expect(wrapper.findComponent(KlineChartStub).props("showFlow")).toBe(false);
  });

  it("没有资金流数据时不显示「资金流」开关", async () => {
    mocks.getKline.mockResolvedValue({ ...BUNDLE, moneyFlows: [] } as unknown as KlineBundle);
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    expect(wrapper.findComponent(KlineChartStub).props("moneyFlows")).toHaveLength(0);
    const button = wrapper.findAll("button").find((b) => b.text().trim() === "资金流");
    expect(button, "没有数据时不该出现资金流开关").toBeFalsy();
  });

  it("布林线/MACD 参数会写进本地配置文件", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    const panelButton = wrapper.findAll("button").find((b) => b.text().includes("指标设置"));
    await panelButton!.trigger("click");
    await nextTick();

    const input = wrapper.find('input[data-param="boll-period"]');
    await input.setValue("34");
    await input.trigger("change");
    await waitForSave();

    const prefs = lastSavedPrefs();
    // 关键：走的是后端配置文件，不再是 localStorage
    expect(prefs.boll).toEqual({ period: 34, mult: 2 });
    expect(prefs.adjust).toBe("qfq");
    expect(prefs.period).toBe("day");
    expect(prefs.macd).toEqual({ fast: 12, slow: 26, signal: 9 });
    expect(localStorage.getItem("freestock.kline.prefs")).toBeNull();
  });

  it("启动时从配置文件读回指标参数", async () => {
    mocks.getKlinePrefs.mockResolvedValue({
      saved: true,
      configPath: "C:\\config.json",
      prefs: {
        period: "month",
        adjust: "hfq",
        showBoll: true,
        showMacd: false,
        showVolume: false,
        showFlow: false,
        boll: { period: 60, mult: 3 },
        macd: { fast: 8, slow: 17, signal: 5 },
        maConfigs: [
          { period: 10, color: "#f5c542", visible: true },
          { period: 30, color: "#ec4899", visible: false },
        ],
      },
    });

    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    const chart = wrapper.findComponent(KlineChartStub);
    expect(chart.props("period")).toBe("month");
    expect(chart.props("showBoll")).toBe(true);
    expect(chart.props("showMacd")).toBe(false);
    expect(chart.props("bollConfig")).toEqual({ period: 60, mult: 3 });
    expect(chart.props("macdConfig")).toEqual({ fast: 8, slow: 17, signal: 5 });

    const periods = (chart.props("maConfigs") as { period: number }[]).map((m) => m.period);
    expect(periods).toEqual([10, 30]);

    // 复权方式来自配置，取数时应当带上 hfq
    expect(mocks.getKline).toHaveBeenCalledWith("600519", "hfq", 1990);
  });

  it("均线增删也会写进配置文件", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    const panelButton = wrapper.findAll("button").find((b) => b.text().includes("指标设置"));
    await panelButton!.trigger("click");
    await nextTick();

    const removeButton = wrapper.find('button[title="删除这条均线"]');
    await removeButton.trigger("click");
    await waitForSave();

    const prefs = lastSavedPrefs();
    expect(prefs.maConfigs).toHaveLength(4);
    expect(prefs.maConfigs.map((m: { period: number }) => m.period)).not.toContain(5);
  });

  it("能显示设置保存在哪个文件", async () => {
    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.findComponent(KlineChartStub).exists()).toBe(true));

    const panelButton = wrapper.findAll("button").find((b) => b.text().includes("指标设置"));
    await panelButton!.trigger("click");
    await nextTick();

    const text = wrapper.text();
    expect(text).toContain("以上设置自动保存到");
    expect(text).toContain("config.json");
  });
});
