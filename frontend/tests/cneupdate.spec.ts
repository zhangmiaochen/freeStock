import { describe, expect, it, vi, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";

/**
 * cne 数据更新卡片的组件测试。
 *
 * 这些用例盯的是**行为契约**，不是像素：
 *  - 新鲜度落后时，表格和「逐日补跑」按钮要出现；已最新时不出现按钮
 *  - 「补抓落后」和「补指定交易日」是两个不同入口（方案 §2.2/§2.3：
 *    stale 不覆盖日行情、非交易日跑普通日更会跳过行情组，不能只给一个按钮）
 *  - 后端拒绝的理由要原样显示（方案 §5.4：被拒时提示明确，不是静默排队）
 *  - 设置里的 warnings 要显示出来，哪怕保存是"成功"的
 */

// ⚠ 必须用 vi.hoisted：`vi.mock` 的工厂会被提升到文件顶部执行，
// 那时普通的 `const mocks` 还没初始化 → 整个文件加载失败。
const mocks = vi.hoisted(() => ({
  updateStatus: vi.fn(),
  catchUpState: vi.fn(),
  checkFreshness: vi.fn(),
  getUpdatePrefs: vi.fn(),
  setUpdatePrefs: vi.fn(),
  manualUpdate: vi.fn(),
  startCatchUp: vi.fn(),
  cancelCatchUp: vi.fn(),
  cancelUpdate: vi.fn(),
  scheduleState: vi.fn(),
  latestRunResult: vi.fn(),
  previewSchedule: vi.fn(),
  applySchedule: vi.fn(),
  autoUpdateStatus: vi.fn(),
  setAutoUpdate: vi.fn(),
  fixScheduleSettings: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    updateStatus: mocks.updateStatus,
    catchUpState: mocks.catchUpState,
    checkFreshness: mocks.checkFreshness,
    getUpdatePrefs: mocks.getUpdatePrefs,
    setUpdatePrefs: mocks.setUpdatePrefs,
    manualUpdate: mocks.manualUpdate,
    startCatchUp: mocks.startCatchUp,
    cancelCatchUp: mocks.cancelCatchUp,
    cancelUpdate: mocks.cancelUpdate,
    scheduleState: mocks.scheduleState,
    latestRunResult: mocks.latestRunResult,
    previewSchedule: mocks.previewSchedule,
    applySchedule: mocks.applySchedule,
    autoUpdateStatus: mocks.autoUpdateStatus,
    setAutoUpdate: mocks.setAutoUpdate,
    fixScheduleSettings: mocks.fixScheduleSettings,
  },
  errorMessage: (e: unknown) => String(e),
}));

import DataUpdateCard from "@/components/settings/DataUpdateCard.vue";

function freshnessBehind(days: number[]) {
  return {
    lastTrading: 20261009,
    maxBehind: days.length,
    behindDays: days,
    summary: `落后 ${days.length} 个交易日 —— 普通日更和「补抓落后」都不会补行情缺口，只有 --trade-date 能补上。`,
    datasets: [
      { name: "daily_bars", latestDate: 20261008, lastTrading: 20261009, behindDays: days, missing: false },
      { name: "index_bars", latestDate: 20261008, lastTrading: 20261009, behindDays: days, missing: false },
      { name: "adj_factors", latestDate: 0, lastTrading: 20261009, behindDays: [], missing: true },
    ],
  };
}

function freshnessOk() {
  return {
    lastTrading: 20261009,
    maxBehind: 0,
    behindDays: [],
    summary: "数据已是最新（最后交易日 20261009）",
    datasets: [
      { name: "daily_bars", latestDate: 20261009, lastTrading: 20261009, behindDays: [], missing: false },
    ],
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.updateStatus.mockResolvedValue({ running: false, kind: "", elapsedSec: 0, logTail: [], steps: [], outcome: "" });
  mocks.catchUpState.mockResolvedValue({ running: false, dates: [], current: 0, done: 0, failed: [], skipped: [], message: "" });
  mocks.getUpdatePrefs.mockResolvedValue({
    prefs: { autoDaily: false, dailyTime: "17:30", autoStale: false, staleTime: "21:00", catchUpOnStart: true, projectDir: "D:\\CNEquity" },
    saved: true,
    configPath: "D:\\FreeStock\\bin\\config.json",
  });
  mocks.checkFreshness.mockResolvedValue(freshnessOk());
  // 任务名现在是从后端读的（不再硬编码在模板里），所以 mock 必须给
  mocks.scheduleState.mockResolvedValue({
    daily: { name: "cne-daily-update", registered: true, nextRun: "2026/10/10 17:30:00", status: "Ready", lastResult: "0", lastRun: "", command: "" },
    stale: { name: "cne-daily-stale", registered: true, nextRun: "2026/10/10 21:00:00", status: "Ready", lastResult: "0", lastRun: "", command: "" },
    wrapperPath: "D:\\CNEquity\\cne-daily.cmd",
    summary: "两个任务都已注册",
    note: "",
  });
  // 最近一次运行：默认给一份"有数据 + 上游暂无数据"的真实形态
  // （数值取自本机 manifest.db 的实测记录）
  mocks.latestRunResult.mockResolvedValue({
    run: { runId: "r1", jobName: "daily", status: "degraded", startedAt: "", finishedAt: "", rowsWritten: 7866, executionStatus: "completed", coverageStatus: "partial" },
    datasets: [
      { dataset: "compact", stage: "aggregated", status: "success", criticality: "", rowsWritten: 3933, usableResult: true },
      { dataset: "fund_flow", stage: "aggregated", status: "failed", criticality: "", rowsWritten: 0, usableResult: false, reasonCode: "source_transient" },
    ],
    succeeded: ["compact"],
    failed: [],
    unavailable: ["fund_flow"],
    rowsWritten: 7866,
    conclusion: "本次运行共 2 个数据集，写入 7866 行；1 个有数据；1 个上游暂无数据（fund_flow）—— 这类明天会自动重试，不用手动补。（cne 进程状态：degraded）",
  });

  mocks.autoUpdateStatus.mockResolvedValue({
    enabled: true,
    dailyEnabled: true,
    staleEnabled: true,
    startWhenAvailable: true,
    summary: "自动更新已开启，且错过时间后会补跑",
    warning: "",
  });
  mocks.setAutoUpdate.mockResolvedValue({
    enabled: false, dailyEnabled: false, staleEnabled: false,
    startWhenAvailable: true, summary: "自动更新已关闭", warning: "",
  });
  mocks.previewSchedule.mockResolvedValue({
    changes: [
      { task: "cne-daily-update", action: "change", from: "17:30", to: "18:00" },
      { task: "cne-daily-stale", action: "keep", from: "21:00", to: "21:00", reason: "时间已经是目标值" },
    ],
    commands: ['schtasks /change /tn "cne-daily-update" /st 18:00'],
    warnings: [],
    hasChanges: true,
    summary: "将要改动 cne-daily-update：17:30 → 18:00",
  });
  mocks.applySchedule.mockResolvedValue({
    changes: [{ task: "cne-daily-update", action: "change", from: "17:30", to: "18:00", reason: "已改为 18:00" }],
    commands: ['schtasks /change /tn "cne-daily-update" /st 18:00'],
    warnings: [],
    hasChanges: true,
    summary: "计划任务时间已更新：cne-daily-update=18:00",
  });
});

async function mountCard() {
  const wrapper = mount(DataUpdateCard);
  await vi.waitFor(() => expect(mocks.checkFreshness).toHaveBeenCalled());
  await new Promise((r) => setTimeout(r, 0));
  return wrapper;
}

describe("cne 数据更新卡片", () => {
  it("已最新时不显示「逐日补跑」按钮，并显示已是最新", async () => {
    const wrapper = await mountCard();
    expect(wrapper.text()).toContain("已是最新");
    expect(wrapper.text()).not.toContain("逐日补跑");
  });

  it("新鲜度落后时显示落后天数、表格与「逐日补跑」按钮", async () => {
    mocks.checkFreshness.mockResolvedValue(freshnessBehind([20261009]));
    const wrapper = await mountCard();
    const text = wrapper.text();
    expect(text).toContain("落后 1 个交易日");
    expect(text).toContain("daily_bars");
    expect(text).toContain("2026-10-08");
    expect(text).toContain("逐日补跑");
    // 未入库的数据集要明确标出来，而不是显示成"落后 0 天"
    expect(text).toContain("未入库");
  });

  it("三个手动入口都在（stale 与指定交易日不能合并成一个按钮）", async () => {
    const wrapper = await mountCard();
    const text = wrapper.text();
    expect(text).toContain("更新到今天");
    expect(text).toContain("补抓落后");
    expect(text).toContain("补指定交易日");
    // 卡片上必须写明这条坑，否则用户会拿 stale 去补行情
    expect(text).toContain("不覆盖日行情");
  });

  it("点「逐日补跑」调用 startCatchUp 并显示后端返回的说明", async () => {
    mocks.checkFreshness.mockResolvedValue(freshnessBehind([20261008, 20261009]));
    mocks.startCatchUp.mockResolvedValue({
      started: true,
      dates: [20261008, 20261009],
      message: "已开始逐日补跑 2 个交易日",
      state: { running: true, dates: [20261008, 20261009], current: 0, done: 0, failed: [], skipped: [], message: "" },
    });
    const wrapper = await mountCard();
    const btn = wrapper.findAll("button").find((b) => b.text().includes("逐日补跑"));
    expect(btn).toBeTruthy();
    await btn!.trigger("click");
    await vi.waitFor(() => expect(mocks.startCatchUp).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(wrapper.text()).toContain("已开始逐日补跑 2 个交易日");
  });

  it("后端拒绝的理由要原样显示（不是静默排队）", async () => {
    mocks.manualUpdate.mockRejectedValue(new Error("已有一个数据更新在进行中（类型 full，已运行 3s）"));
    const wrapper = await mountCard();
    const btn = wrapper.findAll("button").find((b) => b.text().includes("更新到今天"));
    await btn!.trigger("click");
    await vi.waitFor(() => expect(mocks.manualUpdate).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(wrapper.text()).toContain("已有一个数据更新在进行中");
  });

  it("设置保存成功但带 warnings 时，必须把 warnings 显示出来", async () => {
    mocks.setUpdatePrefs.mockResolvedValue({
      prefs: { autoDaily: false, dailyTime: "17:30", autoStale: false, staleTime: "21:00", catchUpOnStart: true, projectDir: "D:\\nope" },
      saved: true,
      configPath: "c.json",
      warnings: ["工程目录不存在：D:\\nope"],
    });
    const wrapper = await mountCard();
    const btn = wrapper.findAll("button").find((b) => b.text().includes("保存设置"));
    await btn!.trigger("click");
    await vi.waitFor(() => expect(mocks.setUpdatePrefs).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    // 保存是"成功"的，但问题必须让用户看到 —— 否则他点更新才失败
    expect(wrapper.text()).toContain("工程目录不存在");
  });

  it("自动更新有真开关，并显示当前实际生效的任务，并显示当前实际生效的任务", async () => {
    const wrapper = await mountCard();
    const text = wrapper.text();
    // 说明为什么没有开关 —— 而不是放一个点了没反应的控件
    expect(text).not.toContain("开关尚未开放");
    expect(text).toContain("自动更新");
    // 当前实际生效的两个任务要从后端读出来显示（不再硬编码在模板里）
    expect(text).toContain("cne-daily-update");
    expect(text).toContain("cne-daily-stale");
    expect(text).toContain("下次");
    expect(text).toContain("cne-daily.cmd");
  });

  it("计划任务未注册时显示「未注册」而不是错误的下次运行时间", async () => {
    mocks.scheduleState.mockResolvedValue({
      daily: { name: "cne-daily-update", registered: false, nextRun: "", status: "", lastResult: "", lastRun: "", command: "" },
      stale: { name: "cne-daily-stale", registered: false, nextRun: "", status: "", lastResult: "", lastRun: "", command: "" },
      wrapperPath: "",
      summary: "两个计划任务都未注册",
      note: "",
    });
    const wrapper = await mountCard();
    const text = wrapper.text();
    expect(text).toContain("未注册");
    expect(text).toContain("两个计划任务都未注册");
    expect(text).not.toContain("下次 2026");
  });

  it("kind 传参正确：full / stale / date（含日期）", async () => {
    mocks.manualUpdate.mockResolvedValue({ jobId: "j1", kind: "full", args: "", logPath: "", message: "ok" });
    const wrapper = await mountCard();

    await wrapper.findAll("button").find((b) => b.text().includes("更新到今天"))!.trigger("click");
    await vi.waitFor(() => expect(mocks.manualUpdate).toHaveBeenCalledWith("full", ""));
    await new Promise((r) => setTimeout(r, 0));

    await wrapper.findAll("button").find((b) => b.text().includes("补抓落后"))!.trigger("click");
    await vi.waitFor(() => expect(mocks.manualUpdate).toHaveBeenCalledWith("stale", ""));
  });
});
describe("最近一次运行结果（退出码不可信）", () => {
  it("显示每个数据集写入的行数与说明，并把上游暂无数据与真失败区分开", async () => {
    const wrapper = await mountCard();
    const text = wrapper.text();
    expect(text).toContain("最近一次运行结果");
    expect(text).toContain("compact");
    expect(text).toContain("3,933");
    expect(text).toContain("fund_flow");
    expect(text).toContain("source_transient");
    expect(text).toContain("明天会自动重试");
    // 有上游无数据、但没有本地失败时，不该报"失败"
    expect(text).toContain("没有本地失败");
  });

  it("有真正失败的数据集时才显示失败徽章", async () => {
    mocks.latestRunResult.mockResolvedValue({
      run: { runId: "r2", jobName: "daily", status: "failed", startedAt: "", finishedAt: "", rowsWritten: 100, executionStatus: "", coverageStatus: "" },
      datasets: [
        { dataset: "hot_rank", stage: "aggregated", status: "success", criticality: "", rowsWritten: 100, usableResult: true },
        { dataset: "fund_flow", stage: "aggregated", status: "failed", criticality: "", rowsWritten: 0, usableResult: false, errorCode: "E_TIMEOUT", errorMessage: "timed out" },
      ],
      succeeded: ["hot_rank"],
      failed: ["fund_flow"],
      unavailable: [],
      rowsWritten: 100,
      conclusion: "1 个失败（fund_flow）",
    });
    const wrapper = await mountCard();
    expect(wrapper.text()).toContain("1 个数据集失败");
    expect(wrapper.text()).toContain("E_TIMEOUT");
  });

  it("卡片要写明进程状态与数据结果不是一回事", async () => {
    const wrapper = await mountCard();
    expect(wrapper.text()).toContain("不是一回事");
    expect(wrapper.text()).toContain("exit=1");
  });

  it("manifest 读不到时（如未配置数据湖）不显示该区块，也不报错", async () => {
    mocks.latestRunResult.mockRejectedValue(new Error("找不到运行清单"));
    const wrapper = await mountCard();
    expect(wrapper.text()).not.toContain("最近一次运行结果");
  });
});
describe("计划任务改动：必须两步，确认前绝不执行", () => {
  it("只点「预览改动」时**不会**调用 applySchedule", async () => {
    const wrapper = await mountCard();
    const btn = wrapper.findAll("button").find((b) => b.text().includes("预览改动"));
    expect(btn).toBeTruthy();
    await btn!.trigger("click");
    await vi.waitFor(() => expect(mocks.previewSchedule).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    // ⚠ 这是本组最重要的断言：预览不得触发任何改动
    expect(mocks.applySchedule).not.toHaveBeenCalled();
  });

  it("预览时把**原始 schtasks 命令**原样显示出来", async () => {
    const wrapper = await mountCard();
    await wrapper.findAll("button").find((b) => b.text().includes("预览改动"))!.trigger("click");
    await vi.waitFor(() => expect(mocks.previewSchedule).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    const text = wrapper.text();
    expect(text).toContain("将要对你机器上的计划任务做以下改动");
    // 命令原文要能看到，而不是只给一句"将改时间"
    expect(text).toContain('schtasks /change /tn "cne-daily-update" /st 18:00');
    expect(text).toContain("17:30");
    expect(text).toContain("18:00");
    // 无需改动的任务要说清
    expect(text).toContain("无需改动");
    // 必须写明不会新建/删除
    expect(text).toContain("不会新建也不会删除");
  });

  it("确认后才执行，且 confirmed 传 true", async () => {
    const wrapper = await mountCard();
    await wrapper.findAll("button").find((b) => b.text().includes("预览改动"))!.trigger("click");
    await vi.waitFor(() => expect(mocks.previewSchedule).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));

    const confirm = wrapper.findAll("button").find((b) => b.text().includes("确认执行"));
    expect(confirm).toBeTruthy();
    await confirm!.trigger("click");
    await vi.waitFor(() => expect(mocks.applySchedule).toHaveBeenCalled());
    // 第二个参数必须是显式的 true
    expect(mocks.applySchedule.mock.calls[0][1]).toBe(true);
    await new Promise((r) => setTimeout(r, 0));
    expect(wrapper.text()).toContain("计划任务时间已更新");
  });

  it("点「放弃」后不执行，且确认区消失", async () => {
    const wrapper = await mountCard();
    await wrapper.findAll("button").find((b) => b.text().includes("预览改动"))!.trigger("click");
    await vi.waitFor(() => expect(mocks.previewSchedule).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(wrapper.text()).toContain("将要对你机器上的计划任务");

    await wrapper.findAll("button").find((b) => b.text().includes("放弃"))!.trigger("click");
    await new Promise((r) => setTimeout(r, 0));
    expect(mocks.applySchedule).not.toHaveBeenCalled();
    expect(wrapper.text()).not.toContain("将要对你机器上的计划任务");
    expect(wrapper.text()).toContain("没有执行任何操作");
  });

  it("没有改动时不显示「确认执行」按钮（避免无意义确认）", async () => {
    mocks.autoUpdateStatus.mockResolvedValue({
    enabled: true,
    dailyEnabled: true,
    staleEnabled: true,
    startWhenAvailable: true,
    summary: "自动更新已开启，且错过时间后会补跑",
    warning: "",
  });
  mocks.setAutoUpdate.mockResolvedValue({
    enabled: false, dailyEnabled: false, staleEnabled: false,
    startWhenAvailable: true, summary: "自动更新已关闭", warning: "",
  });
  mocks.previewSchedule.mockResolvedValue({
      changes: [{ task: "cne-daily-update", action: "keep", from: "17:30", to: "17:30", reason: "时间已经是目标值" }],
      commands: [],
      warnings: [],
      hasChanges: false,
      summary: "两个任务的时间都已经是目标值，无需改动。",
    });
    const wrapper = await mountCard();
    await wrapper.findAll("button").find((b) => b.text().includes("预览改动"))!.trigger("click");
    await vi.waitFor(() => expect(mocks.previewSchedule).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(wrapper.text()).toContain("无需改动");
    expect(wrapper.findAll("button").find((b) => b.text().includes("确认执行"))).toBeUndefined();
  });

  it("任务不存在时明确说不会创建", async () => {
    mocks.autoUpdateStatus.mockResolvedValue({
    enabled: true,
    dailyEnabled: true,
    staleEnabled: true,
    startWhenAvailable: true,
    summary: "自动更新已开启，且错过时间后会补跑",
    warning: "",
  });
  mocks.setAutoUpdate.mockResolvedValue({
    enabled: false, dailyEnabled: false, staleEnabled: false,
    startWhenAvailable: true, summary: "自动更新已关闭", warning: "",
  });
  mocks.previewSchedule.mockResolvedValue({
      changes: [{ task: "cne-daily-update", action: "not-registered", to: "17:30", reason: "任务不存在。本功能不会创建它" }],
      commands: [],
      warnings: ["任务不存在。本功能不会创建它"],
      hasChanges: false,
      summary: "没有需要改的地方",
    });
    const wrapper = await mountCard();
    await wrapper.findAll("button").find((b) => b.text().includes("预览改动"))!.trigger("click");
    await vi.waitFor(() => expect(mocks.previewSchedule).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(wrapper.text()).toContain("不会创建");
  });
});
describe("自动更新开关", () => {
  it("开启且错过会补跑时，显示已开启、不显示修复按钮", async () => {
    const wrapper = await mountCard();
    const text = wrapper.text();
    expect(text).toContain("自动更新");
    expect(text).toContain("已开启");
    expect(text).not.toContain("错过时间不会补跑");
    expect(wrapper.findAll("button").find((b) => b.text().includes("修复"))).toBeUndefined();
    expect(text).toContain("关闭自动更新");
  });

  it("**StartWhenAvailable 没打开时高亮警告并给修复按钮**", async () => {
    mocks.autoUpdateStatus.mockResolvedValue({
      enabled: true,
      dailyEnabled: true,
      staleEnabled: true,
      startWhenAvailable: false,
      summary: "自动更新已开启，但**错过时间后不会补跑**",
      warning: "两个任务都启用了，但「错过计划开始时间后尽快启动」没打开。如果每天 17:30 / 21:00 时电脑关着或休眠，那一天会被直接跳过，而任务列表看上去一切正常。",
    });
    const wrapper = await mountCard();
    const text = wrapper.text();
    expect(text).toContain("错过时间不会补跑");
    expect(text).toContain("直接跳过");
    const fix = wrapper.findAll("button").find((b) => b.text().includes("修复"));
    expect(fix).toBeTruthy();
    await fix!.trigger("click");
    await vi.waitFor(() => expect(mocks.fixScheduleSettings).toHaveBeenCalled());
  });

  it("点「关闭自动更新」会调用 setAutoUpdate(false)", async () => {
    const wrapper = await mountCard();
    await wrapper.findAll("button").find((b) => b.text().includes("关闭自动更新"))!.trigger("click");
    await vi.waitFor(() => expect(mocks.setAutoUpdate).toHaveBeenCalledWith(false));
    await new Promise((r) => setTimeout(r, 0));
    expect(wrapper.text()).toContain("不会再自动更新");
  });

  it("已关闭时显示「开启自动更新」，点了传 true", async () => {
    mocks.autoUpdateStatus.mockResolvedValue({
      enabled: false, dailyEnabled: false, staleEnabled: false,
      startWhenAvailable: true, summary: "自动更新已关闭", warning: "任务存在但被禁用，到点不会跑。",
    });
    mocks.setAutoUpdate.mockResolvedValue({
      enabled: true, dailyEnabled: true, staleEnabled: true,
      startWhenAvailable: true, summary: "自动更新已开启，且错过时间后会补跑", warning: "",
    });
    const wrapper = await mountCard();
    expect(wrapper.text()).toContain("已关闭");
    await wrapper.findAll("button").find((b) => b.text().includes("开启自动更新"))!.trigger("click");
    await vi.waitFor(() => expect(mocks.setAutoUpdate).toHaveBeenCalledWith(true));
  });
});
