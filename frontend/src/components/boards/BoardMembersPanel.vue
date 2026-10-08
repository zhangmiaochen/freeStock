<script setup lang="ts">
/**
 * 板块成分股面板：点某个板块后展开，列出成分股行情。
 *
 * 成分列表单独一次请求（GetBoardMembers），因为板块行情接口一次返回近千个
 * 板块，如果连成分一起带回来，payload 会大到没必要。
 */
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { ChartCandlestick, LoaderCircle, X } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api, errorMessage, type BoardMemberQuote, type BoardQuote } from "@/lib/backend";
import { formatAmount, formatPercent } from "@/lib/format";
import { cn } from "@/lib/utils";

const props = defineProps<{ board: BoardQuote | null }>();
const emit = defineEmits<{ close: [] }>();

const router = useRouter();

const members = ref<BoardMemberQuote[]>([]);
const loading = ref(false);
const error = ref("");
/** 只看上涨 / 只看下跌 */
const filter = ref<"all" | "up" | "down">("all");
const keyword = ref("");

async function load(code: string) {
  loading.value = true;
  error.value = "";
  members.value = [];
  try {
    members.value = await api.getBoardMembers(code);
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    loading.value = false;
  }
}

watch(
  () => props.board?.code,
  (code) => {
    if (code) void load(code);
  },
  { immediate: true },
);

const filtered = computed(() => {
  const word = keyword.value.trim().toLowerCase();
  return members.value.filter((item) => {
    if (filter.value === "up" && !(item.pctChg > 0)) return false;
    if (filter.value === "down" && !(item.pctChg < 0)) return false;
    if (!word) return true;
    return item.code.includes(word) || (item.name ?? "").toLowerCase().includes(word);
  });
});

const stats = computed(() => {
  const quoted = members.value.filter((item) => item.close > 0);
  const up = quoted.filter((item) => item.pctChg > 0).length;
  const down = quoted.filter((item) => item.pctChg < 0).length;
  const amount = quoted.reduce((sum, item) => sum + item.amount, 0);
  return { total: members.value.length, quoted: quoted.length, up, down, amount };
});

function openKline(code: string) {
  void router.push({ path: "/kline", query: { code } });
}

function pctClass(value: number) {
  if (!Number.isFinite(value) || value === 0) return "text-flat";
  return value > 0 ? "text-up" : "text-down";
}
</script>

<template>
  <section v-if="board" class="bg-card overflow-hidden rounded-lg border">
    <header class="flex flex-wrap items-center gap-2 px-4 py-3">
      <h3 class="text-sm font-medium">{{ board.name }}</h3>
      <Badge variant="outline" class="font-mono text-[11px]">{{ board.code }}</Badge>
      <Badge variant="secondary" class="text-[11px]">{{ board.category }}</Badge>
      <span class="tabular text-sm font-medium" :class="pctClass(board.pctChg)">
        {{ formatPercent(board.pctChg) }}
      </span>

      <span class="text-muted-foreground ml-auto text-[11px]">
        成分 {{ stats.total }} 只（有行情 {{ stats.quoted }}）
        　上涨 <b class="text-up">{{ stats.up }}</b>
        　下跌 <b class="text-down">{{ stats.down }}</b>
        　成交额 {{ formatAmount(stats.amount) }}
      </span>
      <Button size="xs" variant="ghost" @click="emit('close')"><X /> 收起</Button>
    </header>

    <div class="flex flex-wrap items-center gap-2 border-t px-4 py-2">
      <Button
        v-for="option in [
          { key: 'all', label: '全部' },
          { key: 'up', label: '仅上涨' },
          { key: 'down', label: '仅下跌' },
        ]"
        :key="option.key"
        size="xs"
        :variant="filter === option.key ? 'default' : 'outline'"
        @click="filter = option.key as typeof filter"
      >
        {{ option.label }}
      </Button>
      <input
        v-model="keyword"
        class="bg-background h-7 min-w-[160px] flex-1 rounded border px-2 text-xs outline-none"
        placeholder="按代码或名称过滤成分股"
      />
    </div>

    <div v-if="loading" class="text-muted-foreground flex items-center justify-center gap-2 py-10 text-xs">
      <LoaderCircle class="size-4 animate-spin" /> 正在读取成分股…
    </div>

    <p v-else-if="error" class="px-4 py-6 text-center text-xs">{{ error }}</p>

    <div v-else class="max-h-[420px] overflow-auto border-t">
      <table class="w-full text-xs">
        <thead class="bg-muted/40 text-muted-foreground sticky top-0 z-10 backdrop-blur">
          <tr>
            <th class="px-3 py-2 text-left font-medium">代码</th>
            <th class="px-3 py-2 text-left font-medium">名称</th>
            <th class="px-3 py-2 text-right font-medium">最新价</th>
            <th class="px-3 py-2 text-right font-medium">涨跌幅</th>
            <th class="px-3 py-2 text-right font-medium">成交额</th>
            <th class="px-3 py-2 text-right font-medium"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in filtered" :key="item.code" class="hover:bg-muted/30 border-t">
            <td class="tabular px-3 py-1.5 font-mono">{{ item.code }}</td>
            <td class="px-3 py-1.5">
              {{ item.name || "—" }}
              <span v-if="!item.close" class="text-muted-foreground text-[10px]">（无行情）</span>
            </td>
            <td class="tabular px-3 py-1.5 text-right">
              {{ item.close ? item.close.toFixed(2) : "—" }}
            </td>
            <td class="tabular px-3 py-1.5 text-right" :class="pctClass(item.pctChg)">
              {{ item.close ? formatPercent(item.pctChg) : "—" }}
            </td>
            <td class="tabular px-3 py-1.5 text-right">
              {{ item.amount ? formatAmount(item.amount) : "—" }}
            </td>
            <td class="px-3 py-1.5 text-right">
              <button
                type="button"
                class="text-muted-foreground hover:text-foreground"
                title="查看 K 线"
                @click="openKline(item.code)"
              >
                <ChartCandlestick class="size-3.5" />
              </button>
            </td>
          </tr>
          <tr v-if="filtered.length === 0">
            <td colspan="6" class="text-muted-foreground px-3 py-8 text-center">
              没有符合条件的成分股
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
