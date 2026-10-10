// 测试环境专用的 localStorage 补齐。
//
// 背景：Node 22+ 内置了一个实验性的 `localStorage`，需要 `--localstorage-file`
// 指向有效路径才可用；在 **Node 25** 上它会先于 happy-dom 装到全局，
// 于是 `globalThis.localStorage` / `window.localStorage` 变成一个
// **没有任何方法的空对象**：
//
//     typeof localStorage        === "object"
//     localStorage               === {}
//     localStorage.clear        === undefined   ← 测试里报的就是这个
//
// 并且 Node 会打印 `--localstorage-file was provided without a valid path`。
// 这不是项目代码的问题，也不能靠改业务代码绕开（业务代码本就该用标准 Storage）。
// 所以在**测试环境**里补一个标准的内存实现，只影响 vitest，不进产物。

class MemoryStorage implements Storage {
  private store = new Map<string, string>();

  get length(): number {
    return this.store.size;
  }

  clear(): void {
    this.store.clear();
  }

  getItem(key: string): string | null {
    const value = this.store.get(String(key));
    return value === undefined ? null : value;
  }

  key(index: number): string | null {
    const keys = Array.from(this.store.keys());
    return index >= 0 && index < keys.length ? keys[index] : null;
  }

  removeItem(key: string): void {
    this.store.delete(String(key));
  }

  setItem(key: string, value: string): void {
    this.store.set(String(key), String(value));
  }
}

function isUsableStorage(candidate: unknown): boolean {
  if (!candidate || typeof candidate !== "object") {
    return false;
  }
  const storage = candidate as Partial<Storage>;
  return (
    typeof storage.clear === "function" &&
    typeof storage.getItem === "function" &&
    typeof storage.setItem === "function" &&
    typeof storage.removeItem === "function"
  );
}

// 只有确实不可用时才覆盖 —— 将来 happy-dom/Node 修好了，这里自动让位，
// 不至于把它们的真实实现换掉。
if (!isUsableStorage(globalThis.localStorage)) {
  const storage = new MemoryStorage();
  const targets: unknown[] = [globalThis];
  const win = (globalThis as unknown as { window?: unknown }).window;
  if (win) {
    targets.push(win);
  }
  for (const target of targets) {
    try {
      Object.defineProperty(target as object, "localStorage", {
        value: storage,
        configurable: true,
        writable: true,
      });
    } catch {
      // 定义失败也不该让整个测试套件挂掉：业务代码会自行处理没有 localStorage 的情况
    }
  }
}

export {};
