import { createRouter, createWebHashHistory } from "vue-router";

export const routes = [
  {
    path: "/",
    redirect: "/stocks",
  },
  {
    path: "/stocks",
    name: "stocks",
    component: () => import("@/views/StockListView.vue"),
    meta: { title: "股票列表", subtitle: "按市场与行业浏览全部 A 股 / 基金" },
  },
  {
    path: "/boards",
    name: "boards",
    component: () => import("@/views/BoardsView.vue"),
    meta: { title: "板块行情", subtitle: "申万行业与概念板块的涨跌、热度与成分股" },
  },
  {
    path: "/temperature",
    name: "temperature",
    component: () => import("@/views/MarketTemperatureView.vue"),
    meta: { title: "市场温度", subtitle: "8 大指数多周期走势与量能合成的市场热度读数" },
  },
  {
    path: "/kline",
    name: "kline",
    component: () => import("@/views/KlineView.vue"),
    meta: { title: "K 线图表", subtitle: "日 / 周 / 月 / 年 K 线、均线与指标" },
  },
  {
    path: "/finance",
    name: "finance",
    component: () => import("@/views/FinanceView.vue"),
    meta: { title: "财务数据", subtitle: "财报科目、股本结构、股东户数与十大股东" },
  },
  {
    path: "/valuation",
    name: "valuation",
    component: () => import("@/views/ValuationView.vue"),
    meta: {
      title: "多期限性价比",
      subtitle: "1/3/6/12/36 个月独立评估：估值四层、成长质量、安全边际与风险约束",
    },
  },
  {
    path: "/indicators",
    name: "indicators",
    component: () => import("@/views/ComingSoonView.vue"),
    meta: { title: "指标计算", subtitle: "MA / MACD / KDJ 等 39 种指标" },
  },
  {
    path: "/screener",
    name: "screener",
    component: () => import("@/views/ScreenerView.vue"),
    meta: { title: "条件选股", subtitle: "基础面 + 行业/概念板块 + 技术形态组合筛选" },
  },
  {
    path: "/portfolio",
    name: "portfolio",
    component: () => import("@/views/PortfolioView.vue"),
    meta: { title: "组合管理", subtitle: "组合、持仓与浮动盈亏" },
  },
  {
    path: "/backtest",
    name: "backtest",
    component: () => import("@/views/BacktestView.vue"),
    meta: { title: "组合回测", subtitle: "用历史评分与日线复盘策略表现" },
  },
  {
    path: "/settings",
    name: "settings",
    component: () => import("@/views/DataSourceView.vue"),
    meta: { title: "数据源设置", subtitle: "切换本地 / 远程 free-stockdb 服务" },
  },
  {
    path: "/:pathMatch(.*)*",
    name: "not-found",
    redirect: "/stocks",
  },
];

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
});
