<script setup lang="ts">
import { computed } from "vue";
import { useRoute } from "vue-router";

import { SidebarTrigger } from "@/components/ui/sidebar";
import { Separator } from "@/components/ui/separator";

const route = useRoute();

const title = computed(() => (route.meta.title as string | undefined) ?? "FreeStock");
const subtitle = computed(() => (route.meta.subtitle as string | undefined) ?? "");
</script>

<template>
  <header
    class="bg-background/80 supports-[backdrop-filter]:bg-background/60 sticky top-0 z-10 flex h-14 shrink-0 items-center gap-3 border-b px-4 backdrop-blur"
  >
    <SidebarTrigger class="-ml-1" />
    <Separator orientation="vertical" class="h-5" />
    <div class="flex min-w-0 flex-col leading-tight">
      <h1 class="truncate text-sm font-semibold">{{ title }}</h1>
      <p v-if="subtitle" class="text-muted-foreground truncate text-xs">{{ subtitle }}</p>
    </div>
    <div class="ml-auto flex items-center gap-2">
      <slot name="actions" />
    </div>
  </header>
</template>
