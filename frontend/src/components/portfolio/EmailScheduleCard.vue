<script setup lang="ts">
/**
 * 每日调仓日报的邮件订阅设置。
 *
 * 几个刻意的设计：
 *   - **授权码不回显**：后端只回传"有没有存过"，输入框留空即表示"不改"。
 *     整份设置的其它字段照常可改，所以需要一个"已保存/未保存"的提示，
 *     否则用户不知道这次改的到底生效了没有。
 *   - **先预览再发**：邮件发出去收不回来，预览是唯一的所见即所得入口。
 *   - **测试邮件与立即发送分开**：前者只验证 SMTP 通不通（不依赖评分数据），
 *     后者是"现在就把今天的日报发出去"。
 *   - 界面明确写出"应用没运行时不会发送" —— 内置调度器的固有限制，
 *     不说清楚的话用户会以为漏发了是 bug。
 */
import { computed, onMounted, ref, watch } from "vue";
import {
  CircleAlert,
  CircleCheck,
  Eye,
  LoaderCircle,
  Mail,
  Save,
  Send,
  Trash2,
  X,
} from "@lucide/vue";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  api,
  errorMessage,
  type EmailPrefs,
  type EmailPrefsInfo,
  type EmailPreview,
  type Portfolio,
} from "@/lib/backend";

const props = defineProps<{ portfolios: Portfolio[]; currentId: string }>();

const info = ref<EmailPrefsInfo | null>(null);
const form = ref<EmailPrefs | null>(null);
const loading = ref(false);
const saving = ref(false);
const testing = ref(false);
const sending = ref(false);
const previewing = ref(false);
const message = ref("");
const errorText = ref("");

/** 授权码输入框：留空表示不改，所以它不参与"未保存"判定之外的回写 */
const passwordInput = ref("");
const preview = ref<EmailPreview | null>(null);
const showPreview = ref(false);
const expanded = ref(false);

/** 周一到周日（0=周日，与后端 time.Weekday 一致，这里只是显示顺序不同） */
const WEEKDAYS: { value: number; label: string }[] = [
  { value: 1, label: "一" },
  { value: 2, label: "二" },
  { value: 3, label: "三" },
  { value: 4, label: "四" },
  { value: 5, label: "五" },
  { value: 6, label: "六" },
  { value: 0, label: "日" },
];

const ENCRYPTIONS: { value: string; label: string; hint: string }[] = [
  { value: "auto", label: "自动", hint: "按端口判断：465 用 SSL，其余用 STARTTLS" },
  { value: "ssl", label: "SSL / TLS", hint: "连上就握手（隐式 TLS），国内邮箱的 465 就是它" },
  { value: "starttls", label: "STARTTLS", hint: "先明文连接再升级加密（常见于 587）" },
  { value: "none", label: "不加密", hint: "仅内网 relay 适用 —— 授权码会明文发出" },
];

/** 收件人数量（按后端归一后的结果算，与真实发送一致） */
const recipientCount = computed(() => info.value?.recipients?.length ?? 0);
const invalidRecipients = computed(() => info.value?.invalidRecipients ?? []);
const schedule = computed(() => info.value?.schedule ?? null);

/** 状态条上的一句话摘要 */
const summaryText = computed(() => {
  const prefs = form.value;
  if (!prefs) return "读取中…";
  if (!prefs.enabled) return "已关闭";
  const scope =
    prefs.scope === "one"
      ? (props.portfolios.find((item) => item.id === prefs.portfolioId)?.name ?? "未选择组合")
      : `${props.portfolios.filter((item) => item.status !== "archived").length} 个活跃组合`;
  return `每日 ${prefs.sendTime} · ${weekdaySummary(prefs.weekdays)} · ${scope}`;
});

function weekdaySummary(days: number[]): string {
  if (!days || days.length === 0) return "每天";
  if (days.length === 5 && [1, 2, 3, 4, 5].every((d) => days.includes(d))) return "工作日";
  const names = ["日", "一", "二", "三", "四", "五", "六"];
  return "周" + days.map((d) => names[d] ?? "?").join("、");
}

async function load() {
  loading.value = true;
  try {
    const loaded = await api.portfolio.getEmailPrefs();
    info.value = loaded;
    // 表单用副本：用户改了但没点保存时，状态条仍显示"未保存"
    form.value = { ...loaded.prefs };
    passwordInput.value = "";
  } catch (err) {
    errorText.value = errorMessage(err);
  } finally {
    loading.value = false;
  }
}

async function save() {
  if (!form.value) return;
  saving.value = true;
  errorText.value = "";
  message.value = "";
  try {
    // 授权码只在用户真的输入了才带上；留空交给后端保留原值
    const payload: EmailPrefs = { ...form.value, password: passwordInput.value };
    const saved = await api.portfolio.saveEmailPrefs(payload);
    info.value = saved;
    form.value = { ...saved.prefs };
    passwordInput.value = "";
    message.value = saved.prefs.enabled
      ? `已保存。${saved.schedule.nextRunHint || "定时发送已开启"}`
      : "已保存（定时发送处于关闭状态，不会自动发送）";
  } catch (err) {
    errorText.value = errorMessage(err);
  } finally {
    saving.value = false;
  }
}

async function clearPassword() {
  if (!window.confirm("清空已保存的授权码？下次发送前需要重新填写。")) return;
  try {
    const saved = await api.portfolio.clearEmailPassword();
    info.value = saved;
    form.value = { ...saved.prefs };
    passwordInput.value = "";
    message.value = "授权码已清空";
  } catch (err) {
    errorText.value = errorMessage(err);
  }
}

async function sendTest() {
  testing.value = true;
  errorText.value = "";
  message.value = "";
  try {
    // 直接发到当前编辑的收件人列表之外更稳：先保存再用设置里的地址，
    // 免得用户以为"测过了就没问题"而实际配置还没落盘。
    const result = await api.portfolio.sendTestEmail("");
    message.value = `测试邮件已发送给 ${result.recipients.join("、")}（耗时 ${result.elapsed}）`;
  } catch (err) {
    errorText.value = `测试发送失败：${errorMessage(err)}`;
  } finally {
    testing.value = false;
  }
}

async function sendNow() {
  if (!window.confirm("立即发送一次日报？会按当前保存的设置发送。")) return;
  sending.value = true;
  errorText.value = "";
  message.value = "";
  try {
    const result = await api.portfolio.sendDailyEmailNow();
    message.value =
      `日报已发送给 ${result.recipients.join("、")}` +
      `（组合：${result.portfolios.join("、") || "无"}，耗时 ${result.elapsed}）`;
    await load();
  } catch (err) {
    errorText.value = `发送失败：${errorMessage(err)}`;
  } finally {
    sending.value = false;
  }
}

async function openPreview() {
  previewing.value = true;
  errorText.value = "";
  try {
    preview.value = await api.portfolio.previewEmail();
    showPreview.value = true;
  } catch (err) {
    errorText.value = `预览失败：${errorMessage(err)}`;
  } finally {
    previewing.value = false;
  }
}

/**
 * 预览用的 srcdoc。
 *
 * ⚠ 用 **iframe 的 srcdoc** 而不是 `v-html`：邮件正文自带一整套表格样式，
 * 直接内联进页面会污染应用的 Tailwind 样式（`table` / `td` 的全局规则），
 * 预览也就不再是"收件人看到的那个样子"了。iframe 把它隔离在独立文档里。
 */
const previewDoc = computed(() => preview.value?.html ?? "");

/** 端口变了但加密方式还是自动时，把判断结果说出来 */
const encryptionHint = computed(() => {
  const prefs = form.value;
  if (!prefs) return "";
  const found = ENCRYPTIONS.find((item) => item.value === prefs.encryption);
  if (!found) return "";
  if (prefs.encryption === "auto") {
    return `${found.hint}（当前端口 ${prefs.port} → ${
      prefs.port === 465 ? "SSL / TLS" : "STARTTLS"
    }）`;
  }
  return found.hint;
});

/**
 * 「指定组合」下拉里的排序：把**左侧当前选中的那个组合排在最前**。
 *
 * 用户多半就是想把日报发给正在看的这个组合，让它第一个出现省一次寻找；
 * 不改变集合，只改顺序。
 */
const portfolioOptions = computed(() => {
  const list = props.portfolios ?? [];
  const selected = list.filter((item) => item.id === props.currentId);
  const rest = list.filter((item) => item.id !== props.currentId);
  return [...selected, ...rest];
});

/**
 * 测试邮件发到的是**已保存**的收件人，不是输入框里还没保存的内容。
 * 这条提示不说清楚的话，用户会以为"填了新地址→测试通过→配置没问题"。
 */
const testHint = computed(() => {
  const saved = info.value?.recipients ?? [];
  if (saved.length === 0) return "测试邮件会发给「已保存」的收件人，当前还没有保存任何收件人。";
  return `测试邮件会发给「已保存」的收件人：${saved.join("、")}` +
    (dirty.value ? "（当前有未保存的修改，测试不会用它们）" : "");
});

/** 表单与已保存设置是否有差异（授权码输入框也算） */
const dirty = computed(() => {
  if (!form.value || !info.value) return false;
  if (passwordInput.value !== "") return true;
  const saved = info.value.prefs;
  return (Object.keys(form.value) as (keyof typeof form.value)[]).some((key) => {
    const a = form.value![key];
    const b = saved[key];
    if (Array.isArray(a) && Array.isArray(b)) return a.join(",") !== b.join(",");
    return a !== b;
  });
});

/** 后端报告的"现在能不能发" */
const ready = computed(() => info.value?.ready ?? false);
const readyError = computed(() => info.value?.readyError ?? "");

/** 保存后重新读一次状态（收件人解析结果、下次发送时间都在里面） */
watch(
  () => form.value?.enabled,
  () => {
    message.value = "";
    errorText.value = "";
  },
);

onMounted(load);

defineExpose({ reload: load });
</script>

<template>
  <section class="bg-card rounded-lg border">
    <!-- 头部：状态 + 展开 -->
    <div class="flex flex-wrap items-center gap-2 px-4 py-2">
      <Mail class="text-muted-foreground size-4" />
      <span class="text-sm font-medium">调仓日报邮件</span>

      <Badge :variant="form?.enabled ? 'default' : 'outline'" class="text-[10px]">
        {{ form?.enabled ? "定时已开启" : "未开启" }}
      </Badge>
      <Badge v-if="recipientCount > 0" variant="secondary" class="text-[10px]">
        {{ recipientCount }} 个收件人
      </Badge>
      <span class="text-muted-foreground text-[11px]">{{ summaryText }}</span>

      <span
        v-if="schedule?.enabled && schedule.nextRunHint"
        class="text-muted-foreground hidden text-[11px] lg:inline"
      >
        · 下次：{{ schedule.nextRunHint }}
      </span>

      <div class="ml-auto flex items-center gap-2">
        <span v-if="schedule?.lastStatus" class="text-[11px]"
          :class="schedule.lastStatus === '成功' ? 'text-up' : 'text-destructive'">
          上次：{{ schedule.lastStatus }}
          <template v-if="schedule.lastSentAt">（{{ schedule.lastSentAt }}）</template>
        </span>
        <Button size="xs" variant="outline" @click="expanded = !expanded">
          {{ expanded ? "收起" : "设置" }}
        </Button>
      </div>
    </div>

    <p v-if="errorText" class="text-destructive border-t px-4 py-2 text-xs">{{ errorText }}</p>
    <p v-if="message" class="text-up border-t px-4 py-2 text-xs">{{ message }}</p>

    <!-- 上次失败的原因要一直可见，而不是只在发送那一刻闪一下 -->
    <p
      v-if="schedule?.lastError && !expanded"
      class="text-destructive border-t px-4 py-2 text-[11px]"
    >
      上次失败原因：{{ schedule.lastError }}
    </p>

    <!-- ============ 设置区 ============ -->
    <div v-if="expanded && form" class="space-y-3 border-t px-4 py-3">
      <p class="text-muted-foreground flex items-start gap-1.5 text-[11px]">
        <CircleAlert class="mt-0.5 size-3 shrink-0" />
        <span>
          定时发送由<b>应用内的调度器</b>执行 —— 应用没运行时不会发送，也不会补发。
          内容按<b>每个活跃组合</b>分段：组合汇总、调仓动作、当前持仓。
        </span>
      </p>

      <!-- 总开关 + 发送范围 -->
      <div class="flex flex-wrap items-center gap-3">
        <label class="flex items-center gap-1.5 text-xs">
          <input v-model="form.enabled" type="checkbox" class="size-3.5" />
          启用定时发送
        </label>

        <label class="flex items-center gap-1.5 text-xs">
          范围
          <select
            v-model="form.scope"
            class="bg-background h-7 rounded border px-1.5 text-xs outline-none"
          >
            <option value="active">全部活跃组合</option>
            <option value="one">指定组合</option>
          </select>
        </label>

        <label v-if="form.scope === 'one'" class="flex items-center gap-1.5 text-xs">
          组合
          <select
            v-model="form.portfolioId"
            class="bg-background h-7 min-w-40 rounded border px-1.5 text-xs outline-none"
          >
            <option value="">（请选择）</option>
            <option v-for="item in portfolioOptions" :key="item.id" :value="item.id">
              {{ item.name }}{{ item.id === currentId ? "（当前）" : "" }}
            </option>
          </select>
        </label>
      </div>

      <!-- SMTP -->
      <div class="grid gap-x-4 gap-y-2 sm:grid-cols-2 lg:grid-cols-4">
        <label class="block">
          <span class="text-muted-foreground text-[11px]">SMTP 服务器</span>
          <input
            v-model.trim="form.host"
            placeholder="smtp.qq.com"
            class="tabular bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
          />
        </label>
        <label class="block">
          <span class="text-muted-foreground text-[11px]">端口</span>
          <input
            v-model.number="form.port"
            type="number"
            min="1"
            max="65535"
            class="tabular bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
          />
        </label>
        <label class="block">
          <span class="text-muted-foreground text-[11px]">加密方式</span>
          <select
            v-model="form.encryption"
            class="bg-background mt-0.5 h-7 w-full rounded border px-1.5 text-xs outline-none"
          >
            <option v-for="item in ENCRYPTIONS" :key="item.value" :value="item.value">
              {{ item.label }}
            </option>
          </select>
        </label>
        <label class="block">
          <span class="text-muted-foreground text-[11px]">发件人显示名</span>
          <input
            v-model.trim="form.fromName"
            placeholder="FreeStock 调仓助手"
            class="bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
          />
        </label>

        <label class="block">
          <span class="text-muted-foreground text-[11px]">邮箱账号</span>
          <input
            v-model.trim="form.username"
            placeholder="you@qq.com"
            class="tabular bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
          />
        </label>
        <label class="block">
          <span class="text-muted-foreground text-[11px]">
            SMTP 授权码
            <span v-if="info?.passwordSet" class="text-up">（已保存，留空表示不改）</span>
          </span>
          <input
            v-model="passwordInput"
            type="password"
            autocomplete="new-password"
            :placeholder="info?.passwordSet ? '••••••••（留空不改）' : '邮箱设置里生成的授权码'"
            class="bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
          />
        </label>
        <label class="block">
          <span class="text-muted-foreground text-[11px]">发件人（留空用账号）</span>
          <input
            v-model.trim="form.from"
            placeholder="同上"
            class="tabular bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
          />
        </label>
        <label class="block">
          <span class="text-muted-foreground text-[11px]">单次发送超时（秒）</span>
          <input
            v-model.number="form.timeoutSeconds"
            type="number"
            min="5"
            max="300"
            class="tabular bg-background mt-0.5 h-7 w-full rounded border px-2 text-xs outline-none"
          />
        </label>
      </div>

      <p class="text-muted-foreground text-[11px]">{{ encryptionHint }}</p>

      <!-- 收件人 -->
      <div>
        <div class="flex items-center gap-2">
          <span class="text-muted-foreground text-[11px]">收件人（多个用换行 / 逗号 / 分号分隔）</span>
          <Badge v-if="recipientCount" variant="secondary" class="text-[10px]">
            已识别 {{ recipientCount }} 个
          </Badge>
        </div>
        <textarea
          v-model="form.recipients"
          rows="3"
          placeholder="a@example.com&#10;b@example.com"
          class="tabular bg-background mt-0.5 w-full rounded border px-2 py-1.5 text-xs outline-none"
        />
        <p v-if="invalidRecipients.length" class="text-destructive mt-1 text-[11px]">
          <CircleAlert class="mr-1 inline size-3" />
          以下地址不合法，发送时会被拒绝：{{ invalidRecipients.join("、") }}
        </p>
        <p class="text-muted-foreground mt-1 text-[11px]">
          保存后才会重新解析 —— 上面的数量是<b>已保存</b>的结果。
        </p>
      </div>

      <!-- 定时 -->
      <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
        <label class="flex items-center gap-1.5 text-xs">
          发送时间
          <input
            v-model="form.sendTime"
            placeholder="17:00"
            class="tabular bg-background h-7 w-20 rounded border px-2 text-xs outline-none"
          />
        </label>

        <div class="flex items-center gap-1.5 text-xs">
          <span class="text-muted-foreground">发送日</span>
          <button
            v-for="day in WEEKDAYS"
            :key="day.value"
            type="button"
            class="h-6 w-6 rounded border text-[11px] transition-colors"
            :class="form.weekdays.includes(day.value)
              ? 'border-primary bg-primary text-primary-foreground'
              : 'hover:bg-muted'"
            @click="
              form.weekdays = form.weekdays.includes(day.value)
                ? form.weekdays.filter((d) => d !== day.value)
                : [...form.weekdays, day.value]
            "
          >
            {{ day.label }}
          </button>
          <button
            type="button"
            class="text-muted-foreground hover:text-foreground ml-1 text-[11px] underline"
            @click="form.weekdays = []"
          >
            每天
          </button>
        </div>
        <span class="text-muted-foreground text-[11px]">
          （不勾任何一天 = 每天发；只勾工作日时<b>法定节假日仍会发</b>，
          本程序没有交易日历判断）
        </span>
      </div>

      <!-- 内容 -->
      <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
        <label class="flex items-center gap-1.5 text-xs">
          <input v-model="form.includeHoldings" type="checkbox" class="size-3.5" />
          附当前持仓明细
        </label>
        <label class="flex items-center gap-1.5 text-xs">
          <input v-model="form.includeCandidates" type="checkbox" class="size-3.5" />
          附候选池
        </label>
        <label v-if="form.includeCandidates" class="flex items-center gap-1.5 text-xs">
          Top
          <input
            v-model.number="form.candidateLimit"
            type="number"
            min="1"
            max="50"
            class="tabular bg-background h-7 w-16 rounded border px-2 text-xs outline-none"
          />
        </label>
        <label class="flex flex-1 items-center gap-1.5 text-xs">
          主题
          <input
            v-model.trim="form.subject"
            class="bg-background h-7 min-w-56 flex-1 rounded border px-2 text-xs outline-none"
          />
        </label>
        <span class="text-muted-foreground text-[11px]">
          可用占位：<code>{date}</code> <code>{portfolio}</code> <code>{count}</code>
        </span>
      </div>

      <!-- 发送前刷新评分 -->
      <div class="border-t pt-3">
        <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
          <label class="flex items-center gap-1.5 text-xs">
            <input v-model="form.refreshScoresBeforeSend" type="checkbox" class="size-3.5" />
            <b>发送前先刷新评分</b>
          </label>
          <label v-if="form.refreshScoresBeforeSend" class="flex items-center gap-1.5 text-xs">
            评分只数
            <select
              v-model.number="form.scoreLimit"
              class="bg-background h-7 rounded border px-1.5 text-xs outline-none"
              title="广度闸门按全市场统计「评分≥70 的只数」。只评一部分会让它被系统性低估，判成「广度不足」并删光买入信号。"
            >
              <option :value="0">全市场（推荐）</option>
              <option :value="3000">3000</option>
              <option :value="1500">1500</option>
              <option :value="800">800</option>
              <option :value="300">300</option>
            </select>
          </label>
          <span v-if="form.refreshScoresBeforeSend" class="text-muted-foreground text-[11px]">
            首次执行要几十秒；当天已经刷过则秒回（不会重复算）
          </span>
        </div>
        <p class="text-muted-foreground mt-1 text-[11px]">
          <template v-if="form.refreshScoresBeforeSend">
            调仓建议是拿<b>某一帧评分</b>算出来的。不先刷的话，那一帧可能还停在昨天
            （应用刚启动、或当天盘后还没点过「刷新评分」），
            发出去的就是一封<b>准时但过期</b>的邮件 —— 内容看着正常，数字却是旧的，
            而收件人完全看不出来。刷新<b>只针对这封邮件用到的策略</b>，走增量。
          </template>
          <template v-else>
            <b class="text-amber-500">已关闭</b>：邮件会用<b>数据库里现有的评分</b>生成。
            如果那帧评分不是最新的，建议就是基于旧数据的 —— 界面上的「生成建议」
            旁边会显示评分日，可以对照。
          </template>
        </p>
      </div>

      <!-- 明文存储说明 -->
      <p class="text-muted-foreground border-t pt-2 text-[11px]">
        {{ info?.passwordNote }}
      </p>
      <p v-if="info?.configPath" class="text-muted-foreground text-[11px]">
        设置文件：<span class="tabular">{{ info.configPath }}</span>
      </p>

      <!-- 校验结果 -->
      <p v-if="!ready && readyError" class="text-amber-500 text-[11px]">
        <CircleAlert class="mr-1 inline size-3" />当前设置还不能发送：{{ readyError }}
      </p>
      <p v-else-if="ready" class="text-muted-foreground text-[11px]">
        <CircleCheck class="mr-1 inline size-3 text-up" />配置完整，可以发送
      </p>

      <!-- 操作 -->
      <div class="flex flex-wrap items-center gap-2 border-t pt-3">
        <Button size="sm" :disabled="saving" @click="save">
          <LoaderCircle v-if="saving" class="animate-spin" />
          <Save v-else />
          {{ saving ? "保存中…" : "保存设置" }}
        </Button>
        <span v-if="dirty" class="text-amber-500 text-[11px]">有未保存的修改</span>
        <div class="ml-auto flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" :disabled="testing" @click="sendTest">
            <LoaderCircle v-if="testing" class="animate-spin" />
            <Send v-else />
            {{ testing ? "发送中…" : "发送测试邮件" }}
          </Button>
          <Button size="sm" variant="outline" :disabled="previewing" @click="openPreview">
            <LoaderCircle v-if="previewing" class="animate-spin" />
            <Eye v-else />
            预览邮件
          </Button>
          <Button size="sm" variant="outline" :disabled="sending" @click="sendNow">
            <LoaderCircle v-if="sending" class="animate-spin" />
            <Mail v-else />
            {{ sending ? "发送中…" : "立即发送日报" }}
          </Button>
          <Button
            v-if="info?.passwordSet"
            size="sm"
            variant="ghost"
            class="text-muted-foreground"
            @click="clearPassword"
          >
            <Trash2 /> 清除授权码
          </Button>
        </div>
        <p class="text-muted-foreground w-full text-[11px]">{{ testHint }}</p>
      </div>
    </div>

    <!-- ============ 预览 ============ -->
    <div
      v-if="showPreview"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-6"
      @click.self="showPreview = false"
    >
      <div class="bg-background flex max-h-full w-full max-w-4xl flex-col rounded-lg border shadow-xl">
        <div class="flex items-center gap-2 border-b px-4 py-2">
          <Mail class="text-muted-foreground size-4" />
          <span class="text-sm font-medium">邮件预览</span>
          <span class="text-muted-foreground truncate text-[11px]">
            主题：{{ preview?.subject }}
          </span>
          <Badge v-if="preview?.warnings?.length" variant="outline" class="text-[10px]">
            {{ preview.warnings.length }} 条提示
          </Badge>
          <Button size="xs" variant="ghost" class="ml-auto" @click="showPreview = false">
            <X />
          </Button>
        </div>

        <div v-if="preview?.warnings?.length" class="border-b px-4 py-1.5 text-[11px]">
          <p v-for="(warning, index) in preview.warnings" :key="index" class="text-muted-foreground">
            · {{ warning }}
          </p>
        </div>

        <!-- srcdoc：把邮件正文隔离在独立文档里，既不被应用样式污染，
             也不会让邮件里的样式漏到应用上 -->
        <iframe
          :srcdoc="previewDoc"
          class="h-[70vh] w-full flex-1 rounded-b-lg bg-white"
          sandbox=""
          title="邮件预览"
        />
      </div>
    </div>
  </section>
</template>
