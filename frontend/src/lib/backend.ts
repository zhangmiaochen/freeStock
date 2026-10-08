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
import * as StockService from "../../bindings/freestock/internal/service/stockservice";

export type { BollPrefs, KlinePrefs, MaPrefs, MacdPrefs };

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
};
