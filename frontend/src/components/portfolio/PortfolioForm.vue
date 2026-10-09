<script setup lang="ts">
/**
 * 组合的新建 / 编辑表单。
 *
 * 字段与取值对齐原项目的新建组合对话框：
 * 名称、类型、策略、基准代码、状态、最大/目标持仓数、总层数、
 * 范围类型 + 市场范围 + 范围内容、备注。
 */
import { computed, ref, watch } from "vue";
import { Save, X } from "@lucide/vue";

import { Button } from "@/components/ui/button";
import type { Portfolio, PortfolioInput, PortfolioOptions, ScopeValue } from "@/lib/backend";

const props = defineProps<{
  options: PortfolioOptions | null;
  /** 传入表示编辑，为空表示新建 */
  editing?: Portfolio | null;
  saving?: boolean;
}>();
const emit = defineEmits<{ submit: [PortfolioInput]; cancel: [] }>();

/**
 * 表单模型：`scopeValue` 在**表单里恒为非空**。
 *
 * 绑定层的 `PortfolioInput.scopeValue` 是可空的（go 侧用指针区分
 * "没传"与"传空"），但本表单**总是提供**范围 —— 编辑时回填当前值、
 * 新建时给空数组。若直接用可空类型，模板里到处都要 `?.`，
 * 反而掩盖了"表单一定有值"这个事实。
 */
type PortfolioFormModel = Omit<PortfolioInput, "scopeValue"> & { scopeValue: ScopeValue };

function emptyForm(): PortfolioFormModel {
  return {
    name: "",
    type: "manual",
    strategyId: "trend_growth",
    benchmarkCode: "000300.SH",
    status: "active",
    targetPositionCount: 5,
    maxStockCount: 5,
    totalLayers: 10,
    scopeType: "all",
    scopeValue: { codes: [], items: [], markets: [] },
    notes: "",
  };
}

const form = ref<PortfolioFormModel>(emptyForm());
/** 范围内容用文本域编辑，提交时按行拆成数组 */
const scopeText = ref("");
const error = ref("");

// 打开编辑时回填
watch(
  () => props.editing,
  (item) => {
    if (!item) {
      form.value = emptyForm();
      scopeText.value = "";
      error.value = "";
      return;
    }
    form.value = {
      name: item.name,
      type: item.type,
      strategyId: item.strategyId,
      benchmarkCode: item.benchmarkCode,
      status: item.status,
      targetPositionCount: item.targetPositionCount,
      maxStockCount: item.maxStockCount,
      totalLayers: item.totalLayers,
      scopeType: item.scopeType,
      scopeValue: { codes: [], items: [], markets: [] },
      notes: item.notes ?? "",
    } as PortfolioFormModel;
    // 回填范围内容
    try {
      const parsed = JSON.parse(item.scopeValueJson || "{}") as ScopeValue;
      form.value.scopeValue = {
        codes: parsed.codes ?? [],
        items: parsed.items ?? [],
        markets: parsed.markets ?? [],
      };
      scopeText.value = (parsed.codes ?? parsed.items ?? []).join("\n");
    } catch {
      scopeText.value = "";
    }
    error.value = "";
  },
  { immediate: true },
);

/** 范围内容是否需要输入框 */
const needsScopeText = computed(
  () => form.value.scopeType === "manual_codes"
    || form.value.scopeType === "industry"
    || form.value.scopeType === "concept",
);
const scopePlaceholder = computed(() => {
  switch (form.value.scopeType) {
    case "manual_codes":
      return "每行一个股票代码，例如\n600519\n000001";
    case "industry":
      return "每行一个行业名，例如\n银行\n证券";
    case "concept":
      return "每行一个概念名，例如\n白酒\n创新药";
    default:
      return "";
  }
});

function toggleMarket(key: string) {
  const markets = new Set(form.value.scopeValue.markets ?? []);
  if (markets.has(key)) markets.delete(key);
  else markets.add(key);
  form.value.scopeValue = { ...form.value.scopeValue, markets: [...markets] };
}

function submit() {
  error.value = "";
  if (!form.value.name.trim()) {
    error.value = "组合名称不能为空";
    return;
  }
  // 范围内容按行拆
  const lines = scopeText.value
    .split(/[\n,，;；]+/)
    .map((item) => item.trim())
    .filter(Boolean);
  const scope: ScopeValue = { markets: form.value.scopeValue.markets ?? [] };
  if (form.value.scopeType === "manual_codes") scope.codes = lines;
  if (form.value.scopeType === "industry" || form.value.scopeType === "concept") scope.items = lines;

  emit("submit", { ...form.value, scopeValue: scope });
}
</script>

<template>
  <form class="bg-card space-y-3 rounded-lg border p-3" @submit.prevent="submit">
    <div class="flex items-center gap-2">
      <span class="text-sm font-medium">{{ editing ? "编辑组合" : "新建组合" }}</span>
      <Button size="xs" variant="ghost" class="ml-auto" type="button" @click="emit('cancel')">
        <X />
      </Button>
    </div>

    <!-- 名称 -->
    <label class="block">
      <span class="text-muted-foreground text-[11px]">组合名称</span>
      <input
        v-model="form.name"
        class="bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
        placeholder="必填"
      />
    </label>

    <!-- 类型 + 策略 -->
    <div class="grid grid-cols-2 gap-2">
      <label class="block">
        <span class="text-muted-foreground text-[11px]">组合类型</span>
        <select
          v-model="form.type"
          class="bg-background mt-0.5 h-7 w-full rounded border px-1.5 text-xs outline-none"
        >
          <option v-for="item in options?.types ?? []" :key="item.key" :value="item.key">
            {{ item.label }}
          </option>
        </select>
      </label>
      <label class="block">
        <span class="text-muted-foreground text-[11px]">组合策略</span>
        <select
          v-model="form.strategyId"
          class="bg-background mt-0.5 h-7 w-full rounded border px-1.5 text-xs outline-none"
        >
          <option v-for="item in options?.strategies ?? []" :key="item.key" :value="item.key">
            {{ item.label }}
          </option>
        </select>
      </label>
    </div>

    <!-- 基准 + 状态 -->
    <div class="grid grid-cols-2 gap-2">
      <label class="block">
        <span class="text-muted-foreground text-[11px]">基准代码</span>
        <input
          v-model="form.benchmarkCode"
          class="bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
          placeholder="000300.SH"
        />
      </label>
      <label class="block">
        <span class="text-muted-foreground text-[11px]">状态</span>
        <select
          v-model="form.status"
          class="bg-background mt-0.5 h-7 w-full rounded border px-1.5 text-xs outline-none"
        >
          <option v-for="item in options?.statuses ?? []" :key="item.key" :value="item.key">
            {{ item.label }}
          </option>
        </select>
      </label>
    </div>

    <!-- 数量 -->
    <div class="grid grid-cols-3 gap-2">
      <label class="block">
        <span class="text-muted-foreground text-[11px]">目标持仓</span>
        <input
          v-model.number="form.targetPositionCount"
          type="number"
          min="1"
          class="tabular bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
        />
      </label>
      <label class="block">
        <span class="text-muted-foreground text-[11px]">最大持仓</span>
        <input
          v-model.number="form.maxStockCount"
          type="number"
          min="1"
          class="tabular bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
        />
      </label>
      <label class="block" title="仓位权重的分母：1 层 = 总资金的 1/总层数">
        <span class="text-muted-foreground text-[11px]">总层数</span>
        <input
          v-model.number="form.totalLayers"
          type="number"
          min="1"
          class="tabular bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
        />
      </label>
    </div>

    <!-- 范围 -->
    <label class="block">
      <span class="text-muted-foreground text-[11px]">选股范围</span>
      <select
        v-model="form.scopeType"
        class="bg-background mt-0.5 h-7 w-full rounded border px-1.5 text-xs outline-none"
      >
        <option v-for="item in options?.scopeTypes ?? []" :key="item.key" :value="item.key">
          {{ item.label }}
        </option>
      </select>
    </label>

    <div>
      <span class="text-muted-foreground text-[11px]">市场范围</span>
      <div class="mt-1 flex flex-wrap gap-1">
        <button
          v-for="market in options?.markets ?? []"
          :key="market.key"
          type="button"
          class="rounded border px-1.5 py-0.5 text-[11px]"
          :class="
            form.scopeValue.markets?.includes(market.key)
              ? 'bg-primary text-primary-foreground border-primary'
              : 'hover:bg-muted/50'
          "
          @click="toggleMarket(market.key)"
        >
          {{ market.label }}
        </button>
      </div>
      <p class="text-muted-foreground mt-1 text-[10px]">不选表示全部市场</p>
    </div>

    <label v-if="needsScopeText" class="block">
      <span class="text-muted-foreground text-[11px]">范围内容</span>
      <textarea
        v-model="scopeText"
        rows="4"
        class="bg-background mt-0.5 w-full rounded border px-2 py-1 text-xs outline-none"
        :placeholder="scopePlaceholder"
      />
    </label>

    <label class="block">
      <span class="text-muted-foreground text-[11px]">备注</span>
      <input
        v-model="form.notes"
        class="bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
      />
    </label>

    <p v-if="error" class="text-destructive text-[11px]">{{ error }}</p>

    <div class="flex gap-2">
      <Button size="sm" type="submit" :disabled="saving" class="flex-1">
        <Save /> {{ saving ? "保存中…" : "保存" }}
      </Button>
      <Button size="sm" type="button" variant="outline" @click="emit('cancel')">取消</Button>
    </div>
  </form>
</template>
