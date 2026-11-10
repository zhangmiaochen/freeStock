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
  type CatchUpState,
  type FreshnessReport,
  type ScheduleState,
  type RunDetail,
  type SchedulePlan,
  type AutoUpdateStatus,
  type UpdatePrefs,
  type UpdatePrefsInfo,
} from "@/lib/backend";

const status = ref<CneUpdateStatus | null>(null);
const prefs = ref<UpdatePrefsInfo | null>(null);
const freshness = ref<FreshnessReport | null>(null);
const catchUp = ref<CatchUpState | null>(null);
const schedule = ref<ScheduleState | null>(null);
/**
 * 最近一次运行的**实际结果**（来自 cne 的 manifest.db）。
 *
 * ⚠ 它才是"这次更新到底成没成"的判据，**不是退出码**：
 * 方案 §2.1 实测 exit=1 时可能 5 个步骤成功、写了 2.5 万行，
 * 而 degraded 的运行反而 exit=0。
 */
const runResult = ref<RunDetail | null>(null);

/** 读回最近一次运行的结果。 */
async function loadRunResult() {
  try {
    runResult.value = await api.latestRunResult();
  } catch {
    runResult.value = null;
  }
}
const projectDir = ref("");
const cnePath = ref("");
const tradeDate = ref("");
/** 历史区间回补的起止日期（YYYY-MM-DD）与数据集 */
const backfillStart = ref("");
const backfillEnd = ref("");
/** ⚠ 个股日线（daily_bars）与指数日线（index_bars）是两个数据集，做长区间回测时两个都要补 */
const backfillDataset = ref("daily_bars");
/** 计划任务时间设置 */
const dailyAt = ref("17:30");
const staleAt = ref("21:00");
/** 改动预览：**必须等用户看过它并确认，才会执行** */
const plan = ref<SchedulePlan | null>(null);
/** 自动更新是否真的会生效（含"错过会不会补跑"） */
const autoUpdate = ref<AutoUpdateStatus | null>(null);
/** 读自动更新状态失败的原因（非空时界面要显示出来，而不是什么都不显示） */
const autoUpdateError = ref("");
/** 诊断报告（排查"系统找不到指定的路径"用） */
const diagnoseReport = ref("");
const busy = ref("");
const message = ref("");
const error = ref("");

let timer: number | undefined;

async function loadStatus() {
  try {
    status.value = await api.updateStatus();
    catchUp.value = await api.catchUpState();
  } catch {
    // 轮询失败不打扰用户
  }
}

/**
 * 新鲜度检查。
 *
 * ⚠ 它不是"锦上添花" —— 方案 §2.2 实测 `--stale-only` 的落后清单里
 * **没有 daily_bars**，只看它会以为数据没落后，而行情可能缺了一天。
 */
/** 读回两个计划任务的状态（只读）—— 让用户看到"确实排上了"。 */
async function loadSchedule() {
  try {
    schedule.value = await api.scheduleState();
  } catch {
    schedule.value = null;
  }
}

/**
 * 读回自动更新状态。
 *
 * ⚠ **失败时不能静默**。原来这里 `catch {}` 直接置 null，于是
 * 「修复：错过也补跑」按钮（条件是 `!startWhenAvailable`）跟着消失 ——
 * 用户会以为功能被删了，而实际是查询失败。用户实测反馈过这一点：
 * "你怎么把之前的修复跳过按钮去掉了"。
 *
 * 现在失败会把原因显示出来，并且**保留一个"重试"入口**。
 */
async function loadAutoUpdate() {
  try {
    autoUpdate.value = await api.autoUpdateStatus();
    autoUpdateError.value = "";
  } catch (e) {
    autoUpdate.value = null;
    autoUpdateError.value = errorMessage(e);
  }
}

/** 开关自动更新（启用/禁用两个计划任务）。 */
async function toggleAutoUpdate(enabled: boolean) {
  if (busy.value) return;
  busy.value = "auto";
  error.value = "";
  message.value = "";
  try {
    const st = await api.setAutoUpdate(enabled);
    autoUpdate.value = st;
    // ⚠ 同步到页面上的 prefs 缓存。
    //
    // 这个开关和「保存设置」写的是**同一份配置**里的 autoDaily/autoStale：
    // 开关走 setAutoUpdate，保存走 setUpdatePrefs（整份覆盖）。
    // 如果这里不跟新，之后点「保存设置」就会展开打开页面时那份缓存
    // （autoDaily=false），把刚开启的开关又写回关闭 ——
    // 这正是"开了自动更新，一保存设置就又变回关闭"的原因。
    if (prefs.value?.prefs) {
      prefs.value.prefs.autoDaily = enabled;
      prefs.value.prefs.autoStale = enabled;
    }
    message.value = enabled ? "自动更新已开启。" : "自动更新已关闭 —— 应用不运行时不会再自动更新。";
    await loadSchedule();
  } catch (e) {
    error.value = errorMessage(e);
    await loadAutoUpdate();
  } finally {
    busy.value = "";
  }
}

/**
 * 打开"错过计划时间后尽快启动"。
 *
 * ⚠ 这是"自动更新看起来配好了、实际不生效"的关键修复：
 * 没打开时，17:30/21:00 机器关着就会**直接跳过那天**。
 */
async function fixScheduleSettings() {
  if (busy.value) return;
  busy.value = "fix";
  error.value = "";
  message.value = "";
  try {
    const st = await api.fixScheduleSettings();
    autoUpdate.value = st;
    message.value = "已打开「错过计划开始时间后尽快启动」—— 之后错过的时间点会在机器可用时补跑。";
    await loadSchedule();
  } catch (e) {
    error.value = errorMessage(e);
    await loadAutoUpdate();
  } finally {
    busy.value = "";
  }
}

/** 跑一次进程内诊断，把结果摊开显示（可直接复制粘贴）。 */
async function runDiagnose() {
  busy.value = "diagnose";
  try {
    diagnoseReport.value = await api.diagnoseSchedule();
  } catch (e) {
    diagnoseReport.value = "诊断失败：" + errorMessage(e);
  } finally {
    busy.value = "";
  }
}

async function loadFreshness() {
  try {
    freshness.value = await api.checkFreshness();
  } catch (e) {
    // 数据湖没配好时这里会失败 —— 属于正常情况，不当作错误弹出来
    freshness.value = null;
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
/** 被跳过的调度组（含原因）—— 用户最需要知道的信息 */
const skippedSteps = computed(() => (status.value?.steps ?? []).filter((s) => s.state === "skipped"));
/** 正在跑的步骤 */
const runningSteps = computed(() => (status.value?.steps ?? []).filter((s) => s.state === "running"));
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

/**
 * 发起**历史区间回补**（`cne backfill daily_bars --start X --end Y`）。
 *
 * 与 `start()` 分开是因为它走的是**另一个 cne 子命令**：上面三种都走
 * `cne run daily`（日更 pipeline，只认单日 `--trade-date`）。
 */
async function startBackfill() {
  if (busy.value) return;
  const start = backfillStart.value.trim();
  const end = backfillEnd.value.trim();
  error.value = "";
  message.value = "";
  if (!start || !end) {
    error.value = "请先填写起止日期（YYYY-MM-DD）";
    return;
  }
  // 定长 YYYY-MM-DD 的字典序就是日期序，所以这里能直接比字符串。
  if (start > end) {
    error.value = `区间起点 ${start} 晚于终点 ${end}`;
    return;
  }
  busy.value = "backfill";
  try {
    const job = await api.manualBackfill(backfillDataset.value, start, end);
    message.value = job?.message ?? "已发起历史回补";
    await loadStatus();
    startPolling();
  } catch (e) {
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

/**
 * 发起逐日补跑。
 *
 * ⚠ 服务端会**自己再跑一次新鲜度检查**来决定补哪些天，不听前端传来的日期 ——
 * 所以这里不传参数，避免界面过期导致补错。
 *
 * 补跑是**串行**的（cne 有写入锁），缺口多时可能跑很久，
 * 界面上把"落后几天"先摆出来让用户自己判断。
 */
async function startCatchUp() {
  if (busy.value || running.value) return;
  busy.value = "catchup";
  error.value = "";
  message.value = "";
  try {
    const info = await api.startCatchUp();
    message.value = info?.message ?? "已发起补跑";
    if (info && !info.started) {
      // 没有缺口时不是错误，只是没必要跑
      await loadFreshness();
    }
    await loadStatus();
    startPolling();
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    busy.value = "";
  }
}

/** 请求停止补跑：当前这一天跑完后不再继续（不中途掐断，避免补一半的状态）。 */
async function cancelCatchUp() {
  if (busy.value) return;
  busy.value = "cancelCatchUp";
  error.value = "";
  try {
    await api.cancelCatchUp();
    message.value = "已请求停止：当前这一天跑完后不再继续。";
    await loadStatus();
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    busy.value = "";
  }
}

/**
 * 预览计划任务改动。**只算不改。**
 *
 * ⚠ 结果里的 commands 是**将要执行的原始 schtasks 命令**，
 * 下面的模板会把它原样渲染出来 —— 让用户明确知道自己机器上会发生什么。
 */
async function previewScheduleChange() {
  if (busy.value) return;
  busy.value = "preview";
  error.value = "";
  message.value = "";
  try {
    plan.value = await api.previewSchedule(dailyAt.value.trim(), staleAt.value.trim());
    if (plan.value && !plan.value.hasChanges) {
      message.value = plan.value.summary;
    }
  } catch (e) {
    plan.value = null;
    error.value = errorMessage(e);
  } finally {
    busy.value = "";
  }
}

/** 放弃这次改动（把预览清掉，不执行任何东西）。 */
function discardPlan() {
  plan.value = null;
  message.value = "已放弃改动，没有执行任何操作。";
}

/**
 * 执行确认过的改动。
 *
 * ⚠ 只有用户**看过上面的原始命令并点这个按钮**才会走到这里。
 * confirmed 传 true 是明确的表态，不是默认值。
 */
async function applyScheduleChange() {
  if (busy.value || !plan.value) return;
  busy.value = "apply";
  error.value = "";
  try {
    const result = await api.applySchedule(plan.value, true);
    message.value = result?.summary ?? "已执行。";
    plan.value = null;
    await loadSchedule();
  } catch (e) {
    error.value = errorMessage(e);
    await loadSchedule();
  } finally {
    busy.value = "";
  }
}

/** 从 "2026/10/10 17:30:00" 里取出 "17:30"，用于把输入框预填成现状。 */
function clockFromSchedule(nextRun: string): string {
  const m = nextRun.match(/(\d{1,2}):(\d{2})/);
  if (!m) return "";
  return `${m[1].padStart(2, "0")}:${m[2]}`;
}

/** 把 20261009 显示成 2026-10-09。 */
function fmtDate(n?: number): string {
  if (!n) return "—";
  const s = String(n);
  if (s.length !== 8) return s;
  return `${s.slice(0, 4)}-${s.slice(4, 6)}-${s.slice(6)}`;
}

async function savePrefs() {
  if (busy.value) return;
  busy.value = "save";
  error.value = "";
  message.value = "";
  try {
    // ⚠ 合并底稿必须用**后端的最新设置**，不能用组件里的缓存。
    //
    // 「开启自动更新」是另一个按钮，它写的是同一份配置里的 autoDaily/autoStale；
    // 而 setUpdatePrefs 是整份覆盖。拿打开页面时那份缓存去展开，
    // 会把开关刚写好的 true 又盖回 false —— 表现为"开启后一保存就自动关闭"。
    // 这里现取一次，再只覆盖界面上真正编辑的两个字段。
    let latest = prefs.value?.prefs;
    try {
      latest = (await api.getUpdatePrefs())?.prefs ?? latest;
    } catch {
      // 取不到就退回缓存：宁可少一次刷新，也不能让保存本身失败
    }

    const next: UpdatePrefs = {
      ...(latest ?? {
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
  await loadFreshness();
  await loadSchedule();
  await loadAutoUpdate();
  if (schedule.value?.daily?.nextRun) {
    const c = clockFromSchedule(schedule.value.daily.nextRun);
    if (c) dailyAt.value = c;
  }
  if (schedule.value?.stale?.nextRun) {
    const c = clockFromSchedule(schedule.value.stale.nextRun);
    if (c) staleAt.value = c;
  }
  await loadRunResult();
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
      <!-- 新鲜度：数据集 / 最新日期 / 最后交易日 / 落后几天。
           方案 §2.2：stale 的落后清单里没有 daily_bars，所以这张表才是
           "数据到底缺没缺"的依据。 -->
      <div class="space-y-2">
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-sm font-medium">数据新鲜度</span>
          <Badge
            v-if="freshness && freshness.behindDays?.length"
            variant="destructive"
            class="text-[10px]"
          >
            落后 {{ freshness.behindDays.length }} 个交易日
          </Badge>
          <Badge v-else-if="freshness" variant="secondary" class="text-[10px]">
            已是最新
          </Badge>
          <Badge v-else variant="outline" class="text-[10px]">未检查</Badge>
          <Button size="xs" variant="ghost" :disabled="!!busy" class="ml-auto" @click="loadFreshness">
            <RefreshCw /> 重新检查
          </Button>
          <Button
            v-if="freshness && freshness.behindDays?.length"
            size="sm"
            variant="outline"
            :disabled="running || !!busy"
            @click="startCatchUp"
          >
            {{ busy === 'catchup' ? '发起中…' : '逐日补跑' }}
          </Button>
          <Button v-if="catchUp?.running" size="sm" variant="ghost" :disabled="!!busy" @click="cancelCatchUp">
            停止补跑
          </Button>
        </div>

        <div v-if="freshness?.datasets?.length" class="overflow-hidden rounded border text-xs">
          <div class="bg-muted grid grid-cols-4 gap-2 px-3 py-1.5 font-medium">
            <span>数据集</span><span>最新日期</span><span>最后交易日</span><span>落后</span>
          </div>
          <div
            v-for="d in freshness.datasets"
            :key="d.name"
            class="grid grid-cols-4 gap-2 border-t px-3 py-1.5"
          >
            <span>{{ d.name }}</span>
            <span>{{ d.missing ? '未入库' : fmtDate(d.latestDate) }}</span>
            <span>{{ fmtDate(d.lastTrading) }}</span>
            <span :class="d.behindDays?.length ? 'text-destructive' : 'text-muted-foreground'">
              {{ d.missing ? '—' : d.behindDays?.length ? `${d.behindDays.length} 天` : '已最新' }}
            </span>
          </div>
        </div>
        <p v-if="freshness" class="text-muted-foreground text-[11px]">{{ freshness.summary }}</p>

        <!-- 补跑进度：串行执行，一条一条来 -->
        <div v-if="catchUp?.running || catchUp?.done" class="text-[11px]">
          <span class="text-muted-foreground">
            补跑：第 {{ catchUp.current }} / {{ catchUp.dates?.length ?? 0 }} 天，
            成功 {{ catchUp.done }} 天<template v-if="catchUp.failed?.length"
              >，失败 {{ catchUp.failed.length }} 天（{{ catchUp.failed.map(fmtDate).join('、') }}）</template
            ><template v-if="catchUp.skipped?.length">，跳过 {{ catchUp.skipped.length }} 天</template>
          </span>
          <p v-if="catchUp.message" class="text-muted-foreground">{{ catchUp.message }}</p>
        </div>
      </div>

      <Separator />

      <!-- 最近一次运行的实际结果。
           ⚠ 这张表的存在理由就是方案 §2.1：**退出码不能当成功判据**。
           所以这里既不给"成功/失败"的总判，也不只看进程状态 ——
           而是把每个数据集写了多少行摆出来，让"有数据"和"上游暂无数据"
           分开呈现。 -->
      <div v-if="runResult" class="space-y-2">
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-sm font-medium">最近一次运行结果</span>
          <Badge
            :variant="runResult.failed?.length ? 'destructive' : 'secondary'"
            class="text-[10px]"
          >
            {{ runResult.failed?.length ? `${runResult.failed.length} 个数据集失败` : '没有本地失败' }}
          </Badge>
          <span class="text-muted-foreground text-[11px]">
            共写 {{ (runResult.rowsWritten ?? 0).toLocaleString() }} 行
          </span>
          <Button size="xs" variant="ghost" :disabled="!!busy" class="ml-auto" @click="loadRunResult">
            <RefreshCw /> 刷新
          </Button>
        </div>

        <div v-if="runResult.datasets?.length" class="overflow-hidden rounded border text-xs">
          <div class="bg-muted grid grid-cols-4 gap-2 px-3 py-1.5 font-medium">
            <span>数据集</span><span>状态</span><span>写入行数</span><span>说明</span>
          </div>
          <div
            v-for="d in runResult.datasets"
            :key="d.dataset"
            class="grid grid-cols-4 gap-2 border-t px-3 py-1.5"
          >
            <span>{{ d.dataset }}</span>
            <span
              :class="
                d.rowsWritten > 0
                  ? 'text-muted-foreground'
                  : runResult.unavailable?.includes(d.dataset)
                    ? 'text-muted-foreground'
                    : 'text-destructive'
              "
            >
              {{ d.status }}
            </span>
            <span>{{ (d.rowsWritten ?? 0).toLocaleString() }}</span>
            <span class="text-muted-foreground truncate">
              {{ d.reasonCode || d.errorCode || d.errorMessage || '—' }}
            </span>
          </div>
        </div>

        <p class="text-muted-foreground text-[11px]">{{ runResult.conclusion }}</p>
        <p v-if="runResult.run?.status" class="text-muted-foreground text-[11px]">
          cne 进程状态：<code>{{ runResult.run.status }}</code>
          <template v-if="runResult.run.coverageStatus">
            · 覆盖：<code>{{ runResult.run.coverageStatus }}</code>
          </template>
          <template v-if="runResult.run.finishedAt">
            · 结束于 {{ runResult.run.finishedAt }}
          </template>
        </p>
        <p class="text-muted-foreground text-[11px]">
          ⚠ 进程状态与数据结果**不是一回事**：曾经出现过 exit=1 但有 5 个步骤成功、
          写了两万多行的情况，也出现过 degraded 却 exit=0。判断更新成没成，看上面的行数。
        </p>
      </div>

      <Separator />

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

        <!-- 历史区间回补：**与上面三个按钮不是同一条命令**。
             上面走 `cne run daily`（日更 pipeline，只认单日 --trade-date），
             这里走 `cne backfill daily_bars --start X --end Y`。 -->
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-muted-foreground text-xs">补历史区间</span>
          <select
            v-model="backfillDataset"
            class="border-input bg-background h-8 rounded-md border px-2 text-xs"
            :disabled="running || !!busy"
          >
            <option value="daily_bars">个股日线 daily_bars</option>
            <option value="index_bars">指数日线 index_bars</option>
          </select>
          <Input v-model="backfillStart" placeholder="起 YYYY-MM-DD" class="h-8 w-36" />
          <span class="text-muted-foreground text-xs">~</span>
          <Input v-model="backfillEnd" placeholder="止 YYYY-MM-DD" class="h-8 w-36" />
          <Button
            size="sm"
            variant="outline"
            :disabled="running || !!busy || !backfillStart || !backfillEnd"
            @click="startBackfill"
          >
            {{ busy === 'backfill' ? '发起中…' : '补历史区间' }}
          </Button>
        </div>
        <p class="text-muted-foreground text-[11px]">
          「补历史区间」用于把数据湖**过去缺失的日子**补齐（例如做长区间回测前的预热窗口）。
          ⚠ 它的成本按<strong>标的</strong>算、与窗口宽窄几乎无关 ——
          <strong>一次给足区间即可，不要按天循环补</strong>。数据湖是"先抓取、再校验、再发布"，
          所以跑完之前 <code>curated</code> 里的分区不会变化，用 <code>cne status</code> 看进度。
        </p>
        <p class="text-muted-foreground text-[11px]">
          ⚠ <strong>个股日线（<code>daily_bars</code>）与指数日线（<code>index_bars</code>）是两个数据集，
          做长区间回测时两个都要补。</strong>交易日历取自基准指数（<code>000300.SH</code>），
          它在 <code>index_bars</code> 里 —— 只补个股的话，回填会停在指数开始的那一天
          （实测：指数只有 728 天时，回填也只有 728 天，前面那一年根本不在日历里）。
        </p>
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
        <!-- ⚠ 非交易日跳过行情组是最容易被误解的情形：用户点"更新到今天"、
             看到任务成功，却不知道**行情数据这次根本没碰**（方案 §2.3）。
             所以这里单独用一句话说清楚。 -->
        <p v-if="skippedSteps.length" class="text-[11px] text-amber-600">
          本次跳过了 {{ skippedSteps.length }} 个调度组：
          {{ skippedSteps.map((s) => s.name).join("、") }}。
          <template v-if="skippedSteps.some((s) => s.rows?.includes('non_trading_day'))">
            原因是<strong>今天不是交易日</strong> —— 普通日更不会碰行情数据。
            要补某个交易日的行情，请用上方的「补指定交易日」。
          </template>
        </p>
        <p v-if="runningSteps.length" class="text-muted-foreground text-[11px]">
          正在跑：{{ runningSteps.map((s) => `${s.name}${s.elapsed ? ` (${s.elapsed})` : ""}`).join("、") }}
          —— cne 每分钟会打一次心跳，长时间没新输出不等于卡死。
        </p>
        <div v-if="status.steps?.length" class="flex flex-wrap gap-1">
          <Badge
            v-for="step in status.steps"
            :key="step.name"
            :variant="step.state === 'failed' ? 'destructive' : step.state === 'success' ? 'secondary' : 'outline'"
            class="text-[10px]"
          >
            {{ step.name }} · {{ step.state }}<template v-if="step.elapsed"> · {{ step.elapsed }}</template
            ><template v-if="step.rows"> · {{ step.rows }}</template>
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
            <span class="text-muted-foreground">cne 工程目录（含 cnequity.toml）</span>
            <Input v-model="projectDir" class="h-8 font-mono" placeholder="/data/.config/cnequity 或 /volume1/docker/CNEquity" />
            <span class="text-[10px] text-muted-foreground">
              认两种布局：<code>configs/cnequity.toml</code> 或 toml 直接放在该目录里
              （Linux 上 cne 默认用 <code>~/.config/cnequity</code>）
            </span>
          </label>
          <label class="flex flex-col gap-1 text-xs">
            <span class="text-muted-foreground">cne 目录或可执行文件（留空则按 PATH 查找）</span>
            <Input v-model="cnePath" class="h-8 font-mono" placeholder="填目录即可，例如 /data/cne-venv" />
            <span class="text-[10px] text-muted-foreground">
              可填目录（会自动在其中找 cne）、可执行文件，或命令名；留空则按 PATH 查找（容器里已含 <code>/data/cne-venv/bin</code>）
            </span>
          </label>
        </div>

        <div class="space-y-2 rounded border border-dashed p-3">
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-sm font-medium">自动更新</span>
            <Badge
              :variant="autoUpdate?.enabled ? 'secondary' : 'outline'"
              class="text-[10px]"
            >
              {{ autoUpdate?.enabled ? '已开启' : '已关闭' }}
            </Badge>
            <Badge
              v-if="autoUpdate && !autoUpdate.startWhenAvailable"
              variant="destructive"
              class="text-[10px]"
            >
              错过时间不会补跑
            </Badge>
            <div class="ml-auto flex items-center gap-2">
              <Button
                size="sm"
                :variant="autoUpdate?.enabled ? 'outline' : 'default'"
                :disabled="!!busy"
                @click="toggleAutoUpdate(!autoUpdate?.enabled)"
              >
                {{ busy === 'auto' ? '切换中…' : autoUpdate?.enabled ? '关闭自动更新' : '开启自动更新' }}
              </Button>
              <Button
                v-if="autoUpdate && !autoUpdate.startWhenAvailable"
                size="sm"
                variant="default"
                :disabled="!!busy"
                @click="fixScheduleSettings"
              >
                {{ busy === 'fix' ? '修复中…' : '修复：错过也补跑' }}
              </Button>
            </div>
          </div>
          <p v-if="autoUpdateError" class="text-destructive flex items-start gap-1 text-[11px]">
            <CircleAlert class="mt-0.5 size-3.5 shrink-0" />
            <span>
              读不到自动更新状态：{{ autoUpdateError }}
              <Button size="xs" variant="ghost" class="ml-1 h-5 px-1" @click="loadAutoUpdate">重试</Button>
            </span>
          </p>
          <p v-else-if="autoUpdate?.warning" class="text-[11px] text-amber-600">{{ autoUpdate.warning }}</p>
          <p v-else-if="autoUpdate?.summary" class="text-muted-foreground text-[11px]">
            {{ autoUpdate.summary }}
          </p>
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-muted-foreground text-xs">当前生效的任务：</span>
            <Button size="xs" variant="ghost" :disabled="!!busy" class="ml-auto" @click="runDiagnose">
              {{ busy === 'diagnose' ? '诊断中…' : '诊断' }}
            </Button>
          </div>
          <!-- 诊断报告：定位"系统找不到指定的路径"用 -->
          <div v-if="diagnoseReport" class="space-y-1">
            <div class="text-[11px] font-medium">进程内诊断报告（可复制）：</div>
            <pre class="bg-muted max-h-64 overflow-auto rounded p-2 text-[10px] leading-relaxed">{{ diagnoseReport }}</pre>
          </div>
          <div v-if="schedule" class="space-y-1 text-xs">
            <div class="grid grid-cols-2 gap-2">
              <div v-for="ti in [schedule.daily, schedule.stale]" :key="ti.name">
                <Badge :variant="ti.registered ? 'secondary' : 'destructive'" class="text-[10px]">
                  {{ ti.name }}
                </Badge>
                <span v-if="ti.registered" class="text-muted-foreground ml-1">
                  下次 {{ ti.nextRun || '—' }} · {{ ti.status || '—' }}
                </span>
                <span v-else class="text-muted-foreground ml-1">未注册</span>
              </div>
            </div>
            <p v-if="schedule.summary" class="text-muted-foreground">{{ schedule.summary }}</p>
            <!-- note 说明"谁在跑"：Windows 是 schtasks，Linux/NAS 是进程内调度器。
                 不显示它的话，两边的行为差异（应用/容器停着会不会跑）用户看不到。 -->
            <p v-if="schedule.note" class="text-muted-foreground">{{ schedule.note }}</p>
            <p v-if="schedule.wrapperPath" class="text-muted-foreground">
              包装脚本：<code>{{ schedule.wrapperPath }}</code>
            </p>
          </div>

          <!-- 改时间：**两步**。先预览（把原始命令摆出来），再确认执行。
               这是"改动前弹确认"的界面落实 —— 确认前绝不调用 applySchedule。 -->
          <div class="space-y-2 border-t pt-2">
            <div class="grid gap-2 md:grid-cols-2">
              <label class="flex flex-col gap-1 text-xs">
                <span class="text-muted-foreground">主日更时间（cne-daily-update）</span>
                <Input v-model="dailyAt" placeholder="17:30" class="h-8" />
              </label>
              <label class="flex flex-col gap-1 text-xs">
                <span class="text-muted-foreground">补抓落后时间（cne-daily-stale）</span>
                <Input v-model="staleAt" placeholder="21:00" class="h-8" />
              </label>
            </div>
            <Button size="sm" variant="outline" :disabled="!!busy" @click="previewScheduleChange">
              {{ busy === 'preview' ? '计算中…' : '预览改动' }}
            </Button>

            <!-- 确认区：把将要执行的**原始命令**原样显示 -->
            <div v-if="plan" class="space-y-2 rounded border border-amber-500/50 bg-amber-500/5 p-3">
              <div class="flex items-start gap-2 text-xs">
                <CircleAlert class="mt-0.5 size-4 shrink-0 text-amber-600" />
                <div class="space-y-1">
                  <div class="font-medium">将要对你机器上的自动更新计划做以下改动</div>
                  <div v-for="c in plan.changes" :key="c.task" class="text-muted-foreground">
                    <template v-if="c.action === 'change'">
                      <code>{{ c.task }}</code>：{{ c.from }} → <strong>{{ c.to }}</strong>
                    </template>
                    <template v-else-if="c.action === 'keep'">
                      <code>{{ c.task }}</code>：无需改动（{{ c.reason }}）
                    </template>
                    <template v-else>
                      <code>{{ c.task }}</code>：**不会创建** —— {{ c.reason }}
                    </template>
                  </div>
                </div>
              </div>

              <div v-if="plan.commands?.length" class="space-y-1">
                <div class="text-[11px] font-medium">将要执行的操作（原样）：</div>
                <pre class="bg-muted overflow-auto rounded p-2 text-[11px]">{{ plan.commands.join("\n") }}</pre>
              </div>

              <div class="flex flex-wrap items-center gap-2">
                <Button
                  v-if="plan.hasChanges"
                  size="sm"
                  variant="destructive"
                  :disabled="!!busy"
                  @click="applyScheduleChange"
                >
                  {{ busy === 'apply' ? '执行中…' : '确认执行以上改动' }}
                </Button>
                <Button size="sm" variant="ghost" :disabled="!!busy" @click="discardPlan">
                  放弃
                </Button>
                <span class="text-muted-foreground text-[11px]">
                  只修改现有任务，<strong>不会新建也不会删除</strong>。
                </span>
              </div>
            </div>
            <p v-if="plan && !plan.hasChanges && plan.warnings?.length" class="text-[11px] text-amber-600">
              {{ plan.warnings.join('；') }}
            </p>
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
