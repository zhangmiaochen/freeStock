/**
 * 板块热力图的配色。
 *
 * A 股习惯是**红涨绿跌**，和欧美相反。板块日涨跌幅大多落在 ±5% 以内，
 * 所以色带在 ±6% 处封顶 —— 否则个别极端值会把整张图压成一片灰。
 */

/** 中性色（涨跌幅≈0） */
const NEUTRAL: [number, number, number] = [63, 63, 70];
/** 上涨端（红） */
const UP: [number, number, number] = [220, 38, 38];
/** 下跌端（绿） */
const DOWN: [number, number, number] = [22, 163, 74];

/** 色带封顶的涨跌幅绝对值 */
export const HEAT_CAP = 6;

function mix(from: [number, number, number], to: [number, number, number], t: number): string {
  const ratio = Math.max(0, Math.min(1, t));
  const channel = (index: number) => Math.round(from[index] + (to[index] - from[index]) * ratio);
  return `rgb(${channel(0)}, ${channel(1)}, ${channel(2)})`;
}

/**
 * 把涨跌幅映射成颜色。
 * 即使涨跌幅很小也保留一点底色（最少 18% 的混色），避免整块图看起来没着色。
 */
export function heatColor(pctChg: number): string {
  if (!Number.isFinite(pctChg) || pctChg === 0) return `rgb(${NEUTRAL.join(", ")})`;
  const intensity = Math.min(Math.abs(pctChg), HEAT_CAP) / HEAT_CAP;
  const target = pctChg > 0 ? UP : DOWN;
  return mix(NEUTRAL, target, 0.18 + intensity * 0.82);
}

/** 文字颜色：背景越深用越亮的字，保证可读性。 */
export function heatTextColor(pctChg: number): string {
  const intensity = Math.min(Math.abs(pctChg ?? 0), HEAT_CAP) / HEAT_CAP;
  return intensity > 0.45 ? "#ffffff" : "#e4e4e7";
}
