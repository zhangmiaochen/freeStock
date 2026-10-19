import { describe, expect, it } from "vitest";

/**
 * ECharts 的 series / 组件注册（SSR）。
 *
 * ⚠ 这个用例盯的是一个**静默失效**的坑：
 *
 *   K 线图在缠论叠加层里加了 `scatter`（分型、买卖点），但当时没在
 *   `echarts.use([...])` 里注册 `ScatterChart`。第一次 `setOption` 看起来正常，
 *   之后**任何** update / resize 都会在 ECharts 内部抛
 *   `Cannot read properties of undefined (reading 'dataTask')`；
 *   更糟的是内部「正在更新」的标志会停在 true，此后所有 `setOption` / `resize`
 *   直接 return —— 表现是「图表再也不刷新、窗口放大缩小它也不跟着变」，
 *   而控制台除了第一次异常之外什么都没有。
 *
 * 所以这里不再逐个 import 图表类型，而是直接用**产品代码同一份注册表**
 * （`src/lib/echarts.ts`）：它漏注册，这个用例就会失败。
 */
describe("ECharts 注册表（SSR）", () => {
  it("蜡烛 + 线 + 柱 + 散点 + markArea 能一起渲染", async () => {
    const { echarts } = await import("@/lib/echarts");
    const { SVGRenderer } = await import("echarts/renderers");
    // SSR 需要 SVG 渲染器；CanvasRenderer 由产品注册表提供
    echarts.use([SVGRenderer]);

    const dates = [20260105, 20260106, 20260107, 20260108, 20260109];
    const chart = echarts.init(null, null, {
      renderer: "svg",
      ssr: true,
      width: 800,
      height: 400,
    });

    chart.setOption({
      xAxis: { type: "category", data: dates, boundaryGap: true },
      yAxis: { type: "value", scale: true },
      series: [
        {
          type: "candlestick",
          data: [
            [10, 10.5, 9.8, 10.8],
            [10.5, 11, 10.4, 11.2],
            [11, 10, 9.9, 11.1],
            [10, 9.5, 9.2, 10.2],
            [9.5, 10.2, 9.4, 10.4],
          ],
          markArea: {
            silent: true,
            itemStyle: { color: "rgba(56,189,248,0.10)" },
            label: { show: false },
            data: [[{ xAxis: 1, yAxis: 10 }, { xAxis: 3, yAxis: 11 }]],
          },
        },
        {
          // 缠论的「笔」：用下标 + 价格
          type: "line",
          data: [
            [0, 10],
            [2, 11.2],
            [4, 9.4],
          ],
          showSymbol: false,
        },
        {
          type: "bar",
          xAxisIndex: 0,
          data: [100, 200, 150, 300, 250],
        },
        {
          // 缠论的「买卖点」：散点 + 每条数据各自带 label / itemStyle
          type: "scatter",
          data: [
            {
              value: [1, 10.4],
              symbol: "triangle",
              label: { show: true, formatter: "一买", position: "bottom" },
              itemStyle: { color: "#ef4444" },
            },
          ],
        },
      ],
    });

    const svg = chart.renderToSVGString();
    chart.dispose();

    expect(svg).toContain("<svg");
    // 散点真的画出来了（三角 symbol + 标签文字）
    expect(svg).toContain("一买");
    // 蜡烛与柱也都在
    expect(svg).toMatch(/<path/);
  });

  it("散点系列的 label / symbol 按数据项生效", async () => {
    const { echarts } = await import("@/lib/echarts");
    const { SVGRenderer } = await import("echarts/renderers");
    echarts.use([SVGRenderer]);

    const chart = echarts.init(null, null, { renderer: "svg", ssr: true, width: 400, height: 300 });
    chart.setOption({
      xAxis: { type: "category", data: [1, 2, 3] },
      yAxis: { type: "value" },
      series: [
        {
          type: "scatter",
          data: [
            {
              value: [0, 10],
              symbol: "triangle",
              symbolRotate: 180,
              symbolSize: 15,
              itemStyle: { color: "#22c55e", borderColor: "#22c55e" },
              label: { show: true, formatter: "三卖", position: "top" },
            },
          ],
        },
      ],
    });
    const svg = chart.renderToSVGString();
    chart.dispose();

    expect(svg).toContain("三卖");
    expect(svg).toContain("#22c55e");
  });
});
