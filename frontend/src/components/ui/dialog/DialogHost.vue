<script setup lang="ts">
/**
 * 全局确认/输入弹窗的渲染宿主（挂在 App.vue 根部）。
 *
 * 用 reka-ui 的 Dialog 原语：焦点锁、Esc 关闭、遮罩点击关闭都是现成的，
 * 样式走应用自己的主题，不用系统弹窗。
 */
import { computed, nextTick, ref, watch } from "vue";
import {
  DialogContent,
  DialogDescription,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "reka-ui";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { dialogInput, dialogState, settleDialog } from "@/lib/dialog";

const inputEl = ref<InstanceType<typeof Input> | null>(null);

const isPrompt = computed(() => dialogState.kind === "prompt");

function onOpenChange(value: boolean) {
  // 点遮罩 / 按 Esc / 点右上角关闭都走这里
  if (!value) settleDialog(false);
}

function confirm() {
  settleDialog(true);
}

watch(
  () => dialogState.open,
  async (open) => {
    if (open && isPrompt.value) {
      await nextTick();
      const el = inputEl.value?.$el as HTMLInputElement | undefined;
      el?.focus();
      el?.select();
    }
  },
);
</script>

<template>
  <DialogRoot :open="dialogState.open" @update:open="onOpenChange">
    <DialogPortal>
      <DialogOverlay
        class="data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 fixed inset-0 z-50 bg-black/60 backdrop-blur-[2px]"
      />
      <DialogContent
        class="bg-background data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 fixed top-1/2 left-1/2 z-50 w-[min(92vw,26rem)] -translate-x-1/2 -translate-y-1/2 rounded-xl border p-5 shadow-2xl outline-none"
        @open-auto-focus.prevent
      >
        <DialogTitle class="text-base font-semibold">{{ dialogState.title }}</DialogTitle>
        <DialogDescription v-if="dialogState.message" class="text-muted-foreground mt-1.5 text-sm">
          {{ dialogState.message }}
        </DialogDescription>

        <div v-if="isPrompt" class="mt-3 flex items-center gap-2">
          <Input
            ref="inputEl"
            v-model="dialogInput"
            :placeholder="dialogState.placeholder"
            class="h-9"
            @keyup.enter="confirm"
          />
          <span v-if="dialogState.inputSuffix" class="text-muted-foreground text-sm">
            {{ dialogState.inputSuffix }}
          </span>
        </div>

        <div class="mt-5 flex justify-end gap-2">
          <Button variant="outline" size="sm" @click="settleDialog(false)">
            {{ dialogState.cancelText }}
          </Button>
          <Button
            :variant="dialogState.danger ? 'destructive' : 'default'"
            size="sm"
            @click="confirm"
          >
            {{ dialogState.confirmText }}
          </Button>
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
