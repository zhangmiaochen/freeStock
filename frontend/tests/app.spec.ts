import { mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import { nextTick } from "vue";

import type {
  BoardCategoryInfo,
  BoardInfo,
  StockItem,
  StockUniverse,
} from "@/lib/backend";

const mocks = vi.hoisted(() => ({
  getStockUniverse: vi.fn(),
  getBoardCategories: vi.fn(),
  getBoards: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    getStockUniverse: mocks.getStockUniverse,
    getBoardCategories: mocks.getBoardCategories,
    getBoards: mocks.getBoards,
    getDataSources: vi.fn(async () => []),
    getCurrentDataSource: vi.fn(async () => ({
      key: "local",
      name: "本地数据（本机 stockdb）",
      url: "http://127.0.0.1:7899",
      kind: "local",
      current: true,
    })),
    testDataSource: vi.fn(),
    setDataSource: vi.fn(),
    clearCache: vi.fn(),
  },
  errorMessage: (err: unknown) => String(err),
  onScanProgress: () => () => {},
}));

import App from "@/App.vue";
import { routes } from "@/router";

function stock(code: string, name: string, market: string, marketCN: string): StockItem {
  return {
    code,
    name,
    market,
    marketCN,
    date: 20260930,
    close: 10,
    pctChg: 1,
    amount: 1e8,
    turnover: 1,
    peTTM: 10,
    totalMV: 1e10,
    floatMV: 1e10,
    pb: 1.5,
    volRatio: 1.2,
    isST: false,
    hasQuote: true,
  };
}

const UNIVERSE: StockUniverse = {
  sourceUrl: "http://127.0.0.1:7899",
  sourceName: "本地数据（本机 stockdb）",
  tradeDate: 20260930,
  loadedAt: "2026-09-30T18:00:00+08:00",
  elapsed: "1.0s",
  total: 2,
  withQuote: 2,
  markets: [
    { key: "sh-main", name: "沪市主板", group: "股票", count: 1 },
    { key: "chinext", name: "创业板", group: "股票", count: 1 },
  ],
  stocks: [
    stock("600000", "浦发银行", "sh-main", "沪市主板"),
    stock("300750", "宁德时代", "chinext", "创业板"),
  ],
};

const CATEGORIES: BoardCategoryInfo[] = [{ key: "申万一级", name: "申万一级", count: 1 }];
const BOARDS: BoardInfo[] = [
  {
    code: "801780.SL",
    name: "银行",
    category: "申万一级",
    type: "sw_1",
    source: "sw",
    count: 1,
    symbols: ["600000"],
  },
];

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getStockUniverse.mockResolvedValue(UNIVERSE);
  mocks.getBoardCategories.mockResolvedValue(CATEGORIES);
  mocks.getBoards.mockResolvedValue(BOARDS);
});

describe("应用外壳（App.vue）", () => {
  it("挂载后能渲染出侧边栏与页面内容", async () => {
    const errors: unknown[] = [];
    const spy = vi.spyOn(console, "error").mockImplementation((...args) => {
      errors.push(args);
    });

    const pinia = createPinia();
    setActivePinia(pinia);

    const router = createRouter({ history: createMemoryHistory(), routes });
    router.push("/stocks");
    await router.isReady();

    const wrapper = mount(App, {
      global: { plugins: [pinia, router] },
      attachTo: document.body,
    });

    await vi.waitFor(() => {
      expect(wrapper.text()).toContain("沪市主板");
    });
    await nextTick();

    const text = wrapper.text();

    // 侧边栏
    expect(text).toContain("FreeStock");
    expect(text).toContain("股票列表");
    expect(text).toContain("数据源设置");
    // 顶栏标题
    expect(text).toContain("按市场与行业浏览全部 A 股 / 基金");
    // 页面内容
    expect(text).toContain("申万一级");
    expect(text).toContain("银行");

    // 侧边栏与内容区必须真的有高度，否则就是「界面没显示」
    const provider = wrapper.find('[data-slot="sidebar-wrapper"]');
    expect(provider.exists()).toBe(true);
    const inset = wrapper.find('[data-slot="sidebar-inset"]');
    expect(inset.exists()).toBe(true);
    // 内容区不是空的（侧边栏 + 页面文字都渲染出来了）
    expect(inset.text().length).toBeGreaterThan(20);

    spy.mockRestore();
    expect(errors, `挂载过程出现 console.error: ${JSON.stringify(errors)}`).toHaveLength(0);

    wrapper.unmount();
  });
});
