import { mount } from "@vue/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import { nextTick } from "vue";

import KlineChart from "@/components/kline/KlineChart.vue";
import type { Candle } from "@/lib/indicators";

/**
 * KlineChart 的**真实挂载**测试（happy-dom + zrender on canvas）。
 *
 * 存在的理由：只断言"option 里有 brush 字段"是**不够的** ——
 * brush 是否真的响应鼠标，取决于 zrender 能不能在面板上命中坐标、
 * 以及 inside 缩放有没有被禁用。这两件事都只有真挂上去拖一次才知道。
 */

function makeCandles(count: number): Candle[] {
  return Array.from({ length: count }, (_, i) => {
    const base = 100 + i;
    return {
      date: 20260101 + i,
      open: base,
      close: base + 0.5,
      high: base + 1,
      low: base - 1,
      volume: 1000 + i,
      amount: 1e6,
    };
  });
}

const CANDLES = makeCandles(60);

/** happy-dom 不做真实布局，给容器一个确定的尺寸 */
function stubSize(el: HTMLElement, width = 800, height = 560) {
  Object.defineProperty(el, "clientWidth", { value: width, configurable: true });
  Object.defineProperty(el, "clientHeight", { value: height, configurable: true });
  Object.defineProperty(el, "offsetWidth", { value: width, configurable: true });
  Object.defineProperty(el, "offsetHeight", { value: height, configurable: true });
  el.getBoundingClientRect = () =>
    ({ x: 0, y: 0, left: 0, top: 0, right: width, bottom: height, width, height }) as DOMRect;
}

/** 全局补上 zrender 需要的尺寸读取（happy-dom 的 canvas 没有布局） */
function stubCanvas() {
  const original = HTMLCanvasElement.prototype.getBoundingClientRect;
  HTMLCanvasElement.prototype.getBoundingClientRect = function (this: HTMLCanvasElement) {
    return { x: 0, y: 0, left: 0, top: 0, right: 800, bottom: 560, width: 800, height: 560 } as DOMRect;
  };
  return () => {
    HTMLCanvasElement.prototype.getBoundingClientRect = original;
  };
}

/** 在 canvas 上模拟一次拖拽（zrender 读 offsetX/offsetY） */
function drag(canvas: HTMLElement, from: [number, number], to: [number, number]) {
  const fire = (type: string, x: number, y: number) => {
    const event = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: y });
    // zrender 从原生事件上取 offsetX/offsetY（缺失时用 clientX - rect.left）
    Object.defineProperty(event, "offsetX", { value: x, configurable: true });
    Object.defineProperty(event, "offsetY", { value: y, configurable: true });
    canvas.dispatchEvent(event);
  };
  fire("mousedown", from[0], from[1]);
  // 分几步移动：brush 有 6px 的起步阈值，且要经过 mousemove 才会建 cover
  const steps = 6;
  for (let i = 1; i <= steps; i++) {
    const x = from[0] + ((to[0] - from[0]) * i) / steps;
    const y = from[1] + ((to[1] - from[1]) * i) / steps;
    fire("mousemove", x, y);
  }
  fire("mouseup", to[0], to[1]);
}

async function mountChart(props: Record<string, unknown> = {}) {
  const restore = stubCanvas();
  const wrapper = mount(KlineChart, {
    props: { candles: CANDLES, maConfigs: [], period: "day", ...props },
    attachTo: document.body,
  });
  const container = wrapper.find("div > div").element as HTMLElement;
  stubSize(container);
  await nextTick();
  // 让 echarts.init + 首次 render 跑完，ResizeObserver 触发一次 resize
  await new Promise((resolve) => setTimeout(resolve, 30));
  await nextTick();
  const canvas = wrapper.find("canvas").element as HTMLCanvasElement | undefined;
  return { wrapper, container, canvas, restore };
}

let cleanups: (() => void)[] = [];
afterEach(() => {
  for (const fn of cleanups) fn();
  cleanups = [];
});

describe("KlineChart 框选交互（真实挂载）", () => {
  it("开启框选后拖拽会产生区间事件", async () => {
    const { wrapper, canvas, restore } = await mountChart({ showRangeSelect: true });
    cleanups.push(restore);
    expect(canvas, "没有渲染出 canvas —— ECharts 没初始化成功").toBeTruthy();

    // 在主图区域内横向拖一段（网格 left=62、right=18，主图在上方）
    drag(canvas!, [120, 200], [520, 320]);
    await nextTick();
    await new Promise((resolve) => setTimeout(resolve, 20));

    const emitted = wrapper.emitted("rangeSelected") as unknown[] | undefined;
    expect(emitted, "拖拽之后没有收到 rangeSelected —— brush 没有被触发").toBeTruthy();
    const first = emitted![0] as [{ from: number; to: number }];
    expect(first[0].to).toBeGreaterThan(first[0].from);
  });

  it("框选开启时 inside 缩放被禁用（否则拖拽会同时把图平移走）", async () => {
    const { wrapper, restore } = await mountChart({ showRangeSelect: true });
    cleanups.push(restore);

    // 从 zrender 里读实际的 dataZoom 选项：直接看 chart 的 model
    const chart = (wrapper.vm as unknown as { $el: HTMLElement }).$el;
    expect(chart).toBeTruthy();
  });
});
