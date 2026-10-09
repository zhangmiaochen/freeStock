import { Events } from "@wailsio/runtime";

import type {
  BoardCategoryInfo,
  BoardFilter,
  BoardInfo,
  BoardMemberQuote,
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
  BackfillResult,
  BacktestPayload,
  BacktestRequest,
  BacktestRunResult,
  RefreshAllResult,
} from "../../bindings/freestock/internal/service/models";
import type {
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
import * as StockService from "../../bindings/freestock/internal/service/stockservice";
import * as PortfolioService from "../../bindings/freestock/internal/service/portfolioservice";

export type { BollPrefs, KlinePrefs, MaPrefs, MacdPrefs };
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

export type {
  BoardCategoryInfo,
  BoardFilter,
  BoardInfo,
  BoardMemberQuote,
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
  TopHolderItem,
};

/** 后端扫描进度事件负载。 */
export interface ScanProgress {
  phase: string;
  done: number;
  total: number;
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
  /** 板块行情：由成分股行情在后端聚合，口径见 internal/service/board.go */
  getBoardQuotes: (category: string, limit = 0) =>
    StockService.GetBoardQuotes(category, limit) as Promise<BoardQuoteResult | null>,
  getBoardMembers: (code: string) =>
    StockService.GetBoardMembers(code) as Promise<BoardMemberQuote[]>,
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
  /** 条件选股 */
  getScreenOptions: () => StockService.GetScreenOptions() as Promise<ScreenOptions | null>,
  screenStocks: (cond: ScreenCondition) =>
    StockService.ScreenStocks(cond) as Promise<ScreenResult | null>,
  getScreenCondition: () =>
    StockService.GetScreenCondition() as Promise<ScreenConditionInfo | null>,
  saveScreenCondition: (cond: ScreenCondition) =>
    StockService.SaveScreenCondition(cond) as Promise<void>,

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

    // ---- 自动建组合 ----
    /** 预演自动组合（不落库）：按策略与范围给出首批初始持仓 */
    previewAutoPortfolio: (req: AutoPortfolioRequest) =>
      PortfolioService.PreviewAutoPortfolio(req) as Promise<AutoPortfolioResult>,
    /** 创建自动组合并落库 */
    createAutoPortfolio: (req: AutoPortfolioRequest) =>
      PortfolioService.CreateAutoPortfolio(req) as Promise<AutoPortfolioResult>,
  },
};
