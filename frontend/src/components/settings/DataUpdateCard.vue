<script setup lang="ts">
/**
 * cne 数据更新卡片（方案 docs/cne数据更新方案.md §8.1）。
 *
 * ⚠ 本卡片对应**第 1 阶段**：手动更新闭环（发起 / 看进度 / 取消 / 存设置）。
 * 自动更新要注册 Windows 计划任务，属于第 3 阶段 —— 那两个开关**在这里
 * 显示但禁用**，并写明原因，而不是放一个点了没反应的控件。
 */
import { computed, onMounted, onUnmounted, ref } from "vue";
import { CircleAlert, CircleCheck, Download, LoaderCircle, RefreshCw, StopCircle } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import {
  api,
  errorMessage,
  type CneUpdateStatus,
  type UpdatePrefs,
  type UpdatePrefsInfo,
} from "@/lib/backend";

const status = ref<CneUpdateStatus | null>(null);
const prefs = ref<UpdatePrefsInfo | null>(null);
const projectDir = ref("");
const cnePath = ref("");
const tradeDate = ref("");
const busy = ref("");
const message = ref("");
const error = ref("");

let timer: number | undefined;

async function loadStatus() {
  try {
    status.value = await api.updateStatus();
  } catch {
    // 轮询失败不打扰用户
  }
}

async function loadPrefs() {
  try {
    const info = await api.getUpdatePrefs();
    prefs.value = info;
    projectDir.value = info.prefs.projectDir ?? "";
    cnePath.value = info.prefs.cnePath ?? "";
  } catch (e) {
    error.value = errorMessage(e);
  }
}

/** 每 3 秒轮询一次进度。cne 单次 8~60 分钟，不需要更密。 */
function startPolling() {
  stopPolling();
  timer = window.setInterval(loadStatus, 3000);
}
function stopPolling() {
  if (timer !== undefined) {
    window.clearInterval(timer);
    timer = undefined;
  }
}

const running = computed(() => status.value?.running ?? false);
const elapsedText = computed(() => {
  const sec = status.value?.elapsedSec ?? 0;
  if (sec < 60) return `${sec} 秒`;
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m} 分 ${s} 秒`;
});

async function start(kind: string) {
  if (busy.value) return;
  error.value = "";
  message.value = "";
  busy.value = kind;
  try {
    const job = await api.manualUpdate(kind, tradeDate.value.trim());
    message.value = job?.message ?? "已发起更新";
    // 立刻拉一次状态，让界面马上切到"运行中"
    await loadStatus();
    startPolling();
  } catch (e) {
    // ⚠ 后端的拒绝理由要原样显示 —— 方案 §5.4 要求"被拒时提示明确，
    // 不是静默排队"，§9 还要求区分"上游熔断（明天自动重试）"这类情况。
    error.value = errorMessage(e);
  } finally {
    busy.value = "";
  }
}

async function cancel() {
  if (busy.value) return;
  busy.value = "cancel";
  error.value = "";
  try {
    await api.cancelUpdate();
    message.value = "已取消。进程树已一并结束，不会留下孤儿子进程。";
    await loadStatus();
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    busy.value = "";
  }
}

async function savePrefs() {
  if (busy.value) return;
  busy.value = "save";
  error.value = "";
  message.value = "";
  try {
    const current = prefs.value?.prefs;
    const next: UpdatePrefs = {
      ...(current ?? {
        autoDaily: false,
        dailyTime: "17:30",
        autoStale: false,
        staleTime: "21:00",
        catchUpOnStart: true,
        groups: [],
      }),
      projectDir: projectDir.value.trim(),
      cnePath: cnePath.value.trim(),
    } as UpdatePrefs;
    const info = await api.setUpdatePrefs(next);
    prefs.value = info;
    // ⚠ 设置里的问题（目录不存在、configs 缺失）**不阻断保存**，
    // 但必须显示出来 —— 否则用户保存成功、点更新时才失败。
    if (info.warnings && info.warnings.length > 0) {
      error.value = `设置已保存，但有以下问题：${info.warnings.join("；")}`;
    } else {
      message.value = "设置已保存。";
    }
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    busy.value = "";
  }
}

onMounted(async () => {
  await loadPrefs();
  await loadStatus();
  // 打开页面时如果任务在跑，直接开始轮询
  if (status.value?.running) startPolling();
});
onUnmounted(stopPolling);
</script>

<template>
  <div class="rounded-lg border">
    <div class="flex flex-wrap items-center gap-2 border-b px-4 py-3">
      <Download class="text-muted-foreground size-4" />
      <span class="text-sm font-medium">cne 数据更新</span>
      <Badge v-if="running" variant="secondary" class="text-[10px]">
        <LoaderCircle class="animate-spin" /> 运行中 {{ elapsedText }}
      </Badge>
      <Badge v-else-if="status?.outcome" variant="outline" class="text-[10px]">
        {{ status.outcome }}
      </Badge>
      <Badge v-else variant="outline" class="text-[10px]">空闲</Badge>
      <span v-if="prefs?.configPath" class="text-muted-foreground ml-auto text-[11px]">
        设置存于 {{ prefs.configPath }}
      </span>
    </div>

    <div class="space-y-4 p-4">
      <!-- 手动更新：三个入口。
           方案 §2.2/§2.3 实测：stale 不覆盖日行情；非交易日跑普通日更会跳过行情组。 -->
      <div class="space-y-2">
        <div class="text-sm font-medium">手动更新</div>
        <div class="flex flex-wrap items-center gap-2">
          <Button size="sm" :disabled="running || !!busy" @click="start('full')">
            <Download v-if="!busy" />
            {{ busy === 'full' ? '发起中…' : '更新到今天' }}
          </Button>
          <Button size="sm" variant="outline" :disabled="running || !!busy" @click="start('stale')">
            <RefreshCw v-if="!busy" />
            {{ busy === 'stale' ? '发起中…' : '补抓落后' }}
          </Button>
          <Input v-model="tradeDate" placeholder="YYYY-MM-DD" class="h-8 w-36" />
          <Button size="sm" variant="outline" :disabled="running || !!busy || !tradeDate" @click="start('date')">
            {{ busy === 'date' ? '发起中…' : '补指定交易日' }}
          </Button>
          <Button v-if="running" size="sm" variant="destructive" :disabled="!!busy" @click="cancel">
            <StopCircle />
            取消
          </Button>
        </div>
        <p class="text-muted-foreground text-[11px]">
          「补抓落后」<strong>不覆盖日行情</strong>；非交易日要补某一天的行情，请用「补指定交易日」。
          单次通常 8~60 分钟，发起后可以关掉这个页面。
        </p>
      </div>

      <Separator />

      <!-- 进度：日志尾部 + 解析出的步骤。方案 §8.1 说 cne 会定期打
           "still working: fund_flow 2m50s"，可以直接判断卡住没卡住。 -->
      <div v-if="status?.logPath" class="space-y-2">
        <div class="text-muted-foreground flex items-center gap-2 text-xs">
          <span>日志</span>
          <code class="truncate">{{ status.logPath }}</code>
        </div>
        <div v-if="status.steps?.length" class="flex flex-wrap gap-1">
          <Badge
            v-for="step in status.steps"
            :key="step.name"
            :variant="step.state === 'failed' ? 'destructive' : step.state === 'success' ? 'secondary' : 'outline'"
            class="text-[10px]"
          >
            {{ step.name }} · {{ step.state }}<template v-if="step.elapsed"> · {{ step.elapsed }}</template>
          </Badge>
        </div>
        <pre
          v-if="status.logTail?.length"
          class="bg-muted max-h-48 overflow-auto rounded p-2 text-[11px] leading-relaxed"
        >{{ status.logTail.join("\n") }}</pre>
      </div>
      <p v-else class="text-muted-foreground text-xs">
        还没有运行记录。上方任选一个入口即可开始。
      </p>

      <Separator />

      <!-- 设置。自动更新开关属于第 3 阶段（要注册计划任务），这里禁用并写明。 -->
      <div class="space-y-3">
        <div class="text-sm font-medium">设置</div>
        <div class="grid gap-3 md:grid-cols-2">
          <label class="flex flex-col gap-1 text-xs">
            <span class="text-muted-foreground">cne 工程目录（须含 configs/cnequity.toml）</span>
            <Input v-model="projectDir" class="h-8" placeholder="D:\workspace\stockTool\CNEquity" />
          </label>
          <label class="flex flex-col gap-1 text-xs">
            <span class="text-muted-foreground">cne 可执行文件（留空则按 PATH 查找）</span>
            <Input v-model="cnePath" class="h-8" placeholder="留空即可" />
          </label>
        </div>

        <div class="flex flex-wrap items-center gap-4 rounded border border-dashed p-3">
          <div class="text-muted-foreground flex items-center gap-2 text-xs">
            <CircleAlert class="size-4" />
            <span>
              <strong>自动更新（计划任务）尚未实现</strong> —— 属于方案第 3 阶段。
              在那之前，到点更新仍由 CNEquity 侧已注册的两个计划任务负责：
              <code>cne-daily-update</code>（17:30）与 <code>cne-daily-stale</code>（21:00）。
            </span>
          </div>
        </div>

        <div class="flex items-center gap-2">
          <Button size="sm" :disabled="!!busy" @click="savePrefs">
            {{ busy === 'save' ? '保存中…' : '保存设置' }}
          </Button>
        </div>
      </div>

      <p v-if="message" class="text-up flex items-start gap-1 text-xs">
        <CircleCheck class="mt-0.5 size-3.5 shrink-0" />
        {{ message }}
      </p>
      <p v-if="error" class="text-destructive flex items-start gap-1 text-xs">
        <CircleAlert class="mt-0.5 size-3.5 shrink-0" />
        {{ error }}
      </p>
    </div>
  </div>
</template>
