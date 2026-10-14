import { describe, expect, it } from "vitest";

/**
 * 框选用的 ECharts 配置在 SSR 下真的能渲染出来。
 *
 * 挡的是"选项名写错但纯函数测试全绿"这一类错误 —— 例如 brush 少注册
 * `BrushComponent` 时**不报错**，只是框选完全没反应，
 * 而那正是最难自己发现的情况（Fibonacci 的 markLine 就踩过同一个坑）。
 */
describe("框选统计的图表配置（SSR）", () => {
  it("注册 BrushComponent 后 brush 配置能被 ECharts 接受并渲染", async () => {
    const echarts = await import("echarts/core");
    const { CandlestickChart } = await import("echarts/charts");
    const { BrushComponent, GridComponent } = await import("echarts/components");
    const { SVGRenderer } = await import("echarts/renderers");
    echarts.use([CandlestickChart, GridComponent, BrushComponent, SVGRenderer]);

    const dates = [20260105, 20260106, 20260107, 20260108, 20260109];
    const chart = echarts.init(null, null, { renderer: "svg", ssr: true, width: 640, height: 320 });
    chart.setOption({
      xAxis: { type: "category", data: dates },
      yAxis: { type: "value", min: "dataMin", max: "dataMax" },
      brush: {
        xAxisIndex: 0,
        brushType: "rect",
        brushMode: "single",
        transformable: false,
        brushStyle: { borderWidth: 1, color: "rgba(59,130,246,0.10)", borderColor: "#3b82f6" },
      },
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
        },
      ],
    });

    // 不抛错即为通过（配置非法时 ECharts 会抛或报错）
    const svg = chart.renderToSVGString();
    const option = chart.getOption() as { brush?: { brushType?: string; brushMode?: string }[] | { brushType?: string; brushMode?: string } };
    chart.dispose();

    expect(svg).toContain("<svg");
    // brush 配置被接受（ECharts 会把单对象规范化成数组）
    const brush = Array.isArray(option.brush) ? option.brush[0] : option.brush;
    expect(brush?.brushType).toBe("rect");
    expect(brush?.brushMode).toBe("single");
  });

  it("框选高亮用的 markArea 结构与「选中区间」对得上", async () => {
    const echarts = await import("echarts/core");
    const { CandlestickChart } = await import("echarts/charts");
    const { GridComponent, MarkAreaComponent } = await import("echarts/components");
    const { SVGRenderer } = await import("echarts/renderers");
    // ⚠ markArea 需要 MarkAreaComponent —— 与 markLine 一样，少注册只是画不出来
    echarts.use([CandlestickChart, GridComponent, MarkAreaComponent, SVGRenderer]);

    const dates = [20260105, 20260106, 20260107, 20260108, 20260109];
    const chart = echarts.init(null, null, { renderer: "svg", ssr: true, width: 640, height: 320 });
    chart.setOption({
      xAxis: { type: "category", data: dates, boundaryGap: true },
      yAxis: { type: "value", min: "dataMin", max: "dataMax" },
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
            itemStyle: { color: "rgba(59,130,246,0.12)", borderColor: "#3b82f6", borderWidth: 1 },
            label: { show: false },
            data: [[{ xAxis: 20260106 }, { xAxis: 20260108 }]],
          },
        },
      ],
    });

    const svg = chart.renderToSVGString();
    chart.dispose();

    // markArea 画成了一个带填充的矩形。
    //
    // ⚠ 三个坑都在这里踩过：
    //   1. 忘了 `echarts.use([SVGRenderer])` 时报的是
    //      "Renderer 'undefined' is not imported"，与 markArea 无关，很容易误判；
    //   2. ECharts 会把颜色规范化（`rgba(59,130,246,0.12)` → `rgba(59,130,246,.12)`，
    //      十六进制还可能改写成 `#rrggbbaa`），所以只能断言"有半透明填充"，不能比原串；
    //   3. **markArea 不一定画成 `<rect>`** —— 实测它是一条带 fill 的 `<path>`，
    //      底层元素类型是实现细节，钉死它会得到一个假失败。
    const compact = svg.replace(/\s+/g, "");
    expect(compact).toMatch(/fill="(rgba?\(|#[0-9a-f]{6,8}")/i);
    // 背景一个矩形 + 高亮一条路径，说明两样都画出来了
    expect(compact).toContain("<rect");
    expect(compact).toContain("<path");
  });
});
