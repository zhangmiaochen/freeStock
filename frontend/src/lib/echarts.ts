/**
 * ECharts 的**按需注册**（全项目共用一份）。
 *
 * ⚠ 图表用到什么，就必须在这里注册什么，**少一个不会立刻报错**：
 *
 *   - 少 `MarkLineComponent` / `MarkAreaComponent`：线/区块静默消失；
 *   - 少一个 series 类型（例如 `ScatterChart`）：第一次 `setOption` 可能看着正常，
 *     但之后**任何** `update` / `resize` 都会在 ECharts 内部抛
 *     `Cannot read properties of undefined (reading 'dataTask')`，
 *     并且内部「正在更新」的标志会停在 true —— 此后所有 `setOption` / `resize`
 *     都直接 return，表现就是**图表从此再也不刷新、窗口缩放也不跟随**。
 *
 * 这个坑在缠论叠加层上加分型/买卖点散点时踩过，所以单独抽一个模块，
 * 由 `tests/echartsRegistration.spec.ts` 用 SSR 盯住"散点能不能渲染"。
 */
import { BarChart, CandlestickChart, LineChart, ScatterChart } from "echarts/charts";
import {
  AxisPointerComponent,
  BrushComponent,
  DataZoomComponent,
  GridComponent,
  LegendComponent,
  MarkAreaComponent,
  MarkLineComponent,
  TooltipComponent,
} from "echarts/components";
import * as echarts from "echarts/core";
import { CanvasRenderer } from "echarts/renderers";

echarts.use([
  CandlestickChart,
  LineChart,
  BarChart,
  // 缠论的分型 / 买卖点用散点。漏了它会让后续 update/resize 全部失效。
  ScatterChart,
  GridComponent,
  TooltipComponent,
  DataZoomComponent,
  AxisPointerComponent,
  LegendComponent,
  MarkAreaComponent,
  MarkLineComponent,
  BrushComponent,
  CanvasRenderer,
]);

export { echarts };
