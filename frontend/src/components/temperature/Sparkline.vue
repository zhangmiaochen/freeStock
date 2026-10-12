<script setup lang="ts">
/**
 * 迷你走势图（内联 SVG）。
 *
 * 为什么不用 ECharts：一页要画 8 个迷你图，ECharts 每个实例都有初始化与
 * resize 观察者，8 个实例的开销和闪烁都比"一条 path"明显。折线本身只是
 * 几十个点，直接算成 SVG path 更简单、更省、也更好测。
 */
import { computed } from "vue";

import { heatColor } from "@/lib/heat";

const props = withDefaults(
  defineProps<{
    /** 收盘价序列（升序） */
    values: number[];
    height?: number;
    /** 是否按涨跌着色（默认按首尾涨跌取色） */
    colored?: boolean;
  }>(),
  { height: 40, colored: true },
);

const WIDTH = 100;
const PADDING = 2;

const geometry = computed(() => {
  const values = props.values;
  if (values.length < 2) return { line: "", area: "", change: 0 };

  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const usable = props.height - PADDING * 2;

  const points = values.map((value, i) => {
    const x = (i / (values.length - 1)) * WIDTH;
    const y = PADDING + (1 - (value - min) / span) * usable;
    return [x, y] as const;
  });

  const line = points.map(([x, y], i) => `${i === 0 ? "M" : "L"}${x.toFixed(2)} ${y.toFixed(2)}`).join(" ");
  const area = `${line} L${WIDTH} ${props.height} L0 ${props.height} Z`;
  const first = values[0] || 1;
  return { line, area, change: (values[values.length - 1] / first - 1) * 100 };
});

const stroke = computed(() => (props.colored ? heatColor(geometry.value.change) : "#a1a1aa"));
</script>

<template>
  <svg
    class="block w-full"
    :viewBox="`0 0 ${WIDTH} ${height}`"
    :height="height"
    preserveAspectRatio="none"
    role="img"
  >
    <path v-if="geometry.area" :d="geometry.area" :fill="stroke" opacity="0.12" />
    <path
      v-if="geometry.line"
      :d="geometry.line"
      fill="none"
      :stroke="stroke"
      stroke-width="1.5"
      vector-effect="non-scaling-stroke"
    />
  </svg>
</template>
