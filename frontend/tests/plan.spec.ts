import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick } from "vue";

import PlanPanel from "@/components/portfolio/PlanPanel.vue";

function row(overrides: Record<string, unknown> = {}) {
  return {
    tsCode: "600519",
    name: "贵州茅台",
    action: "clear",
    actionLabel: "清仓",
    score: 26.42,
    scoreGap: 0,
    rankNo: 0,
    candidateTsCode: "",
    candidateName: "",
    candidateScore: 0,
    expectedImprovement: 0,
    reason: "结构破坏且不可修复，清仓",
    sourceType: "holding",
    currentLayers: 1,
    targetLayers: 0,
    layersDelta: -1,
    decisionState: "trend_break_exit",
    isExecutable: true,
    requiresConfirmation: true,
    executionMode: "confirm_required",
    executionModeLabel: "人工确认后执行",
    actionPrefix: "待确认",
    ...overrides,
  };
}

function plan(overrides: Record<string, unknown> = {}) {
  return {
    portfolioId: "pf_1",
    portfolioName: "核心持仓",
    strategyId: "trend_growth",
    strategyLabel: "趋势成长",
    config: {
      strategyId: "trend_growth",
      marketState: "bear",
      totalLayers: 10,
      targetPositionCount: 3,
      readyScore: 68,
      candidateScoreFloor: 60,
      maxLayersPerStock: 3,
      allowNewAdds: true,
      requireCandidateSignal: false,
      replaceScoreGap: 20,
      warningScore: 60,
      reduceScore: 45,
      clearScore: 35,
      maxReplaceCount: 1,
    },
    marketState: {
      marketState: "bear",
      marketStateLabel: "熊市/风险释放",
      bullScore: 45,
      bearScore: 92,
      proxyRet20: -3.49,
      days: 80,
    },
    strengthDate: 20261008,
    plan: {
      rows: [
        row(),
        row({
          tsCode: "000001", name: "平安银行", action: "observe", actionLabel: "观察",
          score: 59.36, currentLayers: 1, targetLayers: 1, layersDelta: 0,
          decisionState: "score_observe", isExecutable: false, actionPrefix: "",
          reason: "评分低于预警线，观察",
        }),
        row({
          tsCode: "601318", name: "中国平安", action: "reduce", actionLabel: "减仓",
          score: 40, currentLayers: 2, targetLayers: 1, layersDelta: -1,
          decisionState: "partial_rotate", reason: "换入更强标的",
          candidateTsCode: "300750", candidateScore: 72, scoreGap: 32,
          expectedImprovement: 32,
        }),
      ],
      compare: [
        {
          holdingTsCode: "601318", holdingName: "中国平安", holdingScore: 40,
          candidateTsCode: "300750", candidateName: "宁德时代", candidateScore: 72,
          totalScoreGap: 32, rsScoreGap: 8, repairScoreGap: 3,
          mainReason: "结构破坏，换入更强标的", expectedPortfolioUplift: 32,
          mode: "partial_rotate", rankNo: 1,
        },
      ],
      candidates: [],
      marketState: "bear",
      strategyId: "trend_growth",
      entryAllowed: true,
      notes: ["清仓 1 只", "减仓 1 只"],
    },
    holdings: [],
    candidates: [
      {
        tsCode: "300750", name: "宁德时代", totalScore: 72,
        trendScore: 20, structureScore: 12, rsScore: 11, capitalScore: 10, repairScore: 9,
        closePrice: 286.75, relativeStrength20d: 5.2, drawdownFromHigh120: -12.3,
        volumeRatio20: 1.2,
      },
    ],
    notes: [],
    ...overrides,
  };
}

async function mountPanel(data = plan()) {
  const wrapper = mount(PlanPanel, { props: { plan: data as never } });
  await nextTick();
  return wrapper;
}

function tabButton(wrapper: ReturnType<typeof mount>, label: string) {
  return wrapper.findAll("button").find((b) => b.text().includes(label));
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("调仓建议面板", () => {
  it("默认显示调仓动作，含动作标签与层数变化", async () => {
    const wrapper = await mountPanel();
    const text = wrapper.text();

    expect(text).toContain("调仓建议");
    expect(text).toContain("熊市/风险释放");
    expect(text).toContain("清仓");
    expect(text).toContain("观察");
    expect(text).toContain("减仓");
    // 层数变化：2 → 1
    expect(text).toContain("2 → 1");
    // 原因
    expect(text).toContain("结构破坏且不可修复，清仓");
  });

  it("显示评分日与阈值参数", async () => {
    const wrapper = await mountPanel();
    const text = wrapper.text();
    expect(text).toContain("20261008");
    expect(text).toContain("建仓线 68");
    expect(text).toContain("预警 60");
    expect(text).toContain("清仓 35");
  });

  it("把反直觉的口径写在界面上", async () => {
    const wrapper = await mountPanel();
    const text = wrapper.text();
    // 这三个是最容易误解的，必须有说明
    expect(text).toContain("兜底分支");
    expect(text).toContain("是");
    expect(text).toContain("状态");
    expect(text).toContain("急涨兑现");
  });

  it("可执行与不可执行的动作区分显示", async () => {
    const wrapper = await mountPanel();
    const text = wrapper.text();
    // 清仓是可执行 + 需确认
    expect(text).toContain("需确认");
    // observe 是"仅提醒"
    expect(text).toContain("仅提醒");
  });

  it("切换到候选池页签", async () => {
    const wrapper = await mountPanel();
    await tabButton(wrapper, "候选池")!.trigger("click");
    await nextTick();

    const text = wrapper.text();
    expect(text).toContain("300750");
    expect(text).toContain("宁德时代");
    expect(text).toContain("286.75");
    // 分项分
    expect(text).toContain("20.0");
    // 不该再显示调仓动作表的原因列
    expect(text).not.toContain("结构破坏且不可修复，清仓");
  });

  it("候选池为空时给出解释", async () => {
    const wrapper = await mountPanel(plan({ candidates: [] }));
    await tabButton(wrapper, "候选池")!.trigger("click");
    await nextTick();
    expect(wrapper.text()).toContain("候选池为空");
  });

  it("切换到持仓·候选对比页签", async () => {
    const wrapper = await mountPanel();
    await tabButton(wrapper, "持仓·候选对比")!.trigger("click");
    await nextTick();

    const text = wrapper.text();
    expect(text).toContain("中国平安");
    expect(text).toContain("宁德时代");
    expect(text).toContain("+32.00");
    expect(text).toContain("+8.00");
    expect(text).toContain("+3.00");
  });

  it("没有配对时说明为什么（只有真正配成对才写对比）", async () => {
    const data = plan();
    data.plan.compare = [];
    const wrapper = await mountPanel(data);
    await tabButton(wrapper, "持仓·候选对比")!.trigger("click");
    await nextTick();
    expect(wrapper.text()).toContain("只有真正配成对的持仓才会写对比");
  });

  it("点击候选代码可以跳 K 线", async () => {
    const wrapper = await mountPanel();
    await tabButton(wrapper, "候选池")!.trigger("click");
    await nextTick();

    const link = wrapper.findAll("button").find((b) => b.text().trim() === "300750");
    expect(link).toBeTruthy();
    await link!.trigger("click");
    expect(wrapper.emitted("openKline")).toBeTruthy();
    expect(wrapper.emitted("openKline")![0]).toEqual(["300750"]);
  });

  it("渲染后端给的动作顺序，不重排", async () => {
    const wrapper = await mountPanel();
    const firstRow = wrapper.findAll("tbody tr")[0];
    // 后端已按 CASE 排序，第一行应是 clear
    expect(firstRow.text()).toContain("清仓");
    expect(firstRow.text()).toContain("600519");
  });

  it("换手率/回撤按正负着色", async () => {
    const wrapper = await mountPanel();
    await tabButton(wrapper, "候选池")!.trigger("click");
    await nextTick();
    const html = wrapper.html();
    // rs20 = 5.2 → 正；回撤 -12.3 → 负
    expect(html).toContain("text-up");
    expect(html).toContain("text-down");
  });
});
