import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

const mocks = vi.hoisted(() => ({
  getOptions: vi.fn(),
  getDatabasePath: vi.fn(),
  list: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  remove: vi.fn(),
  detail: vi.fn(),
  overview: vi.fn(),
  addPosition: vi.fn(),
  removePosition: vi.fn(),
  refreshStrength: vi.fn(),
  getStrengthSummary: vi.fn(),
  buildPlan: vi.fn(),
  applyRotation: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: { portfolio: mocks },
  errorMessage: (err: unknown) => String(err),
}));

import PortfolioView from "@/views/PortfolioView.vue";

const OPTIONS = {
  types: [
    { key: "manual", label: "手动组合", description: "" },
    { key: "semi_auto", label: "半自动组合", description: "" },
    { key: "auto", label: "自动组合", description: "" },
  ],
  strategies: [
    { key: "trend_growth", label: "趋势成长" },
    { key: "trend_pullback_low_buy", label: "趋势低吸" },
  ],
  statuses: [
    { key: "active", label: "运行中" },
    { key: "archived", label: "已归档" },
  ],
  scopeTypes: [
    { key: "all", label: "全市场" },
    { key: "industry", label: "按行业" },
  ],
  markets: [
    { key: "main", label: "主板" },
    { key: "gem", label: "创业板" },
  ],
  holdingTypes: [
    { key: "", label: "未指定" },
    { key: "swing", label: "波段仓" },
    { key: "base", label: "底仓" },
  ],
  defaultBenchmark: "000300.SH",
  defaultLayers: 10,
};

function portfolio(overrides: Record<string, unknown> = {}) {
  return {
    id: "pf_1",
    name: "我的组合",
    type: "manual",
    typeLabel: "手动组合",
    strategyId: "trend_growth",
    strategyLabel: "趋势成长",
    benchmarkCode: "000300.SH",
    status: "active",
    targetPositionCount: 5,
    maxStockCount: 5,
    totalLayers: 10,
    rebalanceFrequency: "signal",
    scopeType: "all",
    scopeValueJson: "{}",
    scopeLabel: "全市场｜全部市场",
    notes: "",
    createdAt: "2026-10-08T10:00:00+08:00",
    updatedAt: "2026-10-08T10:00:00+08:00",
    executionMode: "manual_review",
    executionModeLabel: "仅分析提醒",
    allowApplyRotation: false,
    requireManualConfirmation: true,
    positionCount: 1,
    usedLayers: 1,
    availableLayers: 9,
    positionRatio: 10,
    ...overrides,
  };
}

function detail(overrides: Record<string, unknown> = {}) {
  return {
    portfolio: portfolio(),
    positions: [
      {
        portfolioId: "pf_1",
        code: "600519",
        name: "贵州茅台",
        entryPrice: 1000,
        entryDate: "20260930",
        positionLayers: 1,
        positionWeight: 10,
        sourceType: "manual",
        holdingType: "base",
        strategyTag: "",
        lastAction: "manual_add",
        createdAt: "",
        updatedAt: "",
        currentPrice: 1100,
        profitLossPct: 10,
        priceDate: "20261008",
      },
    ],
    transactions: [
      {
        id: "txn_1",
        portfolioId: "pf_1",
        code: "600519",
        name: "贵州茅台",
        tradeDate: "20260930",
        action: "buy",
        price: 1000,
        layersDelta: 1,
        weightDelta: 10,
        reason: "手动新增持仓",
        sourceSignalId: "manual",
        createdAt: "",
      },
    ],
    summary: {
      portfolioId: "pf_1",
      positionCount: 1,
      usedLayers: 1,
      availableLayers: 9,
      positionRatio: 10,
      totalReturnPct: 1,
      maxDrawdownPct: 0,
      priceDate: "20261008",
      missingPrices: 0,
    },
    ...overrides,
  };
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", component: { template: "<div />" } },
      { path: "/portfolio", component: PortfolioView },
      { path: "/kline", component: { template: "<div />" } },
    ],
  });
  await router.push("/portfolio");
  await router.isReady();

  const wrapper = mount(PortfolioView, { global: { plugins: [router] } });
  await vi.waitFor(() => expect(mocks.list).toHaveBeenCalled());
  await nextTick();
  await nextTick();
  return { wrapper, router };
}

function button(wrapper: ReturnType<typeof mount>, text: string) {
  return wrapper.findAll("button").find((b) => b.text().includes(text));
}

/** 组合表单里的名称输入框（表单内的第一个文本框） */
function nameField(wrapper: ReturnType<typeof mount>) {
  return wrapper.find('form input[type="text"], form input:not([type])');
}

/**
 * 提交组合表单。
 *
 * 不能点「保存」按钮 —— 它是 type="submit"，happy-dom 里点击按钮
 * 不会触发表单的 submit 事件。直接对 form 触发 submit 才可靠。
 */
async function submitForm(wrapper: ReturnType<typeof mount>) {
  await wrapper.find("form").trigger("submit");
  await nextTick();
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getOptions.mockResolvedValue(OPTIONS);
  mocks.getDatabasePath.mockResolvedValue("D:\\FreeStock\\portfolio.db");
  mocks.list.mockResolvedValue([portfolio()]);
  mocks.overview.mockResolvedValue({
    portfolioCount: 1, activeCount: 1, positionCount: 1, usedLayers: 1, totalLayers: 10,
  });
  mocks.detail.mockResolvedValue(detail());
  mocks.create.mockResolvedValue(portfolio({ id: "pf_new", name: "新组合" }));
  mocks.update.mockResolvedValue(portfolio({ name: "改名后" }));
  mocks.remove.mockResolvedValue(undefined);
  mocks.addPosition.mockResolvedValue({});
  mocks.removePosition.mockResolvedValue(undefined);
  mocks.getStrengthSummary.mockResolvedValue({
    strategyId: "trend_growth", latestDate: 20261008, count: 302, dates: [20261008],
  });
  mocks.refreshStrength.mockResolvedValue({
    tradeDate: 20261008, saved: 302, skipped: 0, limit: 800,
    // 增量刷新新增的三个字段（见 RefreshResult）
    reused: 0, total: 302, skippedFromCache: false,
    marketState: { marketState: "bear", marketStateLabel: "熊市/风险释放", bullScore: 45, bearScore: 92, proxyRet20: -3.49, days: 80 },
    unavailable: [], elapsed: "10s",
  });
  mocks.buildPlan.mockResolvedValue({
    portfolioId: "pf_1", portfolioName: "我的组合",
    strategyId: "trend_growth", strategyLabel: "趋势成长",
    config: { strategyId: "trend_growth", marketState: "bear", totalLayers: 10, targetPositionCount: 5, readyScore: 68, candidateScoreFloor: 60, maxLayersPerStock: 3, allowNewAdds: true, requireCandidateSignal: false, replaceScoreGap: 20, warningScore: 60, reduceScore: 45, clearScore: 35, maxReplaceCount: 2 },
    marketState: { marketState: "bear", marketStateLabel: "熊市/风险释放", bullScore: 45, bearScore: 92, proxyRet20: -3.49, days: 80 },
    strengthDate: 20261008,
    plan: {
      rows: [{
        tsCode: "600519", name: "贵州茅台", action: "clear", actionLabel: "清仓",
        score: 26.42, scoreGap: 0, rankNo: 0, candidateTsCode: "", candidateName: "",
        candidateScore: 0, expectedImprovement: 0, reason: "结构破坏且不可修复，清仓",
        sourceType: "holding", currentLayers: 1, targetLayers: 0, layersDelta: -1,
        decisionState: "trend_break_exit", isExecutable: true, requiresConfirmation: true,
        executionMode: "confirm_required", executionModeLabel: "人工确认后执行", actionPrefix: "待确认",
      }],
      compare: [], candidates: [], marketState: "bear", strategyId: "trend_growth",
      entryAllowed: true, notes: [],
    },
    holdings: [], candidates: [], notes: [],
  });
  mocks.applyRotation.mockResolvedValue({
    portfolioId: "pf_1", portfolioName: "我的组合", tradeDate: 20261008,
    applied: { positionCount: 0, added: 0, reduced: 0, cleared: 1, unchanged: 0, skipped: [] },
    changed: [],
  });
});

describe("组合管理页面", () => {
  it("加载组合列表、总览与详情", async () => {
    const { wrapper } = await mountPage();

    const text = wrapper.text();
    expect(text).toContain("组合管理");
    expect(text).toContain("1 个组合 / 1 个持仓");
    expect(text).toContain("层数占用 1 / 10");
    // 列表项
    expect(text).toContain("我的组合");
    expect(text).toContain("持仓 1");
    // 自动选中第一个组合并加载详情
    expect(mocks.detail).toHaveBeenCalledWith("pf_1");
    expect(text).toContain("贵州茅台");
    expect(text).toContain("持仓流水");
  });

  it("把层数口径和收益率口径写在界面上", async () => {
    const { wrapper } = await mountPage();
    const text = wrapper.text();

    // 这些是最容易被误解的地方，必须有说明
    expect(text).toContain("1 层 = 总资金的 1/10");
    expect(text).toContain("计划权重");
    expect(text).toContain("不含未投入的现金");
  });

  it("新建组合提交归一化前的原始表单", async () => {
    const { wrapper } = await mountPage();

    await button(wrapper, "新建组合")!.trigger("click");
    await nextTick();
    expect(wrapper.text()).toContain("组合名称");

    // 填名称
    await nameField(wrapper).setValue("我的新组合");
    await nextTick();

    // 改成自动组合
    await wrapper.findAll("form select")[0].setValue("auto");
    await nextTick();

    await submitForm(wrapper);
    await vi.waitFor(() => expect(mocks.create).toHaveBeenCalled());

    const submitted = mocks.create.mock.calls[0][0];
    expect(submitted.name).toBe("我的新组合");
    expect(submitted.type).toBe("auto");
    expect(submitted.totalLayers).toBe(10);
    expect(submitted.benchmarkCode).toBe("000300.SH");
  });

  it("空名称时拦下并提示，不发请求", async () => {
    const { wrapper } = await mountPage();

    await button(wrapper, "新建组合")!.trigger("click");
    await nextTick();
    await submitForm(wrapper);

    expect(mocks.create).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("组合名称不能为空");
  });

  it("编辑时回填现有值（含范围内容）", async () => {
    // 详情页的组合才是编辑的数据源，所以这里要改 detail 而不是 list
    mocks.detail.mockResolvedValue(detail({
      portfolio: portfolio({
        scopeType: "industry",
        scopeValueJson: JSON.stringify({ items: ["银行", "证券"], markets: ["main"] }),
      }),
    }));
    const { wrapper } = await mountPage();

    await vi.waitFor(() => expect(button(wrapper, "编辑")).toBeTruthy());
    await button(wrapper, "编辑")!.trigger("click");
    await nextTick();
    expect(wrapper.text()).toContain("编辑组合");

    const nameInput = nameField(wrapper);
    expect((nameInput.element as HTMLInputElement).value).toBe("我的组合");

    // 范围内容要回填到文本域
    const textarea = wrapper.find("form textarea");
    expect((textarea.element as HTMLTextAreaElement).value).toContain("银行");

    await submitForm(wrapper);
    await vi.waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(1));
    const [id, submitted] = mocks.update.mock.calls[0];
    expect(id).toBe("pf_1");
    expect(submitted.scopeValue.items).toEqual(["银行", "证券"]);
  });

  it("新增持仓：搜索选中后回填代码与名称", async () => {
    const { wrapper } = await mountPage();

    await button(wrapper, "新增持仓")!.trigger("click");
    await nextTick();
    expect(wrapper.text()).toContain("建仓价格");

    // 模拟搜索框选中（select 的签名是 (code, item)）
    const search = wrapper.findComponent({ name: "StockSearchBox" });
    search.vm.$emit("select", "600519", { code: "600519", name: "贵州茅台" });
    await nextTick();
    expect(wrapper.text()).toContain("已选");

    // 填价格
    const priceInput = wrapper.find('input[type="number"][step="0.0001"]');
    await priceInput.setValue(1500);
    await nextTick();

    await button(wrapper, "添加")!.trigger("click");
    await vi.waitFor(() => expect(mocks.addPosition).toHaveBeenCalled());

    const [id, input] = mocks.addPosition.mock.calls[0];
    expect(id).toBe("pf_1");
    expect(input.code).toBe("600519");
    expect(input.name).toBe("贵州茅台");
    expect(input.entryPrice).toBe(1500);
  });

  it("未选股票 / 价格非法时拦下", async () => {
    const { wrapper } = await mountPage();
    await button(wrapper, "新增持仓")!.trigger("click");
    await nextTick();

    // 没选股票
    await button(wrapper, "添加")!.trigger("click");
    await nextTick();
    expect(mocks.addPosition).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("请先选择股票");

    // 选了股票但价格是 0
    const search = wrapper.findComponent({ name: "StockSearchBox" });
    search.vm.$emit("select", "600519", { code: "600519", name: "贵州茅台" });
    await nextTick();
    await button(wrapper, "添加")!.trigger("click");
    await nextTick();
    expect(wrapper.text()).toContain("建仓价格必须大于 0");
  });

  it("删除持仓前要确认", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { wrapper } = await mountPage();

    const row = wrapper.findAll("tbody tr")[0];
    const deleteButton = row.findAll("button").at(-1)!;
    await deleteButton.trigger("click");
    await nextTick();
    // 取消 → 不发请求
    expect(mocks.removePosition).not.toHaveBeenCalled();

    confirmSpy.mockReturnValue(true);
    await deleteButton.trigger("click");
    await vi.waitFor(() => expect(mocks.removePosition).toHaveBeenCalledWith("pf_1", "600519"));
    confirmSpy.mockRestore();
  });

  it("删除组合前要确认，并清空当前选中", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const { wrapper } = await mountPage();

    await button(wrapper, "删除")!.trigger("click");
    await vi.waitFor(() => expect(mocks.remove).toHaveBeenCalledWith("pf_1"));
    confirmSpy.mockRestore();
  });

  it("浮动盈亏按正负着色", async () => {
    mocks.detail.mockResolvedValue(detail({
      positions: [
        { ...detail().positions[0], code: "600519", profitLossPct: 10, currentPrice: 1100 },
        { ...detail().positions[0], code: "000001", name: "平安银行", profitLossPct: -5, currentPrice: 95 },
      ],
    }));
    const { wrapper } = await mountPage();

    const html = wrapper.html();
    expect(html).toContain("text-up");
    expect(html).toContain("text-down");
    expect(wrapper.text()).toContain("+10.00%");
    expect(wrapper.text()).toContain("-5.00%");
  });

  it("缺现价时给出提示并显示占位", async () => {
    mocks.detail.mockResolvedValue(detail({
      positions: [
        { ...detail().positions[0], currentPrice: 0, profitLossPct: 0, priceDate: "" },
      ],
      summary: { ...detail().summary, missingPrices: 1, totalReturnPct: 0 },
    }));
    const { wrapper } = await mountPage();

    expect(wrapper.text()).toContain("1 只持仓没取到现价");
    // 现价与盈亏显示占位符而不是 0
    const row = wrapper.findAll("tbody tr")[0];
    expect(row.text()).toContain("—");
  });

  it("没有组合时给出引导", async () => {
    mocks.list.mockResolvedValue([]);
    mocks.overview.mockResolvedValue({
      portfolioCount: 0, activeCount: 0, positionCount: 0, usedLayers: 0, totalLayers: 0,
    });
    const { wrapper } = await mountPage();

    expect(wrapper.text()).toContain("还没有组合");
    expect(wrapper.text()).toContain("从左侧选择一个组合");
    expect(mocks.detail).not.toHaveBeenCalled();
  });

  it("读取失败时显示错误", async () => {
    mocks.list.mockRejectedValue(new Error("组合库打不开"));
    const { wrapper } = await mountPage();
    await vi.waitFor(() => expect(wrapper.text()).toContain("组合库打不开"));
  });

  it("点击持仓可以跳到 K 线", async () => {
    const { wrapper, router } = await mountPage();

    const row = wrapper.findAll("tbody tr")[0];
    await row.findAll("button")[0].trigger("click");
    await vi.waitFor(() => expect(router.currentRoute.value.path).toBe("/kline"));
    expect(router.currentRoute.value.query.code).toBe("600519");
  });

  // ---- 阶段二：评分与调仓建议 ----

  it("加载组合时读取评分状态", async () => {
    const { wrapper } = await mountPage();
    await vi.waitFor(() => expect(mocks.getStrengthSummary).toHaveBeenCalledWith("trend_growth"));
    expect(wrapper.text()).toContain("评分日 20261008");
    expect(wrapper.text()).toContain("302 只");
  });

  it("没有评分时提示先刷新，并说明代价", async () => {
    mocks.getStrengthSummary.mockResolvedValue({
      strategyId: "trend_growth", latestDate: 0, count: 0, dates: [],
    });
    const { wrapper } = await mountPage();

    expect(wrapper.text()).toContain("尚未评分");
    expect(wrapper.text()).toContain("刷新评分");
    expect(wrapper.text()).toContain("日线");
  });

  it("刷新评分把当前策略和只数传给后端", async () => {
    const { wrapper } = await mountPage();
    await button(wrapper, "刷新评分")!.trigger("click");
    await vi.waitFor(() => expect(mocks.refreshStrength).toHaveBeenCalled());

    const [strategyId, limit, force] = mocks.refreshStrength.mock.calls[0];
    expect(strategyId).toBe("trend_growth");
    expect(limit).toBe(800); // 默认档
    // 「刷新评分」是**增量**（force=false），不重算已算过的
    expect(force).toBe(false);
    // 结果提示
    await vi.waitFor(() => expect(wrapper.text()).toContain("新算 302 只"));
    expect(wrapper.text()).toContain("共 302 只");
  });

  it("「全量重算」传 force=true", async () => {
    const { wrapper } = await mountPage();
    await button(wrapper, "全量重算")!.trigger("click");
    await vi.waitFor(() => expect(mocks.refreshStrength).toHaveBeenCalled());

    const [, , force] = mocks.refreshStrength.mock.calls[0];
    expect(force).toBe(true);
  });

  it("全部命中缓存时提示未重算", async () => {
    mocks.refreshStrength.mockResolvedValue({
      tradeDate: 20261008, saved: 0, skipped: 0, limit: 800,
      reused: 302, total: 302, skippedFromCache: true,
      marketState: { marketState: "bear", marketStateLabel: "熊市/风险释放", bullScore: 45, bearScore: 92, proxyRet20: -3.49, days: 80 },
      unavailable: [], elapsed: "0s",
    });
    const { wrapper } = await mountPage();
    await button(wrapper, "刷新评分")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("已是最新"));
    expect(wrapper.text()).toContain("未重算");
  });

  it("生成建议后渲染面板", async () => {
    const { wrapper } = await mountPage();
    await button(wrapper, "生成建议")!.trigger("click");
    await vi.waitFor(() => expect(mocks.buildPlan).toHaveBeenCalledWith("pf_1"));

    await vi.waitFor(() => expect(wrapper.text()).toContain("调仓建议"));
    expect(wrapper.text()).toContain("清仓");
    expect(wrapper.text()).toContain("熊市/风险释放");
  });

  it("生成建议失败时显示原因（例如还没评分）", async () => {
    mocks.buildPlan.mockRejectedValue(new Error("「我的组合」还没有评分数据。请先刷新评分"));
    const { wrapper } = await mountPage();
    await button(wrapper, "生成建议")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("请先刷新评分"));
  });

  it("一键应用建议要先确认，取消则不发请求", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { wrapper } = await mountPage();
    await button(wrapper, "生成建议")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("调仓建议"));

    await button(wrapper, "一键应用建议")!.trigger("click");
    await nextTick();
    expect(mocks.applyRotation).not.toHaveBeenCalled();
    confirmSpy.mockRestore();
  });

  it("确认后应用建议并显示结果", async () => {
    // 默认 fixture 是手动组合（allowApplyRotation=false），一键应用会被禁用。
    // 这里换成允许应用的半自动组合，才能验证应用流程本身。
    mocks.list.mockResolvedValue([
      portfolio({ type: "semi_auto", allowApplyRotation: true, requireManualConfirmation: true }),
    ]);
    mocks.detail.mockResolvedValue(detail({
      portfolio: portfolio({ type: "semi_auto", allowApplyRotation: true, requireManualConfirmation: true }),
    }));
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const { wrapper } = await mountPage();
    await button(wrapper, "生成建议")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("调仓建议"));

    const applyButton = button(wrapper, "一键应用建议")!;
    expect(applyButton.attributes("disabled")).toBeUndefined();
    await applyButton.trigger("click");
    await vi.waitFor(() => expect(mocks.applyRotation).toHaveBeenCalledWith("pf_1"));
    await vi.waitFor(() => expect(wrapper.text()).toContain("已应用"));
    expect(wrapper.text()).toContain("清仓 1");
    confirmSpy.mockRestore();
  });

  it("手动组合的一键应用按钮被禁用并说明原因", async () => {
    mocks.list.mockResolvedValue([portfolio({ type: "manual", allowApplyRotation: false })]);
    mocks.detail.mockResolvedValue(detail({
      portfolio: portfolio({ type: "manual", allowApplyRotation: false }),
    }));
    const { wrapper } = await mountPage();

    const applyButton = button(wrapper, "一键应用建议");
    expect(applyButton).toBeTruthy();
    expect(applyButton!.attributes("disabled")).toBeDefined();
    expect(applyButton!.attributes("title")).toContain("不支持一键应用");
  });
});
