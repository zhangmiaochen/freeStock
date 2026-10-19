<script setup lang="ts">
/**
 * 缠论设置（K 线页「指标设置」里的一块）。
 *
 * 只改 `chanlunPrefs` 这一份配置：级别/类型权重、背驰四维权重与阈值、量价辅助、
 * 信号阈值、显示候选、显示结构矛盾、失效容差、回放步长。
 *
 * 与 K 线指标设置分开保存：两边的读取/写入路径不同，混在一起会让"改一个开关
 * 顺手把另一份配置覆盖掉"这类问题很难查。
 */
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Save } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import { api, errorMessage, type ChanlunConfig } from "@/lib/backend";

const emit = defineEmits<{ changed: [] }>();

const config = ref<ChanlunConfig | null>(null);
const configPath = ref("");
const saved = ref(false);
const error = ref("");
let ready = false;
let saveTimer: number | undefined;

/** 失效容差在配置里是 0~0.5 的小数，界面按百分比展示 */
function tolerancePercent(value: number | undefined): number {
  return Math.round((value ?? 0) * 10000) / 100;
}

function setTolerancePercent(percent: number): void {
  if (!config.value) return;
  const value = Number.isFinite(percent) ? Math.max(0, Math.min(50, percent)) / 100 : 0;
  config.value.invalidTolerance = value;
}

async function load(): Promise<void> {
  // 某些测试环境 / 老版本绑定里可能没有 chanlun 命名空间：缺了就静默跳过，
  // 不要因为一个"可选设置"让整个 K 线页报错
  if (!api.chanlun?.getConfig) {
    ready = true;
    return;
  }
  try {
    const info = await api.chanlun.getConfig();
    config.value = info.config;
    configPath.value = info.configPath ?? "";
    saved.value = info.saved === true;
  } catch (err) {
    error.value = errorMessage(err);
  } finally {
    ready = true;
  }
}

async function flush(): Promise<void> {
  if (!ready || !config.value || !api.chanlun?.saveConfig) return;
  try {
    const info = await api.chanlun.saveConfig(config.value);
    config.value = info.config;
    configPath.value = info.configPath ?? configPath.value;
    saved.value = true;
    error.value = "";
    // 配置变了，当前图上的结构要按新阈值重算
    emit("changed");
  } catch (err) {
    error.value = errorMessage(err);
  }
}

function scheduleSave(): void {
  if (!ready) return;
  window.clearTimeout(saveTimer);
  saveTimer = window.setTimeout(() => void flush(), 500);
}

watch(config, () => scheduleSave(), { deep: true });

onMounted(() => void load());

onBeforeUnmount(() => {
  window.clearTimeout(saveTimer);
  if (ready) void flush();
});
</script>

<template>
  <div class="bg-muted/20 space-y-2 rounded border px-2.5 py-2 text-xs">
    <p class="text-muted-foreground flex items-center gap-1.5 text-[11px]">
      缠论设置
      <Badge variant="outline" class="px-1 py-0 text-[10px]">只影响结构判定，不改变行情</Badge>
      <span v-if="saved" class="ml-auto text-[10px]">已保存</span>
    </p>

    <p v-if="error" class="text-destructive text-[11px]">{{ error }}</p>
    <p v-else-if="!config" class="text-muted-foreground text-[11px]">正在读取配置…</p>

    <template v-else>
      <div class="flex flex-wrap items-center gap-x-4 gap-y-1.5">
        <label class="flex items-center gap-1.5">
          <input v-model="config.volumeAux" type="checkbox" class="accent-primary size-3.5" />
          量价辅助（量能维度）
        </label>
        <label class="flex items-center gap-1.5">
          <input v-model="config.showCandidate" type="checkbox" class="accent-primary size-3.5" />
          展示候选信号
        </label>
        <label class="flex items-center gap-1.5">
          <input v-model="config.showConflict" type="checkbox" class="accent-primary size-3.5" />
          展示结构矛盾
        </label>
      </div>

      <div class="flex flex-wrap items-center gap-x-4 gap-y-1.5">
        <label class="flex items-center gap-1.5">
          背驰阈值
          <input
            v-model.number="config.divergence.threshold"
            class="tabular bg-background h-6 w-14 rounded border px-1.5 text-xs outline-none"
            inputmode="decimal"
            title="后段/前段力度比低于它才判背驰（越小越严格）"
          />
        </label>
        <label class="flex items-center gap-1.5">
          失效容差
          <input
            :value="tolerancePercent(config.invalidTolerance)"
            class="tabular bg-background h-6 w-14 rounded border px-1.5 text-xs outline-none"
            inputmode="decimal"
            title="价格越过保护位多少才判失效（%）"
            @change="setTolerancePercent(Number(($event.target as HTMLInputElement).value))"
          />
          %
        </label>
        <label class="flex items-center gap-1.5">
          综合分阈值
          <input
            v-model.number="config.scoreThreshold"
            class="tabular bg-background h-6 w-14 rounded border px-1.5 text-xs outline-none"
            inputmode="decimal"
          />
        </label>
        <label class="flex items-center gap-1.5">
          回放步长
          <input
            v-model.number="config.replayStep"
            class="tabular bg-background h-6 w-14 rounded border px-1.5 text-xs outline-none"
            inputmode="numeric"
          />
        </label>
      </div>

      <!-- 背驰力度四维权重 -->
      <details class="text-[11px]">
        <summary class="text-muted-foreground cursor-pointer select-none">
          背驰力度权重（结构 / MACD / 斜率 / 量能）
        </summary>
        <div class="mt-1 flex flex-wrap gap-x-4 gap-y-1">
          <label class="flex items-center gap-1">
            结构
            <input
              v-model.number="config.divergence.structureWeight"
              class="tabular bg-background h-6 w-14 rounded border px-1.5 text-xs outline-none"
              inputmode="decimal"
            />
          </label>
          <label class="flex items-center gap-1">
            MACD
            <input
              v-model.number="config.divergence.macdWeight"
              class="tabular bg-background h-6 w-14 rounded border px-1.5 text-xs outline-none"
              inputmode="decimal"
            />
          </label>
          <label class="flex items-center gap-1">
            斜率
            <input
              v-model.number="config.divergence.slopeWeight"
              class="tabular bg-background h-6 w-14 rounded border px-1.5 text-xs outline-none"
              inputmode="decimal"
            />
          </label>
          <label class="flex items-center gap-1">
            量能
            <input
              v-model.number="config.divergence.volumeWeight"
              class="tabular bg-background h-6 w-14 rounded border px-1.5 text-xs outline-none"
              inputmode="decimal"
            />
          </label>
        </div>
      </details>

      <!-- 级别 / 类型权重 -->
      <details class="text-[11px]">
        <summary class="text-muted-foreground cursor-pointer select-none">
          级别权重与买卖点类型权重
        </summary>
        <div class="mt-1 space-y-1">
          <div class="flex flex-wrap gap-x-3 gap-y-1">
            <label v-for="item in config.levelWeights" :key="item.level" class="flex items-center gap-1">
              {{ item.level }}
              <input
                v-model.number="item.weight"
                class="tabular bg-background h-6 w-12 rounded border px-1.5 text-xs outline-none"
                inputmode="decimal"
              />
            </label>
          </div>
          <div class="flex flex-wrap gap-x-3 gap-y-1">
            <label v-for="item in config.typeWeights" :key="item.type" class="flex items-center gap-1">
              {{ item.type }}
              <input
                v-model.number="item.weight"
                class="tabular bg-background h-6 w-12 rounded border px-1.5 text-xs outline-none"
                inputmode="decimal"
              />
            </label>
          </div>
        </div>
      </details>

      <p class="text-muted-foreground flex items-center gap-1.5 text-[10px]">
        <Save class="size-3 shrink-0" />
        <span>
          自动保存到 <code class="font-mono">{{ configPath || "config.json" }}</code>
          的 <code class="font-mono">chanlunPrefs</code>
        </span>
      </p>
    </template>
  </div>
</template>
