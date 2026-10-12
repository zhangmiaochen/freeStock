/**
 * 板块区间（多周期）行情的展示辅助。
 *
 * 后端口径见 internal/service/board_period.go，这里只负责"界面怎么读"：
 *   - 「当日」**不是**区间周期：它一直在 BoardQuote 的当日字段里
 *     （pctChg / amount / turnover / upCount…），所以周期选择器里没有它，
 *     但表格里与区间列并列显示；
 *   - 区间周期是 2 ~ 250 个交易日，每个板块每个周期一条 BoardPeriodStat；
 *     **算不出来的周期没有那一条**（次新股太多、历史太短），
 *     界面必须显示「—」而不是 0% —— 0% 会被当成"这个板块没涨没跌"。
 */
import type { BoardPeriodStat, BoardPeriodWindow, BoardQuote } from "@/lib/backend";
import { formatDate, formatPercent } from "@/lib/format";

/** 界面上的可选周期（交易日），与后端 DefaultBoardPeriods 一致。 */
export const BOARD_PERIOD_OPTIONS = [5, 10, 15, 30, 60];

/** 默认选中的周期：5 个交易日。 */
export const DEFAULT_BOARD_PERIOD = 5;

/** 自定义周期的上下限（后端也会限幅，界面提前拦住，避免白跑一次请求）。 */
export const MIN_BOARD_PERIOD = 2;
export const MAX_BOARD_PERIOD = 250;

/** 当日：不是区间周期，但与区间列并列显示。 */
export const TODAY_PERIOD = 1;

/** 周期标签：1 -> 当日，5 -> 5日。 */
export function periodLabel(days: number): string {
  if (days === TODAY_PERIOD) return "当日";
  return `${days}日`;
}

/** 把用户输入的自定义周期夹到合法范围；非数字时回落到默认周期。 */
export function clampBoardPeriod(days: number): number {
  if (!Number.isFinite(days)) return DEFAULT_BOARD_PERIOD;
  return Math.min(MAX_BOARD_PERIOD, Math.max(MIN_BOARD_PERIOD, Math.round(days)));
}

/** 要向后端请求的周期集合：默认周期 + 自定义周期（去重、升序）。 */
export function boardPeriodRequests(custom?: number | null): number[] {
  const set = new Set<number>(BOARD_PERIOD_OPTIONS);
  if (custom != null && Number.isFinite(custom)) set.add(clampBoardPeriod(custom));
  return [...set].sort((a, b) => a - b);
}

/** 取某个板块在指定周期上的区间行情；没有（算不出来）时返回 undefined。 */
export function periodStat(board: BoardQuote, days: number): BoardPeriodStat | undefined {
  if (days === TODAY_PERIOD) return undefined;
  return board.periods?.find((item) => item.days === days);
}

/**
 * 板块在某周期上的**加权**涨跌幅。
 * 当日走 BoardQuote.PctChg；区间算不出来时返回 undefined（界面显示「—」）。
 */
export function periodPct(board: BoardQuote, days: number): number | undefined {
  if (days === TODAY_PERIOD) return board.pctChg;
  return periodStat(board, days)?.pctChg;
}

/** 等权涨跌幅，口径同上。 */
export function periodPctEqual(board: BoardQuote, days: number): number | undefined {
  if (days === TODAY_PERIOD) return board.pctChgEqual;
  return periodStat(board, days)?.pctChgEqual;
}

/** 取某周期的日历窗口（全分类共用）。 */
export function periodWindow(
  windows: BoardPeriodWindow[] | undefined,
  days: number,
): BoardPeriodWindow | undefined {
  return windows?.find((item) => item.days === days);
}

/** 周期窗口的一句话说明，用于表头与统计条的提示。 */
export function periodWindowText(window: BoardPeriodWindow | undefined): string {
  if (!window) return "";
  if (!window.available) return window.message || "历史交易日不足，这个周期算不出来";
  return `${window.days} 个交易日：${formatDate(window.startDate)} ~ ${formatDate(window.endDate)}` +
    `（基准 ${formatDate(window.baseDate)} 收盘）`;
}

/** 一个板块在所选周期上的详情，用于单元格/热力图的悬停提示。 */
export function periodDetailText(board: BoardQuote, days: number, window?: BoardPeriodWindow): string {
  const lines: string[] = [];
  lines.push(`${board.name}　${periodLabel(days)}`);

  if (days === TODAY_PERIOD) {
    lines.push(`加权 ${formatPercent(board.pctChg)}　等权 ${formatPercent(board.pctChgEqual)}`);
    lines.push(`成交额 ${(board.amount / 1e8).toFixed(2)} 亿　换手 ${board.turnover.toFixed(2)}%`);
    lines.push(`成分 ${board.quotedCount}/${board.memberCount} 只有行情`);
    lines.push(`涨/跌/平 ${board.upCount}/${board.downCount}/${board.flatCount}`);
    if (board.leader?.name) lines.push(`领涨 ${board.leader.name} ${formatPercent(board.leader.pctChg)}`);
    return lines.join("\n");
  }

  const stat = periodStat(board, days);
  const text = periodWindowText(window);
  if (text) lines.push(text);

  if (!stat || stat.quotedCount === 0) {
    lines.push("这个周期算不出来（成分股在区间起点还没有行情）");
    return lines.join("\n");
  }
  lines.push(`加权 ${formatPercent(stat.pctChg)}　等权 ${formatPercent(stat.pctChgEqual)}`);
  lines.push(`区间涨/跌/平 ${stat.upCount}/${stat.downCount}/${stat.flatCount}`);
  lines.push(`成分 ${stat.quotedCount}/${stat.memberCount} 只能算出区间涨跌幅`);
  if (stat.leader?.name) {
    lines.push(`区间领涨 ${stat.leader.name} ${formatPercent(stat.leader.pctChg)}`);
  }
  if (stat.laggard?.name) {
    lines.push(`区间领跌 ${stat.laggard.name} ${formatPercent(stat.laggard.pctChg)}`);
  }
  return lines.join("\n");
}
