<script setup lang="ts">
/**
 * 区间输入：一个可启用勾选 + 最小/最大值。
 *
 * 勾选状态必须是**本地状态**，不能从「值是否为 0」推导 ——
 * 否则用户一勾上（此时值还是 0），勾选状态立刻被算回 false，
 * 输入框永远处于禁用状态，条件根本没法启用。
 *
 * 后端约定：0 表示该端不限制，所以取消勾选时要清成 0。
 */
import { computed, ref, watch } from "vue";

const props = defineProps<{
  label: string;
  /** [min, max]，0 表示该端不限制 */
  modelValue: [number, number];
  /** 单位后缀，如 % 或 亿 */
  unit?: string;
  /** 输入框宽度类 */
  widthClass?: string;
  hint?: string;
}>();

const emit = defineEmits<{ "update:modelValue": [[number, number]] }>();

/** 初值：外部传进来的区间非 0 就视为已启用（例如读回配置文件） */
const enabled = ref(props.modelValue[0] !== 0 || props.modelValue[1] !== 0);

// 外部值后来变成非 0 时（读回配置）也要把勾选补上
watch(
  () => props.modelValue,
  (value) => {
    if (value[0] !== 0 || value[1] !== 0) enabled.value = true;
  },
);

function toggle(value: boolean) {
  enabled.value = value;
  // 取消勾选必须清空，否则后端仍会按这些值过滤
  if (!value) emit("update:modelValue", [0, 0]);
}

function setPart(index: 0 | 1, raw: string) {
  const next: [number, number] = [props.modelValue[0], props.modelValue[1]];
  const parsed = Number(raw);
  next[index] = Number.isFinite(parsed) ? parsed : 0;
  emit("update:modelValue", next);
}

const disabled = computed(() => !enabled.value);
</script>

<template>
  <div class="flex items-center gap-2 py-1" :title="hint">
    <input
      type="checkbox"
      class="accent-primary size-3.5 shrink-0"
      :checked="enabled"
      @change="toggle(($event.target as HTMLInputElement).checked)"
    />
    <span class="w-20 shrink-0 text-xs" :class="disabled ? 'text-muted-foreground' : ''">
      {{ label }}
    </span>
    <input
      class="tabular bg-background h-6 rounded border px-1.5 text-xs outline-none disabled:opacity-40"
      :class="widthClass ?? 'w-16'"
      :value="modelValue[0] || ''"
      :disabled="disabled"
      placeholder="最小"
      inputmode="decimal"
      @change="setPart(0, ($event.target as HTMLInputElement).value)"
    />
    <span class="text-muted-foreground text-xs">~</span>
    <input
      class="tabular bg-background h-6 rounded border px-1.5 text-xs outline-none disabled:opacity-40"
      :class="widthClass ?? 'w-16'"
      :value="modelValue[1] || ''"
      :disabled="disabled"
      placeholder="最大"
      inputmode="decimal"
      @change="setPart(1, ($event.target as HTMLInputElement).value)"
    />
    <span v-if="unit" class="text-muted-foreground text-[11px]">{{ unit }}</span>
  </div>
</template>
