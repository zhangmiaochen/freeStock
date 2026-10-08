<script setup lang="ts">
/**
 * 板块条件编辑器。
 *
 * 原项目（tushare-stock-tonyz）这里用的是同花顺行业/概念、东财行业/概念四个数据源，
 * FreeStock 只有 free-stockdb 的板块数据，所以换成：
 *   申万一级 / 申万二级 / 申万三级 / 概念
 * 语义保持一致：选若干板块名，any=属于任一（并集）、all=必须同时属于（交集）。
 */
import { computed, onMounted, ref, watch } from "vue";
import { Check, ChevronDown, LoaderCircle, Search, Trash2, X } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  api,
  errorMessage,
  type BoardCategoryInfo,
  type BoardFilter,
  type BoardInfo,
} from "@/lib/backend";
import { cn } from "@/lib/utils";

const props = defineProps<{
  modelValue: BoardFilter;
  categories: BoardCategoryInfo[];
}>();
const emit = defineEmits<{ "update:modelValue": [BoardFilter]; remove: [] }>();

const boards = ref<BoardInfo[]>([]);
const loading = ref(false);
const error = ref("");
const expanded = ref(false);
const keyword = ref("");

/** 每个分类的板块列表只拉一次，概念有近千个，切来切去没必要重复请求 */
const cache = new Map<string, BoardInfo[]>();

async function loadBoards(category: string) {
  if (!category) {
    boards.value = [];
    return;
  }
  const cached = cache.get(category);
  if (cached) {
    boards.value = cached;
    return;
  }
  loading.value = true;
  error.value = "";
  try {
    const list = await api.getBoards(category);
    cache.set(category, list);
    boards.value = list;
  } catch (err) {
    error.value = errorMessage(err);
    boards.value = [];
  } finally {
    loading.value = false;
  }
}

onMounted(() => void loadBoards(props.modelValue.category));
watch(
  () => props.modelValue.category,
  (category) => {
    keyword.value = "";
    void loadBoards(category);
  },
);

const filtered = computed(() => {
  const word = keyword.value.trim().toLowerCase();
  const list = word
    ? boards.value.filter((item) => item.name.toLowerCase().includes(word))
    : boards.value;
  // 成分多的板块通常更常用，按成分数排序比按字母序好用
  return list.slice(0, 300);
});

const selected = computed(() => new Set(props.modelValue.names));

function toggle(name: string) {
  const next = new Set(props.modelValue.names);
  if (next.has(name)) next.delete(name);
  else next.add(name);
  emit("update:modelValue", { ...props.modelValue, names: [...next] });
}

function removeName(name: string) {
  emit("update:modelValue", {
    ...props.modelValue,
    names: props.modelValue.names.filter((item) => item !== name),
  });
}

function setCategory(category: string) {
  // 换分类时清空已选，因为板块名在不同分类下含义不同
  emit("update:modelValue", { category, names: [], mode: props.modelValue.mode });
}

function setMode(mode: string) {
  emit("update:modelValue", { ...props.modelValue, mode });
}
</script>

<template>
  <div class="bg-background/60 space-y-2 rounded-md border p-2">
    <div class="flex flex-wrap items-center gap-1.5">
      <select
        class="bg-background h-6 rounded border px-1.5 text-xs outline-none"
        :value="modelValue.category"
        @change="setCategory(($event.target as HTMLSelectElement).value)"
      >
        <option v-for="category in categories" :key="category.key" :value="category.key">
          {{ category.name }}（{{ category.count }}）
        </option>
      </select>

      <select
        class="bg-background h-6 rounded border px-1.5 text-xs outline-none"
        :value="modelValue.mode"
        title="任一：属于其中任意一个板块即可；全部：必须同时属于所有选中板块"
        @change="setMode(($event.target as HTMLSelectElement).value)"
      >
        <option value="any">任一</option>
        <option value="all">全部</option>
      </select>

      <Button size="xs" variant="outline" @click="expanded = !expanded">
        <Search />
        选择板块
        <Badge variant="secondary" class="ml-1 px-1 py-0 text-[10px]">
          {{ modelValue.names.length }}
        </Badge>
        <ChevronDown :class="cn('size-3 transition-transform', expanded && 'rotate-180')" />
      </Button>

      <Button size="xs" variant="ghost" class="ml-auto" title="删除这条板块条件" @click="emit('remove')">
        <Trash2 />
      </Button>
    </div>

    <!-- 已选板块 -->
    <div v-if="modelValue.names.length" class="flex flex-wrap gap-1">
      <span
        v-for="name in modelValue.names"
        :key="name"
        class="bg-secondary text-secondary-foreground inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px]"
      >
        {{ name }}
        <button type="button" class="hover:text-destructive" @click="removeName(name)">
          <X class="size-3" />
        </button>
      </span>
    </div>
    <p v-else class="text-muted-foreground text-[11px]">
      还没有选板块 —— 未选择时这条条件不生效
    </p>

    <!-- 板块选择面板 -->
    <div v-if="expanded" class="space-y-1.5 border-t pt-2">
      <input
        v-model="keyword"
        class="bg-background h-6 w-full rounded border px-2 text-xs outline-none"
        placeholder="搜索板块名称"
      />
      <p v-if="loading" class="text-muted-foreground flex items-center gap-1.5 text-[11px]">
        <LoaderCircle class="size-3 animate-spin" /> 正在读取板块列表…
      </p>
      <p v-else-if="error" class="text-destructive text-[11px]">{{ error }}</p>
      <div v-else class="max-h-52 overflow-auto">
        <button
          v-for="board in filtered"
          :key="board.code + board.name"
          type="button"
          class="hover:bg-muted/50 flex w-full items-center gap-1.5 rounded px-1.5 py-1 text-left text-xs"
          :class="selected.has(board.name) ? 'bg-muted/60' : ''"
          @click="toggle(board.name)"
        >
          <Check
            class="size-3 shrink-0"
            :class="selected.has(board.name) ? 'text-primary' : 'text-transparent'"
          />
          <span class="truncate">{{ board.name }}</span>
          <span class="text-muted-foreground ml-auto shrink-0 text-[10px]">{{ board.count }}</span>
        </button>
        <p v-if="filtered.length === 0" class="text-muted-foreground px-1.5 py-2 text-[11px]">
          没有匹配的板块
        </p>
        <p
          v-if="!keyword && boards.length > filtered.length"
          class="text-muted-foreground px-1.5 py-1 text-[10px]"
        >
          共 {{ boards.length }} 个板块，按成分数显示前 {{ filtered.length }} 个；输入关键词可精确查找
        </p>
      </div>
    </div>
  </div>
</template>
