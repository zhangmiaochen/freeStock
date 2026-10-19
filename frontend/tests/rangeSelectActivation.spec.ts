import { describe, expect, it, vi } from "vitest";

/**
 * 钉住"框选到底有没有被启用"这件事。
 *
 * 背景（这次踩的坑，**只有真浏览器才测得出来**）：
 * 光在 option 里写 `brush` 是**不会**让框选工作的 —— ECharts 内部
 * `BrushView._updateController` 调的是
 * `brushController.enableBrush(brushModel.brushOption)`，而
 * `brushModel.brushOption` 只在 `setAreas` / `setBrushOption` 里才被赋值，
 * 光写 option 时它一直是 `{}`，于是 `enableBrush({brushType: undefined})`
 * 里的 `brushOption.brushType && ...` 为假，画笔永不启用。
 *
 * 症状极具误导性：`getOption().brush` 字段齐全、不报错、**拖拽毫无反应**。
 *
 * 所以组件里显式补了一次 `enableBrush`（见 KlineChart 的 activateBrush）。
 * 这里用 mock 掉的 echarts 断言"确实补了、且只在开关打开时补"。
 */

const mocks = vi.hoisted(() => {
  const enableBrush = vi.fn();
  const brushController = { enableBrush };
  const brushView = { _brushController: brushController };
  const brushModel = { type: "brush" };
  const setOption = vi.fn();
  const dispatchAction = vi.fn();
  let lastOption: Record<string, unknown> | null = null;

  const chart = {
    setOption: (option: Record<string, unknown>, opts?: unknown) => {
      lastOption = option;
      return setOption(option, opts);
    },
    dispatchAction,
    getModel: () => ({
      getComponent: (type: string) => (type === "brush" ? brushModel : undefined),
    }),
    getViewOfComponentModel: (model: unknown) => (model === brushModel ? brushView : undefined),
    getOption: () => lastOption ?? {},
    convertFromPixel: () => [0, 1],
    resize: vi.fn(),
    on: vi.fn(),
    dispose: vi.fn(),
    getZr: () => ({ on: vi.fn() }),
  };

  return { enableBrush, setOption, dispatchAction, chart, getLastOption: () => lastOption };
});

vi.mock("echarts/core", () => ({
  use: vi.fn(),
  init: () => mocks.chart,
}));
vi.mock("echarts/charts", () => ({
  BarChart: {}, CandlestickChart: {}, LineChart: {}, ScatterChart: {},
}));
vi.mock("echarts/components", () => ({
  AxisPointerComponent: {}, BrushComponent: {}, DataZoomComponent: {}, GridComponent: {},
  LegendComponent: {}, MarkAreaComponent: {}, MarkLineComponent: {}, TooltipComponent: {},
}));
vi.mock("echarts/renderers", () => ({ CanvasRenderer: {} }));

import { mount } from "@vue/test-utils";
import { nextTick } from "vue";

import KlineChart from "@/components/kline/KlineChart.vue";
import type { Candle } from "@/lib/indicators";

const CANDLES: Candle[] = Array.from({ length: 120 }, (_, i) => ({
  date: 20260101 + i,
  open: 100 + i,
  close: 100.5 + i,
  high: 101 + i,
  low: 99 + i,
  volume: 1000,
  amount: 1e6,
}));

async function mountChart(props: Record<string, unknown> = {}) {
  const wrapper = mount(KlineChart, {
    props: { candles: CANDLES, maConfigs: [], period: "day", ...props },
  });
  await nextTick();
  await nextTick();
  return wrapper;
}

describe("框选：brush 必须被显式启用", () => {
  it("开关打开时会调用 enableBrush，并带上 rect / single", async () => {
    mocks.enableBrush.mockClear();
    await mountChart({ showRangeSelect: true });

    expect(
      mocks.enableBrush,
      "没有调用 enableBrush —— brushModel.brushOption 是空的，框选不会响应任何拖拽",
    ).toHaveBeenCalled();

    const option = mocks.enableBrush.mock.calls[0][0] as Record<string, unknown>;
    expect(option.brushType).toBe("rect");
    expect(option.brushMode).toBe("single");
    expect(option.transformable).toBe(false);
  });

  it("选框样式用 zrender 的 fill / stroke（写 color/borderColor 不生效）", async () => {
    mocks.enableBrush.mockClear();
    await mountChart({ showRangeSelect: true });

    const option = mocks.enableBrush.mock.calls[0][0] as {
      brushStyle?: Record<string, unknown>;
    };
    const style = option.brushStyle ?? {};

    // ⚠ 实测：brushStyle 走的是 zrender 的样式键。写成 canvas 那套
    // color / borderColor **不报错也不生效**，选框会一直用 ECharts 的
    // 默认样式（浅灰填充），把那层填充盖在 K 线上就挡住了图。
    expect(style.stroke, "边框色要用 stroke").toBeTruthy();
    expect(style.fill, "填充色要用 fill").toBeTruthy();
    expect(style.color, "canvas 的 color 键在这里无效，不该出现").toBeUndefined();
    expect(style.borderColor, "canvas 的 borderColor 键在这里无效，不该出现").toBeUndefined();
    // 填充必须很淡，否则会挡住 K 线
    expect(String(style.fill)).toMatch(/0\.0\d\)$/);
  });

  it("开关关闭时会**显式停用**画笔（不能只清选框）", async () => {
    mocks.enableBrush.mockClear();
    await mountChart({ showRangeSelect: false });

    // ⚠ 关键：关闭时必须调 enableBrush(false)。
    // 只靠"下次 render 不再 enable"是关不掉的 —— 画笔会一直留着，
    // 拖拽照样出框，只是面板不显示，看起来像"拖了没反应"。
    expect(mocks.enableBrush, "关闭时必须显式停用画笔").toHaveBeenCalledWith(false);

    // 而且不该顺手把它 enable 起来
    const enabledCalls = mocks.enableBrush.mock.calls.filter((call) => call[0] !== false);
    expect(enabledCalls, "关闭时不该启用画笔").toHaveLength(0);
  });

  it("开关关闭时 inside 缩放不被禁用（否则用户连平移都用不了）", async () => {
    mocks.setOption.mockClear();
    await mountChart({ showRangeSelect: false });

    const option = mocks.getLastOption() as {
      dataZoom?: { type?: string; disabled?: boolean }[];
    };
    expect(option.dataZoom?.[0]?.type).toBe("inside");
    expect(option.dataZoom?.[0]?.disabled).toBe(false);
  });

  it("开关打开时禁用 inside 缩放（它和框选抢同一个左键拖拽）", async () => {
    await mountChart({ showRangeSelect: true });

    const option = mocks.getLastOption() as {
      dataZoom?: { type?: string; disabled?: boolean }[];
    };
    expect(option.dataZoom?.[0]?.type).toBe("inside");
    expect(
      option.dataZoom?.[0]?.disabled,
      "不禁用 inside 缩放的话，框选过程会同时把图平移走",
    ).toBe(true);
    // 滑条要留着，否则开着框选就没法缩放/平移了
    expect(option.dataZoom?.[1]?.type).toBe("slider");
    expect(option.dataZoom?.[1]?.disabled).toBeFalsy();
  });

  it("option 里仍然写着 brush 配置（否则 ECharts 不会建 brush 组件）", async () => {
    mocks.setOption.mockClear();
    await mountChart({ showRangeSelect: true });

    const option = mocks.getLastOption() as {
      brush?: { brushType?: string; brushMode?: string; xAxisIndex?: number };
    };
    expect(option.brush?.brushType).toBe("rect");
    expect(option.brush?.brushMode).toBe("single");
    expect(option.brush?.xAxisIndex).toBe(0);
  });

  it("开关从关到开会补一次 enableBrush（用户可能只点开关、数据没变）", async () => {
    const wrapper = await mountChart({ showRangeSelect: false });
    mocks.enableBrush.mockClear();

    await wrapper.setProps({ showRangeSelect: true });
    await nextTick();
    await nextTick();

    const enabledCalls = mocks.enableBrush.mock.calls.filter((call) => call[0] !== false);
    expect(enabledCalls.length, "开关打开后必须补一次启用").toBeGreaterThan(0);
  });

  it("开关从开到关会显式停用画笔", async () => {
    const wrapper = await mountChart({ showRangeSelect: true });
    mocks.enableBrush.mockClear();

    await wrapper.setProps({ showRangeSelect: false });
    await nextTick();
    await nextTick();

    expect(mocks.enableBrush, "开关关闭后必须停用画笔，否则拖拽照样出框").toHaveBeenCalledWith(
      false,
    );
  });

  it("框选开启时给图表叠一层不吃事件的十字光标层", async () => {
    const off = await mountChart({ showRangeSelect: false });
    expect(off.html()).not.toContain("cursor-crosshair");

    const on = await mountChart({ showRangeSelect: true });
    const overlay = on.html();
    expect(overlay).toContain("cursor-crosshair");
    // ⚠ 必须是 pointer-events-none：拦掉指针事件就等于把框选本身也拦掉了
    // （brush 走的是 zrender 在 canvas 上的事件）
    expect(overlay).toContain("pointer-events-none");
  });

  it("缩放（datazoom）之后会重新启用画笔", async () => {
    // ⚠ 实测：dataZoom 一变，BrushView 就重绘并调 enableBrush(brushModel.brushOption)，
    // 而那里是 {} → `enableBrush({})` 会把已启用的画笔**停用**。
    // 不补回来的话，用户一拖滑条/滚轮缩放，框选就再也不响应了。
    const wrapper = await mountChart({ showRangeSelect: true });

    // 取出注册在 chart.on('datazoom', ...) 上的回调并触发它
    const onCalls = (mocks.chart.on as unknown as { mock: { calls: unknown[][] } }).mock.calls;
    const datazoomCall = onCalls.find((call) => call[0] === "datazoom");
    expect(datazoomCall, "没有注册 datazoom 监听").toBeTruthy();

    mocks.enableBrush.mockClear();
    (datazoomCall![1] as () => void)();
    await nextTick();

    const enabledCalls = mocks.enableBrush.mock.calls.filter((call) => call[0] !== false);
    expect(enabledCalls.length, "datazoom 之后必须重新启用画笔").toBeGreaterThan(0);
  });

  it("从 brushEnd 的真实 areas 结构算出区间（它没有 x/y/width/height）", async () => {
    // ⚠ 这是第二个致命 bug 的回归用例。
    //
    // 真 Chromium + ECharts 6.1.0 实测的 `areas[0]`：
    //   { brushType, panelId, range: [[px,px],[px,px]],
    //     coordRange: [[x0,y0],[x1,y1]], coordRanges, __rangeOffset }
    // **没有 `x` / `y` / `width` / `height`**。早先组件按 `area.y` /
    // `area.height` 做"框有没有落在图上"的判断，那两个值是 undefined，
    // 算术结果是 NaN、判断恒为假 —— **每一次框选都被拒掉**，
    // 表现就是"框了但统计永远不出来"，以及"清除后再框也没反应"。
    const wrapper = await mountChart({ showRangeSelect: true, resetKey: "600519-day-qfq" });

    const onCalls = (mocks.chart.on as unknown as { mock: { calls: unknown[][] } }).mock.calls;
    // ⚠ 取**最后一个**：chart 是共享 mock，之前用例挂载过的实例也往 on 里塞过回调，
    // 拿第一个会调到已卸载实例上的处理器（它的 chart 已经是 null）
    const brushEndCall = [...onCalls].reverse().find((call) => call[0] === "brushEnd");
    expect(brushEndCall, "没有注册 brushEnd 监听").toBeTruthy();
    (brushEndCall![1] as (p: unknown) => void)({
      areas: [
        {
          brushType: "rect",
          panelId: "grid--\u0000series\u00000\u00000",
          // 实测：把第 10 根框到第 20 根（共 120 根数据）
          range: [
            [206, 342],
            [150, 280],
          ],
          // ⚠ 形状是 [[x0, x1], [y0, y1]] —— x 的两个端点都在 [0] 里。
          // 按 [[x0,y0],[x1,y1]] 取会把 y0=157.79 当成 x 端点，
          // 四舍五入后越界被夹到最后一根 → 区间一路撑到 119。
          coordRange: [
            [10, 20],
            [157.78947368421052, 187.26315789473685],
          ],
        },
      ],
    });
    await nextTick();

    const emitted = wrapper.emitted("rangeSelected") as unknown[] | undefined;
    expect(emitted, "收到了 brushEnd 却没产出区间 —— 换算又被拒了").toBeTruthy();
    // 必须正好是框选的那一段
    expect((emitted![0] as [{ from: number; to: number }])[0]).toEqual({ from: 10, to: 21 });
  });

  it("areas 为空时通知父组件收起面板（不产出区间）", async () => {
    const wrapper = await mountChart({ showRangeSelect: true });
    const onCalls = (mocks.chart.on as unknown as { mock: { calls: unknown[][] } }).mock.calls;
    const brushEndCall = [...onCalls].reverse().find((call) => call[0] === "brushEnd");

    (brushEndCall![1] as (p: unknown) => void)({ areas: [] });
    await nextTick();

    expect(wrapper.emitted("rangeSelected")).toBeFalsy();
    expect(wrapper.emitted("rangeCleared"), "空选区应当通知父组件清除").toBeTruthy();
  });

  it("coordRange 取不到数值时不产出区间（不吐 NaN 下标）", async () => {
    const wrapper = await mountChart({ showRangeSelect: true });
    const onCalls = (mocks.chart.on as unknown as { mock: { calls: unknown[][] } }).mock.calls;
    const brushEndCall = [...onCalls].reverse().find((call) => call[0] === "brushEnd");

    (brushEndCall![1] as (p: unknown) => void)({
      areas: [{ brushType: "rect", panelId: "grid--0", range: [[1, 2], [3, 4]] }],
    });
    await nextTick();

    expect(wrapper.emitted("rangeSelected")).toBeFalsy();
  });
});
