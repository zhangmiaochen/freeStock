<script setup lang="ts">
/**
 * 布林线 / MACD 参数设置。
 *
 * 参数经 normalize* 校验后再往上抛，保证不会出现 slow <= fast 这类非法组合。
 */
import { RotateCcw } from "@lucide/vue";

import { Button } from "@/components/ui/button";
import {
  DEFAULT_BOLL,
  DEFAULT_MACD,
  PARAM_RANGE,
  normalizeBoll,
  normalizeMacd,
  type BollConfig,
  type MacdConfig,
} from "@/lib/indicators";

const props = defineProps<{
  boll: BollConfig;
  macd: MacdConfig;
  bollEnabled: boolean;
  macdEnabled: boolean;
}>();

const emit = defineEmits<{
  "update:boll": [BollConfig];
  "update:macd": [MacdConfig];
}>();

function setBoll(field: keyof BollConfig, raw: string) {
  const value = Number(raw);
  emit("update:boll", normalizeBoll({ ...props.boll, [field]: value }));
}

function setMacd(field: keyof MacdConfig, raw: string) {
  const value = Number(raw);
  emit("update:macd", normalizeMacd({ ...props.macd, [field]: value }));
}

function resetBoll() {
  emit("update:boll", { ...DEFAULT_BOLL });
}

function resetMacd() {
  emit("update:macd", { ...DEFAULT_MACD });
}
</script>

<template>
  <div class="grid gap-2 md:grid-cols-2">
    <!-- 布林线 -->
    <div class="rounded-md border px-3 py-2">
      <div class="mb-1.5 flex items-center gap-2">
        <span
          class="size-2 rounded-full"
          :class="bollEnabled ? 'bg-sky-400' : 'bg-muted-foreground/40'"
        />
        <span class="text-xs font-medium">布林线 BOLL</span>
        <span v-if="!bollEnabled" class="text-muted-foreground text-[11px]">（当前未显示）</span>
        <Button size="xs" variant="ghost" class="ml-auto" @click="resetBoll">
          <RotateCcw /> 默认
        </Button>
      </div>
      <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
        <label class="flex items-center gap-1.5">
          <span class="text-muted-foreground text-[11px]">周期 N</span>
          <input
            class="tabular bg-background w-14 rounded border px-1.5 py-0.5 text-xs outline-none"
            data-param="boll-period"
            :value="boll.period"
            inputmode="numeric"
            :min="PARAM_RANGE.bollPeriod.min"
            :max="PARAM_RANGE.bollPeriod.max"
            @change="setBoll('period', ($event.target as HTMLInputElement).value)"
          />
        </label>
        <label class="flex items-center gap-1.5">
          <span class="text-muted-foreground text-[11px]">倍数 K</span>
          <input
            class="tabular bg-background w-14 rounded border px-1.5 py-0.5 text-xs outline-none"
            data-param="boll-mult"
            :value="boll.mult"
            inputmode="decimal"
            :min="PARAM_RANGE.bollMult.min"
            :max="PARAM_RANGE.bollMult.max"
            @change="setBoll('mult', ($event.target as HTMLInputElement).value)"
          />
        </label>
        <span class="text-muted-foreground text-[11px]">
          中轨 {{ boll.period }} 日均线，上下轨 ±{{ boll.mult }} 倍标准差
        </span>
      </div>
    </div>

    <!-- MACD -->
    <div class="rounded-md border px-3 py-2">
      <div class="mb-1.5 flex items-center gap-2">
        <span
          class="size-2 rounded-full"
          :class="macdEnabled ? 'bg-amber-400' : 'bg-muted-foreground/40'"
        />
        <span class="text-xs font-medium">MACD</span>
        <span v-if="!macdEnabled" class="text-muted-foreground text-[11px]">（当前未显示）</span>
        <Button size="xs" variant="ghost" class="ml-auto" @click="resetMacd">
          <RotateCcw /> 默认
        </Button>
      </div>
      <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
        <label class="flex items-center gap-1.5">
          <span class="text-muted-foreground text-[11px]">快线</span>
          <input
            class="tabular bg-background w-14 rounded border px-1.5 py-0.5 text-xs outline-none"
            data-param="macd-fast"
            :value="macd.fast"
            inputmode="numeric"
            :min="PARAM_RANGE.macdFast.min"
            :max="PARAM_RANGE.macdFast.max"
            @change="setMacd('fast', ($event.target as HTMLInputElement).value)"
          />
        </label>
        <label class="flex items-center gap-1.5">
          <span class="text-muted-foreground text-[11px]">慢线</span>
          <input
            class="tabular bg-background w-14 rounded border px-1.5 py-0.5 text-xs outline-none"
            data-param="macd-slow"
            :value="macd.slow"
            inputmode="numeric"
            :min="PARAM_RANGE.macdSlow.min"
            :max="PARAM_RANGE.macdSlow.max"
            @change="setMacd('slow', ($event.target as HTMLInputElement).value)"
          />
        </label>
        <label class="flex items-center gap-1.5">
          <span class="text-muted-foreground text-[11px]">信号</span>
          <input
            class="tabular bg-background w-14 rounded border px-1.5 py-0.5 text-xs outline-none"
            data-param="macd-signal"
            :value="macd.signal"
            inputmode="numeric"
            :min="PARAM_RANGE.macdSignal.min"
            :max="PARAM_RANGE.macdSignal.max"
            @change="setMacd('signal', ($event.target as HTMLInputElement).value)"
          />
        </label>
        <span class="text-muted-foreground text-[11px]">
          DIF = EMA{{ macd.fast }} − EMA{{ macd.slow }}，DEA = DIF 的 EMA{{ macd.signal }}
        </span>
      </div>
    </div>
  </div>
</template>
