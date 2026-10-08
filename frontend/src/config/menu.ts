import type { Component } from "vue";
import {
  Activity,
  BarChart3,
  ChartLine,
  Database,
  Funnel,
  LayoutGrid,
  List,
} from "@lucide/vue";

export interface MenuItem {
  title: string;
  to: string;
  icon: Component;
  /** 尚未实现的入口显示这个标记 */
  badge?: string;
}

export interface MenuGroup {
  label: string;
  items: MenuItem[];
}

/**
 * 左侧导航菜单。
 * 新功能只需在这里加一项 + 在 src/router/index.ts 注册路由即可。
 */
export const menuGroups: MenuGroup[] = [
  {
    label: "行情数据",
    items: [
      { title: "股票列表", to: "/stocks", icon: List },
      { title: "财务数据", to: "/finance", icon: BarChart3 },
      { title: "板块行情", to: "/boards", icon: LayoutGrid },
    ],
  },
  {
    label: "技术分析",
    items: [
      { title: "K 线图表", to: "/kline", icon: ChartLine },
      { title: "指标计算", to: "/indicators", icon: Activity, badge: "规划中" },
      { title: "条件选股", to: "/screener", icon: Funnel },
    ],
  },
  {
    label: "系统",
    items: [{ title: "数据源设置", to: "/settings", icon: Database }],
  },
];
