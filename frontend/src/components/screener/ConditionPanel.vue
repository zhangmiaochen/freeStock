<script setup lang="ts">
/**
 * 条件选股的条件面板。
 *
 * 条件分组对应后端的执行层次（快照 → 板块 → 技术），
 * 界面上也按这个顺序排，用户能直观看到"越往下越慢"。
 */
import { computed, ref } from "vue";
import { CalendarDays, ChevronDown, Database, Info, Plus, Trash2 } from "@lucide/vue";

import BoardConditionEditor from "@/components/screener/BoardConditionEditor.vue";
import RangeField from "@/components/screener/RangeField.vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { BoardFilter, ScreenCondition, ScreenOptions } from "@/lib/backend";
import { cn } from "@/lib/utils";

const props = defineProps<{
  condition: ScreenCondition;
  options: ScreenOptions | null;
}>();
const emit = defineEmits<{ "update:condition": [ScreenCondition] }>();

/** 分组折叠状态 */
const open = ref({ date: true, snapshot: true, lake: true, board: true, tech: false, unsupported: false });

/**
 * 数据湖条件的数据集可用性。
 *
 * 字段已经对接好了，但数据集可能还没入库 —— 这两件事对用户是不同的问题，
 * 所以界面要说清楚"条件能用"还是"在等数据入库"。
 */
const lakeStatus = computed(() => {
  const map = new Map<string, { available: boolean; dataset: string }>();
  for (const item of props.options?.lakeConditions ?? []) {
    map.set(item.name, { available: item.available, dataset: item.dataset });
  }
  return map;
});

function lakeAvailable(name: string): boolean {
  return lakeStatus.value.get(name)?.available ?? false;
}

function lakeDataset(name: string): string {
  return lakeStatus.value.get(name)?.dataset ?? "";
}

function patch(part: Partial<ScreenCondition>) {
  emit("update:condition", { ...props.condition, ...part });
}

// ---- 交易日 ----
//
// 界面上用 <input type="date">（给原生日历），后端用 YYYYMMDD 整数，
// 这里做一次转换。留空表示"最新交易日"。
function toDateInput(value: number): string {
  if (!value || value <= 0) return "";
  const text = String(value);
  if (text.length !== 8) return "";
  return `${text.slice(0, 4)}-${text.slice(4, 6)}-${text.slice(6, 8)}`;
}

function fromDateInput(value: string): number {
  const digits = value.replace(/\D/g, "");
  if (digits.length !== 8) return 0;
  return Number(digits);
}
</script>

<template>
  <div class="space-y-3">
    <!-- ============ 交易日 ============ -->
    <section class="bg-card rounded-lg border">
      <button
        type="button"
        class="flex w-full items-center gap-2 px-3 py-2"
        @click="open.date = !open.date"
      >
        <ChevronDown :class="cn('size-3.5 transition-transform', !open.date && '-rotate-90')" />
        <CalendarDays class="size-3.5" />
        <span class="text-sm font-medium">交易日</span>
        <Badge v-if="condition.tradeDate" variant="default" class="text-[10px]">
          {{ condition.tradeDate }}
        </Badge>
        <Badge v-else variant="secondary" class="text-[10px]">最新</Badge>
      </button>

      <div v-if="open.date" class="space-y-2 border-t px-3 py-2">
        <div class="flex items-center gap-2">
          <input
            type="date"
            class="bg-background h-7 flex-1 rounded border px-2 text-xs outline-none"
            :value="toDateInput(condition.tradeDate)"
            @change="patch({ tradeDate: fromDateInput(($event.target as HTMLInputElement).value) })"
          />
          <Button
            size="xs"
            variant="outline"
            :disabled="!condition.tradeDate"
            title="改回使用最新交易日"
            @click="patch({ tradeDate: 0 })"
          >
            用最新
          </Button>
        </div>
        <p class="text-muted-foreground flex items-start gap-1.5 text-[11px] leading-relaxed">
          <Info class="mt-0.5 size-3 shrink-0" />
          <span>
            留空表示用<b>最新交易日</b>。指定日期时会取<b>该日或之前最近一个有数据的交易日</b> ——
            当天数据常常还没同步，按"指定日期或之前"取数才不会筛出半天的数据。
            <br />
            注意：<b>板块成分名单只有当前这一份</b>，指定历史日期时板块条件仍按今天的成分股匹配，
            只有行情类条件用历史数据。
          </span>
        </p>
      </div>
    </section>

    <!-- ============ 快照层 ============ -->
    <section class="bg-card rounded-lg border">
      <button
        type="button"
        class="flex w-full items-center gap-2 px-3 py-2"
        @click="open.snapshot = !open.snapshot"
      >
        <ChevronDown :class="cn('size-3.5 transition-transform', !open.snapshot && '-rotate-90')" />
        <span class="text-sm font-medium">基础条件</span>
        <Badge variant="secondary" class="text-[10px]">立即返回</Badge>
      </button>

      <div v-if="open.snapshot" class="space-y-3 border-t px-3 py-2">
        <!-- 规模 -->
        <div>
          <p class="text-muted-foreground mb-1 text-[11px]">规模</p>
          <RangeField
            label="总市值"
            unit="亿"
            :model-value="[condition.minTotalMV, condition.maxTotalMV]"
            @update:model-value="(v) => patch({ minTotalMV: v[0], maxTotalMV: v[1] })"
          />
          <RangeField
            label="流通市值"
            unit="亿"
            :model-value="[condition.minFloatMV, condition.maxFloatMV]"
            @update:model-value="(v) => patch({ minFloatMV: v[0], maxFloatMV: v[1] })"
          />
          <RangeField
            label="成交额"
            unit="亿"
            :model-value="[condition.minAmount, condition.maxAmount]"
            @update:model-value="(v) => patch({ minAmount: v[0], maxAmount: v[1] })"
          />
        </div>

        <!-- 估值 -->
        <div>
          <p class="text-muted-foreground mb-1 text-[11px]">
            估值　<span class="opacity-70">ROE 由 PB ÷ PE 推导，仅当 PE 为正时有意义</span>
          </p>
          <RangeField
            label="市盈率"
            :model-value="[condition.minPE, condition.maxPE]"
            @update:model-value="(v) => patch({ minPE: v[0], maxPE: v[1] })"
          />
          <RangeField
            label="市净率"
            :model-value="[condition.minPB, condition.maxPB]"
            @update:model-value="(v) => patch({ minPB: v[0], maxPB: v[1] })"
          />
          <RangeField
            label="ROE"
            unit="%"
            :model-value="[condition.minROE, condition.maxROE]"
            @update:model-value="(v) => patch({ minROE: v[0], maxROE: v[1] })"
          />
        </div>

        <!-- 交易 -->
        <div>
          <p class="text-muted-foreground mb-1 text-[11px]">交易</p>
          <RangeField
            label="换手率"
            unit="%"
            :model-value="[condition.minTurnover, condition.maxTurnover]"
            @update:model-value="(v) => patch({ minTurnover: v[0], maxTurnover: v[1] })"
          />
          <RangeField
            label="涨跌幅"
            unit="%"
            hint="负数填负值，例如 -5 ~ -2 表示跌 2% 到 5%"
            :model-value="[condition.minPctChg, condition.maxPctChg]"
            @update:model-value="(v) => patch({ minPctChg: v[0], maxPctChg: v[1] })"
          />
          <RangeField
            label="量比"
            :model-value="[condition.minVolumeRatio, condition.maxVolumeRatio]"
            @update:model-value="(v) => patch({ minVolumeRatio: v[0], maxVolumeRatio: v[1] })"
          />
        </div>

        <!-- 开关 -->
        <div class="space-y-1.5">
          <label class="flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              class="accent-primary size-3.5"
              :checked="condition.excludeST"
              @change="patch({ excludeST: ($event.target as HTMLInputElement).checked })"
            />
            排除 ST
          </label>
          <label class="flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              class="accent-primary size-3.5"
              :checked="condition.hasQuoteOnly"
              @change="patch({ hasQuoteOnly: ($event.target as HTMLInputElement).checked })"
            />
            仅保留有行情的标的
          </label>
        </div>

        <!-- 市场 -->
        <div>
          <p class="text-muted-foreground mb-1 text-[11px]">
            市场板块　<span class="opacity-70">不选表示全部</span>
          </p>
          <div class="flex flex-wrap gap-1">
            <button
              v-for="market in options?.markets ?? []"
              :key="market.key"
              type="button"
              class="rounded border px-1.5 py-0.5 text-[11px]"
              :class="
                condition.markets.includes(market.key)
                  ? 'bg-primary text-primary-foreground border-primary'
                  : 'hover:bg-muted/50'
              "
              @click="
                patch({
                  markets: condition.markets.includes(market.key)
                    ? condition.markets.filter((k) => k !== market.key)
                    : [...condition.markets, market.key],
                })
              "
            >
              {{ market.name }}
            </button>
          </div>
        </div>
      </div>
    </section>

    <!-- ============ 数据湖条件 ============ -->
    <section class="bg-card rounded-lg border">
      <button
        type="button"
        class="flex w-full items-center gap-2 px-3 py-2"
        @click="open.lake = !open.lake"
      >
        <ChevronDown :class="cn('size-3.5 transition-transform', !open.lake && '-rotate-90')" />
        <Database class="size-3.5" />
        <span class="text-sm font-medium">数据湖条件</span>
        <Badge variant="outline" class="text-[10px]">cnequity</Badge>
      </button>

      <div v-if="open.lake" class="space-y-3 border-t px-3 py-2">
        <p class="text-muted-foreground text-[11px] leading-relaxed">
          这四项 free-stockdb 没有对应字段，字段取自 cnequity 数据湖。
          数据集未入库时条件会被跳过并在结果上方说明，不会报错。
        </p>

        <!-- 市销率 -->
        <div>
          <p class="text-muted-foreground mb-1 flex items-center gap-1.5 text-[11px]">
            估值
            <Badge v-if="lakeAvailable('市销率 PS')" variant="secondary" class="px-1 py-0 text-[10px]">可用</Badge>
            <Badge v-else variant="outline" class="px-1 py-0 text-[10px]">
              等 {{ lakeDataset('市销率 PS') }} 入库
            </Badge>
          </p>
          <RangeField
            label="市销率"
            :model-value="[condition.minPS, condition.maxPS]"
            @update:model-value="(v) => patch({ minPS: v[0], maxPS: v[1] })"
          />
        </div>

        <!-- 机构参与度 -->
        <div>
          <p class="text-muted-foreground mb-1 flex items-center gap-1.5 text-[11px]">
            机构参与度
            <Badge v-if="lakeAvailable('机构参与度')" variant="secondary" class="px-1 py-0 text-[10px]">可用</Badge>
            <Badge v-else variant="outline" class="px-1 py-0 text-[10px]">
              等 {{ lakeDataset('机构参与度') }} 入库
            </Badge>
          </p>
          <RangeField
            label="机构持股"
            unit="%"
            hint="十大流通股东里机构持股比例的合计"
            :model-value="[condition.minInstitution, condition.maxInstitution]"
            @update:model-value="(v) => patch({ minInstitution: v[0], maxInstitution: v[1] })"
          />
        </div>

        <!-- 季报增长 -->
        <div>
          <p class="text-muted-foreground mb-1 flex items-center gap-1.5 text-[11px]">
            季报增长
            <Badge v-if="lakeAvailable('季报增长')" variant="secondary" class="px-1 py-0 text-[10px]">可用</Badge>
            <Badge v-else variant="outline" class="px-1 py-0 text-[10px]">
              等 {{ lakeDataset('季报增长') }} 入库
            </Badge>
          </p>
          <div class="flex items-center gap-2 py-1">
            <input
              type="checkbox"
              class="accent-primary size-3.5 shrink-0"
              :checked="condition.minQuarterGrowth !== 0"
              @change="
                patch({
                  minQuarterGrowth: ($event.target as HTMLInputElement).checked
                    ? (condition.minQuarterGrowth || 20)
                    : 0,
                })
              "
            />
            <span class="w-20 shrink-0 text-xs">单季同比 ≥</span>
            <input
              class="tabular bg-background h-6 w-16 rounded border px-1.5 text-xs outline-none disabled:opacity-40"
              :value="condition.minQuarterGrowth || ''"
              :disabled="condition.minQuarterGrowth === 0"
              placeholder="20"
              inputmode="decimal"
              @change="patch({ minQuarterGrowth: Number(($event.target as HTMLInputElement).value) || 0 })"
            />
            <span class="text-muted-foreground text-[11px]">%</span>
          </div>
        </div>

        <!-- 相对强度 -->
        <div>
          <p class="text-muted-foreground mb-1 flex items-center gap-1.5 text-[11px]">
            相对强度
            <Badge v-if="lakeAvailable('相对强度 RS')" variant="secondary" class="px-1 py-0 text-[10px]">可用</Badge>
            <Badge v-else variant="outline" class="px-1 py-0 text-[10px]">
              等 {{ lakeDataset('相对强度 RS') }} 入库
            </Badge>
          </p>
          <label class="flex items-center gap-2 py-1 text-xs">
            <input
              type="checkbox"
              class="accent-primary size-3.5"
              :checked="condition.enableRelativeStrength"
              @change="
                patch({
                  enableRelativeStrength: ($event.target as HTMLInputElement).checked,
                  relativeStrengthDays: condition.relativeStrengthDays || 20,
                })
              "
            />
            启用（需要拉日线，较慢）
          </label>
          <template v-if="condition.enableRelativeStrength">
            <div class="flex items-center gap-2 py-1">
              <span class="w-20 shrink-0 text-xs">窗口</span>
              <input
                class="tabular bg-background h-6 w-16 rounded border px-1.5 text-xs outline-none"
                :value="condition.relativeStrengthDays || 20"
                inputmode="numeric"
                @change="patch({ relativeStrengthDays: Number(($event.target as HTMLInputElement).value) || 0 })"
              />
              <span class="text-muted-foreground text-[11px]">交易日</span>
            </div>
            <div class="flex items-center gap-2 py-1">
              <span class="w-20 shrink-0 text-xs">RS ≥</span>
              <input
                class="tabular bg-background h-6 w-16 rounded border px-1.5 text-xs outline-none"
                :value="condition.minRelativeStrength || ''"
                placeholder="0"
                inputmode="decimal"
                @change="patch({ minRelativeStrength: Number(($event.target as HTMLInputElement).value) || 0 })"
              />
              <span class="text-muted-foreground text-[11px]">%</span>
            </div>
            <p class="text-muted-foreground text-[11px]">
              对标：创业板指 / 科创 50 / 深证成指 / 上证指数 / 沪深 300（按代码自动选）
            </p>
          </template>
        </div>
      </div>
    </section>

    <!-- ============ 板块层 ============ -->
    <section class="bg-card rounded-lg border">
      <button
        type="button"
        class="flex w-full items-center gap-2 px-3 py-2"
        @click="open.board = !open.board"
      >
        <ChevronDown :class="cn('size-3.5 transition-transform', !open.board && '-rotate-90')" />
        <span class="text-sm font-medium">行业 / 概念板块</span>
        <Badge variant="secondary" class="text-[10px]">立即返回</Badge>
      </button>

      <div v-if="open.board" class="space-y-2 border-t px-3 py-2">
        <p class="text-muted-foreground text-[11px] leading-relaxed">
          多条条件之间是 <b>且</b>（必须同时满足）；条内「任一 / 全部」决定选中的多个板块是并集还是交集。
        </p>

        <BoardConditionEditor
          v-for="(filter, index) in condition.boardFilters"
          :key="index"
          :model-value="filter"
          :categories="options?.categories ?? []"
          @update:model-value="
            (v: BoardFilter) =>
              patch({ boardFilters: condition.boardFilters.map((item, i) => (i === index ? v : item)) })
          "
          @remove="
            patch({ boardFilters: condition.boardFilters.filter((_, i) => i !== index) })
          "
        />

        <Button
          size="xs"
          variant="outline"
          @click="
            patch({
              boardFilters: [
                ...condition.boardFilters,
                {
                  category: options?.categories?.[0]?.key ?? '申万一级',
                  names: [],
                  mode: 'any',
                },
              ],
            })
          "
        >
          <Plus /> 添加板块条件
        </Button>
      </div>
    </section>

    <!-- ============ 技术层 ============ -->
    <section class="bg-card rounded-lg border">
      <button
        type="button"
        class="flex w-full items-center gap-2 px-3 py-2"
        @click="open.tech = !open.tech"
      >
        <ChevronDown :class="cn('size-3.5 transition-transform', !open.tech && '-rotate-90')" />
        <span class="text-sm font-medium">技术形态</span>
        <Badge variant="outline" class="text-[10px]">需要拉日线，较慢</Badge>
      </button>

      <div v-if="open.tech" class="space-y-3 border-t px-3 py-2">
        <!-- 均线 -->
        <div>
          <p class="text-muted-foreground mb-1 text-[11px]">均线条件（多条之间为「且」）</p>
          <div
            v-for="(ma, index) in condition.maConditions"
            :key="index"
            class="mb-1 flex items-center gap-1.5"
          >
            <input
              class="tabular bg-background h-6 w-14 rounded border px-1.5 text-xs outline-none"
              :value="ma.fast"
              inputmode="numeric"
              @change="
                patch({
                  maConditions: condition.maConditions.map((item, i) =>
                    i === index ? { ...item, fast: Number(($event.target as HTMLInputElement).value) || 0 } : item,
                  ),
                })
              "
            />
            <select
              class="bg-background h-6 rounded border px-1 text-xs outline-none"
              :value="ma.op"
              @change="
                patch({
                  maConditions: condition.maConditions.map((item, i) =>
                    i === index ? { ...item, op: ($event.target as HTMLSelectElement).value } : item,
                  ),
                })
              "
            >
              <option value="gt">大于</option>
              <option value="lt">小于</option>
            </select>
            <input
              class="tabular bg-background h-6 w-14 rounded border px-1.5 text-xs outline-none"
              :value="ma.slow"
              inputmode="numeric"
              @change="
                patch({
                  maConditions: condition.maConditions.map((item, i) =>
                    i === index ? { ...item, slow: Number(($event.target as HTMLInputElement).value) || 0 } : item,
                  ),
                })
              "
            />
            <button
              type="button"
              class="text-muted-foreground hover:text-destructive"
              @click="patch({ maConditions: condition.maConditions.filter((_, i) => i !== index) })"
            >
              <Trash2 class="size-3.5" />
            </button>
          </div>
          <div class="flex flex-wrap gap-1.5">
            <Button
              size="xs"
              variant="outline"
              @click="patch({ maConditions: [...condition.maConditions, { fast: 5, op: 'gt', slow: 20 }] })"
            >
              <Plus /> 添加均线条件
            </Button>
            <Button
              size="xs"
              variant="ghost"
              title="常用组合：5 日 > 20 日 > 60 日，即均线多头排列"
              @click="
                patch({
                  maConditions: [
                    { fast: 5, op: 'gt', slow: 20 },
                    { fast: 20, op: 'gt', slow: 60 },
                  ],
                })
              "
            >
              多头排列预设
            </Button>
          </div>
        </div>

        <!-- 回撤 -->
        <div>
          <p class="text-muted-foreground mb-1 text-[11px]">
            阶段回撤　<span class="opacity-70">窗口内最高点回落到现价的幅度</span>
          </p>
          <label class="mb-1 flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              class="accent-primary size-3.5"
              :checked="condition.enableDrawdown"
              @change="patch({ enableDrawdown: ($event.target as HTMLInputElement).checked })"
            />
            启用
          </label>
          <div class="flex items-center gap-2">
            <span class="w-20 text-xs">窗口</span>
            <input
              class="tabular bg-background h-6 w-16 rounded border px-1.5 text-xs outline-none"
              :value="condition.drawdownDays || ''"
              placeholder="30"
              inputmode="numeric"
              @change="patch({ drawdownDays: Number(($event.target as HTMLInputElement).value) || 0 })"
            />
            <span class="text-muted-foreground text-[11px]">交易日</span>
          </div>
          <RangeField
            label="回撤幅度"
            unit="%"
            :model-value="[condition.minDrawdown, condition.maxDrawdown]"
            @update:model-value="(v) => patch({ minDrawdown: v[0], maxDrawdown: v[1] })"
          />
        </div>
      </div>
    </section>

    <!-- ============ 不支持的条件 ============ -->
    <section v-if="options?.unsupported?.length" class="bg-card rounded-lg border">
      <button
        type="button"
        class="flex w-full items-center gap-2 px-3 py-2"
        @click="open.unsupported = !open.unsupported"
      >
        <ChevronDown
          :class="cn('size-3.5 transition-transform', !open.unsupported && '-rotate-90')"
        />
        <span class="text-sm font-medium">暂不支持的条件</span>
        <Badge variant="outline" class="text-[10px]">{{ options.unsupported.length }} 项</Badge>
      </button>
      <ul v-if="open.unsupported" class="text-muted-foreground space-y-1 border-t px-3 py-2 text-[11px]">
        <li v-for="item in options.unsupported" :key="item" class="flex items-start gap-1.5">
          <Info class="mt-0.5 size-3 shrink-0" />
          <span>{{ item }}</span>
        </li>
      </ul>
    </section>
  </div>
</template>
