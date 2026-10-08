import { computed, ref, shallowRef } from "vue";
import { defineStore } from "pinia";

import {
  api,
  errorMessage,
  onScanProgress,
  type BoardCategoryInfo,
  type BoardInfo,
  type ScanProgress,
  type StockItem,
  type StockUniverse,
} from "@/lib/backend";

export type SortKey = "code" | "name" | "close" | "pctChg" | "amount" | "turnover" | "totalMV";
export type SortDir = "asc" | "desc";

/** 一个行业分组（用于分组视图）。 */
export interface IndustryGroup {
  code: string;
  name: string;
  stocks: StockItem[];
}

export const PAGE_SIZE_OPTIONS = [50, 100, 200, 500];

export const useStockStore = defineStore("stock", () => {
  // ---- 原始数据 ----
  const universe = shallowRef<StockUniverse | null>(null);
  const loading = ref(false);
  const error = ref("");
  const progress = ref<ScanProgress | null>(null);

  const categories = ref<BoardCategoryInfo[]>([]);
  const boards = ref<BoardInfo[]>([]);
  const boardsLoading = ref(false);
  const boardsError = ref("");
  /** 已加载过的分类缓存（不需要响应式）。 */
  const boardCache = new Map<string, BoardInfo[]>();

  // ---- 筛选与展示状态 ----
  const marketKey = ref("all");
  const categoryKey = ref("");
  const boardCode = ref("");
  const keyword = ref("");
  const hideST = ref(false);
  const onlyQuoted = ref(false);
  const grouped = ref(true);
  const sortKey = ref<SortKey>("code");
  const sortDir = ref<SortDir>("asc");
  const page = ref(1);
  const pageSize = ref(50);

  let unsubscribeProgress: (() => void) | null = null;

  // ---- 派生数据 ----
  const markets = computed(() => universe.value?.markets ?? []);
  const allStocks = computed<StockItem[]>(() => universe.value?.stocks ?? []);
  const tradeDate = computed(() => universe.value?.tradeDate ?? 0);
  const sourceName = computed(() => universe.value?.sourceName ?? "");
  const sourceUrl = computed(() => universe.value?.sourceUrl ?? "");

  /** 当前股票池里出现过的代码，用于校正板块数量。 */
  const universeCodes = computed(() => {
    const set = new Set<string>();
    for (const stock of allStocks.value) set.add(stock.code);
    return set;
  });

  /**
   * 板块列表 + 落在当前股票池内的真实数量。
   *
   * 服务端给的 count 是板块的全部成分股（含退市、不在股票代码表里的代码），
   * 直接显示会和分组视图里的实际条数对不上，这里按股票池重新统计。
   */
  const boardsWithCounts = computed(() => {
    const codes = universeCodes.value;
    return boards.value.map((board) => {
      let count = 0;
      for (const code of board.symbols) {
        if (codes.has(code)) count++;
      }
      return { ...board, count };
    });
  });

  /** 当前分类下：股票代码 -> 所属板块列表。 */
  const industryIndex = computed(() => {
    const map = new Map<string, BoardInfo[]>();
    for (const board of boards.value) {
      for (const stockCode of board.symbols) {
        const list = map.get(stockCode);
        if (list) list.push(board);
        else map.set(stockCode, [board]);
      }
    }
    return map;
  });

  /** 股票代码 -> 行业名称（用于表格「行业」列）。 */
  const industryNames = computed(() => {
    const map = new Map<string, string[]>();
    for (const [stockCode, list] of industryIndex.value) {
      map.set(
        stockCode,
        list.map((board) => board.name),
      );
    }
    return map;
  });

  const filtersActive = computed(
    () =>
      marketKey.value !== "all" ||
      boardCode.value !== "" ||
      keyword.value.trim() !== "" ||
      hideST.value ||
      onlyQuoted.value,
  );

  /** 经过市场 / 行业 / 搜索过滤后的股票。 */
  const filtered = computed<StockItem[]>(() => {
    const kw = keyword.value.trim().toLowerCase();
    const market = marketKey.value;
    const board = boardCode.value;
    const index = industryIndex.value;

    return allStocks.value.filter((stock) => {
      if (market !== "all" && stock.market !== market) return false;
      if (hideST.value && stock.isST) return false;
      if (onlyQuoted.value && !stock.hasQuote) return false;
      if (board && !index.get(stock.code)?.some((item) => item.code === board)) return false;
      if (kw) {
        const name = (stock.name || "").toLowerCase();
        if (!stock.code.includes(kw) && !name.includes(kw)) return false;
      }
      return true;
    });
  });

  const sorted = computed<StockItem[]>(() => {
    const list = [...filtered.value];
    const key = sortKey.value;
    const dir = sortDir.value === "asc" ? 1 : -1;

    list.sort((a, b) => {
      let diff = 0;
      if (key === "code" || key === "name") {
        const av = key === "code" ? a.code : a.name || a.code;
        const bv = key === "code" ? b.code : b.name || b.code;
        diff = av.localeCompare(bv, "zh-Hans-CN");
      } else {
        diff = (a[key] as number) - (b[key] as number);
      }
      if (diff === 0) diff = a.code.localeCompare(b.code);
      return diff * dir;
    });
    return list;
  });

  const totalPages = computed(() =>
    Math.max(1, Math.ceil(sorted.value.length / pageSize.value)),
  );

  const currentPage = computed(() => Math.min(page.value, totalPages.value));

  const pagedStocks = computed(() => {
    const start = (currentPage.value - 1) * pageSize.value;
    return sorted.value.slice(start, start + pageSize.value);
  });

  /**
   * 按行业分组（市场分类之后，再按行业分类）。
   * 未归入任何板块的股票放在「未分类」。
   */
  const groups = computed<IndustryGroup[]>(() => {
    if (!grouped.value || boards.value.length === 0) return [];

    const buckets = new Map<string, StockItem[]>();
    const unclassified: StockItem[] = [];

    for (const stock of sorted.value) {
      const list = industryIndex.value.get(stock.code);
      if (!list || list.length === 0) {
        unclassified.push(stock);
        continue;
      }
      for (const board of list) {
        const bucket = buckets.get(board.code);
        if (bucket) bucket.push(stock);
        else buckets.set(board.code, [stock]);
      }
    }

    const result: IndustryGroup[] = [];
    for (const board of boards.value) {
      const stocks = buckets.get(board.code);
      if (stocks && stocks.length > 0) {
        result.push({ code: board.code, name: board.name, stocks });
      }
    }
    if (unclassified.length > 0) {
      result.push({ code: "__none__", name: "未分类", stocks: unclassified });
    }
    return result;
  });

  // ---- 行为 ----
  function ensureProgressSubscription() {
    if (unsubscribeProgress) return;
    unsubscribeProgress = onScanProgress((payload) => {
      progress.value = payload;
    });
  }

  async function loadUniverse(refresh = false) {
    if (loading.value) return;
    loading.value = true;
    error.value = "";
    progress.value = null;
    ensureProgressSubscription();

    try {
      const result = await api.getStockUniverse(refresh);
      if (!result) {
        throw new Error("后端没有返回股票列表");
      }
      universe.value = result;
      // 市场列表来自后端，若当前选中的市场已不存在则回到「全部」
      if (marketKey.value !== "all" && !result.markets.some((m) => m.key === marketKey.value)) {
        marketKey.value = "all";
      }
    } catch (err) {
      error.value = errorMessage(err);
    } finally {
      loading.value = false;
      progress.value = null;
    }
  }

  async function loadCategories() {
    try {
      const list = await api.getBoardCategories();
      categories.value = list;
      if (!categoryKey.value && list.length > 0) {
        // 默认使用申万一级；没有则用第一个
        const preferred = list.find((item) => item.key === "申万一级") ?? list[0];
        await selectCategory(preferred.key);
      }
    } catch (err) {
      boardsError.value = errorMessage(err);
    }
  }

  async function selectCategory(key: string) {
    categoryKey.value = key;
    boardCode.value = "";
    page.value = 1;
    boardsError.value = "";

    const cached = boardCache.get(key);
    if (cached) {
      boards.value = cached;
      return;
    }

    boardsLoading.value = true;
    try {
      const list = await api.getBoards(key);
      boardCache.set(key, list);
      if (categoryKey.value === key) {
        boards.value = list;
      }
    } catch (err) {
      boardsError.value = errorMessage(err);
      boards.value = [];
    } finally {
      boardsLoading.value = false;
    }
  }

  function selectMarket(key: string) {
    marketKey.value = key;
    page.value = 1;
  }

  function selectBoard(code: string) {
    boardCode.value = boardCode.value === code ? "" : code;
    page.value = 1;
  }

  function setKeyword(value: string) {
    keyword.value = value;
    page.value = 1;
  }

  function setSort(key: SortKey) {
    if (sortKey.value === key) {
      sortDir.value = sortDir.value === "asc" ? "desc" : "asc";
    } else {
      sortKey.value = key;
      sortDir.value = key === "code" || key === "name" ? "asc" : "desc";
    }
    page.value = 1;
  }

  function setPageSize(size: number) {
    pageSize.value = size;
    page.value = 1;
  }

  function resetFilters() {
    marketKey.value = "all";
    boardCode.value = "";
    keyword.value = "";
    hideST.value = false;
    onlyQuoted.value = false;
    page.value = 1;
  }

  /** 行业名称摘要，用于表格单元格。 */
  function industryLabel(code: string, limit = 2): string {
    const names = industryNames.value.get(code);
    if (!names || names.length === 0) return "—";
    if (names.length <= limit) return names.join("、");
    return `${names.slice(0, limit).join("、")} 等 ${names.length} 个`;
  }

  return {
    // state
    universe,
    loading,
    error,
    progress,
    categories,
    boards,
    boardsWithCounts,
    boardsLoading,
    boardsError,
    marketKey,
    categoryKey,
    boardCode,
    keyword,
    hideST,
    onlyQuoted,
    grouped,
    sortKey,
    sortDir,
    page,
    pageSize,
    // getters
    markets,
    allStocks,
    tradeDate,
    sourceName,
    sourceUrl,
    filtersActive,
    filtered,
    sorted,
    groups,
    totalPages,
    currentPage,
    pagedStocks,
    // actions
    loadUniverse,
    loadCategories,
    selectCategory,
    selectMarket,
    selectBoard,
    setKeyword,
    setSort,
    setPageSize,
    resetFilters,
    industryLabel,
  };
});
