<script setup lang="ts">
import AppSidebar from "@/components/layout/AppSidebar.vue";
import AppTopbar from "@/components/layout/AppTopbar.vue";
import DialogHost from "@/components/ui/dialog/DialogHost.vue";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
</script>

<template>
  <SidebarProvider class="h-svh overflow-hidden">
    <AppSidebar />
    <SidebarInset class="flex h-svh min-h-0 flex-col overflow-hidden">
      <AppTopbar />
      <div class="min-h-0 flex-1 overflow-hidden">
        <!--
          KeepAlive：左侧菜单切换时**保留页面状态**。

          没有它的话每次切菜单都会卸载组件，已加载的数据、筛选条件、
          图表实例全部丢失，回来要重新加载一遍。

          依赖路由参数的页面（K 线 / 财务数据用 ?code=xxx）不受影响 ——
          它们本来就是 watch(route.query.code) 而不是只在 onMounted 里读，
          所以从别的页面跳过来仍会加载新的股票。
        -->
        <RouterView v-slot="{ Component }">
          <KeepAlive>
            <component :is="Component" class="h-full" />
          </KeepAlive>
        </RouterView>
      </div>
    </SidebarInset>
    <!-- 全局确认/输入弹窗（替代系统 confirm/prompt） -->
    <DialogHost />
  </SidebarProvider>
</template>
