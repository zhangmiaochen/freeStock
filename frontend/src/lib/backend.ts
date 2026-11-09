import { Call, Events } from "@wailsio/runtime";

import type {
  BoardCategoryInfo,
  BoardFilter,
  BoardInfo,
  BoardMemberQuote,
  BoardPeriodStat,
  BoardPeriodWindow,
  BoardQuote,
  BoardQuoteResult,
  DataLakeInfo,
  DataSourceInfo,
  DerivedFundamentals,
  FinanceSnapshot,
  FinanceSourceStatus,
  FinancialStatements,
  KlineBundle,
  KlineCandle,
  KlinePrefsInfo,
  LakeScreenCondition,
  MACondition,
  MarketIndexStat,
  MarketTemperatureResult,
  MoneyFlowSummary,
  ScreenCondition,
  ScreenConditionInfo,
  ScreenItem,
  ScreenOptions,
  ScreenResult,
  ShareStructureItem,
  ShareholderCountItem,
  SourceStatus,
  StockItem,
  StockProfile,
  StockSearchItem,
  StockUniverse,
  TemperatureFactor,
  TopHolderItem,
} from "../../bindings/freestock/internal/service/models";
// 数据湖相关的类型定义在 cnlake 包里，Wails 按包生成模型
import type { DatasetStatus, Partition } from "../../bindings/freestock/internal/cnlake/models";
// K 线偏好的数据结构定义在 config 包，外层包装（含配置文件路径）在 service 包
import type { BollPrefs, KlinePrefs, MaPrefs, MacdPrefs } from "../../bindings/freestock/internal/config/models";
// 组合管理的类型定义在 portfolio 包里，Wails 按包生成模型
import type {
  BacktestDaily,
  BacktestRun,
  BacktestTrade,
  Detail as PortfolioDetail,
  Overview as PortfolioOverview,
  Portfolio,
  PortfolioInput,
  Position,
  PositionInput,
  ScopeValue,
  Summary as PortfolioSummary,
  Transaction as PortfolioTransaction,
} from "../../bindings/freestock/internal/portfolio/models";
// 回测相关的类型定义在 backtest 包
import type { Metrics as BacktestMetrics } from "../../bindings/freestock/internal/backtest/models";
// 回测的服务层入参/出参
import type {
  AutoPortfolioRequest,
  AutoPortfolioResult,
  BackfillAllResult,
  BackfillResult,
  BacktestPayload,
  BacktestRequest,
  BacktestRunResult,
  RefreshAllResult,
} from "../../bindings/freestock/internal/service/models";
import type {
  EmailPrefsInfo,
  EmailPreview,
  EmailScheduleInfo,
  EmailSendResult,
  AppliedChange,
  ApplyResult,
  PlanResult,
  RefreshResult,
  StrengthSummary,
} from "../../bindings/freestock/internal/service/models";
import type {
  MarketState,
  ReplacementPair,
  RotationPlan,
  RotationRow,
  Row as ScoreRow,
  StrategyConfig,
} from "../../bindings/freestock/internal/score/models";
import type { PortfolioOptions } from "../../bindings/freestock/internal/service/models";
// 多期限性价比评估：服务层结果 + 引擎侧类型（valuation 包）
import type {
  IndustryValuationDetail,
  IndustryDistributionRow,
  IndexValuationResult,
  PinyinInfo,
  StockValuation,
  ValuationIndexOption,
  ValuationParamsInfo,
  ValuationReport,
} from "../../bindings/freestock/internal/service/models";
import type {
  Confidence,
  Evidence,
  FairValue,
  Growth,
  Horizon,
  HorizonRanking,
  HorizonScore,
  IndexEvaluation,
  IndustryValuation,
  MethodResult,
  Outlook,
  Quality,
  QualityIssue,
  RankRow,
  Rating,
  RatingThresholds,
  RelativeValuation,
  RiskFlag,
  Scenario,
  ScoreDimension,
  ScoreDistribution,
  SelfValuation,
  Snapshot,
  StockScore,
  ValuationBand,
  ValuationPoint,
} from "../../bindings/freestock/internal/valuation/models";
// 缠论引擎：结构类型定义在 chanlun 包里，Wails 按包生成模型
import type {
  Adaptability as ChanlunAdaptability,
  Analysis as ChanlunAnalysis,
  Bundle as ChanlunBundle,
  Config as ChanlunConfig,
  DimScore as ChanlunDimScore,
  Divergence as ChanlunDivergence,
  DivergenceOutcome as ChanlunDivergenceOutcome,
  Fractal as ChanlunFractal,
  Level as ChanlunLevel,
  LevelSummary as ChanlunLevelSummary,
  MergedBarInfo as ChanlunMergedBar,
  MultiLevel as ChanlunMultiLevel,
  Pivot as ChanlunPivot,
  Position as ChanlunPosition,
  Replay as ChanlunReplay,
  ReplayStep as ChanlunReplayStep,
  Scenario as ChanlunScenario,
  ScoreFactor as ChanlunScoreFactor,
  Segment as ChanlunSegment,
  Signal as ChanlunSignal,
  SignalState as ChanlunSignalState,
  SignalType as ChanlunSignalType,
  StateInfo as ChanlunState,
  Stroke as ChanlunStroke,
  Trend as ChanlunTrend,
  TrendType as ChanlunTrendType,
} from "../../bindings/freestock/internal/chanlun/models";
// 缠论的服务层入参/出参
import type {
  ChanlunConfigInfo,
  ChanlunHit,
  ChanlunLevelOption,
  ChanlunRequest,
  ChanlunScreenCondition,
  ChanlunSignalBrief,
} from "../../bindings/freestock/internal/service/models";
import * as StockService from "../../bindings/freestock/internal/service/stockservice";
import * as CneUpdateService from "../../bindings/freestock/internal/service/cneupdateservice";
import * as PortfolioService from "../../bindings/freestock/internal/service/portfolioservice";

export type { BollPrefs, KlinePrefs, MaPrefs, MacdPrefs };
// cne 数据更新相关类型（对应 internal/service/cneupdate.go 的 json tag）
import type { JobInfo, UpdatePrefsInfo } from "../../bindings/freestock/internal/service/models";
import type { Status as CneUpdateStatus, FreshnessReport, CatchUpState } from "../../bindings/freestock/internal/cne/models";
import type { CatchUpInfo, AutoUpdateStatus } from "../../bindings/freestock/internal/service/models";
import type { ScheduleState, RunDetail, RunSummary, SchedulePlan } from "../../bindings/freestock/internal/cne/models";
import type { UpdatePrefs } from "../../bindings/freestock/internal/config/models";
import type { EmailPrefs } from "../../bindings/freestock/internal/config/models";
export type { JobInfo, UpdatePrefsInfo, CneUpdateStatus, UpdatePrefs, FreshnessReport, CatchUpState, CatchUpInfo, AutoUpdateStatus, ScheduleState, RunDetail, RunSummary, SchedulePlan };
export type { EmailPrefs, EmailPrefsInfo, EmailPreview, EmailScheduleInfo, EmailSendResult };
// 多期限性价比评估相关类型（服务层结果 + 引擎侧结构）。
//
// ⚠ 引擎侧（valuation 包）的类型要逐个列出而不是整包 re-export：
// 与其它域（score / portfolio / backtest）保持同一写法，便于查"这个字段谁在用"。
export type {
  Confidence,
  Evidence,
  FairValue,
  Growth,
  Horizon,
  HorizonRanking,
  HorizonScore,
  IndexEvaluation,
  IndexValuationResult,
  IndustryDistributionRow,
  IndustryValuation,
  IndustryValuationDetail,
  MethodResult,
  Outlook,
  Quality,
  QualityIssue,
  RankRow,
  Rating,
  RatingThresholds,
  RelativeValuation,
  RiskFlag,
  Scenario,
  ScoreDimension,
  ScoreDistribution,
  SelfValuation,
  Snapshot,
  StockScore,
  StockValuation,
  PinyinInfo,
  ValuationBand,
  ValuationIndexOption,
  ValuationParamsInfo,
  ValuationPoint,
  ValuationReport,
};
export type {
  AppliedChange,
  ApplyResult,
  MarketState,
  PlanResult,
  Portfolio,
  PortfolioDetail,
  PortfolioInput,
  PortfolioOptions,
  PortfolioOverview,
  PortfolioSummary,
  PortfolioTransaction,
  Position,
  PositionInput,
  RefreshResult,
  ReplacementPair,
  RotationPlan,
  RotationRow,
  ScopeValue,
  ScoreRow,
  StrategyConfig,
  StrengthSummary,
};

// 缠论相关类型（引擎侧 + 服务层）
export type {
  ChanlunAdaptability,
  ChanlunAnalysis,
  ChanlunBundle,
  ChanlunConfig,
  ChanlunDimScore,
  ChanlunDivergence,
  ChanlunDivergenceOutcome,
  ChanlunFractal,
  ChanlunLevel,
  ChanlunLevelSummary,
  ChanlunMergedBar,
  ChanlunMultiLevel,
  ChanlunPivot,
  ChanlunPosition,
  ChanlunReplay,
  ChanlunReplayStep,
  ChanlunScenario,
  ChanlunScoreFactor,
  ChanlunSegment,
  ChanlunSignal,
  ChanlunSignalState,
  ChanlunSignalType,
  ChanlunState,
  ChanlunStroke,
  ChanlunTrend,
  ChanlunTrendType,
  ChanlunConfigInfo,
  ChanlunHit,
  ChanlunLevelOption,
  ChanlunRequest,
  ChanlunScreenCondition,
  ChanlunSignalBrief,
};

export type {
  BoardCategoryInfo,
  BoardFilter,
  BoardInfo,
  BoardMemberQuote,
  BoardPeriodStat,
  BoardPeriodWindow,
  BoardQuote,
  BoardQuoteResult,
  DataLakeInfo,
  DataSourceInfo,
  DatasetStatus,
  DerivedFundamentals,
  FinanceSnapshot,
  FinanceSourceStatus,
  FinancialStatements,
  KlineBundle,
  KlineCandle,
  LakeScreenCondition,
  MACondition,
  MarketIndexStat,
  MarketTemperatureResult,
  MoneyFlowSummary,
  Partition,
  ScreenCondition,
  ScreenConditionInfo,
  ScreenItem,
  ScreenOptions,
  ScreenResult,
  ShareStructureItem,
  ShareholderCountItem,
  SourceStatus,
  StockItem,
  StockProfile,
  StockSearchItem,
  StockUniverse,
  TemperatureFactor,
  TopHolderItem,
};

/** 后端扫描进度事件负载。 */
export interface ScanProgress {
  phase: string;
  done: number;
  total: number;
}

/** 组合资金视图：当前本金（权益）= 现金 + 持仓市值。 */
export interface MoneyView {
  initialCapital: number;
  cash: number;
  marketValue: number;
  equity: number;
  returnPct: number;
}

const PROGRESS_EVENT = "stock:progress";
/** 条件选股的技术层要逐只拉日线，单独一个进度事件 */
const SCREEN_PROGRESS_EVENT = "screen:progress";

/** 订阅后端进度事件，返回取消订阅函数。 */
export function onScanProgress(callback: (progress: ScanProgress) => void): () => void {
  return Events.On(PROGRESS_EVENT, (event) => {
    callback(event.data as ScanProgress);
  });
}

/** 订阅条件选股技术层的进度（逐只拉日线，可能要十几秒）。 */
export function onScreenProgress(callback: (progress: ScanProgress) => void): () => void {
  return Events.On(SCREEN_PROGRESS_EVENT, (event) => {
    callback(event.data as ScanProgress);
  });
}

/** 把 Wails 抛出的各种错误形态统一成一句可展示的文案。 */
export function errorMessage(err: unknown): string {
  if (typeof err === "string" && err.trim()) return err.trim();
  if (err instanceof Error) return err.message;
  if (err && typeof err === "object" && "message" in err) {
    return String((err as { message: unknown }).message);
  }
  return String(err ?? "未知错误");
}

/** 后端服务调用入口（与 Go 侧 StockService 一一对应）。 */
export const api = {
  getStockUniverse: (refresh = false) =>
    StockService.GetStockUniverse(refresh) as Promise<StockUniverse | null>,
  getBoardCategories: () =>
    StockService.GetBoardCategories() as Promise<BoardCategoryInfo[]>,
  getBoards: (category: string) => StockService.GetBoards(category) as Promise<BoardInfo[]>,
  /**
   * 板块行情：由成分股行情在后端聚合，口径见 internal/service/board.go 与 board_period.go。
   *
   * periodDays 是区间周期（交易日数，如 5/10/15/30/60），空数组时后端用默认周期。
   * 1 日不是区间周期 —— 它是「当日」，数据在 BoardQuote 的当日字段里。
   */
  getBoardQuotes: (category: string, periodDays: number[] = [], limit = 0) =>
    StockService.GetBoardQuotes(category, periodDays, limit) as Promise<BoardQuoteResult | null>,
  getBoardMembers: (code: string) =>
    StockService.GetBoardMembers(code) as Promise<BoardMemberQuote[]>,
  /**
   * 市场温度：由 8 大指数的多周期走势与量能算出（口径见 internal/service/market_temperature.go）。
   *
   * periodDays = 1 表示当日（默认），其余为 2~250 个交易日。
   * 指数行情只在本地数据湖的 index_bars 里；数据不可用时返回 Available=false + Message，
   * 而不是抛错 —— 界面据此显示可解释的空状态。
   */
  getMarketTemperature: (periodDays = 1) =>
    StockService.GetMarketTemperature(periodDays) as Promise<MarketTemperatureResult | null>,
  getDataSources: () => StockService.GetDataSources() as Promise<DataSourceInfo[]>,
  getCurrentDataSource: () =>
    StockService.GetCurrentDataSource() as Promise<DataSourceInfo>,
  /** kind 为 "cnlake" 时检测本地数据湖，否则按 HTTP 地址检测 free-stockdb */
  testDataSource: (kind: string, url: string) =>
    StockService.TestDataSource(kind, url) as Promise<SourceStatus>,
  setDataSource: (kind: string, url: string) =>
    StockService.SetDataSource(kind, url) as Promise<SourceStatus>,
  clearCache: () => StockService.ClearCache() as Promise<void>,
  searchStocks: (keyword: string, limit = 20) =>
    StockService.SearchStocks(keyword, limit) as Promise<StockSearchItem[]>,
  getKline: (code: string, adjust = "qfq", startYear = 1990) =>
    StockService.GetKline(code, adjust, startYear) as Promise<KlineBundle | null>,
  getFinanceSnapshot: (code: string) =>
    StockService.GetFinanceSnapshot(code) as Promise<FinanceSnapshot | null>,
  getFinancialStatements: (code: string, limit = 200) =>
    StockService.GetFinancialStatements(code, limit) as Promise<FinancialStatements | null>,
  getDataLakeStatus: () => StockService.GetDataLakeStatus() as Promise<DataLakeInfo | null>,
  setDataLakeRoot: (root: string) =>
    StockService.SetDataLakeRoot(root) as Promise<DataLakeInfo | null>,
  /** K 线页的指标参数与显示开关，持久化在本地配置文件里 */
  getKlinePrefs: () => StockService.GetKlinePrefs() as Promise<KlinePrefsInfo>,
  saveKlinePrefs: (prefs: KlinePrefs) =>
    StockService.SaveKlinePrefs(prefs) as Promise<KlinePrefsInfo>,
  getConfigPath: () => StockService.GetConfigPath() as Promise<string>,

  // ---- A 股多期限性价比评估（见 docs/A股多期限性价比评估方案.md）----
  //
  // 口径要点（与 Go 侧一致，界面必须能解释清楚）：
  //  · 收益/波动/回撤用**后复权价**，市值与 PE/PB/PS 用**不复权价 × 当期股本**；
  //  · 历史估值是自建重建的（数据湖 valuation_metrics 只有 5 个交易日）；
  //  · 财务是年内累计口径、Q4 是全年值，TTM = 上年全年 + 本年至今 − 去年同期；
  //  · 五档期限（1/3/6/12/36 个月）用不同权重、不同收敛速度、不同情景概率。
  //
  // 数据不可用时后端**不抛错**，返回 Available=false + Message，
  // 界面据此显示可解释的空状态（与市场温度页同一约定）。
  /**
   * 全市场评估。limit > 0 时只评估市值前 limit 只（首刷用），0 = 全市场。
   * 结果缓存 30 分钟；refresh=true 强制重算。
   */
  getValuationReport: (limit = 150, refresh = false) =>
    StockService.GetValuationReport(limit, refresh) as Promise<ValuationReport | null>,
  /** 单只标的的完整评估（按需计算，不等全市场评估；因此拿不到排名） */
  getStockValuation: (code: string) =>
    StockService.GetStockValuation(code) as Promise<StockValuation | null>,
  /** 行业详情：估值环境（加权/中位/分位分列）+ 行业内排名 */
  getIndustryValuation: (codeOrName: string) =>
    StockService.GetIndustryValuation(codeOrName) as Promise<IndustryValuationDetail | null>,
  /** 指数详情（独立于个股的评估模型） */
  getIndexValuation: (code: string) =>
    StockService.GetIndexValuation(code) as Promise<IndexValuationResult | null>,
  /** 可评估的指数清单（8 个指数里只有 5 个有成分与权重） */
  listValuationIndices: () =>
    StockService.ListValuationIndices() as Promise<ValuationIndexOption[]>,
  /** 当前生效的模型参数（五档权重、情景概率、评级分界），供界面解释口径 */
  getValuationParams: () => StockService.GetValuationParams() as Promise<ValuationParamsInfo>,
  /**
   * 批量取名称的拼音（全拼 + 首字母，含多音字多写法）。
   *
   * 走后端是为了与股票列表页 / K 线页的搜索规则**完全一致**：
   * 那边用的是 `internal/service/kline.go` 的同一套实现
   * （go-pinyin + 列出多音字所有读音，所以 `payh` 与 `payx` 都能搜到「平安银行」）。
   * 前端再引一套 JS 拼音库会出现"同一名称两个页面搜出不同结果"。
   *
   * 键是**入参名称原样**；未命中的名称不出现在结果里。
   */
  getNamePinyin: (names: string[]) =>
    StockService.GetNamePinyin(names) as Promise<Record<string, PinyinInfo>>,

  // ---- cne 数据更新（见 docs/cne数据更新方案.md）----
  //
  // ⚠ kind 的取值与 Go 侧 `internal/cne` 的常量一一对应：
  //   full  更新到今天（普通日更）
  //   stale 补抓落后（--stale-only）
  //   date  补指定交易日（--trade-date）
  // 方案 §2.2/§2.3 实测过：**stale 不覆盖日行情，非交易日跑普通日更会跳过行情组** ——
  // 所以界面上必须给三个入口，不能只给一个按钮。
  /** 发起手动更新，**立即返回**（单次 8~60 分钟，不能同步等待） */
  manualUpdate: (kind: string, tradeDate = "") =>
    CneUpdateService.ManualUpdate(kind, tradeDate) as Promise<JobInfo | null>,
  /**
   * 发起**历史区间回补**（`cne backfill <dataset> --start X --end Y`）。
   *
   * ⚠ 与 manualUpdate 不是同一条命令：上面三种都走 `cne run daily`（日更 pipeline，
   * 只认单日 `--trade-date`），补历史走的是另一个子命令 `cne backfill` ——
   * 这就是"手动更新只能补交易日、不能补区间"的原因。
   *
   * ⚠ **dataset 必须显式给**：个股日线 `daily_bars` 与指数日线 `index_bars`
   * 是两个数据集。只补个股会留下"交易日历里没有那一年"的空档 ——
   * 交易日历取自基准指数（`000300.SH`），而它在 `index_bars` 里。
   *
   * 成本按**标的**算、与窗口宽窄几乎无关，所以不要按天循环去补一整年。
   */
  manualBackfill: (dataset: string, start: string, end: string) =>
    CneUpdateService.ManualBackfill(dataset, start, end) as Promise<JobInfo | null>,
  /** 当前/最近任务的进度快照 */
  updateStatus: () => CneUpdateService.UpdateStatus() as Promise<CneUpdateStatus | null>,
  /** 取消当前任务（杀整棵进程树，不留孤儿 python） */
  cancelUpdate: () => CneUpdateService.CancelUpdate() as Promise<void>,
  getUpdatePrefs: () => CneUpdateService.GetUpdatePrefs() as Promise<UpdatePrefsInfo>,
  setUpdatePrefs: (prefs: UpdatePrefs) =>
    CneUpdateService.SetUpdatePrefs(prefs) as Promise<UpdatePrefsInfo>,

  /**
   * 检查各关键数据集的新鲜度：最新日期 vs 最后已收盘交易日，含缺失的交易日。
   *
   * ⚠ 这是**漏日补跑的依据**。方案 §2.2 实测 `--stale-only` 算出的落后清单里
   * 根本没有 daily_bars —— 只看它的结论会以为"没落后"，而行情可能缺了一天。
   */
  checkFreshness: () => CneUpdateService.CheckFreshness() as Promise<FreshnessReport | null>,
  /**
   * 按新鲜度结果**逐日串行**补跑缺失的交易日，立即返回。
   * 服务端会自己再跑一次新鲜度检查，不听前端传来的日期。
   */
  startCatchUp: () => CneUpdateService.StartCatchUp() as Promise<CatchUpInfo | null>,
  catchUpState: () => CneUpdateService.CatchUpState() as Promise<CatchUpState>,
  /** 请求停止补跑：当前这一天跑完后不再继续（不中途掐断） */
  cancelCatchUp: () => CneUpdateService.CancelCatchUp() as Promise<void>,
  /**
   * 读回两个计划任务的状态（**只读**）。
   *
   * 界面靠它显示"确实排上了"，而不是只给用户一个开关。
   * 目前只提供观测，不提供创建/修改/删除 —— 那会改动用户机器上的计划任务，
   * 需要连同"确认 CNEquity 侧调度器已关闭、避免双跑"一起设计。
   */
  scheduleState: () => CneUpdateService.ScheduleState() as Promise<ScheduleState | null>,
  /**
   * 算出把两个计划任务改到目标时间需要做什么。**只算不改。**
   *
   * ⚠ 结果里的 `commands` 是**将要执行的原始 schtasks 命令**，
   * 必须原样展示给用户看 —— 让他知道自己机器上会发生什么。
   */
  /**
   * 自动更新是否真的会生效。
   *
   * ⚠ 不只看"任务存不存在"，还查**是否 Enabled** 和
   * **StartWhenAvailable**（错过时间后会不会补跑）——
   * 后者没打开时，17:30/21:00 机器关着就会永远跳过那天，
   * 而任务列表看上去一切正常。
   */
  /**
   * 从**应用进程自身**实测一组事实，用于定位"系统找不到指定的路径"。
   *
   * ⚠ 存在理由：同一个调用在 `go test`（有控制台的进程）里永远成功，
   * 说明差异在应用进程的环境 —— 只有从应用里跑才看得到。
   * 返回纯文本，可直接复制。
   */
  diagnoseSchedule: () => CneUpdateService.DiagnoseSchedule() as Promise<string>,
  autoUpdateStatus: () => CneUpdateService.AutoUpdateStatus() as Promise<AutoUpdateStatus | null>,
  /** 开启/关闭自动更新（启用或禁用两个计划任务）。只改启用状态，不改时间。 */
  setAutoUpdate: (enabled: boolean) =>
    CneUpdateService.SetAutoUpdate(true === enabled) as Promise<AutoUpdateStatus | null>,
  /** 把两个任务改成「错过时间后也能补跑」。只改设置，不动时间与命令。 */
  fixScheduleSettings: () => CneUpdateService.FixScheduleSettings() as Promise<AutoUpdateStatus | null>,
  previewSchedule: (dailyAt: string, staleAt: string) =>
    CneUpdateService.PreviewSchedule(dailyAt, staleAt) as Promise<SchedulePlan | null>,
  /**
   * 执行计划任务的改动。**必须 confirmed=true**。
   *
   * 界面顺序必须是：previewSchedule → 展示原始命令 → 用户确认 → applySchedule(plan, true)。
   * 后端只改现有的两个任务，**不新建、不删除**。
   */
  applySchedule: (plan: SchedulePlan, confirmed: boolean) =>
    CneUpdateService.ApplySchedule(plan, true === confirmed) as Promise<SchedulePlan | null>,

  /**
   * 最近的运行记录（来自 cne 的 manifest.db）。
   *
   * ⚠ **这是判断"这次更新到底成没成"的唯一可靠方式**：
   * 方案 §2.1 实测 exit=1 时可能 5 个步骤成功、写了 2.5 万行，
   * 而 degraded 的运行反而 exit=0。**退出码不能当成功判据。**
   */
  recentRuns: (limit = 10) => CneUpdateService.RecentRuns(limit) as Promise<RunSummary[]>,
  /** 最近一次运行的详细结果：每个数据集写了多少行、失败还是上游暂无数据 */
  latestRunResult: () => CneUpdateService.LatestRunResult() as Promise<RunDetail | null>,
  /** 条件选股 */
  getScreenOptions: () => StockService.GetScreenOptions() as Promise<ScreenOptions | null>,
  screenStocks: (cond: ScreenCondition) =>
    StockService.ScreenStocks(cond) as Promise<ScreenResult | null>,
  getScreenCondition: () =>
    StockService.GetScreenCondition() as Promise<ScreenConditionInfo | null>,
  saveScreenCondition: (cond: ScreenCondition) =>
    StockService.SaveScreenCondition(cond) as Promise<void>,

  // ---- 缠论（见 docs/缠论功能开发方案.md 与 docs/缠论功能开发计划.md）----
  //
  // 口径要点：
  //  · 每个级别**独立**算分型/笔/线段/中枢/走势，大级别只用于印证与压制；
  //  · 无中枢不背驰、无趋势不做趋势背驰，买卖点带状态机且仅作参考；
  //  · 回到前端的标注全部按**日期**给出（不是下标），图只消费数据、不重算缠论。
  chanlun: {
    /** 单只标的在指定级别的完整分析（含多级别印证） */
    analyze: (req: ChanlunRequest) =>
      StockService.GetChanlunAnalysis(req) as Promise<ChanlunBundle | null>,
    /** 读取 / 保存缠论配置（级别权重、类型权重、背驰力度、阈值） */
    getConfig: () => StockService.GetChanlunConfig() as Promise<ChanlunConfigInfo>,
    saveConfig: (prefs: ChanlunConfig) =>
      StockService.SaveChanlunConfig(prefs) as Promise<ChanlunConfigInfo>,
    /** 逐根推进的回放快照（无未来数据验证） */
    replay: (req: ChanlunRequest) =>
      StockService.ReplayChanlun(req) as Promise<ChanlunReplay | null>,
  },

  // ---- 组合管理 ----
  portfolio: {
    getOptions: () => PortfolioService.GetPortfolioOptions() as Promise<PortfolioOptions>,
    getDatabasePath: () => PortfolioService.GetDatabasePath() as Promise<string>,
    list: () => PortfolioService.ListPortfolios() as Promise<Portfolio[]>,
    create: (input: PortfolioInput) =>
      PortfolioService.CreatePortfolio(input) as Promise<Portfolio>,
    update: (id: string, input: PortfolioInput) =>
      PortfolioService.UpdatePortfolio(id, input) as Promise<Portfolio>,
    remove: (id: string) => PortfolioService.DeletePortfolio(id) as Promise<void>,
    detail: (id: string) => PortfolioService.GetPortfolioDetail(id) as Promise<PortfolioDetail>,
    overview: () => PortfolioService.GetPortfolioOverview() as Promise<PortfolioOverview>,
    addPosition: (portfolioID: string, input: PositionInput) =>
      PortfolioService.AddPosition(portfolioID, input) as Promise<Position>,
    removePosition: (portfolioID: string, code: string) =>
      PortfolioService.DeletePosition(portfolioID, code) as Promise<void>,

    // ---- 阶段二：评分与轮动建议 ----
    /**
     * 刷新某策略的评分。
     *
     * `force=false` 是**增量刷新**：已算过的代码不再重算
     *（评分只取决于 代码 + 交易日 + 策略，重算值完全相同，
     *  但每只都省掉一次 320 个交易日的行情拉取）。
     */
    refreshStrength: (strategyId: string, limit: number, force = false) =>
      PortfolioService.RefreshStrength(strategyId, limit, force) as Promise<RefreshResult>,

    /**
     * 刷新**所有活跃策略**的评分（原版 `refresh_latest` 的语义）。
     *
     * 原版取"所有非归档组合用到的策略"去重后一次刷完；
     * 只刷当前组合的话，其它组合的评分会**悄悄过期**。
     */
    refreshStrengthAll: (limit: number, force = false) =>
      PortfolioService.RefreshStrengthAll(limit, force) as Promise<RefreshAllResult>,
    /** 当前活跃策略列表（非归档组合用到的，去重） */
    activeStrategyIds: () =>
      PortfolioService.ActiveStrategyIDs() as Promise<string[]>,
    getStrengthSummary: (strategyId: string) =>
      PortfolioService.GetStrengthSummary(strategyId) as Promise<StrengthSummary>,
    /** 生成调仓建议 */
    buildPlan: (portfolioID: string) =>
      PortfolioService.BuildPlan(portfolioID) as Promise<PlanResult>,
    /** 一键应用建议（手动组合会被拒绝） */
    applyRotation: (portfolioID: string) =>
      PortfolioService.ApplyRotation(portfolioID) as Promise<ApplyResult>,

    // ---- 资金（本金 / 现金 / 持仓市值）----
    //
    // 这两个方法用 Call.ByName 调用：新方法还没进生成绑定，
    // 按名字调用不需要等 wails 重新生成 bindings。
    /** 读资金视图：当前本金 = 现金 + 持仓市值 */
    getMoney: (portfolioID: string) =>
      Call.ByName(
        "freestock/internal/service.PortfolioService.GetPortfolioMoney",
        portfolioID,
      ) as Promise<MoneyView>,
    /** 设置本金（本地保存），并按新本金重置现金、重算股数 */
    setCapital: (portfolioID: string, capital: number) =>
      Call.ByName(
        "freestock/internal/service.PortfolioService.SetPortfolioCapital",
        portfolioID,
        capital,
      ) as Promise<MoneyView>,

    // ---- 阶段三：回测 ----
    /**
     * 历史回填：给一个日期区间内的每个交易日打分并落库。
     * **回测的前提** —— 没有历史评分就跑不了回测。
     */
    /**
     * 历史回填：给区间内每个交易日打分并落库（回测的前提）。
     *
     * `limit <= 0` 表示**全市场** —— 回测就该用全市场，只评一部分的话
     * 候选池与实盘建议不是一回事。
     *
     * `force=false` 时**增量**：已算过的 (交易日, 代码) 直接跳过。
     */
    refreshBackfill: (
      strategyId: string,
      startDate: number,
      endDate: number,
      limit: number,
      force = false,
    ) =>
      PortfolioService.RefreshBackfill(strategyId, startDate, endDate, limit, force) as Promise<BackfillResult>,

    /**
     * 区间回填**所有活跃策略**（原版 `refresh_backfill` 的语义 —— 它也是全局的，
     * 取所有非归档组合用到的策略一次刷完）。
     */
    refreshBackfillAll: (startDate: number, endDate: number, limit: number, force = false) =>
      PortfolioService.RefreshBackfillAll(startDate, endDate, limit, force) as Promise<BackfillAllResult>,
    /** 跑一次回测（区间内必须有已回填的历史评分） */
    createBacktest: (req: BacktestRequest) =>
      PortfolioService.CreateBacktest(req) as Promise<BacktestRunResult>,
    /** 读一次回测的完整结果 */
    getBacktest: (runId: string) =>
      PortfolioService.GetBacktestPayload(runId) as Promise<BacktestPayload>,
    /** 列出回测记录（portfolioId 为空时列全部） */
    listBacktests: (portfolioId: string) =>
      PortfolioService.ListBacktests(portfolioId) as Promise<BacktestRun[]>,
    /** 删除一次回测 */
    deleteBacktest: (runId: string) => PortfolioService.DeleteBacktest(runId),
    /** 批量删除回测（多选）：返回成功条数；失败走 reject（Wails 把 Go 的 error 映射成异常） */
    deleteBacktests: (runIds: string[]) =>
      PortfolioService.DeleteBacktests(runIds) as Promise<number>,

    // ---- 自动建组合 ----
    /** 预演自动组合（不落库）：按策略与范围给出首批初始持仓 */
    previewAutoPortfolio: (req: AutoPortfolioRequest) =>
      PortfolioService.PreviewAutoPortfolio(req) as Promise<AutoPortfolioResult>,
    /** 创建自动组合并落库 */
    createAutoPortfolio: (req: AutoPortfolioRequest) =>
      PortfolioService.CreateAutoPortfolio(req) as Promise<AutoPortfolioResult>,

    // ---- 每日调仓日报邮件 ----
    //
    // 定时发送由**应用内**的调度器负责（Go 侧协程，每 30 秒看一次是否到点）。
    // 所以应用没在运行时不会发送 —— 界面上把这一点写出来了。
    /** 读邮件设置与调度状态（**不含授权码原文**，只有「有没有存过」） */
    getEmailPrefs: () => PortfolioService.GetEmailPrefs() as Promise<EmailPrefsInfo>,
    /**
     * 保存邮件设置。
     *
     * ⚠ `prefs.password` 留空表示**不改**（界面拿到的是脱敏设置，
     * 直接整份回写会把已存的授权码清掉）。要清空请用 `clearEmailPassword`。
     */
    saveEmailPrefs: (prefs: EmailPrefs) =>
      PortfolioService.SaveEmailPrefs(prefs) as Promise<EmailPrefsInfo>,
    /** 清掉已保存的授权码 */
    clearEmailPassword: () =>
      PortfolioService.ClearEmailPassword() as Promise<EmailPrefsInfo>,
    /** 渲染一封"当前设置下的日报"，**不发送**（发出去就收不回来，必须先能看） */
    previewEmail: () => PortfolioService.PreviewEmail() as Promise<EmailPreview>,
    /**
     * 发一封 SMTP 测试邮件。不依赖评分数据，随时可用。
     *
     * `to` 留空时用设置里的收件人列表；填了就先只发给它（便于逐个排查）。
     */
    sendTestEmail: (to = "") =>
      PortfolioService.SendTestEmail(to) as Promise<EmailSendResult>,
    /** 立刻发送一次日报（与定时发送走同一条路径） */
    sendDailyEmailNow: () =>
      PortfolioService.SendDailyEmailNow() as Promise<EmailSendResult>,
  },
};
