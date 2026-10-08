/**
 * K 线周期聚合与技术指标计算。
 *
 * 这里全部是纯函数：输入蜡烛数组，输出指标数组，方便单测，也方便在切换周期 /
 * 增删均线时立刻重算，不用再往后端跑一趟。
 *
 * 算法口径与 free-stockdb 官方 Python SDK 的 `_merge_to_period` 保持一致：
 * 周线按 ISO 周分组，月线按自然月分组，开盘取首日、最高取最大、最低取最小、
 * 收盘取末日、成交量与成交额求和，日期取该周期最后一个交易日。
 */

export interface Candle {
  date: number;
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
  amount: number;
}

export type Period = "day" | "week" | "month" | "year";

export const PERIODS: { key: Period; label: string; title: string }[] = [
  { key: "day", label: "日", title: "日线" },
  { key: "week", label: "周", title: "周线" },
  { key: "month", label: "月", title: "月线" },
  { key: "year", label: "年", title: "年线" },
];

/** 布林线参数。 */
export interface BollConfig {
  /** 中轨周期 */
  period: number;
  /** 标准差倍数 */
  mult: number;
}

/** MACD 参数。 */
export interface MacdConfig {
  fast: number;
  slow: number;
  signal: number;
}

export const DEFAULT_BOLL: BollConfig = { period: 20, mult: 2 };
export const DEFAULT_MACD: MacdConfig = { fast: 12, slow: 26, signal: 9 };

/** 参数取值范围，用于输入校验。 */
export const PARAM_RANGE = {
  maPeriod: { min: 1, max: 1000 },
  bollPeriod: { min: 2, max: 250 },
  bollMult: { min: 0.5, max: 10, step: 0.1 },
  macdFast: { min: 2, max: 200 },
  macdSlow: { min: 3, max: 400 },
  macdSignal: { min: 1, max: 100 },
};

function clampInt(value: number, min: number, max: number, fallback: number): number {
  if (!Number.isFinite(value)) return fallback;
  return Math.min(max, Math.max(min, Math.round(value)));
}

/** 校正一份参数，保证取值合法且 fast < slow。 */
export function normalizeBoll(config: Partial<BollConfig>): BollConfig {
  const period = clampInt(Number(config.period), PARAM_RANGE.bollPeriod.min, PARAM_RANGE.bollPeriod.max, DEFAULT_BOLL.period);
  let mult = Number(config.mult);
  if (!Number.isFinite(mult)) mult = DEFAULT_BOLL.mult;
  mult = Math.min(PARAM_RANGE.bollMult.max, Math.max(PARAM_RANGE.bollMult.min, mult));
  return { period, mult: Math.round(mult * 100) / 100 };
}

export function normalizeMacd(config: Partial<MacdConfig>): MacdConfig {
  const fast = clampInt(Number(config.fast), PARAM_RANGE.macdFast.min, PARAM_RANGE.macdFast.max, DEFAULT_MACD.fast);
  let slow = clampInt(Number(config.slow), PARAM_RANGE.macdSlow.min, PARAM_RANGE.macdSlow.max, DEFAULT_MACD.slow);
  // 慢线必须大于快线，否则 MACD 没有意义
  if (slow <= fast) slow = Math.min(PARAM_RANGE.macdSlow.max, fast + 1);
  const signal = clampInt(Number(config.signal), PARAM_RANGE.macdSignal.min, PARAM_RANGE.macdSignal.max, DEFAULT_MACD.signal);
  return { fast, slow, signal };
}

/** 均线配置。 */
export interface MaConfig {
  period: number;
  color: string;
  visible: boolean;
}

/** 默认均线周期，与需求一致：5 / 20 / 60 / 120 / 250。 */
export const DEFAULT_MA_PERIODS = [5, 20, 60, 120, 250];

/** 按默认周期生成一份均线配置。 */
export function defaultMaConfigs(): MaConfig[] {
  return DEFAULT_MA_PERIODS.map((period, index) => ({
    period,
    color: MA_COLORS[index % MA_COLORS.length],
    visible: true,
  }));
}

/** 均线配色，按顺序循环使用。 */
export const MA_COLORS = [
  "#f5c542",
  "#ec4899",
  "#38bdf8",
  "#a78bfa",
  "#34d399",
  "#fb923c",
  "#f472b6",
  "#4ade80",
  "#60a5fa",
  "#facc15",
];

// ---------------------------------------------------------------------------
// 周期聚合
// ---------------------------------------------------------------------------

/** 计算 ISO 年与 ISO 周（与 Python isocalendar 一致）。 */
function isoWeekOf(year: number, month: number, day: number): [number, number] {
  const date = new Date(Date.UTC(year, month - 1, day));
  const dayNum = date.getUTCDay() || 7;
  date.setUTCDate(date.getUTCDate() + 4 - dayNum);
  const isoYear = date.getUTCFullYear();
  const yearStart = new Date(Date.UTC(isoYear, 0, 1));
  const week = Math.ceil(((date.getTime() - yearStart.getTime()) / 86400000 + 1) / 7);
  return [isoYear, week];
}

function periodKey(date: number, period: Period): string {
  const year = Math.floor(date / 10000);
  const month = Math.floor(date / 100) % 100;
  const day = date % 100;

  switch (period) {
    case "month":
      return `${year}-${String(month).padStart(2, "0")}`;
    case "year":
      return String(year);
    case "week": {
      const [isoYear, isoWeek] = isoWeekOf(year, month, day);
      return `${isoYear}-W${String(isoWeek).padStart(2, "0")}`;
    }
    default:
      return String(date);
  }
}

/** 把日线聚合为周 / 月 / 年线；period 为 day 时原样返回。 */
export function aggregate(candles: Candle[], period: Period): Candle[] {
  if (period === "day" || candles.length === 0) return candles;

  const out: Candle[] = [];
  let bucket: Candle | null = null;
  let bucketKey = "";

  for (const candle of candles) {
    const key = periodKey(candle.date, period);

    if (key !== bucketKey || bucket === null) {
      if (bucket) out.push(bucket);
      bucketKey = key;
      bucket = { ...candle };
      continue;
    }

    bucket.high = Math.max(bucket.high, candle.high);
    bucket.low = Math.min(bucket.low, candle.low);
    bucket.close = candle.close;
    bucket.volume += candle.volume;
    bucket.amount += candle.amount;
    bucket.date = candle.date;
  }

  if (bucket) out.push(bucket);
  return out;
}

// ---------------------------------------------------------------------------
// 资金流
// ---------------------------------------------------------------------------

/** 资金流点，字段与后端 MoneyFlowPoint 对应。 */
export interface MoneyFlowPoint {
  date: number;
  mainNet: number;
  jumboNet: number;
  bigNet: number;
  midNet: number;
  smallNet: number;
}

export type MoneyFlowField = "mainNet" | "jumboNet" | "bigNet" | "midNet" | "smallNet";

/**
 * 把资金流按 K 线周期对齐，返回与 `aggregate(candles, period)` 等长的数组。
 *
 * 资金流是「流量」不是「存量」：周线/月线/年线上应该**求和**，
 * 而不是像收盘价那样取最后一根。某个周期内没有资金流数据时返回 null。
 */
export function aggregateFlow(
  candles: Candle[],
  flows: MoneyFlowPoint[],
  period: Period,
  field: MoneyFlowField = "mainNet",
): (number | null)[] {
  const byDate = new Map<number, number>();
  for (const flow of flows) {
    const value = flow[field];
    if (Number.isFinite(value)) byDate.set(flow.date, value);
  }

  const out: (number | null)[] = [];
  let bucketKey = "";
  let started = false;
  let sum = 0;
  let seen = false;

  for (const candle of candles) {
    const key = periodKey(candle.date, period);
    if (!started || key !== bucketKey) {
      if (started) out.push(seen ? roundTo(sum, 2) : null);
      started = true;
      bucketKey = key;
      sum = 0;
      seen = false;
    }
    const value = byDate.get(candle.date);
    if (value !== undefined) {
      sum += value;
      seen = true;
    }
  }
  if (started) out.push(seen ? roundTo(sum, 2) : null);
  return out;
}

function roundTo(value: number, decimals: number): number {
  const factor = 10 ** decimals;
  return Math.round(value * factor) / factor;
}

/**
 * 移动平均，容忍 null：取窗口内**有效值**的平均，窗口内一个有效值都没有时才为 null。
 * 用于「N 期主力净流入」这类平滑线，数据稀疏时也能画出来。
 */
export function rollingMean(values: (number | null)[], window: number): (number | null)[] {
  if (window <= 1) return values.slice();
  const out: (number | null)[] = new Array(values.length).fill(null);

  for (let i = 0; i < values.length; i++) {
    let sum = 0;
    let count = 0;
    for (let j = Math.max(0, i - window + 1); j <= i; j++) {
      const value = values[j];
      if (value === null || value === undefined) continue;
      sum += value;
      count++;
    }
    if (count > 0) out[i] = roundTo(sum / count, 2);
  }
  return out;
}

// ---------------------------------------------------------------------------
// 指标
// ---------------------------------------------------------------------------

/** 简单移动平均，不足周期处为 null。 */
export function sma(values: number[], period: number): (number | null)[] {
  const out: (number | null)[] = new Array(values.length).fill(null);
  if (period <= 0) return out;

  let sum = 0;
  for (let i = 0; i < values.length; i++) {
    sum += values[i];
    if (i >= period) sum -= values[i - period];
    if (i >= period - 1) out[i] = sum / period;
  }
  return out;
}

/** 指数移动平均（首值取第一个样本，与国内券商软件一致）。 */
export function ema(values: number[], period: number): number[] {
  const out: number[] = new Array(values.length).fill(0);
  if (values.length === 0) return out;
  if (period <= 0) return [...values];

  const alpha = 2 / (period + 1);
  let prev = values[0];
  out[0] = prev;
  for (let i = 1; i < values.length; i++) {
    prev = alpha * values[i] + (1 - alpha) * prev;
    out[i] = prev;
  }
  return out;
}

export interface BollResult {
  mid: (number | null)[];
  upper: (number | null)[];
  lower: (number | null)[];
}

/** 布林带（中轨 = N 日均线，上下轨 = 中轨 ± k 倍总体标准差）。 */
export function boll(closes: number[], period = 20, mult = 2): BollResult {
  const mid = sma(closes, period);
  const upper: (number | null)[] = new Array(closes.length).fill(null);
  const lower: (number | null)[] = new Array(closes.length).fill(null);

  for (let i = period - 1; i < closes.length; i++) {
    const mean = mid[i];
    if (mean === null) continue;

    let variance = 0;
    for (let j = i - period + 1; j <= i; j++) {
      const diff = closes[j] - mean;
      variance += diff * diff;
    }
    const std = Math.sqrt(variance / period);
    upper[i] = mean + mult * std;
    lower[i] = mean - mult * std;
  }

  return { mid, upper, lower };
}

export interface MacdResult {
  dif: number[];
  dea: number[];
  bar: number[];
}

/** MACD（DIF = EMA12 - EMA26，DEA = DIF 的 EMA9，柱 = 2 × (DIF - DEA)）。 */
export function macd(closes: number[], fast = 12, slow = 26, signal = 9): MacdResult {
  const emaFast = ema(closes, fast);
  const emaSlow = ema(closes, slow);

  const dif = closes.map((_, i) => emaFast[i] - emaSlow[i]);
  const dea = ema(dif, signal);
  const bar = dif.map((value, i) => (value - dea[i]) * 2);

  return { dif, dea, bar };
}

/** 涨跌幅（相对前一根收盘价）。 */
export function pctChange(candles: Candle[]): (number | null)[] {
  return candles.map((candle, i) => {
    if (i === 0) return null;
    const prev = candles[i - 1].close;
    if (!prev) return null;
    return ((candle.close - prev) / prev) * 100;
  });
}
