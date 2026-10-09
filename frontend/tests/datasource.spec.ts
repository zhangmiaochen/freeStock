import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { KeepAlive, defineComponent, nextTick } from "vue";

// 数据源设置页：**切换按钮永远能再次点击**。
//
// 背景（用户报的问题）：
//  1. 切换成功后按钮要能再点
//  2. 切到别的页面再切回来，按钮也要恢复可点
//
// 原因是 `useSource` 把 busy 一直占着到"全市场 7000+ 只列表加载完"才释放，
// 而且 DataSourceView 在 KeepAlive 里，切走再回来不会重新 mount，
// busy 会一直停在 "switch"。
const mocks = vi.hoisted(() => ({
  getDataSources: vi.fn(),
  getCurrentDataSource: vi.fn(),
  testDataSource: vi.fn(),
  setDataSource: vi.fn(),
  clearCache: vi.fn(),
  getConfigPath: vi.fn(),
  loadUniverse: vi.fn(),
  loadCategories: vi.fn(),
}));

vi.mock("@/lib/backend", () => ({
  api: {
    getDataSources: mocks.getDataSources,
    getCurrentDataSource: mocks.getCurrentDataSource,
    testDataSource: mocks.testDataSource,
    setDataSource: mocks.setDataSource,
    clearCache: mocks.clearCache,
    getConfigPath: mocks.getConfigPath,
    errorMessage: undefined,
  },
  errorMessage: (err: unknown) => String(err),
}));

vi.mock("@/stores/stock", () => ({
  useStockStore: () => ({
    universe: null,
    loadUniverse: mocks.loadUniverse,
    loadCategories: mocks.loadCategories,
  }),
}));

import DataSourceView from "@/views/DataSourceView.vue";

const SOURCES = [
  {
    key: "local", name: "本地数据（本机 stockdb）", url: "http://127.0.0.1:7899",
    kind: "stockdb", current: true, available: true,
  },
  {
    key: "remote", name: "远程数据（公共测试节点）", url: "http://8.138.149.215:7899",
    kind: "stockdb", current: false, available: true,
  },
  {
    key: "cnlake", name: "本地数据湖（cnequity）", url: "D:/lake",
    kind: "cnlake", current: false, available: true,
  },
];

// 模拟后端：切到谁，谁就是 current
function mockBackend() {
  let currentKey = "local";
  const list = () =>
    SOURCES.map((s) => ({ ...s, current: s.key === currentKey }));
  mocks.getDataSources.mockImplementation(async () => list());
  mocks.getCurrentDataSource.mockImplementation(async () => {
    const found = list().find((s) => s.current)!;
    return { ...found, current: true };
  });
  mocks.setDataSource.mockImplementation(async (kind: string, url: string) => {
    const target = SOURCES.find((s) => s.kind === kind && s.url === url);
    if (!target) throw new Error(`未知数据源 ${kind} ${url}`);
    currentKey = target.key;
    return { ...target, ok: true, message: "已连接", total: 7000, elapsed: "1s" };
  });
}

function switchButtons(wrapper: ReturnType<typeof mount>) {
  // 「切换」/「重新应用」两个文案都算切换按钮
  return wrapper.findAll("button").filter((b) => {
    const text = b.text();
    return text.includes("切换") || text.includes("重新应用");
  });
}

async function mountPage() {
  if (!mocks.getDataSources.getMockImplementation()) mockBackend();
  mocks.getConfigPath.mockResolvedValue("D:/FreeStock/config.json");
  const wrapper = mount(DataSourceView);
  // 等首屏加载完
  await vi.waitFor(() => expect(mocks.getDataSources).toHaveBeenCalled());
  await nextTick();
  await nextTick();
  return { wrapper };
}

describe("数据源设置页", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.loadUniverse.mockResolvedValue(undefined);
    mocks.loadCategories.mockResolvedValue(undefined);
  });

  it("切换成功后按钮立即恢复可点击（不等全市场列表加载完）", async () => {
    // 让"加载全市场列表"永远挂住 —— 模拟真实里 7000+ 只的慢
    let releaseUniverse: () => void = () => {};
    mocks.loadUniverse.mockReturnValue(
      new Promise<void>((resolve) => {
        releaseUniverse = resolve;
      }),
    );
    mocks.setDataSource.mockResolvedValue({
      ...SOURCES[1], ok: true, message: "已连接", total: 7000, elapsed: "1s",
    });

    const { wrapper } = await mountPage();
    const buttons = switchButtons(wrapper);
    expect(buttons.length).toBeGreaterThan(0);

    await buttons[0]!.trigger("click");
    await vi.waitFor(() => expect(mocks.setDataSource).toHaveBeenCalled());

    // ★ 关键断言：列表**还没加载完**，按钮就必须已经能点
    await vi.waitFor(() => {
      const stillBusy = switchButtons(wrapper).some((b) =>
        b.attributes("disabled") !== undefined,
      );
      expect(stillBusy).toBe(false);
    });

    // 收尾，避免挂起的 promise 影响后续用例
    releaseUniverse();
    await nextTick();
  });

  it("切到数据湖后，再切回本机 stockdb 要生效", async () => {
    mockBackend();
    const { wrapper } = await mountPage();

    // ① 切到数据湖
    const lakeRow = wrapper
      .findAll("div.bg-card")
      .find((d) => d.text().includes("本地数据湖"))!;
    expect(lakeRow).toBeTruthy();
    const lakeBtn = lakeRow
      .findAll("button")
      .find((b) => b.text().includes("切换"))!;
    await lakeBtn.trigger("click");
    await vi.waitFor(() =>
      expect(mocks.setDataSource).toHaveBeenCalledWith("cnlake", "D:/lake"),
    );
    await vi.waitFor(() => {
      const row = wrapper
        .findAll("div.bg-card")
        .find((d) => d.text().includes("本地数据湖"))!;
      expect(row.text()).toContain("使用中");
    });

    // ② 再切回本机 stockdb —— 这一步"不生效"是用户报的问题
    const localRow = wrapper
      .findAll("div.bg-card")
      .find((d) => d.text().includes("本机 stockdb"))!;
    expect(localRow).toBeTruthy();
    const localBtn = localRow
      .findAll("button")
      .find((b) => b.text().includes("切换") || b.text().includes("重新应用"))!;
    await localBtn.trigger("click");
    await vi.waitFor(() =>
      expect(mocks.setDataSource).toHaveBeenCalledWith("stockdb", "http://127.0.0.1:7899"),
    );

    // ★ 界面必须把「本机 stockdb」标成使用中，且数据湖不再是
    await vi.waitFor(() => {
      const local = wrapper
        .findAll("div.bg-card")
        .find((d) => d.text().includes("本机 stockdb"))!;
      expect(local.text()).toContain("使用中");
    });
    const lakeAfter = wrapper
      .findAll("div.bg-card")
      .find((d) => d.text().includes("本地数据湖"))!;
    expect(lakeAfter.text()).not.toContain("使用中");
  });

  it("切换失败后按钮也能恢复可点击", async () => {
    mocks.setDataSource.mockRejectedValue(new Error("数据湖缺少 daily_bars"));
    const { wrapper } = await mountPage();

    await switchButtons(wrapper)[0]!.trigger("click");
    await vi.waitFor(() => expect(mocks.setDataSource).toHaveBeenCalled());

    await vi.waitFor(() => {
      const stillBusy = switchButtons(wrapper).some((b) =>
        b.attributes("disabled") !== undefined,
      );
      expect(stillBusy).toBe(false);
    });
  });

  it("快速来回切时，列表加载必须串行（后一次的慢请求不能覆盖前一次）", async () => {
    mockBackend();
    const { wrapper } = await mountPage();

    // 第一次加载（切到数据湖）故意拖慢；第二次（切回 stockdb）很快
    const order: string[] = [];
    let releaseSlow: () => void = () => {};
    mocks.loadUniverse
      .mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            releaseSlow = () => {
              order.push("湖");
              resolve();
            };
          }),
      )
      .mockImplementationOnce(async () => {
        order.push("本机");
      });

    // ① 切到数据湖（触发慢加载）
    const lakeRow = wrapper
      .findAll("div.bg-card")
      .find((d) => d.text().includes("本地数据湖"))!;
    await lakeRow
      .findAll("button")
      .find((b) => b.text().includes("切换"))!
      .trigger("click");
    await vi.waitFor(() => expect(mocks.loadUniverse).toHaveBeenCalledTimes(1));

    // ② 还没加载完就切回本机 stockdb
    const localRow = wrapper
      .findAll("div.bg-card")
      .find((d) => d.text().includes("本机 stockdb"))!;
    await localRow
      .findAll("button")
      .find((b) => b.text().includes("切换") || b.text().includes("重新应用"))!
      .trigger("click");
    await vi.waitFor(() => expect(mocks.setDataSource).toHaveBeenCalledTimes(2));

    // 放行慢加载
    releaseSlow();
    await vi.waitFor(() => expect(mocks.loadUniverse).toHaveBeenCalledTimes(2));

    // ★ 串行化 → 顺序必须是「先湖、后本机」，
    // 这样 store 里最终留下的是本机 stockdb 的数据
    await vi.waitFor(() => expect(order.length).toBe(2));
    expect(order).toEqual(["湖", "本机"]);
  });

  it("切走再切回来，按钮恢复可点击（真 KeepAlive）", async () => {
    // 用真的 <KeepAlive> 包一层，靠 v-if 切换触发 deactivate/activate ——
    // 这样才真的走到组件的 onActivated（上一版我伪造 $emit("hook:activated")，
    // 那个断言等于白测，已改正）
    const Host = defineComponent({
      components: { DataSourceView, KeepAlive },
      props: { show: { type: Boolean, default: true } },
      template: `<KeepAlive><DataSourceView v-if="show" /></KeepAlive>`,
    });

    // 让"加载全市场列表"挂住，制造"切走时还在忙"的场景
    let releaseUniverse: () => void = () => {};
    mocks.loadUniverse.mockReturnValue(
      new Promise<void>((resolve) => {
        releaseUniverse = resolve;
      }),
    );
    mocks.setDataSource.mockResolvedValue({
      ...SOURCES[1], ok: true, message: "已连接", total: 7000, elapsed: "1s",
    });

    const wrapper = mount(Host, { props: { show: true } });
    await vi.waitFor(() => expect(mocks.getDataSources).toHaveBeenCalled());
    await nextTick();

    const buttons = switchButtons(wrapper);
    expect(buttons.length).toBeGreaterThan(0);
    await buttons[0]!.trigger("click");
    await vi.waitFor(() => expect(mocks.setDataSource).toHaveBeenCalled());

    // 切走（deactivate）再切回来（activate）
    await wrapper.setProps({ show: false });
    await nextTick();
    await wrapper.setProps({ show: true });
    await nextTick();
    // 回到页面应当**重新拉一次**数据源列表（onActivated 生效）
    await vi.waitFor(() =>
      expect(mocks.getDataSources.mock.calls.length).toBeGreaterThan(1),
    );

    // ★ 回到页面后，按钮必须能点
    await vi.waitFor(() => {
      const stillBusy = switchButtons(wrapper).some((b) =>
        b.attributes("disabled") !== undefined,
      );
      expect(stillBusy).toBe(false);
    });

    releaseUniverse();
    await nextTick();
  });
});
