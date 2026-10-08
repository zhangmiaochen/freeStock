/** 数字与日期格式化工具。 */

/** 金额（元）转成「亿 / 万」的易读形式。 */
export function formatAmount(value: number): string {
  if (!Number.isFinite(value) || value === 0) return "—";
  const abs = Math.abs(value);
  if (abs >= 1e8) return `${(value / 1e8).toFixed(2)} 亿`;
  if (abs >= 1e4) return `${(value / 1e4).toFixed(2)} 万`;
  return value.toFixed(0);
}

/** 市值等大数字。 */
export function formatLargeNumber(value: number): string {
  if (!Number.isFinite(value) || value === 0) return "—";
  const abs = Math.abs(value);
  if (abs >= 1e12) return `${(value / 1e12).toFixed(2)} 万亿`;
  if (abs >= 1e8) return `${(value / 1e8).toFixed(2)} 亿`;
  if (abs >= 1e4) return `${(value / 1e4).toFixed(2)} 万`;
  return value.toFixed(2);
}

/** 价格：基金保留 3 位，股票 2 位。 */
export function formatPrice(value: number, isFund = false): string {
  if (!Number.isFinite(value) || value === 0) return "—";
  return value.toFixed(isFund ? 3 : 2);
}

/** 涨跌幅，带正负号。 */
export function formatPercent(value: number): string {
  if (!Number.isFinite(value)) return "—";
  const sign = value > 0 ? "+" : "";
  return `${sign}${value.toFixed(2)}%`;
}

/** 换手率。 */
export function formatTurnover(value: number): string {
  if (!Number.isFinite(value) || value === 0) return "—";
  return `${value.toFixed(2)}%`;
}

/** 20260930 -> 2026-09-30 */
export function formatDate(value: number): string {
  const text = String(value ?? "");
  if (text.length !== 8) return text || "—";
  return `${text.slice(0, 4)}-${text.slice(4, 6)}-${text.slice(6, 8)}`;
}

/** 涨跌方向，用于配色。 */
export function trendClass(value: number): string {
  if (!Number.isFinite(value) || value === 0) return "text-flat";
  return value > 0 ? "text-up" : "text-down";
}

/** 基金代码判断（用于价格精度）。 */
export function isFundCode(code: string): boolean {
  return /^[15]/.test(code);
}
