<script setup lang="ts">
import { computed } from "vue";
import { RouterLink, useRoute } from "vue-router";
import { ChartCandlestick, CircleDot } from "@lucide/vue";

import { menuGroups } from "@/config/menu";
import { useStockStore } from "@/stores/stock";
import { Badge } from "@/components/ui/badge";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  SidebarSeparator,
} from "@/components/ui/sidebar";

const route = useRoute();
const store = useStockStore();

const activePath = computed(() => route.path);

const sourceLabel = computed(() => store.sourceName || "未连接");

const sourceBadgeVariant = computed(() =>
  store.error ? "destructive" : store.universe ? "secondary" : "outline",
);

function isActive(to: string) {
  return activePath.value === to;
}
</script>

<template>
  <Sidebar collapsible="icon">
    <SidebarHeader>
      <div class="flex items-center gap-2 px-2 py-1.5">
        <div
          class="bg-primary text-primary-foreground flex size-8 shrink-0 items-center justify-center rounded-lg"
        >
          <ChartCandlestick class="size-4" />
        </div>
        <div class="grid min-w-0 flex-1 leading-tight group-data-[collapsible=icon]:hidden">
          <span class="truncate text-sm font-semibold">FreeStock</span>
          <span class="text-muted-foreground truncate text-xs">A 股数据工具</span>
        </div>
      </div>
    </SidebarHeader>

    <SidebarSeparator />

    <SidebarContent>
      <SidebarGroup v-for="group in menuGroups" :key="group.label">
        <SidebarGroupLabel>{{ group.label }}</SidebarGroupLabel>
        <SidebarMenu>
          <SidebarMenuItem v-for="item in group.items" :key="item.to">
            <SidebarMenuButton
              as-child
              :is-active="isActive(item.to)"
              :tooltip="item.title"
            >
              <RouterLink :to="item.to">
                <component :is="item.icon" />
                <span>{{ item.title }}</span>
              </RouterLink>
            </SidebarMenuButton>
            <SidebarMenuBadge v-if="item.badge">{{ item.badge }}</SidebarMenuBadge>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarGroup>
    </SidebarContent>

    <SidebarFooter>
      <div class="flex flex-col gap-1 px-2 py-1.5 group-data-[collapsible=icon]:hidden">
        <div class="flex items-center gap-2">
          <CircleDot
            class="size-3 shrink-0"
            :class="store.error ? 'text-destructive' : 'text-up'"
          />
          <span class="text-muted-foreground truncate text-xs">{{ sourceLabel }}</span>
        </div>
        <Badge
          v-if="store.universe"
          :variant="sourceBadgeVariant"
          class="w-fit max-w-full truncate font-mono text-[10px]"
        >
          {{ store.allStocks.length }} 只标的
        </Badge>
      </div>
    </SidebarFooter>

    <SidebarRail />
  </Sidebar>
</template>
