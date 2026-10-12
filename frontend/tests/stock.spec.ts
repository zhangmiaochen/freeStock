import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

import type {
  BoardCategoryInfo,
  BoardInfo,
  StockItem,
  StockUniverse,
} from "@/lib/backend";

// ---- 用假的 backend 替换 Wails 绑定 -------------------------------------
const mocks = vi.hoisted(() => ({
  getStockUniverse: vi.fn(),
  getBoardCategories: vi.fn(),
  getBoards: vi.fn(),
  searchStocks: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    getStockUniverse: mocks.getStockUniverse,
    getBoardCategories: mocks.getBoardCategories,
    getBoards: mocks.getBoards,
    searchStocks: mocks.searchStocks,
    getDataSources: vi.fn(),
    getCurrentDataSource: vi.fn(),
    testDataSource: vi.fn(),
    setDataSource: vi.fn(),
    clearCache: vi.fn(),
  },
  errorMessage: (err: unknown) => String(err),
  onScanProgress: () => () => {},
}));

import StockListView from "@/views/StockListView.vue";
import { useStockStore } from "@/stores/stock";

// ---- 测试数据（结构与 free-stockdb 真实返回一致） -----------------------
function stock(
  code: string,
  name: string,
  market: string,
  marketCN: string,
  close: number,
  pctChg: number,
  amount = 1e8,
): StockItem {
  return {
    code,
    name,
    market,
    marketCN,
    date: 20260930,
    close,
    pctChg,
    amount,
    turnover: 1.5,
    peTTM: 12,
    totalMV: 1e11,
    floatMV: 1e11,
    pb: 1.5,
    volRatio: 1.2,
    isST: false,
    hasQuote: true,
  };
}

const ALL_STOCKS: StockItem[] = [
  stock("600000", "浦发银行", "sh-main", "沪市主板", 10.5, 1.2),
  stock("600031", "三一重工", "sh-main", "沪市主板", 18.2, -0.8),
  stock("601398", "工商银行", "sh-main", "沪市主板", 6.1, 0.3),
  stock("688981", "中芯国际", "star", "科创板", 82.4, 3.1),
  stock("688111", "金山办公", "star", "科创板", 245.0, -1.5),
  stock("000425", "徐工机械", "sz-main", "深市主板", 7.9, 0.6),
  stock("002415", "海康威视", "sz-main", "深市主板", 32.8, 2.2, 0),
  stock("300750", "宁德时代", "chinext", "创业板", 210.0, -2.4),
  stock("300124", "汇川技术", "chinext", "创业板", 62.3, 1.1),
  stock("920001", "同心传动", "bse", "北交所", 12.0, 0.0),
];

const MARKETS = [
  { key: "sh-main", name: "沪市主板", group: "股票", count: 3 },
  { key: "star", name: "科创板", group: "股票", count: 2 },
  { key: "sz-main", name: "深市主板", group: "股票", count: 2 },
  { key: "chinext", name: "创业板", group: "股票", count: 2 },
  { key: "bse", name: "北交所", group: "股票", count: 1 },
];

const UNIVERSE: StockUniverse = {
  sourceUrl: "http://127.0.0.1:7899",
  sourceName: "本地数据（本机 stockdb）",
  tradeDate: 20260930,
  loadedAt: "2026-09-30T18:00:00+08:00",
  elapsed: "1.05s",
  total: ALL_STOCKS.length,
  withQuote: ALL_STOCKS.filter((s) => s.hasQuote).length,
  markets: MARKETS,
  stocks: ALL_STOCKS,
};

const CATEGORIES: BoardCategoryInfo[] = [
  { key: "申万一级", name: "申万一级", count: 3 },
  { key: "概念", name: "概念", count: 2 },
];

function board(code: string, name: string, category: string, symbols: string[]): BoardInfo {
  return { code, name, category, type: "sw_1", source: "sw", count: symbols.length, symbols };
}

const SW1_BOARDS: BoardInfo[] = [
  board("801890.SL", "机械设备", "申万一级", ["600031", "000425", "300124"]),
  board("801080.SL", "电子", "申万一级", ["002415", "300750", "688981"]),
  board("801780.SL", "银行", "申万一级", ["600000", "601398"]),
];

const CONCEPT_BOARDS: BoardInfo[] = [
  board("309025.TI", "AIGC概念", "概念", ["688111", "002415"]),
  board("308300.TI", "MCU芯片", "概念", ["688981"]),
];

beforeEach(() => {
  setActivePinia(createPinia());
  vi.clearAllMocks();
  mocks.getStockUniverse.mockResolvedValue(UNIVERSE);
  mocks.getBoardCategories.mockResolvedValue(CATEGORIES);
  mocks.getBoards.mockImplementation(async (category: string) =>
    category === "概念" ? CONCEPT_BOARDS : SW1_BOARDS,
  );
});

describe("股票列表 store", () => {
  it("加载全量股票并得到市场分类", async () => {
    const store = useStockStore();
    await store.loadUniverse(false);

    expect(store.allStocks).toHaveLength(10);
    expect(store.markets.map((m) => m.name)).toContain("沪市主板");
    expect(store.universe?.tradeDate).toBe(20260930);
    expect(store.error).toBe("");
  });

  it("默认选中申万一级并加载对应板块", async () => {
    const store = useStockStore();
    await store.loadUniverse(false);
    await store.loadCategories();

    expect(store.categoryKey).toBe("申万一级");
    expect(store.boards.map((b) => b.name)).toEqual(["机械设备", "电子", "银行"]);
  });

  it("按市场筛选", async () => {
    const store = useStockStore();
    await store.loadUniverse(false);

    store.selectMarket("star");
    expect(store.sorted.map((s) => s.code)).toEqual(["688111", "688981"]);

    store.selectMarket("all");
    expect(store.sorted).toHaveLength(10);
  });

  it("按行业筛选（申万一级 -> 机械设备）", async () => {
    const store = useStockStore();
    await store.loadUniverse(false);
    await store.loadCategories();

    store.selectBoard("801890.SL");
    expect(store.sorted.map((s) => s.code).sort()).toEqual(["000425", "300124", "600031"]);
  });

  it("按代码或名称搜索", async () => {
    const store = useStockStore();
    await store.loadUniverse(false);

    store.setKeyword("浦发");
    expect(store.sorted.map((s) => s.code)).toEqual(["600000"]);

    store.setKeyword("3007");
    expect(store.sorted.map((s) => s.code)).toEqual(["300750"]);
  });

  it("市场分类之后再按行业分组", async () => {
    const store = useStockStore();
    await store.loadUniverse(false);
    await store.loadCategories();

    store.selectMarket("sh-main");
    store.grouped = true;

    const names = store.groups.map((g) => g.name);
    expect(names).toContain("机械设备");
    expect(names).toContain("银行");

    const machines = store.groups.find((g) => g.name === "机械设备");
    // 沪市主板里只有 600031 属于机械设备
    expect(machines?.stocks.map((s) => s.code)).toEqual(["600031"]);

    const banks = store.groups.find((g) => g.name === "银行");
    expect(banks?.stocks.map((s) => s.code)).toEqual(["600000", "601398"]);
  });

  it("未归入任何板块的股票进入「未分类」分组", async () => {
    const store = useStockStore();
    await store.loadUniverse(false);
    await store.loadCategories();

    store.grouped = true;
    const unclassified = store.groups.find((g) => g.name === "未分类");
    // 688111 与 920001 都不在上面任何申万一级板块的成分股里
    expect(unclassified?.stocks.map((s) => s.code)).toEqual(["688111", "920001"]);
    // 未分类分组永远排在最后
    expect(store.groups[store.groups.length - 1].name).toBe("未分类");
  });

  it("切换行业分类会重新加载板块并清空行业筛选", async () => {
    const store = useStockStore();
    await store.loadUniverse(false);
    await store.loadCategories();
    store.selectBoard("801890.SL");

    await store.selectCategory("概念");
    expect(store.boardCode).toBe("");
    expect(store.boards.map((b) => b.name)).toEqual(["AIGC概念", "MCU芯片"]);
  });

  it("分页与排序", async () => {
    const store = useStockStore();
    await store.loadUniverse(false);

    store.setPageSize(50);
    expect(store.pagedStocks).toHaveLength(10);

    store.setSort("pctChg"); // 首次点选：降序
    expect(store.sortDir).toBe("desc");
    expect(store.sorted[0].pctChg).toBeGreaterThanOrEqual(store.sorted[1].pctChg);

    store.setSort("pctChg"); // 再点一次：升序
    expect(store.sortDir).toBe("asc");
    expect(store.sorted[0].pctChg).toBeLessThanOrEqual(store.sorted[1].pctChg);
  });
});

describe("股票列表页面", () => {
  it("渲染市场分类、行业分组与股票", async () => {
    // 组件与断言必须共用同一个 pinia 实例
    const pinia = createPinia();
    setActivePinia(pinia);

    // 列表行现在可以点击跳转到 K 线页，需要提供 router
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/", component: { template: "<div />" } },
        { path: "/stocks", component: { template: "<div />" } },
        { path: "/kline", component: { template: "<div />" } },
      ],
    });
    await router.push("/stocks");
    await router.isReady();

    const wrapper = mount(StockListView, {
      global: {
        plugins: [pinia, router],
      },
    });
    // 等待 onMounted 里的两个异步加载
    await vi.waitFor(() => {
      expect(wrapper.text()).toContain("沪市主板");
    });
    await nextTick();

    const text = wrapper.text();
    // 数据源 / 交易日 / 总数
    expect(text).toContain("本地数据（本机 stockdb）");
    expect(text).toContain("2026-09-30");
    expect(text).toContain("申万一级");
    expect(text).toContain("概念");
    // 市场分类
    expect(text).toContain("科创板");
    expect(text).toContain("北交所");
    // 行业分组
    expect(text).toContain("机械设备");
    expect(text).toContain("电子");
    expect(text).toContain("未分类");

    // 切到平铺视图后应能看到具体股票
    const store = useStockStore();
    store.grouped = false;
    await nextTick();
    await nextTick();
    const flat = wrapper.text();
    expect(flat).toContain("600000");
    expect(flat).toContain("浦发银行");
    expect(flat).toContain("+1.20%");
    expect(flat).toContain("-2.40%");
  });

  it("列表页的搜索框也用同一套联想规则（拼音首字母），选中后筛出这一只", async () => {
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

    const pinia = createPinia();
    setActivePinia(pinia);
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/", component: { template: "<div />" } },
        { path: "/stocks", component: { template: "<div />" } },
        { path: "/kline", component: { template: "<div />" } },
      ],
    });
    await router.push("/stocks");
    await router.isReady();

    const wrapper = mount(StockListView, { global: { plugins: [pinia, router] } });
    await vi.waitFor(() => expect(wrapper.text()).toContain("沪市主板"));

    // 搜索框存在，且提示语说明支持拼音首字母
    const input = wrapper.find('input[placeholder*="拼音首字母"]');
    expect(input.exists()).toBe(true);

    // 输入拼音首字母 gzmt，等防抖后应触发联想请求
    await input.setValue("gzmt");
    await new Promise((resolve) => setTimeout(resolve, 300));
    await nextTick();
    expect(mocks.searchStocks).toHaveBeenCalledWith("gzmt", 12);

    // 下拉里出现贵州茅台，并且标了命中方式
    const option = wrapper.findAll("button").find((b) => b.text().includes("贵州茅台"));
    expect(option, "联想下拉里没有贵州茅台").toBeTruthy();
    expect(option!.text()).toContain("拼音首字母");

    // 选中后按代码筛出这一只，并切到平铺列表（否则可能被折叠在行业分组里看不到）
    await option!.trigger("click");
    await nextTick();
    const store = useStockStore();
    expect(store.keyword).toBe("600519");
    expect(store.grouped).toBe(false);
  });

  // 列表页的搜索是用来**筛股票列表**的，指数不在列表里、筛不出来，
  // 所以要在联想里过滤掉（指数请去 K 线页搜）。
  it("列表页的联想里不出现指数", async () => {
    mocks.searchStocks.mockResolvedValue([
      {
        code: "000001",
        name: "平安银行",
        market: "sz-main",
        marketCN: "深市主板",
        pinyin: "pinganyinhang",
        initials: "payh",
        close: 11.2,
        pctChg: 0.5,
        matchOn: "code",
        isIndex: false,
      },
      {
        code: "000001.SH",
        name: "上证指数",
        market: "index",
        marketCN: "指数",
        pinyin: "shangzhengzhishu",
        initials: "szzs",
        close: 3813.79,
        pctChg: 0.05,
        matchOn: "code",
        isIndex: true,
      },
    ]);

    const pinia = createPinia();
    setActivePinia(pinia);
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/", component: { template: "<div />" } },
        { path: "/stocks", component: { template: "<div />" } },
      ],
    });
    await router.push("/stocks");
    await router.isReady();

    const wrapper = mount(StockListView, { global: { plugins: [pinia, router] } });
    await vi.waitFor(() => expect(wrapper.text()).toContain("沪市主板"));

    const input = wrapper.find('input[placeholder*="拼音首字母"]');
    await input.setValue("000001");
    await new Promise((resolve) => setTimeout(resolve, 300));
    await nextTick();

    const text = wrapper.text();
    expect(text).toContain("平安银行");
    expect(text).not.toContain("上证指数");
    expect(text).not.toContain("000001.SH");
  });
});
