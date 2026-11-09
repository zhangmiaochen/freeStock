import { reactive, ref } from "vue";

/**
 * 全局确认/输入弹窗。
 *
 * 为什么不直接用 window.confirm / prompt：
 * 系统弹窗样式由操作系统决定，和应用的深色主题完全不一致，而且无法定制文案按钮。
 * 这里用一个全局单例 + DialogHost 组件渲染，调用方 `await confirmDialog(...)` 即可。
 */

export type DialogKind = "confirm" | "prompt";

export interface DialogOptions {
  /** 标题 */
  title: string;
  /** 补充说明（可缺省） */
  message?: string;
  confirmText?: string;
  cancelText?: string;
  /** 危险操作：确认按钮用红色 */
  danger?: boolean;
  /** prompt 的默认值与占位符 */
  defaultValue?: string;
  placeholder?: string;
  inputSuffix?: string;
}

export const dialogState = reactive({
  open: false,
  kind: "confirm" as DialogKind,
  title: "",
  message: "",
  confirmText: "确定",
  cancelText: "取消",
  danger: false,
  placeholder: "",
  inputSuffix: "",
});

/** prompt 的输入框内容 */
export const dialogInput = ref("");

type Resolver = (value: boolean | string | null) => void;

let resolver: Resolver | null = null;
let currentKind: DialogKind = "confirm";

function open(kind: DialogKind, opts: DialogOptions): Promise<boolean | string | null> {
  // 已有弹窗未关闭时，先按"取消"结算，避免上一个 Promise 永远悬挂
  if (resolver) {
    const previous = resolver;
    resolver = null;
    previous(currentKind === "prompt" ? null : false);
  }
  currentKind = kind;
  dialogState.kind = kind;
  dialogState.title = opts.title;
  dialogState.message = opts.message ?? "";
  dialogState.confirmText = opts.confirmText ?? (kind === "prompt" ? "保存" : "确定");
  dialogState.cancelText = opts.cancelText ?? "取消";
  dialogState.danger = opts.danger ?? false;
  dialogState.placeholder = opts.placeholder ?? "";
  dialogState.inputSuffix = opts.inputSuffix ?? "";
  dialogInput.value = opts.defaultValue ?? "";
  dialogState.open = true;
  return new Promise<boolean | string | null>((resolve) => {
    resolver = resolve;
  });
}

/** 确认框：返回 true = 确定，false = 取消。 */
export function confirmDialog(opts: DialogOptions): Promise<boolean> {
  return open("confirm", opts) as Promise<boolean>;
}

/** 输入框：返回输入内容（已 trim），取消返回 null。 */
export function promptDialog(opts: DialogOptions): Promise<string | null> {
  return open("prompt", opts) as Promise<string | null>;
}

/** DialogHost 调用：ok=true 表示确定/保存。 */
export function settleDialog(ok: boolean): void {
  if (!resolver) return;
  const resolve = resolver;
  resolver = null;
  dialogState.open = false;
  resolve(currentKind === "prompt" ? (ok ? dialogInput.value.trim() : null) : ok);
}
