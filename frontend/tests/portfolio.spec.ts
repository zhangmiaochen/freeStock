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
  refreshStrengthAll: vi.fn(),
  getStrengthSummary: vi.fn(),
  buildPlan: vi.fn(),
  applyRotation: vi.fn(),
  // 邮件订阅卡片在页面挂载时就会读设置
  getEmailPrefs: vi.fn(),
  saveEmailPrefs: vi.fn(),
  clearEmailPassword: vi.fn(),
  previewEmail: vi.fn(),
  sendTestEmail: vi.fn(),
  sendDailyEmailNow: vi.fn(),
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

/**
 * 邮件订阅设置的默认桩数据。
 *
 * 形状与 Go 侧 `EmailPrefsInfo` 一一对应（`prefs` 是脱敏后的设置：
 * **没有授权码原文**，只有 `passwordSet`）。
 */
function emailPrefs(overrides: Record<string, unknown> = {}) {
  return {
    enabled: false,
    host: "smtp.qq.com",
    port: 465,
    username: "me@qq.com",
    password: "",
    encryption: "auto",
    from: "",
    fromName: "",
    timeoutSeconds: 30,
    recipients: "a@example.com\nb@example.com",
    subject: "【FreeStock】{date} 调仓日报 · {portfolio}",
    sendTime: "17:00",
    weekdays: [1, 2, 3, 4, 5],
    scope: "active",
    portfolioId: "",
    // 发送前先刷评分：默认开（不刷的话建议可能基于旧评分）
    refreshScoresBeforeSend: true,
    scoreLimit: 0, // 0 = 全市场
    includeHoldings: true,
    includeCandidates: false,
    candidateLimit: 10,
    ...overrides,
  };
}

function emailInfo(overrides: Record<string, unknown> = {}) {
  return {
    prefs: emailPrefs(),
    passwordSet: true,
    passwordNote: "授权码以明文保存在 config.json 里（没有加密）。",
    configPath: "D:\\FreeStock\\config.json",
    recipients: ["a@example.com", "b@example.com"],
    invalidRecipients: [],
    schedule: {
      enabled: false,
      sendTime: "17:00",
      weekdays: [1, 2, 3, 4, 5],
      nextRun: "",
      nextRunHint: "定时发送已关闭",
      lastSentAt: "",
      lastStatus: "",
      lastError: "",
      lastRecipients: [],
      running: false,
    },
    ready: true,
    readyError: "",
    ...overrides,
  };
}

function resetEmailMocks() {
  mocks.getEmailPrefs.mockResolvedValue(emailInfo());
  mocks.saveEmailPrefs.mockImplementation((prefs: Record<string, unknown>) =>
    Promise.resolve(emailInfo({ prefs: { ...emailPrefs(), ...prefs } })),
  );
  mocks.clearEmailPassword.mockResolvedValue(
    emailInfo({ passwordSet: false, prefs: emailPrefs({ password: "" }) }),
  );
  mocks.previewEmail.mockResolvedValue({
    subject: "【FreeStock】2026-10-13 调仓日报 · 我的组合",
    html: "<html><body><b>调仓动作</b></body></html>",
    text: "调仓动作",
    portfolios: ["我的组合"],
    warnings: [],
  });
  mocks.sendTestEmail.mockResolvedValue({
    sent: true,
    recipients: ["a@example.com", "b@example.com"],
    subject: "【FreeStock】SMTP 测试邮件",
    portfolios: [],
    html: "",
    elapsed: "312ms",
    sentAt: "2026-10-13 17:00:00",
    warnings: [],
  });
  mocks.sendDailyEmailNow.mockResolvedValue({
    sent: true,
    recipients: ["a@example.com"],
    subject: "【FreeStock】2026-10-13 调仓日报",
    portfolios: ["我的组合"],
    html: "",
    elapsed: "450ms",
    sentAt: "2026-10-13 17:00:00",
    warnings: [],
  });
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
  resetEmailMocks();
  mocks.refreshStrengthAll.mockResolvedValue({
    tradeDate: 20261008, limit: 800, elapsed: "10s",
    saved: 302, reused: 0, total: 302,
    strategies: [
      {
        strategyId: "trend_growth", strategyLabel: "趋势成长",
        saved: 302, reused: 0, total: 302, skipped: 0, fromCache: false,
      },
    ],
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
      noActionReasons: [],
      // 广度明细（原实现 policy/summary 里的 market_breadth_* 字段）
      breadth: {
        enabled: true, readyCount: 180, minReadyCount: 20,
        universeCount: 7553, readyScore: 70,
      },
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
    // 按钮上必须写明"全部策略" —— 用户此前看不出它会统一刷新
    expect(wrapper.text()).toContain("刷新评分（全部策略）");
    // 提示要说清评的是**全市场**（只评一部分会让广度闸门误判）
    expect(wrapper.text()).toContain("全市场");
  });

  it("刷新评分刷所有活跃策略并传只数", async () => {
    const { wrapper } = await mountPage();
    await button(wrapper, "刷新评分")!.trigger("click");
    await vi.waitFor(() => expect(mocks.refreshStrengthAll).toHaveBeenCalled());

    const [limit, force] = mocks.refreshStrengthAll.mock.calls[0];
    // ⚠ **默认必须是 0（全市场）** —— 原版根本没有 limit 参数，
    // 而广度闸门按"全市场评分≥70 的只数"统计；默认只评 800 只会让它
    // 被系统性低估、判成"广度不足"并删光买入信号（回测一笔都不成交）。
    expect(limit).toBe(0);
    // 「刷新评分」是**增量**（force=false），不重算已算过的；
    // 且**不再传策略 ID** —— 原版是一次刷所有活跃策略
    expect(force).toBe(false);
    // 结果提示
    await vi.waitFor(() => expect(wrapper.text()).toContain("新算 302 条"));
    expect(wrapper.text()).toContain("合计 302 条");
  });

  it("「全量重算」传 force=true", async () => {
    const { wrapper } = await mountPage();
    await button(wrapper, "全量重算")!.trigger("click");
    await vi.waitFor(() => expect(mocks.refreshStrengthAll).toHaveBeenCalled());

    const [, force] = mocks.refreshStrengthAll.mock.calls[0];
    expect(force).toBe(true);
  });

  it("全部命中缓存时提示未重算", async () => {
    mocks.refreshStrengthAll.mockResolvedValue({
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

/**
 * 每日调仓日报的邮件订阅卡片。
 *
 * 这里钉住的是几处**容易做错、且错了用户看不出来**的行为：
 *   - 授权码不回显（界面只能显示"已保存"）
 *   - 留空提交不能把已存的授权码清掉
 *   - 预览用 iframe 隔离，且能看到正文
 */
describe("组合页 · 调仓日报邮件", () => {
  async function openCard() {
    const { wrapper } = await mountPage();
    await vi.waitFor(() => expect(mocks.getEmailPrefs).toHaveBeenCalled());
    await nextTick();
    await button(wrapper, "设置")!.trigger("click");
    await nextTick();
    return wrapper;
  }

  it("挂载时就读取邮件设置，并显示收件人数量", async () => {
    const { wrapper } = await mountPage();
    await vi.waitFor(() => expect(mocks.getEmailPrefs).toHaveBeenCalledTimes(1));
    await nextTick();
    expect(wrapper.text()).toContain("调仓日报邮件");
    expect(wrapper.text()).toContain("2 个收件人");
    expect(wrapper.text()).toContain("未开启");
  });

  it("展开后显示定时摘要与下次发送提示", async () => {
    mocks.getEmailPrefs.mockResolvedValue(
      emailInfo({
        prefs: emailPrefs({ enabled: true }),
        schedule: {
          enabled: true, sendTime: "17:00", weekdays: [1, 2, 3, 4, 5],
          nextRun: "2026-10-14T17:00:00+08:00", nextRunHint: "明天 17:00 发送",
          lastSentAt: "2026-10-13 17:00:05", lastStatus: "成功", lastError: "",
          lastRecipients: ["a@example.com"], running: false,
        },
      }),
    );
    const { wrapper } = await mountPage();
    await vi.waitFor(() => expect(mocks.getEmailPrefs).toHaveBeenCalled());
    await nextTick();
    expect(wrapper.text()).toContain("定时已开启");
    expect(wrapper.text()).toContain("明天 17:00 发送");
    expect(wrapper.text()).toContain("上次：成功");

    // 展开后摘要里的发送日读法也要对（"工作日"而不是"周一、周二…"）
    await button(wrapper, "设置")!.trigger("click");
    await nextTick();
    expect(wrapper.text()).toContain("工作日");
  });

  it("授权码不回显：只提示已保存，输入框保持为空", async () => {
    const wrapper = await openCard();
    const password = wrapper.find('input[type="password"]');

    expect(password.exists()).toBe(true);
    expect((password.element as HTMLInputElement).value).toBe("");
    expect(wrapper.text()).toContain("已保存，留空表示不改");
  });

  it("保存时把输入框里的授权码一起提交", async () => {
    const wrapper = await openCard();
    await wrapper.find('input[type="password"]').setValue("new-auth-code");

    await button(wrapper, "保存设置")!.trigger("click");
    await vi.waitFor(() => expect(mocks.saveEmailPrefs).toHaveBeenCalled());

    const payload = mocks.saveEmailPrefs.mock.calls[0][0];
    expect(payload.password).toBe("new-auth-code");
    expect(payload.host).toBe("smtp.qq.com");
  });

  it("授权码留空提交 = 不改（不能把已存的清掉）", async () => {
    const wrapper = await openCard();

    await button(wrapper, "保存设置")!.trigger("click");
    await vi.waitFor(() => expect(mocks.saveEmailPrefs).toHaveBeenCalled());

    // 空串交给后端保留原值 —— 见 Go 侧 SaveEmailPrefs 的注释
    expect(mocks.saveEmailPrefs.mock.calls[0][0].password).toBe("");
  });

  it("改动未保存时会提示，避免以为已经生效", async () => {
    const wrapper = await openCard();
    expect(wrapper.text()).not.toContain("有未保存的修改");

    await wrapper.find('input[placeholder="smtp.qq.com"]').setValue("smtp.163.com");
    await nextTick();
    expect(wrapper.text()).toContain("有未保存的修改");

    await button(wrapper, "保存设置")!.trigger("click");
    await vi.waitFor(() => expect(mocks.saveEmailPrefs).toHaveBeenCalled());
    await nextTick();
    expect(wrapper.text()).not.toContain("有未保存的修改");
  });

  it("说明测试邮件发的是已保存的收件人", async () => {
    const wrapper = await openCard();
    // 用户容易以为"测试通过 = 我新填的地址也没问题"，所以这句话必须在
    expect(wrapper.text()).toContain("测试邮件会发给「已保存」的收件人");
    expect(wrapper.text()).toContain("a@example.com");
  });

  it("保存后显示下次发送时间", async () => {
    mocks.saveEmailPrefs.mockResolvedValue(
      emailInfo({
        prefs: emailPrefs({ enabled: true }),
        schedule: {
          enabled: true, sendTime: "17:00", weekdays: [1, 2, 3, 4, 5],
          nextRun: "", nextRunHint: "今天 17:00 发送",
          lastSentAt: "", lastStatus: "", lastError: "", lastRecipients: [], running: false,
        },
      }),
    );
    const wrapper = await openCard();
    await button(wrapper, "保存设置")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("今天 17:00 发送"));
  });

  it("保存失败时把后端的原因显示出来", async () => {
    mocks.saveEmailPrefs.mockRejectedValue(new Error("以下收件人地址不合法：bad-address"));
    const wrapper = await openCard();
    await button(wrapper, "保存设置")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("bad-address"));
  });

  it("非法收件人在展开后能被点名", async () => {
    mocks.getEmailPrefs.mockResolvedValue(
      emailInfo({
        recipients: ["a@example.com"],
        invalidRecipients: ["bad-address", "x@"],
      }),
    );
    const wrapper = await openCard();
    expect(wrapper.text()).toContain("以下地址不合法");
    expect(wrapper.text()).toContain("bad-address");
  });

  it("发送测试邮件走 sendTestEmail 并显示结果", async () => {
    const wrapper = await openCard();
    await button(wrapper, "发送测试邮件")!.trigger("click");
    await vi.waitFor(() => expect(mocks.sendTestEmail).toHaveBeenCalled());
    await vi.waitFor(() => expect(wrapper.text()).toContain("测试邮件已发送给"));
    expect(wrapper.text()).toContain("312ms");
  });

  it("测试失败时显示原因", async () => {
    mocks.sendTestEmail.mockRejectedValue(new Error("SMTP 认证失败：请确认填的是授权码"));
    const wrapper = await openCard();
    await button(wrapper, "发送测试邮件")!.trigger("click");
    await vi.waitFor(() => expect(wrapper.text()).toContain("测试发送失败"));
    expect(wrapper.text()).toContain("授权码");
  });

  it("预览用 iframe 隔离，并渲染出邮件正文", async () => {
    const wrapper = await openCard();
    await button(wrapper, "预览邮件")!.trigger("click");
    await vi.waitFor(() => expect(mocks.previewEmail).toHaveBeenCalled());

    const iframe = wrapper.find("iframe");
    expect(iframe.exists()).toBe(true);
    // 用 srcdoc 而不是 v-html：邮件自带表格样式，内联进页面会污染应用样式
    expect(iframe.attributes("srcdoc")).toContain("调仓动作");
    expect(wrapper.text()).toContain("主题：【FreeStock】2026-10-13");
  });

  it("立即发送日报会先确认，并展示发送范围", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const wrapper = await openCard();
    await button(wrapper, "立即发送日报")!.trigger("click");
    await vi.waitFor(() => expect(mocks.sendDailyEmailNow).toHaveBeenCalled());
    await vi.waitFor(() => expect(wrapper.text()).toContain("日报已发送给"));
    expect(wrapper.text()).toContain("我的组合");
    confirmSpy.mockRestore();
  });

  it("取消确认时不发送", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    const wrapper = await openCard();
    await button(wrapper, "立即发送日报")!.trigger("click");
    await nextTick();
    expect(mocks.sendDailyEmailNow).not.toHaveBeenCalled();
    confirmSpy.mockRestore();
  });

  it("配置不完整时给出不能发送的原因", async () => {
    mocks.getEmailPrefs.mockResolvedValue(
      emailInfo({ ready: false, readyError: "SMTP 服务器地址为空" }),
    );
    const wrapper = await openCard();
    expect(wrapper.text()).toContain("当前设置还不能发送");
    expect(wrapper.text()).toContain("SMTP 服务器地址为空");
  });

  it("上次失败的原因在收起状态也可见", async () => {
    mocks.getEmailPrefs.mockResolvedValue(
      emailInfo({
        schedule: {
          enabled: true, sendTime: "17:00", weekdays: [1],
          nextRun: "", nextRunHint: "明天 17:00 发送",
          lastSentAt: "2026-10-13 17:00:05", lastStatus: "失败",
          lastError: "SMTP 认证失败：请确认填的是授权码",
          lastRecipients: [], running: false,
        },
      }),
    );
    const { wrapper } = await mountPage();
    await vi.waitFor(() => expect(wrapper.text()).toContain("上次：失败"));
    // 没有展开也应该看得到失败原因，否则用户不知道为什么没收到
    expect(wrapper.text()).toContain("上次失败原因");
    expect(wrapper.text()).toContain("授权码");
  });

  it("明确写出应用没运行时不会发送", async () => {
    const wrapper = await openCard();
    expect(wrapper.text()).toContain("应用没运行时不会发送");
  });

  it("明文存储的提示来自后端而不是前端编的", async () => {
    const wrapper = await openCard();
    expect(wrapper.text()).toContain("授权码以明文保存在 config.json 里");
  });

  it("工作日快捷键把星期重置为周一至周五", async () => {
    mocks.getEmailPrefs.mockResolvedValue(
      emailInfo({ prefs: emailPrefs({ weekdays: [] }) }),
    );
    const wrapper = await openCard();
    expect(wrapper.text()).toContain("每天");

    // 逐个点亮周一~周五
    for (const label of ["一", "二", "三", "四", "五"]) {
      const dayButton = wrapper
        .findAll("button")
        .find((b) => b.text().trim() === label);
      expect(dayButton, `找不到周${label}的按钮`).toBeTruthy();
      await dayButton!.trigger("click");
    }
    await nextTick();
    expect(wrapper.text()).toContain("工作日");
  });

  // ---- 发送前先刷新评分 ----

  it("默认开启「发送前先刷新评分」，并写明为什么", async () => {
    const wrapper = await openCard();

    // 默认必须是开的：不刷的话发出去可能是一封"准时但过期"的邮件
    expect(wrapper.text()).toContain("发送前先刷新评分");
    // 理由要写在界面上，否则用户不知道为什么发信前要等
    expect(wrapper.text()).toContain("某一帧评分");
    expect(wrapper.text()).toContain("准时但过期");
    // 代价也要说（首次几十秒、已刷过则秒回）
    expect(wrapper.text()).toContain("首次执行要几十秒");
  });

  it("刷新评分的只数上限默认全市场", async () => {
    const wrapper = await openCard();
    const select = wrapper
      .findAll("select")
      .find((s) => s.text().includes("全市场（推荐）"));
    expect(select, "找不到「评分只数」下拉").toBeTruthy();
    expect((select!.element as HTMLSelectElement).value).toBe("0");
  });

  it("关掉刷新开关时明确警告建议可能基于旧评分", async () => {
    mocks.getEmailPrefs.mockResolvedValue(
      emailInfo({ prefs: emailPrefs({ refreshScoresBeforeSend: false }) }),
    );
    const wrapper = await openCard();

    // ⚠ 断言文案要够独特：页面上另有"定时发送已关闭"这类字样
    expect(wrapper.text()).toContain("邮件会用");
    expect(wrapper.text()).toContain("旧数据");
    // 关掉后不该再显示只数选择与代价说明
    expect(wrapper.text()).not.toContain("首次执行要几十秒");
  });

  it("保存时把刷新开关与只数上限一起提交", async () => {
    const wrapper = await openCard();

    // 改只数上限为 800
    const select = wrapper
      .findAll("select")
      .find((s) => s.text().includes("全市场（推荐）"))!;
    await select.setValue("800");

    await button(wrapper, "保存设置")!.trigger("click");
    await vi.waitFor(() => expect(mocks.saveEmailPrefs).toHaveBeenCalled());

    const payload = mocks.saveEmailPrefs.mock.calls[0][0];
    expect(payload.refreshScoresBeforeSend).toBe(true);
    expect(payload.scoreLimit).toBe(800);
  });

  it("读回已保存的只数上限", async () => {
    mocks.getEmailPrefs.mockResolvedValue(
      emailInfo({ prefs: emailPrefs({ refreshScoresBeforeSend: true, scoreLimit: 300 }) }),
    );
    const wrapper = await openCard();

    const select = wrapper
      .findAll("select")
      .find((s) => s.text().includes("全市场（推荐）"))!;
    expect((select.element as HTMLSelectElement).value).toBe("300");
  });

  it("读回关闭状态，重新打开后仍给出说明", async () => {
    mocks.getEmailPrefs.mockResolvedValue(
      emailInfo({ prefs: emailPrefs({ refreshScoresBeforeSend: false }) }),
    );
    const wrapper = await openCard();
    expect(wrapper.text()).toContain("邮件会用");

    const refreshToggle = wrapper
      .findAll("label")
      .find((l) => l.text().includes("发送前先刷新评分"))!
      .find('input[type="checkbox"]');
    await refreshToggle.setValue(true);
    await nextTick();

    expect(wrapper.text()).toContain("首次执行要几十秒");
    expect(wrapper.text()).not.toContain("邮件会用");
  });
});
