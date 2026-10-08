<script setup lang="ts">
/**
 * cnequity 数据湖卡片。
 *
 * 存在的意义：free-stockdb 的默认本地数据集**不含财务报表**，
 * 而 ROE 之外的科目（营收 / 毛利率 / 资产负债 / 现金流）只能从别处补。
 * 这张卡片告诉用户：数据湖在哪、财报入库没有、没入库该怎么办。
 */
import { computed, onMounted, ref } from "vue";
import { CircleAlert, CircleCheck, Database, FolderOpen, RefreshCw } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { api, errorMessage, type DataLakeInfo, type DatasetStatus } from "@/lib/backend";

const info = ref<DataLakeInfo | null>(null);
const rootInput = ref("");
const busy = ref(false);
const message = ref("");

async function load() {
  busy.value = true;
  message.value = "";
  try {
    const result = await api.getDataLakeStatus();
    info.value = result;
    rootInput.value = result?.root ?? "";
  } catch (err) {
    message.value = errorMessage(err);
  } finally {
    busy.value = false;
  }
}

async function applyRoot() {
  busy.value = true;
  message.value = "";
  try {
    info.value = await api.setDataLakeRoot(rootInput.value.trim());
    message.value = info.value?.available ? "已切换数据湖" : "该路径下没有 curated 目录";
  } catch (err) {
    message.value = errorMessage(err);
  } finally {
    busy.value = false;
  }
}

onMounted(load);

const status = computed<DatasetStatus[]>(() => info.value?.status ?? []);
const statement = computed(() => status.value.find((item) => item.name === "financial_statement_items"));
/** curated 下真实存在的数据集：来自后端直接列目录，而不是上面那几个财务数据集的探测结果 */
const presentDatasets = computed(() => info.value?.datasets ?? []);

function partitionSummary(item: DatasetStatus): string {
  if (item.partitions.length === 0) return "—";
  if (item.partitions.length <= 4) {
    return item.partitions.map((part) => part.value).join("、");
  }
  const values = item.partitions.map((part) => part.value);
  return `${values[0]} … ${values[values.length - 1]}（共 ${values.length} 个）`;
}

function formatRows(rows: number): string {
  if (rows >= 1e8) return `${(rows / 1e8).toFixed(2)} 亿行`;
  if (rows >= 1e4) return `${(rows / 1e4).toFixed(1)} 万行`;
  return `${rows} 行`;
}
</script>

<template>
  <section class="rounded-lg border p-4">
    <div class="flex flex-wrap items-center gap-2">
      <Database class="text-muted-foreground size-4" />
      <h3 class="text-sm font-medium">财务数据来源（cnequity 数据湖）</h3>
      <Badge v-if="info?.available" variant="secondary" class="text-[11px]">
        <CircleCheck class="mr-1 size-3" /> 已连接
      </Badge>
      <Badge v-else variant="outline" class="text-[11px]">
        <CircleAlert class="mr-1 size-3" /> 未连接
      </Badge>
      <Button size="xs" variant="ghost" class="ml-auto" :disabled="busy" @click="load">
        <RefreshCw :class="busy ? 'animate-spin' : ''" /> 重新检测
      </Button>
    </div>

    <p class="text-muted-foreground mt-2 text-xs leading-relaxed">
      免费数据源（free-stockdb）只提供行情、复权与资金流，<b>没有财务报表</b>。
      因此界面上的 ROE 等指标是用 PE/PB 推导的 TTM 值；如果要在意营收、毛利率、
      资产负债这些科目，需要从外部补充。cnequity 的
      <code>financial_statement_items</code> 就是干这个的。
    </p>

    <Separator class="my-3" />

    <!-- 路径设置 -->
    <div class="flex flex-wrap items-center gap-2">
      <FolderOpen class="text-muted-foreground size-4 shrink-0" />
      <Input
        v-model="rootInput"
        class="h-8 min-w-[320px] flex-1 font-mono text-xs"
        placeholder="数据湖根目录（包含 curated 子目录的那一级），留空则自动探测"
        @keyup.enter="applyRoot"
      />
      <Button size="sm" variant="outline" :disabled="busy" @click="applyRoot">应用</Button>
    </div>
    <p class="text-muted-foreground mt-1.5 text-[11px]">
      也可以设置环境变量 <code>FREESTOCK_CNLAKE</code> 指向同一位置。
    </p>

    <p v-if="message" class="text-muted-foreground mt-2 text-xs">{{ message }}</p>

    <!-- 状态说明 -->
    <div
      v-if="info"
      class="mt-3 rounded-md border px-3 py-2 text-xs leading-relaxed"
      :class="statement?.exists ? 'bg-emerald-500/5' : 'bg-amber-500/5'"
    >
      <span class="font-medium">
        {{ statement?.exists ? "财报已入库" : "财报尚未入库" }}
      </span>
      <span class="text-muted-foreground"> — {{ info.detail }}</span>
    </div>

    <!-- 各数据集 -->
    <div v-if="status.length" class="mt-3 overflow-hidden rounded-md border">
      <table class="w-full text-xs">
        <thead class="bg-muted/40 text-muted-foreground">
          <tr>
            <th class="px-3 py-1.5 text-left font-medium">数据集</th>
            <th class="px-3 py-1.5 text-left font-medium">状态</th>
            <th class="px-3 py-1.5 text-right font-medium">行数</th>
            <th class="px-3 py-1.5 text-left font-medium">分区</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in status" :key="item.name" class="border-t">
            <td class="px-3 py-1.5 font-mono">{{ item.name }}</td>
            <td class="px-3 py-1.5">
              <span v-if="item.exists" class="text-emerald-500">已入库</span>
              <span v-else class="text-muted-foreground">未入库</span>
            </td>
            <td class="tabular px-3 py-1.5 text-right">
              {{ item.exists ? formatRows(item.rows) : "—" }}
            </td>
            <td class="text-muted-foreground px-3 py-1.5">{{ partitionSummary(item) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <p v-if="info?.available && !statement?.exists" class="text-muted-foreground mt-3 text-[11px] leading-relaxed">
      补充办法：让 cnequity 的 <code>cne init</code> 跑到 <code>fundamentals</code> 阶段。
      注意 <code>phase2c_daily_bars_backfill</code> 一旦失败会阻断后续所有 phase，
      财报不会入库 —— 那时日志里会出现
      <code>still incomplete after retry</code>。
    </p>

    <p
      v-if="info?.available && presentDatasets.length"
      class="text-muted-foreground mt-2 text-[11px]"
    >
      当前 curated 下已有：{{ presentDatasets.join("、") }}
    </p>
  </section>
</template>
