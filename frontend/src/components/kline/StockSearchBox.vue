<script setup lang="ts">
/**
 * 股票 / 指数搜索框：支持代码、名称、拼音全拼、拼音首字母，带联想下拉与键盘导航。
 *
 * 两种选中行为（mode）：
 *   - navigate：选中后清空输入框（K 线页用来切换标的）
 *   - filter  ：选中后把代码留在输入框（列表页用来筛出这一只）
 *
 * 无论哪种模式，输入内容变化都会通过 keyword-change 抛给父组件，
 * 方便父组件做本地筛选。
 *
 * **指数**也在这份联想里（`isIndex`），但指数必须带交易所后缀
 * （`000001.SH` 上证指数 vs `000001` 平安银行），所以界面直接把带后缀的代码
 * 显示出来，并打一个「指数」标记。
 */
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { LoaderCircle, Search, X } from "@lucide/vue";

import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { api, errorMessage, type StockSearchItem } from "@/lib/backend";
import { cn } from "@/lib/utils";

const props = withDefaults(
  defineProps<{
    placeholder?: string;
    /** 输入框宽度类名 */
    widthClass?: string;
    autofocus?: boolean;
    /** 选中联想项后的行为 */
    mode?: "navigate" | "filter";
    /**
     * 是否把**指数**放进联想结果。
     *
     * 列表页要关掉：那个页面是"筛出股票"，给一个筛不出来的指数只会让人困惑
     * （指数不在股票列表里）。K 线页保持开启 —— 那里指数与股票走同一个入口。
     */
    allowIndex?: boolean;
  }>(),
  {
    placeholder: "搜索代码 / 名称 / 拼音首字母（支持指数，如 000001.SH）",
    widthClass: "w-full max-w-xl",
    autofocus: true,
    mode: "navigate",
    allowIndex: true,
  },
);

const emit = defineEmits<{
  select: [code: string, item: StockSearchItem];
  "keyword-change": [keyword: string];
}>();

const MATCH_LABEL: Record<string, string> = {
  code: "代码",
  name: "名称",
  initials: "拼音首字母",
  pinyin: "拼音",
};

const keyword = ref("");
const results = ref<StockSearchItem[]>([]);
const loading = ref(false);
const open = ref(false);
const activeIndex = ref(0);
const error = ref("");
const root = ref<HTMLElement | null>(null);
const inputRef = ref<InstanceType<typeof Input> | null>(null);

let debounceTimer: number | undefined;
let requestSeq = 0;
/** 选中联想项后会把代码写回输入框，这次写入不需要再触发一次搜索 */
let skipNextSearch = false;

async function runSearch(raw: string) {
  const seq = ++requestSeq;
  loading.value = true;
  try {
    const list = (await api.searchStocks(raw, 12)) ?? [];
    if (seq !== requestSeq) return; // 只采纳最后一次请求的结果
    // 列表页不显示指数：它筛的是股票列表，指数筛不出来
    results.value = props.allowIndex ? list : list.filter((item) => !item.isIndex);
    activeIndex.value = 0;
    open.value = true;
    error.value = "";
  } catch (err) {
    if (seq !== requestSeq) return;
    error.value = errorMessage(err);
    results.value = [];
    open.value = true;
  } finally {
    if (seq === requestSeq) loading.value = false;
  }
}

watch(keyword, (value) => {
  window.clearTimeout(debounceTimer);
  const trimmed = value.trim();

  // 输入内容变化都通知父组件（列表页据此做本地筛选）
  emit("keyword-change", trimmed);

  if (!trimmed) {
    results.value = [];
    open.value = false;
    error.value = "";
    return;
  }
  if (skipNextSearch) {
    skipNextSearch = false;
    return;
  }
  debounceTimer = window.setTimeout(() => void runSearch(trimmed), 150);
});

function choose(item: StockSearchItem) {
  emit("select", item.code, item);
  results.value = [];
  open.value = false;

  if (props.mode === "filter") {
    // 把代码留在输入框里，父组件据此筛出这一只
    skipNextSearch = true;
    keyword.value = item.code;
    return;
  }
  keyword.value = "";
}

function onKeydown(event: KeyboardEvent) {
  if (!open.value || results.value.length === 0) {
    if (event.key === "Enter") {
      const trimmed = keyword.value.trim();
      if (trimmed) {
        window.clearTimeout(debounceTimer);
        void runSearch(trimmed);
      }
    }
    return;
  }

  switch (event.key) {
    case "ArrowDown":
      event.preventDefault();
      activeIndex.value = (activeIndex.value + 1) % results.value.length;
      break;
    case "ArrowUp":
      event.preventDefault();
      activeIndex.value = (activeIndex.value - 1 + results.value.length) % results.value.length;
      break;
    case "Enter":
      event.preventDefault();
      choose(results.value[activeIndex.value]);
      break;
    case "Escape":
      open.value = false;
      break;
  }
}

function onDocumentMouseDown(event: MouseEvent) {
  if (root.value && !root.value.contains(event.target as Node)) {
    open.value = false;
  }
}

onMounted(() => {
  document.addEventListener("mousedown", onDocumentMouseDown);
  if (props.autofocus) {
    window.setTimeout(() => inputRef.value?.$el?.focus?.(), 60);
  }
});

onBeforeUnmount(() => {
  document.removeEventListener("mousedown", onDocumentMouseDown);
  window.clearTimeout(debounceTimer);
});
</script>

<template>
  <div ref="root" :class="cn('relative', props.widthClass)">
    <div class="relative">
      <Search
        class="text-muted-foreground pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2"
      />
      <Input
        ref="inputRef"
        v-model="keyword"
        :placeholder="props.placeholder"
        class="h-10 pr-9 pl-9"
        spellcheck="false"
        autocomplete="off"
        @keydown="onKeydown"
        @focus="keyword.trim() && (open = true)"
      />
      <LoaderCircle
        v-if="loading"
        class="text-muted-foreground absolute top-1/2 right-3 size-4 -translate-y-1/2 animate-spin"
      />
      <button
        v-else-if="keyword"
        type="button"
        class="text-muted-foreground hover:text-foreground absolute top-1/2 right-3 -translate-y-1/2"
        @click="keyword = ''"
      >
        <X class="size-4" />
      </button>
    </div>

    <!-- 联想下拉 -->
    <div
      v-if="open && (results.length > 0 || error)"
      class="bg-popover text-popover-foreground absolute z-50 mt-1 max-h-80 w-full overflow-y-auto rounded-lg border shadow-lg"
    >
      <p v-if="error" class="text-destructive px-3 py-2 text-xs">{{ error }}</p>
      <button
        v-for="(item, index) in results"
        :key="item.code"
        type="button"
        :class="
          cn(
            'flex w-full items-center gap-3 px-3 py-2 text-left transition-colors',
            index === activeIndex ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/50',
          )
        "
        @mouseenter="activeIndex = index"
        @click="choose(item)"
      >
        <span class="tabular w-20 shrink-0 font-mono text-xs">{{ item.code }}</span>
        <Badge v-if="item.isIndex" variant="secondary" class="shrink-0 px-1 py-0 text-[10px]">指数</Badge>
        <span class="min-w-0 flex-1 truncate text-sm">{{ item.name }}</span>
        <span class="text-muted-foreground shrink-0 text-[11px]">
          {{ MATCH_LABEL[item.matchOn] ?? item.matchOn }}
        </span>
        <span
          class="tabular w-20 shrink-0 text-right text-xs"
          :class="item.pctChg > 0 ? 'text-up' : item.pctChg < 0 ? 'text-down' : 'text-flat'"
        >
          {{ item.pctChg > 0 ? "+" : "" }}{{ item.pctChg.toFixed(2) }}%
        </span>
      </button>
      <p
        v-if="!error && results.length === 0 && !loading"
        class="text-muted-foreground px-3 py-3 text-xs"
      >
        没有匹配的{{ allowIndex ? "股票 / 指数" : "股票" }}
      </p>
    </div>
  </div>
</template>
