<script setup lang="ts">
/**
 * 均线设置：增删均线、修改周期、单独显示/隐藏、恢复默认。
 */
import { Eye, EyeOff, Plus, RotateCcw, Trash2 } from "@lucide/vue";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { DEFAULT_MA_PERIODS, MA_COLORS, type MaConfig } from "@/lib/indicators";
import { cn } from "@/lib/utils";

const props = defineProps<{ configs: MaConfig[] }>();
const emit = defineEmits<{ change: [MaConfig[]] }>();

function update(next: MaConfig[]) {
  emit("change", next);
}

function toggleVisible(index: number) {
  const next = props.configs.map((item, i) =>
    i === index ? { ...item, visible: !item.visible } : item,
  );
  update(next);
}

function removeAt(index: number) {
  update(props.configs.filter((_, i) => i !== index));
}

function setPeriod(index: number, raw: string) {
  const value = Number.parseInt(raw, 10);
  if (!Number.isFinite(value) || value < 1 || value > 1000) return;
  const next = props.configs.map((item, i) => (i === index ? { ...item, period: value } : item));
  update(next);
}

function add() {
  const used = new Set(props.configs.map((item) => item.period));
  // 从常用周期里挑一个还没用过的，否则在最大周期上 +10
  const candidate =
    [5, 10, 20, 30, 60, 90, 120, 250].find((period) => !used.has(period)) ??
    Math.max(0, ...props.configs.map((item) => item.period)) + 10;
  const color = MA_COLORS[props.configs.length % MA_COLORS.length];
  update([...props.configs, { period: candidate, color, visible: true }]);
}

function reset() {
  update(
    DEFAULT_MA_PERIODS.map((period, index) => ({
      period,
      color: MA_COLORS[index % MA_COLORS.length],
      visible: true,
    })),
  );
}

function colorOf(index: number) {
  return MA_COLORS[index % MA_COLORS.length];
}
</script>

<template>
  <div class="bg-card rounded-lg border p-3">
    <div class="mb-2 flex items-center gap-2">
      <span class="text-xs font-medium">均线设置</span>
      <span class="text-muted-foreground text-[11px]">可增删均线、自定义周期</span>
      <div class="ml-auto flex items-center gap-1.5">
        <Button size="xs" variant="outline" @click="add"><Plus /> 添加均线</Button>
        <Button size="xs" variant="ghost" @click="reset"><RotateCcw /> 恢复默认</Button>
      </div>
    </div>

    <div class="flex flex-wrap gap-2">
      <div
        v-for="(config, index) in configs"
        :key="`${config.period}-${index}`"
        :class="
          cn(
            'flex items-center gap-1.5 rounded-md border px-2 py-1',
            config.visible ? '' : 'opacity-50',
          )
        "
      >
        <span
          class="size-2.5 shrink-0 rounded-full"
          :style="{ backgroundColor: config.color || colorOf(index) }"
        />
        <span class="text-muted-foreground text-[11px]">MA</span>
        <input
          class="tabular w-14 bg-transparent text-center text-xs outline-none"
          data-param="ma-period"
          :value="config.period"
          inputmode="numeric"
          @change="setPeriod(index, ($event.target as HTMLInputElement).value)"
        />
        <button
          type="button"
          class="text-muted-foreground hover:text-foreground"
          :title="config.visible ? '隐藏这条均线' : '显示这条均线'"
          @click="toggleVisible(index)"
        >
          <Eye v-if="config.visible" class="size-3.5" />
          <EyeOff v-else class="size-3.5" />
        </button>
        <button
          type="button"
          class="text-muted-foreground hover:text-destructive"
          title="删除这条均线"
          @click="removeAt(index)"
        >
          <Trash2 class="size-3.5" />
        </button>
      </div>

      <p v-if="configs.length === 0" class="text-muted-foreground py-1 text-xs">
        还没有均线，点「添加均线」开始
      </p>
    </div>
  </div>
</template>
