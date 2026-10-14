/**
 * 框选区间的涨跌信息（纯函数，方便单测）。
 *
 * 用户在图上框一段 K 线，回答的问题是「这一段时间里涨跌了多少、
 * 高点和低点在哪、波动有多大」。所以这里的口径要写清楚，
 * 因为「涨跌幅」在一段区间里有好几种读法，**算错一个百分点就是误导**：
 *
 *   区间涨跌幅 = (末根收盘 − 首根开盘) / 首根开盘 × 100
 *
 * 取自**首根开盘价**而不是「首根收盘价」或「前一根收盘价」：
 *
 *   - 首根**收盘**：会漏掉这段行情自身的开盘跳空（当天从 10 开到 11 收，
 *     却按 11 起算，等于把第一天的涨幅算丢了）；
 *   - **前一根**收盘：需要区间外的数据，而用户框的就是"这一段"，
 *     越出框外取值会让结果与框对不上。
 *
 * 与行情软件里「区间统计」的通常口径一致（同花顺/通达信统计框选区间时
 * 也是用首开、末收、区间最高/最低）。
 *
 * 另外几项也都给"口径注解"，避免被读成别的东西：
 *
 *   - **最大回撤**：在**收盘价**序列上算（不用盘中最低价）。用盘中价会把
 *     "当天插针后收回"也算成回撤，而收盘价回撤才是持有者真实的资金曲线回撤。
 *   - **振幅**：(区间最高 − 区间最低) / 首根开盘。衡量这段的波动幅度。
 *   - **波动率**：逐日涨跌幅的**均值绝对偏差**（不是标准差、也没有年化）。
 *     它回答的是"平均每天动多少"，年化标准差对散户读数字没有帮助，
 *     而且需要假设（250 个交易日、收益率独立同分布）才会成立。
 *   - **上涨/下跌天数**：按逐日涨跌幅的正负号数；平盘既不算涨也不算跌。
 */

import type { Candle } from "@/lib/indicators";

/** 一段区间的涨跌信息。 */
export interface RangeStats {
  /** 起点下标（含） */
  from: number;
  /** 终点下标（不含） */
  to: number;
  /** 参与统计的 K 线根数（= 交易日数） */
  count: number;

  startDate: number;
  endDate: number;
  /** 自然日跨度（含首尾），与交易日数分开给：停牌/长假会差很多 */
  calendarDays: number;

  /** 首根开盘价（区间涨跌幅的基准） */
  startPrice: number;
  /** 末根收盘价 */
  endPrice: number;
  /** 区间涨跌幅 % */
  changePct: number;
  /** 绝对涨跌额 */
  changeAmount: number;

  high: number;
  highDate: number;
  /** 最高价高于起点价的百分比（可能为负：区间高点就在起点） */
  highPct: number;
  low: number;
  lowDate: number;
  /** 最低价低于起点价的百分比（总为负或 0） */
  lowPct: number;
  /** 振幅 % = (最高 − 最低) / 起点价 */
  amplitudePct: number;

  /** 最大回撤 %（正数表示回撤了多少；收盘价序列口径） */
  maxDrawdownPct: number;
  /** 最大回撤的谷底日期 */
  maxDrawdownDate: number;

  /** 逐日涨跌幅的均值绝对偏差 %（未年化） */
  volatilityPct: number;
  /** 上涨交易日数 */
  upDays: number;
  /** 下跌交易日数 */
  downDays: number;
  /**
   * 区间的总成交额。
   *
   * ⚠ **指数没有成交额**（数据源只给价格与成交量），此时为 0，
   * 界面要据此隐藏这一项而不是显示 0 亿。
   */
  totalAmount: number;
  /** 区间的总成交量 */
  totalVolume: number;

  /** 走势方向：先到高点=下跌段，先到低点=上涨段 */
  direction: "up" | "down" | "flat";
}

/**
 * 计算区间涨跌信息。
 *
 * `from` / `to` 会被夹到数组范围内（框选换算出来的下标是浮点数取整来的，
 * 越界一拍就会算出 undefined 参与运算的 NaN）。
 *
 * **少于 2 根返回 null**：「区间涨跌幅」至少要有一个开一个收才成立，
 * 单根 K 线的"涨跌"是它自己的开盘到收盘，不是区间统计，
 * 拿它冒充区间涨跌会误导。
 */
export function rangeStats(candles: Candle[], from: number, to: number): RangeStats | null {
  if (!candles || candles.length === 0) return null;

  const total = candles.length;
  let start = Math.floor(Number.isFinite(from) ? from : 0);
  let end = Math.ceil(Number.isFinite(to) ? to : total);
  if (start < 0) start = 0;
  if (end > total) end = total;
  if (end <= start) return null;

  const slice = candles.slice(start, end);
  // 过滤掉脏数据的可能性：high/low 非有限值的行无法参与统计
  const usable = slice.filter(
    (candle) => candle && Number.isFinite(candle.high) && Number.isFinite(candle.low),
  );
  if (usable.length < 2) return null;

  const first = usable[0];
  const last = usable[usable.length - 1];
  const startPrice = first.open;
  const endPrice = last.close;

  // 首根开盘价为 0（脏数据）时算不出比例，退化成"涨跌额为 0"
  const changeAmount = endPrice - startPrice;
  const changePct = startPrice > 0 ? (changeAmount / startPrice) * 100 : 0;

  let high = Number.NEGATIVE_INFINITY;
  let low = Number.POSITIVE_INFINITY;
  let highDate = first.date;
  let lowDate = first.date;
  let highIndex = 0;
  let lowIndex = 0;
  let totalAmount = 0;
  let totalVolume = 0;

  usable.forEach((candle, index) => {
    if (candle.high > high) {
      high = candle.high;
      highDate = candle.date;
      highIndex = index;
    }
    if (candle.low < low) {
      low = candle.low;
      lowDate = candle.date;
      lowIndex = index;
    }
    totalAmount += Number.isFinite(candle.amount) ? candle.amount : 0;
    totalVolume += Number.isFinite(candle.volume) ? candle.volume : 0;
  });

  const highPct = startPrice > 0 ? ((high - startPrice) / startPrice) * 100 : 0;
  const lowPct = startPrice > 0 ? ((low - startPrice) / startPrice) * 100 : 0;
  const amplitudePct = startPrice > 0 ? ((high - low) / startPrice) * 100 : 0;

  const { maxDrawdownPct, maxDrawdownDate } = drawdownOf(usable);

  // 逐日涨跌幅：第一天用「首根开盘 → 首根收盘」，之后用「前收 → 本收」。
  // 这样第一天的跳空不会被丢掉，而且与区间涨跌幅的"首开"口径首尾一致。
  let upDays = 0;
  let downDays = 0;
  let absSum = 0;
  let dayCount = 0;
  for (let i = 0; i < usable.length; i++) {
    const candle = usable[i];
    const base = i === 0 ? candle.open : usable[i - 1].close;
    if (!(base > 0) || !Number.isFinite(candle.close)) continue;
    const pct = ((candle.close - base) / base) * 100;
    absSum += Math.abs(pct);
    dayCount++;
    if (pct > 0) upDays++;
    else if (pct < 0) downDays++;
  }

  return {
    from: start,
    to: end,
    count: usable.length,

    startDate: first.date,
    endDate: last.date,
    calendarDays: calendarDaysBetween(first.date, last.date),

    startPrice,
    endPrice,
    changePct,
    changeAmount,

    high,
    highDate,
    highPct,
    low,
    lowDate,
    lowPct,
    amplitudePct,

    maxDrawdownPct,
    maxDrawdownDate,

    volatilityPct: dayCount > 0 ? absSum / dayCount : 0,
    upDays,
    downDays,

    totalAmount,
    totalVolume,

    direction: highIndex < lowIndex ? "down" : lowIndex < highIndex ? "up" : "flat",
  };
}

/** 收盘价序列上的最大回撤（返回正数，谷底日期一并给出）。 */
function drawdownOf(candles: Candle[]): { maxDrawdownPct: number; maxDrawdownDate: number } {
  let peak = Number.NEGATIVE_INFINITY;
  let peakIndex = 0;
  let worst = 0;
  let worstDate = candles[0]?.date ?? 0;

  for (let i = 0; i < candles.length; i++) {
    const close = candles[i].close;
    if (!Number.isFinite(close) || close <= 0) continue;
    if (close > peak) {
      peak = close;
      peakIndex = i;
      continue;
    }
    if (!(peak > 0) || peakIndex === i) continue;
    const drawdown = ((peak - close) / peak) * 100;
    if (drawdown > worst) {
      worst = drawdown;
      worstDate = candles[i].date;
    }
  }
  return { maxDrawdownPct: worst, maxDrawdownDate: worstDate };
}

/**
 * 两个 `YYYYMMDD` 之间的自然日跨度（含首尾）。
 *
 * 与"交易日数"分开给是必要的：春节前后 10 个交易日可能横跨 25 个自然日，
 * 只说"10 天"或"25 天"都会让人误解这段行情有多长。
 * 日期非法时返回 0（界面显示 `—`）。
 */
export function calendarDaysBetween(start: number, end: number): number {
  const a = parseDate(start);
  const b = parseDate(end);
  if (!a || !b) return 0;
  const diff = Math.round((b.getTime() - a.getTime()) / 86400000);
  return diff >= 0 ? diff + 1 : 0;
}

/** `YYYYMMDD` → Date（本地时区零点）；非法返回 null。 */
function parseDate(value: number): Date | null {
  const text = String(value ?? "");
  if (text.length !== 8) return null;
  const year = Number(text.slice(0, 4));
  const month = Number(text.slice(4, 6));
  const day = Number(text.slice(6, 8));
  if (!year || month < 1 || month > 12 || day < 1 || day > 31) return null;
  const date = new Date(year, month - 1, day);
  // 反查一遍，挡掉 20260231 这类"语法合法但日子不存在"的输入
  if (date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== day) {
    return null;
  }
  return date;
}

/** 绘制 markArea 时用的选中区间信息。 */
export interface RangeSelection {
  from: number;
  to: number;
}

/**
 * 把 ECharts 的刷选结果（x 轴上的像素区间）折算成 K 线下标区间。
 *
 * 与 `lib/fibonacci.ts` 的 `visibleIndexRange` 是两件事：
 * 那个把 **dataZoom 百分比**折算成下标，这个把**像素坐标**折算成下标，
 * 所以不能在纯函数里做 —— 像素到数据的换算必须由 ECharts 自己来
 * （`convertFromPixel`，见 KlineChart）。这里只做"接收已算好的
 * 首尾数值再收敛成合法下标"这一步，好让边界逻辑可单测。
 */
export function selectionFromAxisValues(
  total: number,
  startValue: number,
  endValue: number,
): RangeSelection | null {
  if (!Number.isFinite(total) || total <= 0) return null;
  if (!Number.isFinite(startValue) || !Number.isFinite(endValue)) return null;

  // 拖拽可以从右往左，所以两个端点都要排序
  const lo = Math.min(startValue, endValue);
  const hi = Math.max(startValue, endValue);

  // convertFromPixel 给的是轴上的**连续**位置（可能落在两根之间），
  // 取整到最近的一根：`round` 而不是 `floor`，否则从右往左框会少一根。
  let from = Math.round(lo);
  let to = Math.round(hi);
  if (from > to) {
    const swap = from;
    from = to;
    to = swap;
  }

  // ⚠ **先夹起点再判断**：只夹终点的话，"整段框在数据右侧之外"会变成
  // `from=50, to=9` —— 两个值都越界，但 clamp 之后 `to < from` 又成立，
  // 于是被误判成"空区间"返回 null。正确的做法是把起点也夹进范围，
  // 这样两个越界端点会一起塌到最后一根（用户意图是"最后那一段"），
  // 而真正不碰任何一根的框（from 夹完仍大于 to）才返回 null。
  if (from < 0) from = 0;
  if (from > total - 1) from = total - 1;
  if (to < 0) to = 0;
  if (to > total - 1) to = total - 1;
  // 闭区间 → 半开区间
  if (to < from) return null;
  return { from, to: to + 1 };
}

/**
 * 区间信息里"值得看"的那几项的顺序与口径说明。
 *
 * 放在 lib 里（而不是组件里拼）是为了让**口径文案**也能被单测钉住 ——
 * 这几句话是用户判断数字含义的唯一依据，写含糊了数字就会被误读。
 */
export const RANGE_METRIC_NOTES: { key: keyof RangeStats; label: string; note: string }[] = [
  {
    key: "changePct",
    label: "区间涨跌幅",
    note: "首根开盘价 → 末根收盘价。用首根**开盘**是为了把这段行情自身的跳空算进去",
  },
  {
    key: "maxDrawdownPct",
    label: "最大回撤",
    note: "在**收盘价**序列上算。用盘中最低价会把「当天插针后收回」也算成回撤，虚高",
  },
  {
    key: "amplitudePct",
    label: "振幅",
    note: "(区间最高 − 区间最低) ÷ 首根开盘价，衡量这段的波动幅度",
  },
  {
    key: "volatilityPct",
    label: "日均波动",
    note: "逐日涨跌幅的均值绝对偏差，**未年化** —— 回答「平均每天动多少」",
  },
];
