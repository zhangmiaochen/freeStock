import { mount } from "@vue/test-utils";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { nextTick } from "vue";

import type { DataLakeInfo } from "@/lib/backend";

const mocks = vi.hoisted(() => ({
  getDataLakeStatus: vi.fn(),
  setDataLakeRoot: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    getDataLakeStatus: mocks.getDataLakeStatus,
    setDataLakeRoot: mocks.setDataLakeRoot,
  },
  errorMessage: (err: unknown) => String(err),
}));

import DataLakeCard from "@/components/settings/DataLakeCard.vue";

function dataset(
  name: string,
  exists: boolean,
  rows = 0,
  partitions: { key: string; value: string; files: number }[] = [],
) {
  return { name, exists, files: exists ? 1 : 0, rows, latestDate: 0, partitions, error: "" };
}

function lakeInfo(overrides: Partial<DataLakeInfo> = {}): DataLakeInfo {
  return {
    root: "D:\\workspace\\stockTool\\CNEquity\\data\\cnequity",
    available: true,
    detail: "数据湖可用，已有 3 个数据集",
    datasets: ["corporate_actions", "instruments", "trading_calendar"],
    status: [
      dataset("financial_statement_items", false),
      dataset("earnings_disclosure_schedule", false),
      dataset("valuation_metrics", false),
      dataset("share_structure", false),
      dataset("shareholder_counts", false),
      dataset("top_holders", false),
    ],
    configPath: "C:\\Users\\x\\AppData\\Roaming\\FreeStock\\config.json",
    ...overrides,
  } as DataLakeInfo;
}

async function mountCard() {
  const wrapper = mount(DataLakeCard);
  await vi.waitFor(() => expect(mocks.getDataLakeStatus).toHaveBeenCalled());
  await nextTick();
  await nextTick();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("数据湖卡片", () => {
  it("数据湖存在但财报未入库时，说清楚缺什么、怎么办", async () => {
    mocks.getDataLakeStatus.mockResolvedValue(lakeInfo());

    const wrapper = await mountCard();
    const text = wrapper.text();

    expect(text).toContain("cnequity 数据湖");
    expect(text).toContain("已连接");
    expect(text).toContain("财报尚未入库");
    // 已经入库的数据集要列出来
    expect(text).toContain("corporate_actions");
    expect(text).toContain("instruments");
    // 六个财务数据集都要出现在表里
    for (const name of [
      "financial_statement_items",
      "valuation_metrics",
      "share_structure",
      "shareholder_counts",
      "top_holders",
    ]) {
      expect(text).toContain(name);
    }
    // 给出可操作的下一步，而不是只说失败
    expect(text).toContain("phase2c_daily_bars_backfill");
    // 根目录回填到输入框
    const input = wrapper.find("input");
    expect((input.element as HTMLInputElement).value).toContain("CNEquity");
  });

  it("财报已入库时显示为已入库并统计行数/分区", async () => {
    mocks.getDataLakeStatus.mockResolvedValue(
      lakeInfo({
        detail: "来自 cnequity 数据湖，共 123456 行、2 个分区",
        status: [
          dataset("financial_statement_items", true, 123456, [
            { key: "report_period", value: "2024Q4", files: 1 },
            { key: "report_period", value: "2025Q1", files: 1 },
          ]),
          dataset("share_structure", true, 8000, [{ key: "change_date", value: "2025", files: 1 }]),
        ],
      }),
    );

    const wrapper = await mountCard();
    const text = wrapper.text();

    expect(text).toContain("财报已入库");
    expect(text).toContain("12.3 万行");
    expect(text).toContain("2024Q4");
    expect(text).toContain("2025Q1");
    expect(text).not.toContain("财报尚未入库");
  });

  it("没有数据湖时提示未连接并给出设置办法", async () => {
    mocks.getDataLakeStatus.mockResolvedValue(
      lakeInfo({
        root: "",
        available: false,
        detail: "未找到 cnequity 数据湖。设置数据湖根目录（含 curated 子目录的那一级），或设置环境变量 FREESTOCK_CNLAKE。",
        datasets: [],
        status: [dataset("financial_statement_items", false)],
      }),
    );

    const wrapper = await mountCard();
    const text = wrapper.text();

    expect(text).toContain("未连接");
    expect(text).toContain("FREESTOCK_CNLAKE");
    expect(text).toContain("财报尚未入库");
  });

  it("应用新路径会调用后端并刷新状态", async () => {
    mocks.getDataLakeStatus.mockResolvedValue(lakeInfo());
    mocks.setDataLakeRoot.mockResolvedValue(lakeInfo({ root: "E:\\other\\cnequity" }));

    const wrapper = await mountCard();
    const input = wrapper.find("input");
    await input.setValue("E:\\other\\cnequity");

    const applyButton = wrapper.findAll("button").find((b) => b.text().includes("应用"));
    expect(applyButton).toBeTruthy();
    await applyButton!.trigger("click");

    await vi.waitFor(() => expect(mocks.setDataLakeRoot).toHaveBeenCalledWith("E:\\other\\cnequity"));
    await nextTick();
    expect(wrapper.text()).toContain("已切换数据湖");
  });
});
