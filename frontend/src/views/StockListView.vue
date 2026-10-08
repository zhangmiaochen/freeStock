<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { ArrowUpDown, ChevronRight, Info, RefreshCw, Search } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import StockSearchBox from "@/components/kline/StockSearchBox.vue";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  formatAmount,
  formatDate,
  formatPercent,
  formatPrice,
  formatTurnover,
  isFundCode,
  trendClass,
} from "@/lib/format";
import { cn } from "@/lib/utils";
import type { StockItem } from "@/lib/backend";
import { PAGE_SIZE_OPTIONS, useStockStore, type SortKey } from "@/stores/stock";

const store = useStockStore();
const router = useRouter();

/** 点击列表某一行，跳到 K 线图表页并加载这只股票 */
function openKline(code: string) {
  void router.push({ path: "/kline", query: { code } });
}

onMounted(async () => {
  await store.loadUniverse(false);
  await store.loadCategories();
});

// ---- 搜索：交给 StockSearchBox，联想规则与 K 线页一致（代码/名称/拼音/首字母） ----
/** 换 key 可以强制重建搜索框，用于「清除筛选」时清空输入内容 */
const searchBoxKey = ref(0);

/** 从联想下拉里选中某只股票：清掉市场/行业筛选，只留这只 */
function onPickStock(code: string) {
  store.resetFilters();
  store.setKeyword(code);
  // 切到平铺列表：分组视图里这一只可能被折叠在行业下面，看不到会让人以为没搜到
  store.grouped = false;
}

/** 清除所有筛选，同时清空搜索框 */
function clearAll() {
  searchBoxKey.value += 1;
  store.resetFilters();
}

// ---- 行业筛选（本地关键字 + 分页展示） ----
const industryKeyword = ref("");
const industryLimit = ref(120);
const visibleBoards = computed(() => {
  const kw = industryKeyword.value.trim().toLowerCase();
  const list = kw
    ? store.boardsWithCounts.filter((board) => board.name.toLowerCase().includes(kw))
    : store.boardsWithCounts;
  return list.slice(0, industryLimit.value);
});
const hiddenBoardCount = computed(() => {
  const kw = industryKeyword.value.trim().toLowerCase();
  const total = kw
    ? store.boardsWithCounts.filter((board) => board.name.toLowerCase().includes(kw)).length
    : store.boardsWithCounts.length;
  return Math.max(0, total - visibleBoards.value.length);
});
watch(industryKeyword, () => {
  industryLimit.value = 120;
});

// ---- 分组视图状态 ----
const expandedGroups = ref<Set<string>>(new Set());
const groupLimits = ref<Record<string, number>>({});
const GROUP_ROW_LIMIT = 100;

function isGroupOpen(code: string) {
  return expandedGroups.value.has(code);
}

function toggleGroup(code: string) {
  const next = new Set(expandedGroups.value);
  if (next.has(code)) next.delete(code);
  else next.add(code);
  expandedGroups.value = next;
}

function toggleAllGroups() {
  if (expandedGroups.value.size > 0) {
    expandedGroups.value = new Set();
  } else {
    expandedGroups.value = new Set(store.groups.map((group) => group.code));
  }
}

function groupRows(code: string, stocks: StockItem[]) {
  const limit = groupLimits.value[code] ?? GROUP_ROW_LIMIT;
  return stocks.slice(0, limit);
}

function groupLimit(code: string) {
  return groupLimits.value[code] ?? GROUP_ROW_LIMIT;
}

function showMoreInGroup(code: string) {
  groupLimits.value = {
    ...groupLimits.value,
    [code]: (groupLimits.value[code] ?? GROUP_ROW_LIMIT) + 200,
  };
}

// ---- 进度 ----
const progressPercent = computed(() => {
  const current = store.progress;
  if (!current || !current.total) return 0;
  return Math.min(100, Math.round((current.done / current.total) * 100));
});

const progressLabel = computed(() => {
  const current = store.progress;
  if (!current) return "正在连接数据源…";
  const phase = current.phase === "fallback" ? "补抓停牌标的" : "读取最新行情";
  return `${phase} ${current.done} / ${current.total}`;
});

// ---- 展示辅助 ----
const marketChips = computed(() => [
  { key: "all", name: "全部", count: store.allStocks.length },
  ...store.markets.map((market) => ({
    key: market.key,
    name: market.name,
    count: market.count,
  })),
]);

const sortableColumns: { key: SortKey; label: string; align?: "right" }[] = [
  { key: "code", label: "代码" },
  { key: "name", label: "名称" },
  { key: "close", label: "最新价", align: "right" },
  { key: "pctChg", label: "涨跌幅", align: "right" },
  { key: "amount", label: "成交额", align: "right" },
  { key: "turnover", label: "换手率", align: "right" },
];

const resultCount = computed(() => store.sorted.length);
const hasData = computed(() => store.allStocks.length > 0);

function priceOf(stock: StockItem) {
  return formatPrice(stock.close, isFundCode(stock.code));
}

function chipClass(active: boolean) {
  return cn(
    "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs transition-colors",
    active
      ? "border-primary bg-primary text-primary-foreground"
      : "border-border bg-background text-muted-foreground hover:bg-accent hover:text-accent-foreground",
  );
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <!-- ============ 加载中 ============ -->
    <div
      v-if="store.loading && !hasData"
      class="flex flex-1 flex-col items-center justify-center gap-4 px-10"
    >
      <div class="text-center">
        <p class="text-sm font-medium">正在读取全市场股票数据</p>
        <p class="text-muted-foreground mt-1 text-xs">{{ progressLabel }}</p>
      </div>
      <Progress :model-value="progressPercent" class="w-full max-w-md" />
      <div class="grid w-full max-w-3xl grid-cols-4 gap-3">
        <Skeleton v-for="i in 8" :key="i" class="h-16 rounded-lg" />
      </div>
    </div>

    <!-- ============ 错误 ============ -->
    <div
      v-else-if="store.error && !hasData"
      class="flex flex-1 flex-col items-center justify-center gap-3 px-10 text-center"
    >
      <Badge variant="destructive">数据读取失败</Badge>
      <p class="max-w-xl text-sm">{{ store.error }}</p>
      <p class="text-muted-foreground max-w-xl text-xs">
        请确认 free-stockdb 已经启动（默认为 http://127.0.0.1:7899），或在「数据源设置」中切换到可用的数据源。
      </p>
      <div class="mt-1 flex gap-2">
        <Button size="sm" @click="store.loadUniverse(false)">
          <RefreshCw /> 重试
        </Button>
        <RouterLink to="/settings">
          <Button size="sm" variant="outline">去设置数据源</Button>
        </RouterLink>
      </div>
    </div>

    <!-- ============ 主体 ============ -->
    <template v-else>
      <!-- 筛选区 -->
      <div class="shrink-0 space-y-3 border-b px-5 py-4">
        <!-- 状态行 -->
        <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
          <div class="flex items-center gap-2">
            <Badge variant="secondary" class="font-mono text-[11px]">
              {{ store.sourceName || "未知数据源" }}
            </Badge>
            <span class="text-muted-foreground text-xs">
              最新交易日 {{ formatDate(store.tradeDate) }} · 共
              {{ store.universe?.total ?? 0 }} 只 · 有效行情
              {{ store.universe?.withQuote ?? 0 }} 只 · 耗时
              {{ store.universe?.elapsed ?? "-" }}
            </span>
          </div>
          <div class="ml-auto flex items-center gap-2">
            <Button
              size="sm"
              variant="outline"
              :disabled="store.loading"
              @click="store.loadUniverse(true)"
            >
              <RefreshCw :class="store.loading ? 'animate-spin' : ''" />
              {{ store.loading ? "刷新中" : "刷新数据" }}
            </Button>
          </div>
        </div>

        <!-- 市场分类 -->
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-muted-foreground w-14 shrink-0 text-xs">市场</span>
          <button
            v-for="chip in marketChips"
            :key="chip.key"
            type="button"
            :class="chipClass(store.marketKey === chip.key)"
            @click="store.selectMarket(chip.key)"
          >
            {{ chip.name }}
            <span class="tabular opacity-70">{{ chip.count }}</span>
          </button>
        </div>

        <!-- 行业分类 -->
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-muted-foreground w-14 shrink-0 text-xs">行业分类</span>
          <button
            v-for="category in store.categories"
            :key="category.key"
            type="button"
            :class="chipClass(store.categoryKey === category.key)"
            @click="store.selectCategory(category.key)"
          >
            {{ category.name }}
            <span class="tabular opacity-70">{{ category.count }}</span>
          </button>
          <span v-if="store.boardsLoading" class="text-muted-foreground text-xs">
            正在加载板块…
          </span>
          <span v-else-if="store.boardsError" class="text-destructive text-xs">
            {{ store.boardsError }}
          </span>
        </div>

        <!-- 行业列表 -->
        <div v-if="store.boards.length" class="flex items-start gap-2">
          <span class="text-muted-foreground w-14 shrink-0 pt-1 text-xs">行业</span>
          <div class="min-w-0 flex-1 space-y-2">
            <div class="flex max-h-28 flex-wrap gap-1.5 overflow-y-auto pr-1">
              <button
                type="button"
                :class="chipClass(store.boardCode === '')"
                @click="store.selectBoard('')"
              >
                全部行业
              </button>
              <button
                v-for="board in visibleBoards"
                :key="board.code"
                type="button"
                :class="chipClass(store.boardCode === board.code)"
                @click="store.selectBoard(board.code)"
              >
                {{ board.name }}
                <span class="tabular opacity-70">{{ board.count }}</span>
              </button>
            </div>
            <div class="flex items-center gap-2">
              <div class="relative w-52">
                <Search
                  class="text-muted-foreground pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2"
                />
                <Input
                  v-model="industryKeyword"
                  placeholder="搜索行业名称"
                  class="h-7 pl-7 text-xs"
                />
              </div>
              <Button
                v-if="hiddenBoardCount > 0"
                size="xs"
                variant="ghost"
                @click="industryLimit += 200"
              >
                还有 {{ hiddenBoardCount }} 个行业，显示更多
              </Button>
            </div>
          </div>
        </div>

        <Separator />

        <!-- 搜索与视图选项 -->
        <div class="flex flex-wrap items-center gap-3">
          <StockSearchBox
            :key="searchBoxKey"
            mode="filter"
            width-class="w-72"
            :autofocus="false"
            placeholder="搜索代码 / 名称 / 拼音首字母"
            @keyword-change="store.setKeyword"
            @select="onPickStock"
          />

          <div class="flex items-center gap-1.5">
            <Button
              size="xs"
              :variant="store.grouped ? 'default' : 'outline'"
              @click="store.grouped = true"
            >
              按行业分组
            </Button>
            <Button
              size="xs"
              :variant="!store.grouped ? 'default' : 'outline'"
              @click="store.grouped = false"
            >
              平铺列表
            </Button>
          </div>

          <Button
            size="xs"
            :variant="store.hideST ? 'default' : 'outline'"
            @click="store.hideST = !store.hideST"
          >
            隐藏 ST
          </Button>
          <Button
            size="xs"
            :variant="store.onlyQuoted ? 'default' : 'outline'"
            @click="store.onlyQuoted = !store.onlyQuoted"
          >
            仅有行情
          </Button>

          <div class="ml-auto flex items-center gap-2">
            <span class="text-muted-foreground text-xs">
              命中 <span class="text-foreground tabular font-medium">{{ resultCount }}</span> 只
            </span>
            <Button
              v-if="store.filtersActive"
              size="xs"
              variant="ghost"
              @click="clearAll()"
            >
              清除筛选
            </Button>
          </div>
        </div>
      </div>

      <!-- 列表区 -->
      <div class="min-h-0 flex-1 overflow-auto">
        <!-- 分组视图 -->
        <div v-if="store.grouped" class="space-y-2 px-5 py-4">
          <div v-if="store.groups.length" class="flex items-center gap-2 pb-1">
            <Button size="xs" variant="ghost" @click="toggleAllGroups">
              {{ expandedGroups.size > 0 ? "全部收起" : "全部展开" }}
            </Button>
            <span class="text-muted-foreground text-xs">
              共 {{ store.groups.length }} 个行业分组（同一只股票可能属于多个概念板块）
            </span>
          </div>

          <Collapsible
            v-for="group in store.groups"
            :key="group.code"
            :open="isGroupOpen(group.code)"
            @update:open="toggleGroup(group.code)"
          >
            <div class="bg-card overflow-hidden rounded-lg border">
              <CollapsibleTrigger as-child>
                <button
                  type="button"
                  class="hover:bg-accent/50 flex w-full items-center gap-2 px-3 py-2 text-left transition-colors"
                >
                  <ChevronRight
                    class="text-muted-foreground size-4 shrink-0 transition-transform"
                    :class="isGroupOpen(group.code) ? 'rotate-90' : ''"
                  />
                  <span class="truncate text-sm font-medium">{{ group.name }}</span>
                  <Badge variant="secondary" class="tabular shrink-0 text-[11px]">
                    {{ group.stocks.length }}
                  </Badge>
                </button>
              </CollapsibleTrigger>

              <CollapsibleContent>
                <Separator />
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead class="w-[100px]">代码</TableHead>
                      <TableHead>名称</TableHead>
                      <TableHead class="w-[110px]">市场</TableHead>
                      <TableHead class="w-[100px] text-right">最新价</TableHead>
                      <TableHead class="w-[100px] text-right">涨跌幅</TableHead>
                      <TableHead class="w-[120px] text-right">成交额</TableHead>
                      <TableHead class="w-[90px] text-right">换手率</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    <TableRow
                      v-for="stock in groupRows(group.code, group.stocks)"
                      :key="`${group.code}-${stock.code}`"
                      class="cursor-pointer"
                      title="点击查看 K 线"
                      @click="openKline(stock.code)"
                    >
                      <TableCell class="tabular font-mono text-xs">
                        {{ stock.code }}
                      </TableCell>
                      <TableCell class="max-w-[240px] truncate">
                        <span class="inline-flex items-center gap-1.5">
                          {{ stock.name || "—" }}
                          <Badge
                            v-if="stock.isST"
                            variant="destructive"
                            class="px-1 py-0 text-[10px]"
                          >
                            ST
                          </Badge>
                        </span>
                      </TableCell>
                      <TableCell class="text-muted-foreground text-xs">
                        {{ stock.marketCN }}
                      </TableCell>
                      <TableCell class="tabular text-right">
                        {{ priceOf(stock) }}
                      </TableCell>
                      <TableCell
                        class="tabular text-right font-medium"
                        :class="trendClass(stock.pctChg)"
                      >
                        {{ stock.hasQuote ? formatPercent(stock.pctChg) : "—" }}
                      </TableCell>
                      <TableCell class="tabular text-muted-foreground text-right text-xs">
                        {{ formatAmount(stock.amount) }}
                      </TableCell>
                      <TableCell class="tabular text-muted-foreground text-right text-xs">
                        {{ formatTurnover(stock.turnover) }}
                      </TableCell>
                    </TableRow>
                  </TableBody>
                </Table>
                <div
                  v-if="group.stocks.length > groupLimit(group.code)"
                  class="border-t px-3 py-2"
                >
                  <Button size="xs" variant="ghost" @click="showMoreInGroup(group.code)">
                    显示更多（已显示 {{ groupLimit(group.code) }} /
                    {{ group.stocks.length }}）
                  </Button>
                </div>
              </CollapsibleContent>
            </div>
          </Collapsible>

          <div
            v-if="store.groups.length === 0"
            class="text-muted-foreground flex flex-col items-center gap-2 py-16 text-sm"
          >
            <Info class="size-5" />
            当前筛选条件下没有匹配的股票
          </div>
        </div>

        <!-- 平铺视图 -->
        <div v-else class="px-5 py-4">
          <div class="bg-card overflow-hidden rounded-lg border">
            <Table>
              <TableHeader class="bg-muted/40 sticky top-0 z-10">
                <TableRow>
                  <TableHead
                    v-for="column in sortableColumns"
                    :key="column.key"
                    :class="[
                      column.key === 'code' ? 'w-[100px]' : '',
                      column.key === 'name' ? 'min-w-[160px]' : '',
                      column.align === 'right' ? 'text-right' : '',
                    ]"
                  >
                    <button
                      type="button"
                      class="hover:text-foreground inline-flex items-center gap-1"
                      :class="column.align === 'right' ? 'flex-row-reverse' : ''"
                      @click="store.setSort(column.key)"
                    >
                      {{ column.label }}
                      <ArrowUpDown
                        class="size-3"
                        :class="
                          store.sortKey === column.key
                            ? 'text-foreground'
                            : 'text-muted-foreground/50'
                        "
                      />
                    </button>
                  </TableHead>
                  <TableHead class="w-[110px]">市场</TableHead>
                  <TableHead class="min-w-[180px]">行业</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow
                  v-for="stock in store.pagedStocks"
                  :key="stock.code"
                  class="cursor-pointer"
                  title="点击查看 K 线"
                  @click="openKline(stock.code)"
                >
                  <TableCell class="tabular font-mono text-xs">{{ stock.code }}</TableCell>
                  <TableCell class="max-w-[220px] truncate">
                    <span class="inline-flex items-center gap-1.5">
                      {{ stock.name || "—" }}
                      <Badge
                        v-if="stock.isST"
                        variant="destructive"
                        class="px-1 py-0 text-[10px]"
                      >
                        ST
                      </Badge>
                    </span>
                  </TableCell>
                  <TableCell class="tabular text-right">{{ priceOf(stock) }}</TableCell>
                  <TableCell
                    class="tabular text-right font-medium"
                    :class="trendClass(stock.pctChg)"
                  >
                    {{ stock.hasQuote ? formatPercent(stock.pctChg) : "—" }}
                  </TableCell>
                  <TableCell class="tabular text-muted-foreground text-right text-xs">
                    {{ formatAmount(stock.amount) }}
                  </TableCell>
                  <TableCell class="tabular text-muted-foreground text-right text-xs">
                    {{ formatTurnover(stock.turnover) }}
                  </TableCell>
                  <TableCell class="text-muted-foreground text-xs">
                    {{ stock.marketCN }}
                  </TableCell>
                  <TableCell class="text-muted-foreground max-w-[260px] truncate text-xs">
                    {{ store.industryLabel(stock.code) }}
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </div>

          <div
            v-if="store.pagedStocks.length === 0"
            class="text-muted-foreground flex flex-col items-center gap-2 py-16 text-sm"
          >
            <Info class="size-5" />
            当前筛选条件下没有匹配的股票
          </div>
        </div>
      </div>

      <!-- 分页（仅平铺视图） -->
      <div
        v-if="!store.grouped && store.sorted.length > 0"
        class="flex shrink-0 flex-wrap items-center gap-3 border-t px-5 py-2"
      >
        <div class="flex items-center gap-1.5">
          <span class="text-muted-foreground text-xs">每页</span>
          <Button
            v-for="size in PAGE_SIZE_OPTIONS"
            :key="size"
            size="xs"
            :variant="store.pageSize === size ? 'default' : 'outline'"
            @click="store.setPageSize(size)"
          >
            {{ size }}
          </Button>
        </div>

        <div class="ml-auto flex items-center gap-2">
          <span class="text-muted-foreground tabular text-xs">
            第 {{ store.currentPage }} / {{ store.totalPages }} 页
          </span>
          <Button
            size="xs"
            variant="outline"
            :disabled="store.currentPage <= 1"
            @click="store.page = 1"
          >
            首页
          </Button>
          <Button
            size="xs"
            variant="outline"
            :disabled="store.currentPage <= 1"
            @click="store.page = store.currentPage - 1"
          >
            上一页
          </Button>
          <Button
            size="xs"
            variant="outline"
            :disabled="store.currentPage >= store.totalPages"
            @click="store.page = store.currentPage + 1"
          >
            下一页
          </Button>
          <Button
            size="xs"
            variant="outline"
            :disabled="store.currentPage >= store.totalPages"
            @click="store.page = store.totalPages"
          >
            末页
          </Button>
        </div>
      </div>
    </template>
  </div>
</template>
