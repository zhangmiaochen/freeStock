<script setup lang="ts">
/**
 * 财务数据页面。
 *
 * 数据全部来自 cnequity 数据湖，**与当前选中的行情源无关**：
 * 即使行情走 free-stockdb，这里照样能读到财报、股本结构、股东户数、十大股东。
 *
 * 数据湖里没有的表会如实说明「未入库」，而不是显示成空表 ——
 * 「这只股票没记录」和「这张表根本没同步」是两回事。
 */
import { computed, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import {
  AlertCircle,
  BarChart3,
  Building2,
  CircleCheck,
  Info,
  LoaderCircle,
  RefreshCw,
  Users,
} from "@lucide/vue";

import StockSearchBox from "@/components/kline/StockSearchBox.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import {
  api,
  errorMessage,
  type FinancialStatements,
  type ShareholderCountItem,
  type ShareStructureItem,
  type TopHolderItem,
} from "@/lib/backend";
import { formatDate, formatLargeNumber } from "@/lib/format";

const route = useRoute();
const router = useRouter();

const code = ref("");
const data = ref<FinancialStatements | null>(null);
const loading = ref(false);
const error = ref("");

/** 该股票是否压根没有任何财务记录 */
const isEmpty = computed(() => {
  const item = data.value;
  if (!item) return true;
  return (
    item.items.length === 0 &&
    item.shareStructure.length === 0 &&
    item.shareholderCounts.length === 0 &&
    item.topHolders.length === 0
  );
});

/** 已入库的表 */
const availableTables = computed(() => {
  const status = data.value?.tableStatus ?? {};
  return Object.entries(status)
    .filter(([, exists]) => exists)
    .map(([name]) => name);
});

const missingTables = computed(() => {
  const status = data.value?.tableStatus ?? {};
  return Object.entries(status)
    .filter(([, exists]) => !exists)
    .map(([name]) => name);
});

/** 财报科目按报告期倒序分组 */
const statementGroups = computed(() => {
  const items = data.value?.items ?? [];
  const grouped = new Map<string, { itemCode: string; statementType: string; value: number }[]>();
  for (const item of items) {
    const key = item.reportPeriod || "未知报告期";
    if (!grouped.has(key)) grouped.set(key, []);
    grouped.get(key)!.push({
      itemCode: item.itemCode,
      statementType: item.statementType,
      value: item.itemValue,
    });
  }
  return [...grouped.entries()]
    .map(([period, rows]) => ({ period, rows }))
    .sort((a, b) => b.period.localeCompare(a.period));
});

/** 股东户数最新一期相对上一期的变化 */
function holderTrend(rows: ShareholderCountItem[]) {
  if (rows.length < 2) return null;
  const latest = rows[rows.length - 1];
  const prev = rows[rows.length - 2];
  if (!prev.holderCount) return null;
  return ((latest.holderCount - prev.holderCount) / prev.holderCount) * 100;
}

const latestHolders = computed<ShareholderCountItem | null>(() => {
  const rows = data.value?.shareholderCounts ?? [];
  return rows.length ? rows[rows.length - 1] : null;
});

const latestStructure = computed<ShareStructureItem | null>(() => {
  const rows = data.value?.shareStructure ?? [];
  return rows.length ? rows[rows.length - 1] : null;
});

async function load(target?: string) {
  const next = (target ?? code.value).trim();
  if (!next) return;
  code.value = next;

  loading.value = true;
  error.value = "";
  try {
    const result = await api.getFinancialStatements(next, 500);
    data.value = result;
  } catch (err) {
    error.value = errorMessage(err);
    data.value = null;
  } finally {
    loading.value = false;
  }
}

function onSelect(selected: string) {
  code.value = selected;
  void router.replace({ path: "/finance", query: { code: selected } });
  void load(selected);
}

onMounted(() => {
  const initial = (route.query.code as string | undefined) ?? "";
  if (initial) {
    code.value = initial;
    void load(initial);
  }
});

watch(
  () => route.query.code,
  (value) => {
    const next = (value as string | undefined) ?? "";
    if (next && next !== code.value) {
      code.value = next;
      void load(next);
    }
  },
);

function scopeLabel(scope: string) {
  if (scope === "top10") return "十大股东";
  if (scope === "top10_float") return "十大流通股东";
  return scope || "—";
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <!-- 顶部搜索 -->
    <div class="flex shrink-0 flex-wrap items-center gap-3 border-b px-5 py-3">
      <StockSearchBox class="min-w-[280px] flex-1" @select="onSelect" />
      <Button size="xs" variant="outline" :disabled="loading || !code" @click="load()">
        <RefreshCw :class="loading ? 'animate-spin' : ''" /> 刷新
      </Button>
    </div>

    <!-- 未选择 -->
    <div
      v-if="!code"
      class="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center"
    >
      <div class="bg-muted text-muted-foreground flex size-12 items-center justify-center rounded-xl">
        <BarChart3 class="size-5" />
      </div>
      <h2 class="text-base font-semibold">搜索一只股票查看财务数据</h2>
      <p class="text-muted-foreground max-w-lg text-sm">
        这里的数据来自 cnequity 本地数据湖，包含财报科目、股本结构、股东户数与十大股东。
        它与「数据源设置」里选的行情源无关 —— 行情走 free-stockdb 时这里照样可读。
      </p>
    </div>

    <div v-else-if="loading && !data" class="flex flex-1 flex-col gap-3 px-5 py-4">
      <Skeleton class="h-20 rounded-lg" />
      <Skeleton class="h-40 rounded-lg" />
      <p class="text-muted-foreground flex items-center justify-center gap-2 text-xs">
        <LoaderCircle class="size-3.5 animate-spin" /> 正在读取 {{ code }} 的财务数据…
      </p>
    </div>

    <div
      v-else-if="error"
      class="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center"
    >
      <Badge variant="destructive">读取失败</Badge>
      <p class="max-w-xl text-sm">{{ error }}</p>
      <Button size="sm" variant="outline" @click="load()"><RefreshCw /> 重试</Button>
    </div>

    <!-- 内容 -->
    <div v-else-if="data" class="min-h-0 flex-1 overflow-auto px-5 py-4">
      <div class="mx-auto max-w-5xl space-y-4">
        <!-- 来源状态 -->
        <section class="bg-card rounded-lg border px-4 py-3">
          <div class="flex flex-wrap items-center gap-2">
            <Info class="text-muted-foreground size-4" />
            <span class="text-sm font-medium">{{ code }} 财务数据</span>
            <Badge v-if="data.available" variant="secondary" class="gap-1 text-[11px]">
              <CircleCheck class="size-3" /> 财报已入库
            </Badge>
            <Badge v-else variant="outline" class="gap-1 text-[11px]">
              <AlertCircle class="size-3" /> 财报未入库
            </Badge>
          </div>
          <p class="text-muted-foreground mt-1.5 text-xs leading-relaxed">{{ data.detail }}</p>
          <div class="text-muted-foreground mt-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px]">
            <span>
              已入库：{{ availableTables.length ? availableTables.join("、") : "无" }}
            </span>
            <span v-if="missingTables.length">未入库：{{ missingTables.join("、") }}</span>
            <span v-if="data.reportPeriods.length">
              报告期：{{ data.reportPeriods.join("、") }}
            </span>
          </div>
        </section>

        <!-- 什么都没有 -->
        <section
          v-if="isEmpty"
          class="text-muted-foreground rounded-lg border border-dashed px-4 py-8 text-center text-sm"
        >
          <p>这张表里没有 {{ code }} 的记录。</p>
          <p class="mt-1 text-xs">
            注意区分两种情况：数据表本身没入库（看上面的「未入库」），
            还是表在库但这只股票确实没有披露。
          </p>
        </section>

        <template v-else>
          <!-- 股本结构 -->
          <section v-if="data.shareStructure.length" class="bg-card rounded-lg border">
            <header class="flex items-center gap-2 px-4 py-3">
              <Building2 class="text-muted-foreground size-4" />
              <h3 class="text-sm font-medium">股本结构</h3>
              <span class="text-muted-foreground text-[11px]">
                按变更日期记录，共 {{ data.shareStructure.length }} 次
              </span>
              <Badge v-if="latestStructure" variant="outline" class="ml-auto text-[11px]">
                最新 {{ formatDate(latestStructure.date) }}
              </Badge>
            </header>
            <Separator />
            <div class="overflow-x-auto">
              <table class="w-full text-xs">
                <thead class="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th class="px-4 py-2 text-left font-medium">变更日期</th>
                    <th class="px-4 py-2 text-right font-medium">总股本</th>
                    <th class="px-4 py-2 text-right font-medium">流通股本</th>
                    <th class="px-4 py-2 text-right font-medium">限售股</th>
                    <th class="px-4 py-2 text-right font-medium">自由流通股</th>
                    <th class="px-4 py-2 text-left font-medium">变更原因</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="row in [...data.shareStructure].reverse()" :key="row.date" class="border-t">
                    <td class="px-4 py-2">{{ formatDate(row.date) }}</td>
                    <td class="tabular px-4 py-2 text-right">{{ formatLargeNumber(row.totalShares) }}</td>
                    <td class="tabular px-4 py-2 text-right">{{ formatLargeNumber(row.floatShares) }}</td>
                    <td class="tabular px-4 py-2 text-right">
                      {{ row.restrictedShares ? formatLargeNumber(row.restrictedShares) : "—" }}
                    </td>
                    <td class="tabular px-4 py-2 text-right">
                      {{ row.freeFloatShares ? formatLargeNumber(row.freeFloatShares) : "—" }}
                    </td>
                    <td class="text-muted-foreground px-4 py-2">{{ row.changeReason || "—" }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>

          <!-- 股东户数 -->
          <section v-if="data.shareholderCounts.length" class="bg-card rounded-lg border">
            <header class="flex items-center gap-2 px-4 py-3">
              <Users class="text-muted-foreground size-4" />
              <h3 class="text-sm font-medium">股东户数</h3>
              <span class="text-muted-foreground text-[11px]">
                户数下降通常意味着筹码集中
              </span>
              <Badge
                v-if="holderTrend(data.shareholderCounts) !== null"
                :variant="(holderTrend(data.shareholderCounts) ?? 0) < 0 ? 'default' : 'secondary'"
                class="ml-auto text-[11px]"
              >
                最新一期 {{ (holderTrend(data.shareholderCounts) ?? 0) > 0 ? "+" : ""
                }}{{ (holderTrend(data.shareholderCounts) ?? 0).toFixed(2) }}%
              </Badge>
            </header>
            <Separator />
            <div class="overflow-x-auto">
              <table class="w-full text-xs">
                <thead class="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th class="px-4 py-2 text-left font-medium">统计日</th>
                    <th class="px-4 py-2 text-right font-medium">股东户数</th>
                    <th class="px-4 py-2 text-right font-medium">环比</th>
                    <th class="px-4 py-2 text-right font-medium">户均流通股</th>
                    <th class="px-4 py-2 text-right font-medium">户均市值</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="row in [...data.shareholderCounts].reverse()" :key="row.date" class="border-t">
                    <td class="px-4 py-2">{{ formatDate(row.date) }}</td>
                    <td class="tabular px-4 py-2 text-right">
                      {{ row.holderCount ? row.holderCount.toLocaleString("zh-CN") : "—" }}
                    </td>
                    <td class="tabular text-muted-foreground px-4 py-2 text-right">
                      {{ row.changePct ? `${row.changePct > 0 ? "+" : ""}${row.changePct.toFixed(2)}%` : "—" }}
                    </td>
                    <td class="tabular px-4 py-2 text-right">
                      {{ row.avgFloatShares ? row.avgFloatShares.toFixed(0) : "—" }}
                    </td>
                    <td class="tabular px-4 py-2 text-right">
                      {{ row.avgHoldingValue ? formatLargeNumber(row.avgHoldingValue) : "—" }}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>

          <!-- 十大股东 -->
          <section v-if="data.topHolders.length" class="bg-card rounded-lg border">
            <header class="flex items-center gap-2 px-4 py-3">
              <Users class="text-muted-foreground size-4" />
              <h3 class="text-sm font-medium">十大股东</h3>
              <Badge variant="outline" class="ml-auto text-[11px]">
                报告期 {{ formatDate(data.topHolderDate) }}
              </Badge>
            </header>
            <Separator />
            <div class="overflow-x-auto">
              <table class="w-full text-xs">
                <thead class="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th class="px-4 py-2 text-left font-medium">名次</th>
                    <th class="px-4 py-2 text-left font-medium">股东名称</th>
                    <th class="px-4 py-2 text-left font-medium">口径</th>
                    <th class="px-4 py-2 text-right font-medium">持股数</th>
                    <th class="px-4 py-2 text-right font-medium">持股比例</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="row in data.topHolders" :key="`${row.rank}-${row.name}`" class="border-t">
                    <td class="px-4 py-2">{{ row.rank }}</td>
                    <td class="px-4 py-2">
                      <span class="inline-flex items-center gap-1.5">
                        {{ row.name }}
                        <Badge v-if="row.isInstitution" variant="secondary" class="text-[10px]">机构</Badge>
                      </span>
                    </td>
                    <td class="text-muted-foreground px-4 py-2">{{ scopeLabel(row.scope) }}</td>
                    <td class="tabular px-4 py-2 text-right">{{ formatLargeNumber(row.holdingShares) }}</td>
                    <td class="tabular px-4 py-2 text-right">
                      {{ row.holdingPct ? `${row.holdingPct.toFixed(2)}%` : "—" }}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </section>

          <!-- 财报科目 -->
          <section v-if="statementGroups.length" class="bg-card rounded-lg border">
            <header class="flex items-center gap-2 px-4 py-3">
              <BarChart3 class="text-muted-foreground size-4" />
              <h3 class="text-sm font-medium">财报科目</h3>
              <span class="text-muted-foreground text-[11px]">
                长表结构：每个科目一行，按报告期分组
              </span>
            </header>
            <Separator />
            <div class="space-y-3 p-4">
              <div v-for="group in statementGroups" :key="group.period">
                <p class="mb-1.5 text-xs font-medium">{{ group.period }}</p>
                <div class="grid gap-x-6 gap-y-1 sm:grid-cols-2 lg:grid-cols-3">
                  <div
                    v-for="row in group.rows"
                    :key="`${group.period}-${row.itemCode}`"
                    class="flex items-baseline justify-between gap-3 text-xs"
                  >
                    <span class="text-muted-foreground truncate font-mono text-[11px]" :title="row.itemCode">
                      {{ row.itemCode }}
                    </span>
                    <span class="tabular shrink-0">{{ formatLargeNumber(row.value) }}</span>
                  </div>
                </div>
              </div>
            </div>
          </section>
        </template>
      </div>
    </div>
  </div>
</template>
