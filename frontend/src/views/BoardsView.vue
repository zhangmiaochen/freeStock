<script setup lang="ts">
/**
 * 板块行情页面。
 *
 * 数据来源：板块成分名单（后端已缓存）+ 成分股行情 → 后端聚合成板块行情。
 * 之所以聚合放后端：口径（加权方式、覆盖率、重叠）属于领域逻辑，
 * 而且股票列表的行情在后端本来就有缓存，一次聚合就能出全部板块。
 *
 * 页面结构：分类切换 → 统计条 → 热力图 → 表格 →（选中后）成分股面板。
 */
import { computed, onMounted, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { Info, LayoutGrid, LoaderCircle, RefreshCw } from "@lucide/vue";

import BoardHeatmap from "@/components/boards/BoardHeatmap.vue";
import BoardMembersPanel from "@/components/boards/BoardMembersPanel.vue";
import BoardTable from "@/components/boards/BoardTable.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  api,
  errorMessage,
  type BoardCategoryInfo,
  type BoardQuote,
  type BoardQuoteResult,
} from "@/lib/backend";
import { formatAmount, formatDate, formatPercent } from "@/lib/format";
import { cn } from "@/lib/utils";

const router = useRouter();

const categories = ref<BoardCategoryInfo[]>([]);
const categoryKey = ref("");
const result = ref<BoardQuoteResult | null>(null);
const selected = ref<BoardQuote | null>(null);
const loading = ref(false);
const error = ref("");
const panel = ref<HTMLElement | null>(null);

/** 热力图容量：板块少就全画，近千个概念板块只画成交额靠前的 */
const heatmapLimit = computed(() => {
  const total = result.value?.boards.length ?? 0;
  return total <= 80 ? total : 60;
});

async function load() {
  const key = categoryKey.value;
  if (!key) return;

  loading.value = true;
  error.value = "";
  try {
    const data = await api.getBoardQuotes(key, 0);
    // 切分类是异步的，回来时可能已经不是当前分类了
    if (categoryKey.value !== key) return;
    result.value = data;
  } catch (err) {
    error.value = errorMessage(err);
    result.value = null;
  } finally {
    loading.value = false;
  }
}

async function selectCategory(key: string) {
  if (categoryKey.value === key) return;
  categoryKey.value = key;
  selected.value = null;
  await load();
}

function selectBoard(board: BoardQuote) {
  selected.value = selected.value?.code === board.code ? null : board;
  if (selected.value) {
    // 面板在表格下方，选中后滚过去，否则用户以为没反应
    void Promise.resolve().then(() => panel.value?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }
}

function openKline(code: string) {
  void router.push({ path: "/kline", query: { code } });
}

onMounted(async () => {
  try {
    categories.value = await api.getBoardCategories();
    if (categories.value.length > 0) {
      const preferred = categories.value.find((item) => item.key === "申万一级") ?? categories.value[0];
      categoryKey.value = preferred.key;
      await load();
    }
  } catch (err) {
    error.value = errorMessage(err);
  }
});

// 支持的分类有限，不做缓存失效；切回来重新拉一次保证是最新的行情
watch(categoryKey, () => {
  if (result.value && result.value.category !== categoryKey.value) result.value = null;
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <!-- 工具栏 -->
    <div class="flex shrink-0 flex-wrap items-center gap-3 border-b px-5 py-3">
      <LayoutGrid class="text-muted-foreground size-4" />
      <span class="text-sm font-medium">板块分类</span>
      <div class="flex flex-wrap items-center gap-1.5">
        <Button
          v-for="category in categories"
          :key="category.key"
          size="xs"
          :variant="categoryKey === category.key ? 'default' : 'outline'"
          @click="selectCategory(category.key)"
        >
          {{ category.name }}
          <Badge variant="secondary" class="ml-1 px-1 py-0 text-[10px]">{{ category.count }}</Badge>
        </Button>
      </div>
      <Button size="xs" variant="outline" class="ml-auto" :disabled="loading || !categoryKey" @click="load">
        <RefreshCw :class="loading ? 'animate-spin' : ''" /> 刷新
      </Button>
    </div>

    <!-- 加载中 -->
    <div v-if="loading && !result" class="flex flex-1 flex-col gap-3 px-5 py-4">
      <Skeleton class="h-10 rounded-lg" />
      <Skeleton class="h-[300px] rounded-lg" />
      <Skeleton class="h-[200px] rounded-lg" />
      <p class="text-muted-foreground flex items-center justify-center gap-2 text-xs">
        <LoaderCircle class="size-3.5 animate-spin" /> 正在聚合板块行情…
      </p>
    </div>

    <!-- 出错 -->
    <div
      v-else-if="error"
      class="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center"
    >
      <Badge variant="destructive">板块行情读取失败</Badge>
      <p class="max-w-xl text-sm">{{ error }}</p>
      <Button size="sm" variant="outline" @click="load"><RefreshCw /> 重试</Button>
    </div>

    <!-- 内容 -->
    <div v-else-if="result" class="min-h-0 flex-1 overflow-auto px-5 py-4">
      <div class="space-y-4">
        <!-- 统计条 -->
        <section class="bg-card flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border px-4 py-3 text-xs">
          <span class="text-muted-foreground">
            交易日 <b class="text-foreground">{{ formatDate(result.tradeDate) }}</b>
          </span>
          <span class="text-muted-foreground">
            上涨板块 <b class="text-up">{{ result.upBoards }}</b>
            <span class="mx-1">/</span>
            下跌 <b class="text-down">{{ result.downBoards }}</b>
            <span v-if="result.flatBoards" class="ml-1">/ 平盘 {{ result.flatBoards }}</span>
          </span>
          <span class="text-muted-foreground">
            <template v-if="result.overlapping">
              <span
                :title="`这个分类下板块互相重叠（成分总数是标的数的 ${result.overlapRatio} 倍），求和会重复计算，不是市场总成交额`"
              >
                成分成交额合计（有重叠）<b class="text-foreground">{{ formatAmount(result.totalAmount) }}</b>
              </span>
            </template>
            <template v-else>
              总成交额 <b class="text-foreground">{{ formatAmount(result.totalAmount) }}</b>
            </template>
          </span>
          <span class="text-muted-foreground">板块数 <b class="text-foreground">{{ result.boardCount }}</b></span>
          <span class="text-muted-foreground ml-auto">聚合耗时 {{ result.elapsed }}</span>
        </section>

        <!-- 重叠提示：这是最容易误读的地方，单独说一次 -->
        <p
          v-if="result.overlapping"
          class="text-muted-foreground flex items-start gap-1.5 text-[11px]"
        >
          <Info class="mt-0.5 size-3 shrink-0" />
          <span>
            「{{ result.category }}」是多对多分类：一只股票同时属于多个板块，
            成分总数是全部标的的 <b>{{ result.overlapRatio }}</b> 倍。
            因此各板块的成交额会重复计入，合计数字只用于横向比较热度，<b>不是市场总成交额</b>。
            涨跌幅不受影响（每个板块各自加权）。
          </span>
        </p>

        <!-- 热力图 -->
        <section class="h-[360px]">
          <BoardHeatmap :boards="result.boards" :limit="heatmapLimit" @select="selectBoard" />
        </section>

        <!-- 板块表格 -->
        <BoardTable
          :boards="result.boards"
          :selected-code="selected?.code"
          @select="selectBoard"
        />

        <!-- 成分股面板 -->
        <div ref="panel">
          <BoardMembersPanel
            v-if="selected"
            :board="selected"
            @close="selected = null"
          />
        </div>
      </div>
    </div>
  </div>
</template>
