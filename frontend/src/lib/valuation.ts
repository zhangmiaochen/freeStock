/**
 * 「A 股多期限性价比评估」的界面读法。
 *
 * 与 `lib/temperature.ts` / `lib/boardPeriod.ts` 同一分工：
 * **口径计算全在后端**（internal/valuation），前端只负责"把数字翻译成界面语言"——
 * 配色、标签、缺值文案、枚举到中文的映射。
 *
 * ## 两条约定
 *
 * 1. **缺值一律显示「—」而不是 0**。这个功能里大量字段是"算不出来"而不是"等于 0"
 *    （分位样本不足、估值方法不适用、行业成分缺失）。把 0 当值显示会给出
 *    完全错误的信号 —— 例如"PE 分位 0%"读起来像"史上最便宜"，
 *    而实际含义是"没有历史样本，分位算不出来"。
 *    用 `hasPercentile()` 区分这两者。
 *
 * 2. **所有映射函数的入参写成 `unknown`**。原因是引擎侧的 `Rating` /
 *    `RiskSeverity` / `ScoreDimension` 在 Wails 生成的 bindings 里是 TS **enum**，
 *    而本文件里的映射表是"字符串 → 文案"。`Record<string, T>` 拿 enum 当索引时
 *    TS 会取到数字索引签名并报错；把入参放宽成 `unknown` 后，
 *    组件里传 enum、传字符串、传 undefined 都能用，而且不需要在模板里撒 `as string`。
 *    内部一律先 `keyOf()` 折成字符串再查表。
 */

/** 把任意枚举/字符串值折成可查表的字符串。 */
export function keyOf(value: unknown): string {
  if (value === undefined || value === null) return "";
  return String(value);
}

/** 五档期限的顺序与标签，与 Go 侧 valuation.Horizons 一一对应。 */
export const HORIZON_KEYS = ["1m", "3m", "6m", "12m", "36m"] as const;
export type HorizonKey = (typeof HORIZON_KEYS)[number];

export const HORIZON_LABELS: Record<string, string> = {
  "1m": "1 个月",
  "3m": "3 个月",
  "6m": "6 个月",
  "12m": "12 个月",
  "36m": "36 个月",
};

/**
 * 维度顺序与中文名，与 Go 侧 valuation.ScoreDimensions 一一对应。
 *
 * 刻意用字符串数组而不是 `ScoreDimension[]`：它同时被用来索引
 * `dimensions` / `weights` / `contributions` 三个 map，字符串键最省事，
 * 也不会因为 enum 的数字索引签名让 TS 报错。
 */
export const DIMENSION_ORDER: string[] = [
  "selfValuation",
  "indValuation",
  "relValuation",
  "growth",
  "quality",
  "capital",
  "margin",
  "return",
  "data",
];

export const DIMENSION_LABELS: Record<string, string> = {
  selfValuation: "个股自身估值",
  indValuation: "行业估值环境",
  relValuation: "相对同行估值",
  growth: "成长能力",
  quality: "成长与盈利质量",
  capital: "资本回报效率",
  margin: "安全边际",
  return: "预期收益与风险",
  data: "数据质量与置信度",
};

/**
 * 评级中文名与配色。
 *
 * 配色刻意**不用红涨绿跌**：评级表达的是性价比（风险收益匹配），
 * 借用涨跌配色会让人误读成"预测会涨"。
 */
export const RATING_LABELS: Record<string, string> = {
  excellent: "优秀",
  good: "良好",
  neutral: "中性",
  poor: "偏低",
  avoid: "回避",
  unavailable: "暂无法可靠评估",
};

export function ratingLabel(rating: unknown): string {
  return RATING_LABELS[keyOf(rating)] ?? "—";
}

export function ratingClass(rating: unknown): string {
  switch (keyOf(rating)) {
    case "excellent":
      return "border-emerald-500/40 bg-emerald-500/10 text-emerald-400";
    case "good":
      return "border-teal-500/40 bg-teal-500/10 text-teal-400";
    case "neutral":
      return "border-zinc-500/40 bg-zinc-500/10 text-zinc-300";
    case "poor":
      return "border-amber-500/40 bg-amber-500/10 text-amber-400";
    case "avoid":
      return "border-rose-500/40 bg-rose-500/10 text-rose-400";
    default:
      return "border-zinc-700 bg-zinc-800/40 text-muted-foreground";
  }
}

/** 综合分配色（0~100 → 灰 → 青 → 绿）。 */
export function scoreClass(score: number): string {
  if (!Number.isFinite(score)) return "text-muted-foreground";
  if (score >= 72) return "text-emerald-400";
  if (score >= 62) return "text-teal-400";
  if (score >= 50) return "text-foreground";
  if (score >= 38) return "text-amber-400";
  return "text-rose-400";
}

/** 估值分位配色：越低越便宜 → 越绿。 */
export function percentileClass(percentile: number): string {
  if (!Number.isFinite(percentile) || percentile <= 0) return "text-muted-foreground";
  if (percentile <= 20) return "text-emerald-400";
  if (percentile <= 40) return "text-teal-400";
  if (percentile <= 60) return "text-foreground";
  if (percentile <= 80) return "text-amber-400";
  return "text-rose-400";
}

/** 分位的中文读法（与 Go 侧 valuation.PercentileLabel 同一套）。 */
export function percentileLabel(percentile: number | undefined): string {
  const value = Number(percentile ?? 0);
  if (!value) return "分位不可用";
  if (value <= 10) return "历史极低位";
  if (value <= 30) return "历史低位";
  if (value <= 45) return "历史中位偏下";
  if (value <= 55) return "历史中位";
  if (value <= 70) return "历史中位偏上";
  if (value <= 90) return "历史高位";
  return "历史极高位";
}

/**
 * 是否算出了分位。
 *
 * `0` 在引擎里表示"算不出来"（样本不足），不是"历史最低"——
 * 所以 0 必须走"不可用"分支。
 */
export function hasPercentile(value: number | undefined): boolean {
  return Number(value ?? 0) > 0;
}

/** 倍数字段（PE / PB / PS / 价格）：0 或非有限值一律「—」。 */
export function formatMultiple(value: number | undefined, digits = 2): string {
  const num = Number(value ?? 0);
  if (!Number.isFinite(num) || num <= 0) return "—";
  return num.toFixed(digits);
}

/** 小数比值（扣非占比、现金含量等）。 */
export function formatRatio(value: number | undefined, digits = 2): string {
  const num = Number(value ?? 0);
  if (!Number.isFinite(num) || num === 0) return "—";
  return num.toFixed(digits);
}

/** 小数形式的百分数（0.1234 → 「+12.3%」）。 */
export function formatPct(value: number | undefined, digits = 1, signed = true): string {
  const num = Number(value ?? 0);
  if (!Number.isFinite(num)) return "—";
  const text = `${(num * 100).toFixed(digits)}%`;
  return signed && num > 0 ? `+${text}` : text;
}

/** 已经是百分数形式的字段（分位 35 → 「35%」）。 */
export function formatPctValue(value: number | undefined, digits = 0): string {
  const num = Number(value ?? 0);
  if (!Number.isFinite(num)) return "—";
  return `${num.toFixed(digits)}%`;
}

/** 市值（元）→ 亿 / 万亿。 */
export function formatCap(value: number | undefined): string {
  const num = Number(value ?? 0);
  if (!Number.isFinite(num) || num <= 0) return "—";
  if (num >= 1e12) return `${(num / 1e12).toFixed(2)} 万亿`;
  return `${(num / 1e8).toFixed(0)} 亿`;
}

export const RISK_SEVERITY_LABELS: Record<string, string> = {
  notice: "一般预警",
  cap: "限制评级",
  unavailable: "无法可靠评估",
};

export function riskSeverityLabel(severity: unknown): string {
  return RISK_SEVERITY_LABELS[keyOf(severity)] ?? "—";
}

export function riskSeverityClass(severity: unknown): string {
  switch (keyOf(severity)) {
    case "cap":
      return "border-amber-500/40 bg-amber-500/10 text-amber-400";
    case "unavailable":
      return "border-rose-500/40 bg-rose-500/10 text-rose-400";
    default:
      return "border-zinc-600/40 bg-zinc-700/10 text-muted-foreground";
  }
}

/** 估值方法标识 → 中文（与 Go 侧 MethodLabel 同一套）。 */
export const METHOD_LABELS: Record<string, string> = {
  pe: "PE（市盈率）",
  pb: "PB（市净率）",
  ps: "PS（市销率）",
  ev_ebitda: "EV/EBITDA",
  dcf: "DCF（现金流折现）",
  peg: "PEG",
  pb_roe: "PB-ROE",
  cyclical_adj: "周期调整估值",
  dividend: "股息率",
};

export function methodLabel(method: unknown): string {
  const key = keyOf(method);
  return METHOD_LABELS[key] ?? key ?? "—";
}

/** 四象限标识 → 中文结论。 */
export const QUADRANT_LABELS: Record<string, string> = {
  cheap_good: "估值折价 + 基本面优于行业",
  cheap_bad: "估值折价 + 基本面弱于行业",
  expensive_good: "估值溢价 + 基本面优于行业",
  expensive_bad: "估值溢价 + 基本面没有优势",
  mixed: "估值与行业基本持平或数据不足",
};

export function quadrantLabel(quadrant: unknown): string {
  return QUADRANT_LABELS[keyOf(quadrant)] ?? "—";
}

export function quadrantClass(quadrant: unknown): string {
  switch (keyOf(quadrant)) {
    case "cheap_good":
      return "text-emerald-400";
    case "cheap_bad":
      return "text-amber-400";
    case "expensive_bad":
      return "text-rose-400";
    default:
      return "text-muted-foreground";
  }
}

/** 行业阶段 → 中文。 */
export const STAGE_LABELS: Record<string, string> = {
  growth: "成长",
  mature: "成熟",
  decline: "衰退",
  cyclical: "周期波动",
  unknown: "未知",
};

export function stageLabel(stage: unknown): string {
  return STAGE_LABELS[keyOf(stage)] ?? "未知";
}

/** 置信度等级 → 中文与配色。 */
export function confidenceLabel(level: unknown): string {
  switch (keyOf(level)) {
    case "high":
      return "高";
    case "medium":
      return "中";
    default:
      return "低";
  }
}

export function confidenceClass(level: unknown): string {
  switch (keyOf(level)) {
    case "high":
      return "text-emerald-400";
    case "medium":
      return "text-amber-400";
    default:
      return "text-rose-400";
  }
}

/** 成长形态标识 → 中文。 */
export const GROWTH_FLAG_LABELS: Record<string, string> = {
  low_base: "低基数高增长",
  acquisition: "增长未获现金流印证",
  cyclical: "可能是行业性反弹",
  one_off: "增长含一次性收益",
  turnaround: "扭亏",
  decelerating: "增长连续减速",
};

export function growthFlagLabel(code: unknown): string {
  const key = keyOf(code);
  return GROWTH_FLAG_LABELS[key] ?? key;
}

/** 质量问题标识 → 中文。 */
export const QUALITY_ISSUE_LABELS: Record<string, string> = {
  low_deducted: "利润含较多非经常性损益",
  weak_cash: "利润缺少现金流支持",
  high_debt: "资产负债率偏高",
  persistent_fin: "依赖持续融资",
  margin_erosion: "毛利率下滑",
  not_main_business: "利润不主要来自主营",
  volatile_profit: "盈利波动较大",
};

export function qualityIssueLabel(code: unknown): string {
  const key = keyOf(code);
  return QUALITY_ISSUE_LABELS[key] ?? key;
}

/** 风险类型标识 → 中文。 */
export const RISK_CODE_LABELS: Record<string, string> = {
  cheap_but_deteriorating: "估值低但盈利持续恶化",
  industry_expensive: "个股便宜但行业仍贵",
  growth_no_cash: "高增长但现金流质量差",
  premium_unsupported: "估值溢价但无基本面支撑",
  downside_too_large: "预期收益与下行风险不匹配",
  optimism_dependent: "估值依赖乐观假设",
  cyclical_misjudge: "可能存在周期性误判",
  data_quality: "数据质量不足",
  method_conflict: "估值方法结论冲突",
  // 这一对是"高增长行业里最该防的那件事"（见开发计划 §6）：
  // 行业好、公司好，但价格已经把未来多年的高增长提前反映完了。
  growth_expectation_too_high: "股价隐含的成长要求高于公司自身能力",
  growth_duration_demanding: "当前价格依赖长期的高成长兑现",
};

/** 市场隐含预期的结论档位文案（over/fair/under/unknown）。 */
export const IMPLIED_VERDICT_LABELS: Record<string, string> = {
  over: "市场要求高于自身能力",
  fair: "市场要求与自身能力匹配",
  under: "市场要求低于自身能力",
  unknown: "无法反解",
};

/** 兑现概率档位文案。 */
export const FULFILLMENT_LABELS: Record<string, string> = {
  high: "高",
  medium: "中",
  low: "低",
  unknown: "未知",
};

export function impliedVerdictLabel(verdict: unknown): string {
  const key = keyOf(verdict);
  return IMPLIED_VERDICT_LABELS[key] ?? key;
}

export function fulfillmentLabel(level: unknown): string {
  const key = keyOf(level);
  return FULFILLMENT_LABELS[key] ?? key;
}

export function riskCodeLabel(code: unknown): string {
  const key = keyOf(code);
  return RISK_CODE_LABELS[key] ?? key;
}

/** 首刷只数选项：全市场评估要几十秒以上，所以默认只评市值前 150 只。 */
export const LIMIT_OPTIONS = [
  { value: 80, label: "前 80 只" },
  { value: 150, label: "前 150 只" },
  { value: 400, label: "前 400 只" },
  { value: 0, label: "全市场" },
];

export function limitLabel(limit: number): string {
  return limit <= 0 ? "全市场" : `市值前 ${limit} 只`;
}
