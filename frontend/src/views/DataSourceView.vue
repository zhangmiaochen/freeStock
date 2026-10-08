<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import {
  Check,
  Database,
  FileCog,
  HardDrive,
  LoaderCircle,
  RefreshCw,
  Server,
  TriangleAlert,
} from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import DataLakeCard from "@/components/settings/DataLakeCard.vue";
import { api, errorMessage, type DataSourceInfo, type SourceStatus } from "@/lib/backend";
import { useStockStore } from "@/stores/stock";

const store = useStockStore();

const sources = ref<DataSourceInfo[]>([]);
const current = ref<DataSourceInfo | null>(null);
const customURL = ref("");
const status = ref<SourceStatus | null>(null);
const busy = ref<"test" | "switch" | "cache" | "">("");
const message = ref("");
const configPath = ref("");

const isOK = computed(() => status.value?.ok === true);
const hasStatus = computed(() => status.value !== null);

async function loadSources() {
  try {
    sources.value = await api.getDataSources();
    current.value = await api.getCurrentDataSource();
    customURL.value = current.value?.url ?? "";
    configPath.value = (await api.getConfigPath()) ?? "";
  } catch (err) {
    message.value = errorMessage(err);
  }
}

onMounted(loadSources);

async function test(kind: string, url: string) {
  busy.value = "test";
  message.value = "";
  try {
    status.value = await api.testDataSource(kind, url);
    if (status.value.ok) message.value = status.value.message;
  } catch (err) {
    status.value = { kind, url, ok: false, message: errorMessage(err), total: 0, elapsed: "" };
  } finally {
    busy.value = "";
  }
}

async function useSource(kind: string, url: string) {
  busy.value = "switch";
  message.value = "";
  try {
    status.value = await api.setDataSource(kind, url);
    message.value = `已切换到 ${status.value.url}（${status.value.message}）`;
    await loadSources();
    // 数据源变了，清空股票列表让用户重新加载
    store.universe = null;
    await store.loadUniverse(true);
    await store.loadCategories();
  } catch (err) {
    // 切换失败时后端会给出具体原因（例如数据湖缺少日线），原样展示
    message.value = errorMessage(err);
    await loadSources();
  } finally {
    busy.value = "";
  }
}

async function clearCache() {
  busy.value = "cache";
  try {
    await api.clearCache();
    message.value = "后端缓存已清空，下次读取会重新拉取数据。";
  } catch (err) {
    message.value = errorMessage(err);
  } finally {
    busy.value = "";
  }
}

function kindLabel(kind: string) {
  if (kind === "local") return "本地";
  if (kind === "remote") return "远程";
  if (kind === "cnlake") return "本地数据湖";
  if (kind === "stockdb") return "free-stockdb";
  return "自定义";
}
</script>

<template>
  <div class="h-full overflow-auto">
    <div class="mx-auto max-w-3xl space-y-6 px-6 py-6">
      <!-- 当前数据源 -->
      <section class="bg-card space-y-3 rounded-lg border p-5">
        <div class="flex items-center gap-2">
          <Database class="size-4" />
          <h2 class="text-sm font-semibold">当前数据源</h2>
        </div>
        <div v-if="current" class="space-y-2 text-sm">
          <div class="flex items-center gap-2">
            <Badge variant="secondary">{{ kindLabel(current.kind) }}</Badge>
            <span class="font-medium">{{ current.name }}</span>
          </div>
          <p class="text-muted-foreground font-mono text-xs">{{ current.url }}</p>
        </div>
        <p v-else class="text-muted-foreground text-sm">还没有选择数据源。</p>
        <div class="flex gap-2 pt-1">
          <Button
            size="sm"
            variant="outline"
            :disabled="busy !== ''"
            @click="current && test(current.kind, current.url)"
          >
            <LoaderCircle v-if="busy === 'test'" class="animate-spin" />
            <RefreshCw v-else />
            测试连通性
          </Button>
          <Button size="sm" variant="ghost" :disabled="busy !== ''" @click="clearCache">
            清空缓存
          </Button>
        </div>
      </section>

      <!-- 预设 -->
      <section class="space-y-3">
        <h2 class="text-sm font-semibold">内置数据源</h2>
        <div class="grid gap-3">
          <div
            v-for="source in sources"
            :key="source.key"
            class="bg-card flex items-center gap-4 rounded-lg border p-4"
          >
            <HardDrive v-if="source.kind === 'cnlake'" class="text-muted-foreground size-4 shrink-0" />
            <Server v-else class="text-muted-foreground size-4 shrink-0" />
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <span class="text-sm font-medium">{{ source.name }}</span>
                <Badge v-if="source.current" variant="default" class="gap-1">
                  <Check class="size-3" /> 使用中
                </Badge>
                <Badge v-if="source.kind === 'cnlake'" variant="secondary" class="text-[11px]">
                  全部数据
                </Badge>
              </div>
              <p class="text-muted-foreground truncate font-mono text-xs">
                {{ source.url || "未找到数据湖" }}
              </p>
              <p v-if="source.kind === 'cnlake'" class="text-muted-foreground mt-0.5 text-[11px]">
                行情、估值、资金流、分红、行业板块与财报全部来自本地 parquet 数据湖
              </p>
            </div>
            <Button
              size="sm"
              variant="outline"
              :disabled="source.current || busy !== ''"
              @click="useSource(source.kind, source.url)"
            >
              切换
            </Button>
          </div>
        </div>
        <p class="text-muted-foreground text-xs">
          远程节点是 free-stockdb 文档中提供的公共测试服务，稳定性与数据完整性请以本地数据为准。
        </p>
      </section>

      <Separator />

      <!-- 自定义 -->
      <section class="space-y-3">
        <h2 class="text-sm font-semibold">自定义地址</h2>
        <div class="flex gap-2">
          <Input
            v-model="customURL"
            placeholder="http://192.168.1.10:7899"
            class="font-mono text-xs"
          />
          <Button
            size="sm"
            variant="outline"
            :disabled="busy !== '' || !customURL"
            @click="test('stockdb', customURL)"
          >
            测试
          </Button>
          <Button
            size="sm"
            :disabled="busy !== '' || !customURL"
            @click="useSource('stockdb', customURL)"
          >
            使用
          </Button>
        </div>
        <p class="text-muted-foreground text-xs">
          free-stockdb 支持局域网部署，填局域网地址即可多人共用同一份数据。
        </p>
      </section>

      <!-- 结果 -->
      <section v-if="hasStatus" class="rounded-lg border p-4 text-sm">
        <div class="flex items-center gap-2">
          <Check v-if="isOK" class="text-up size-4" />
          <TriangleAlert v-else class="text-destructive size-4" />
          <span class="font-medium">{{ isOK ? "连接成功" : "连接失败" }}</span>
          <span v-if="status?.elapsed" class="text-muted-foreground text-xs">
            耗时 {{ status.elapsed }}
          </span>
        </div>
        <p class="text-muted-foreground mt-1 font-mono text-xs">{{ status?.url }}</p>
        <p class="mt-1 text-xs">{{ status?.message }}</p>
      </section>

      <p v-if="message" class="text-muted-foreground text-xs">{{ message }}</p>

      <!-- 财务数据来源：行情库没有财报，需要外部数据湖补 -->
      <DataLakeCard />

      <!-- 设置文件位置：数据源与 K 线指标参数都存在这里 -->
      <section v-if="configPath" class="bg-card rounded-lg border p-4">
        <div class="flex items-center gap-2">
          <FileCog class="text-muted-foreground size-4" />
          <h3 class="text-sm font-medium">设置文件</h3>
        </div>
        <p class="text-muted-foreground mt-2 font-mono text-xs break-all">{{ configPath }}</p>
        <p class="text-muted-foreground mt-1.5 text-[11px] leading-relaxed">
          数据源选择、数据湖路径，以及 <b>K 线图表的指标参数</b>（均线 / 布林线 / MACD / 显示开关）
          都存在这个文件里，重启后自动恢复。直接编辑它也可以，非法值会被自动纠正。
        </p>
      </section>
    </div>
  </div>
</template>
