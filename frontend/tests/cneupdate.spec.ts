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

  it("自动更新的开关未开放时要写明原因，并显示当前实际生效的任务", async () => {
    const wrapper = await mountCard();
    const text = wrapper.text();
    // 说明为什么没有开关 —— 而不是放一个点了没反应的控件
    expect(text).toContain("自动更新的开关尚未开放");
    expect(text).toContain("双跑");
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
