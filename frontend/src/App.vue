<script setup lang="ts">
import AppSidebar from "@/components/layout/AppSidebar.vue";
import AppTopbar from "@/components/layout/AppTopbar.vue";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
</script>

<template>
  <SidebarProvider class="h-svh overflow-hidden">
    <AppSidebar />
    <SidebarInset class="flex h-svh min-h-0 flex-col overflow-hidden">
      <AppTopbar />
      <div class="min-h-0 flex-1 overflow-hidden">
        <RouterView v-slot="{ Component }">
          <component :is="Component" class="h-full" />
        </RouterView>
      </div>
    </SidebarInset>
  </SidebarProvider>
</template>
