<script setup lang="ts">
import { computed } from "vue";
import { useRoute } from "vue-router";
import { Hammer } from "@lucide/vue";

import { Badge } from "@/components/ui/badge";

const route = useRoute();
const title = computed(() => (route.meta.title as string | undefined) ?? "功能");
</script>

<template>
  <div class="flex h-full flex-col items-center justify-center gap-3 px-6 text-center">
    <div class="bg-muted text-muted-foreground flex size-12 items-center justify-center rounded-xl">
      <Hammer class="size-5" />
    </div>
    <div class="flex items-center gap-2">
      <h2 class="text-base font-semibold">{{ title }}</h2>
      <Badge variant="outline">规划中</Badge>
    </div>
    <p class="text-muted-foreground max-w-md text-sm">
      这个功能还没有实现。左侧菜单与内容区的结构已经搭好，后续按同样的方式新增页面即可。
    </p>
  </div>
</template>
