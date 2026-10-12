/**
 * 市场温度页的展示辅助。
 *
 * 口径全在后端（internal/service/market_temperature.go），这里只负责
 * "把数字翻译成界面语言"：周期选项、温度配色、等级与趋势标签的文案。
 *
 * 三条必须让用户看懂的事（页面上会写出来）：
 *   1. 温度是**综合分在全部样本里的分位**："比历史上 X% 的交易日更热"；
 *   2. 每个分项也先在全部样本里取分位，所以 5 日与 60 日共用同一套阈值；
 *   3. 「位置」不是点数高低，而是**点位相对自身 250 日均线的偏离**在历史中的分位
 *      —— 指数长期向上，直接比点数会把任何时点都判成高位。
 *
 * 注意"全部样本"不完全等于"指数历史全部交易日"：分项里有两个需要 250 日均线
 * 与近一年量能，所以温度只在**四个分项都有值**的交易日上统计（见后端说明）。
 */

/** 可选周期：1 = 当日（默认） */
export const TEMPERATURE_PERIOD_OPTIONS = [1, 5, 10, 15, 30, 60];

/** 默认周期：当日 */
export const DEFAULT_TEMPERATURE_PERIOD = 1;

export const MIN_TEMPERATURE_PERIOD = 1;
export const MAX_TEMPERATURE_PERIOD = 250;

/** 周期标签：1 → 当日，5 → 5日 */
export function temperaturePeriodLabel(days: number): string {
  return days <= 1 ? "当日" : `${days}日`;
}

/** 把用户输入的周期夹到合法范围（1~250）。 */
export function clampTemperaturePeriod(days: number): number {
  if (!Number.isFinite(days)) return DEFAULT_TEMPERATURE_PERIOD;
  return Math.min(MAX_TEMPERATURE_PERIOD, Math.max(MIN_TEMPERATURE_PERIOD, Math.round(days)));
}

/**
 * 温度配色：冷（蓝）→ 中性（灰）→ 热（红）。
 *
 * 与行情涨跌的红绿方向一致（红=热），所以温度条和指数的涨跌不会互相打架。
 */
export function temperatureColor(temperature: number): string {
  const value = Math.max(0, Math.min(100, temperature));
  if (value <= 50) {
    // 0 → 蓝(59,130,246)，50 → 灰(113,113,122)
    const t = value / 50;
    return mix([59, 130, 246], [113, 113, 122], t);
  }
  // 50 → 灰，100 → 红(220,38,38)
  const t = (value - 50) / 50;
  return mix([113, 113, 122], [220, 38, 38], t);
}

function mix(from: [number, number, number], to: [number, number, number], t: number): string {
  const ratio = Math.max(0, Math.min(1, t));
  const channel = (i: number) => Math.round(from[i] + (to[i] - from[i]) * ratio);
  return `rgb(${channel(0)}, ${channel(1)}, ${channel(2)})`;
}

/** 温度等级的徽章样式（冷色 → 热色）。 */
export function temperatureBadgeClass(level: string): string {
  switch (level) {
    case "过热":
      return "border-transparent bg-red-600 text-white";
    case "偏热":
      return "border-transparent bg-red-500/15 text-red-500";
    case "中性":
      return "border-transparent bg-zinc-500/15 text-zinc-400";
    case "偏冷":
      return "border-transparent bg-blue-500/15 text-blue-400";
    default:
      return "border-transparent bg-blue-600 text-white";
  }
}

/** 趋势标签的配色：强势/偏强用红，偏弱/弱势用绿，震旦用中性色。 */
export function trendLabelClass(label: string): string {
  switch (label) {
    case "强势上行":
      return "border-transparent bg-red-600 text-white";
    case "偏强":
      return "border-transparent bg-red-500/15 text-red-500";
    case "偏弱":
      return "border-transparent bg-green-500/15 text-green-500";
    case "弱势下行":
      return "border-transparent bg-green-600 text-white";
    default:
      return "border-transparent bg-zinc-500/15 text-zinc-400";
  }
}

/** 分项得分配色：与温度同一条色带。 */
export function factorScoreColor(score: number): string {
  return temperatureColor(score);
}

/**
 * 分项展示顺序与一句话口径。
 *
 * 权重写在界面上（后端也会返回），因为"温度怎么算的"必须可核对 ——
 * 否则用户没法判断这个数字该不该信。
 */
export const FACTOR_HINTS: Record<string, string> = {
  trend: "所选周期各指数涨跌幅的等权平均",
  volume: "所选周期日均成交额 ÷ 近一年日均成交额",
  position: "点位相对自身 250 日均线的平均偏离",
  breadth: "所选周期上涨的指数占比",
};

/** 走势图/迷你图里每个指数的固定颜色（8 个够用，多余的循环）。 */
export const INDEX_COLORS = [
  "#ef4444",
  "#f59e0b",
  "#eab308",
  "#22c55e",
  "#06b6d4",
  "#3b82f6",
  "#8b5cf6",
  "#ec4899",
  "#14b8a6",
  "#f97316",
];

/** 指数颜色：按结果里的顺序取，保证同一指数在图表与卡片里颜色一致。 */
export function indexColor(index: number): string {
  return INDEX_COLORS[index % INDEX_COLORS.length];
}
