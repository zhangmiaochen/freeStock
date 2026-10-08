import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, nextTick } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

import type { FinancialStatements } from "@/lib/backend";

const mocks = vi.hoisted(() => ({
  getFinancialStatements: vi.fn(),
  searchStocks: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    getFinancialStatements: mocks.getFinancialStatements,
    searchStocks: mocks.searchStocks,
  },
  errorMessage: (err: unknown) => String(err),
}));

import FinanceView from "@/views/FinanceView.vue";

/** 完整的一份财务数据（模拟数据湖里四张表都已入库） */
function fullStatements(): FinancialStatements {
  return {
    source: "cnlake",
    available: true,
    lakeAvailable: true,
    lakeRoot: "D:\\workspace\\stockTool\\CNEquity\\data\\cnequity",
    detail: "来自 cnequity 数据湖 D:\\...，财报共 12000 行、2 个报告期",
    datasets: [],
    reportPeriods: ["2025Q2", "2025Q1"],
    items: [
      { symbol: "600519.SH", reportPeriod: "2025Q2", statementType: "income", itemCode: "TOTAL_OPERATE_INCOME", itemValue: 9.1e10, announceDate: 20250815, source: "eastmoney" },
      { symbol: "600519.SH", reportPeriod: "2025Q2", statementType: "income", itemCode: "PARENT_NETPROFIT", itemValue: 4.5e10, announceDate: 20250815, source: "eastmoney" },
      { symbol: "600519.SH", reportPeriod: "2025Q1", statementType: "income", itemCode: "TOTAL_OPERATE_INCOME", itemValue: 4.4e10, announceDate: 20250425, source: "eastmoney" },
    ],
    shareStructure: [
      { date: 20240101, totalShares: 1.25e9, floatShares: 1.25e9, restrictedShares: 0, freeFloatShares: 0, changeReason: "定期报告" },
      { date: 20250701, totalShares: 1.256e9, floatShares: 1.256e9, restrictedShares: 0, freeFloatShares: 0, changeReason: "回购注销" },
    ],
    shareholderCounts: [
      { date: 20250331, holderCount: 200000, changePct: -5.2, avgFloatShares: 6250, avgHoldingValue: 7.8e6 },
      { date: 20250630, holderCount: 185000, changePct: -7.5, avgFloatShares: 6790, avgHoldingValue: 8.5e6 },
    ],
    topHolders: [
      { recordDate: 20250630, scope: "top10", rank: 1, name: "中国贵州茅台酒厂(集团)有限责任公司", holdingShares: 6.78e8, holdingPct: 54, isInstitution: true, holderType: "state" },
      { recordDate: 20250630, scope: "top10", rank: 2, name: "香港中央结算有限公司", holdingShares: 1.0e8, holdingPct: 8, isInstitution: true, holderType: "institution" },
    ],
    topHolderDate: 20250630,
    tableStatus: {
      financial_statement_items: true,
      share_structure: true,
      shareholder_counts: true,
      top_holders: true,
    },
  } as unknown as FinancialStatements;
}

async function mountPage(query: Record<string, string> = {}) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", component: { template: "<div />" } },
      { path: "/finance", component: FinanceView },
    ],
  });
  await router.push({ path: "/finance", query });
  await router.isReady();

  const wrapper = mount(FinanceView, {
    global: {
      plugins: [router],
      stubs: { StockSearchBox: defineComponent({ name: "StockSearchBox", template: "<div class='search-stub' />" }) },
    },
  });
  return { wrapper, router };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getFinancialStatements.mockResolvedValue(fullStatements());
  mocks.searchStocks.mockResolvedValue([]);
});

describe("财务数据页面", () => {
  it("未选股票时说明数据来源，避免误解成行情的一部分", async () => {
    const { wrapper } = await mountPage();
    const text = wrapper.text();

    expect(text).toContain("搜索一只股票查看财务数据");
    expect(text).toContain("cnequity 本地数据湖");
    // 关键：说清楚它和行情源无关
    expect(text).toContain("与「数据源设置」里选的行情源无关");
    expect(mocks.getFinancialStatements).not.toHaveBeenCalled();
  });

  it("带 code 进入时加载并展示四类财务数据", async () => {
    const { wrapper } = await mountPage({ code: "600519" });

    await vi.waitFor(() => expect(mocks.getFinancialStatements).toHaveBeenCalledWith("600519", 500));
    await nextTick();
    await nextTick();

    const text = wrapper.text();
    // 来源状态
    expect(text).toContain("财报已入库");
    expect(text).toContain("已入库：financial_statement_items");
    expect(text).toContain("2025Q2");
    // 股本结构
    expect(text).toContain("股本结构");
    expect(text).toContain("回购注销");
    // 股东户数
    expect(text).toContain("股东户数");
    expect(text).toContain("185,000");
    // 十大股东
    expect(text).toContain("十大股东");
    expect(text).toContain("香港中央结算有限公司");
    expect(text).toContain("54.00%");
    // 财报科目
    expect(text).toContain("财报科目");
    expect(text).toContain("TOTAL_OPERATE_INCOME");
    expect(text).toContain("PARENT_NETPROFIT");
  });

  it("财报未入库时如实说明，并指出后续 phase 的坑", async () => {
    mocks.getFinancialStatements.mockResolvedValue({
      source: "cnlake",
      available: false,
      lakeAvailable: true,
      lakeRoot: "D:\\lake",
      detail: "数据湖已找到（D:\\lake），但财报数据集尚未入库。当前已有：instruments。",
      datasets: [],
      reportPeriods: [],
      items: [],
      shareStructure: [],
      shareholderCounts: [],
      topHolders: [],
      topHolderDate: 0,
      tableStatus: {
        financial_statement_items: false,
        share_structure: false,
        shareholder_counts: false,
        top_holders: false,
      },
    } as unknown as FinancialStatements);

    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.text()).toContain("财报未入库"));

    const text = wrapper.text();
    expect(text).toContain("财报未入库");
    expect(text).toContain("未入库：financial_statement_items");
    expect(text).toContain("这张表里没有 600519 的记录");
  });

  it("区分「表没入库」和「表在库但这只股票没记录」", async () => {
    mocks.getFinancialStatements.mockResolvedValue({
      source: "cnlake",
      available: true,
      lakeAvailable: true,
      lakeRoot: "D:\\lake",
      detail: "来自 cnequity 数据湖，财报共 12000 行、2 个报告期",
      datasets: [],
      reportPeriods: ["2025Q2"],
      items: [],
      shareStructure: [],
      shareholderCounts: [],
      topHolders: [],
      topHolderDate: 0,
      // 表在库
      tableStatus: {
        financial_statement_items: true,
        share_structure: true,
        shareholder_counts: true,
        top_holders: true,
      },
    } as unknown as FinancialStatements);

    const { wrapper } = await mountPage({ code: "300750" });
    await vi.waitFor(() => expect(mocks.getFinancialStatements).toHaveBeenCalled());
    await nextTick();
    await nextTick();

    const text = wrapper.text();
    // 表在库 -> 显示「已入库」，不能显示成「未入库」
    expect(text).toContain("已入库：financial_statement_items");
    expect(text).not.toContain("未入库：");
    // 但这只股票没记录
    expect(text).toContain("这张表里没有 300750 的记录");
  });

  it("读取失败时给出错误与重试入口", async () => {
    mocks.getFinancialStatements.mockRejectedValue(new Error("数据湖读取失败"));

    const { wrapper } = await mountPage({ code: "600519" });
    await vi.waitFor(() => expect(wrapper.text()).toContain("读取失败"));

    expect(wrapper.text()).toContain("数据湖读取失败");
    const retry = wrapper.findAll("button").find((b) => b.text().includes("重试"));
    expect(retry).toBeTruthy();
  });
});
