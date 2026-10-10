import { fileURLToPath, URL } from "node:url";
import vue from "@vitejs/plugin-vue";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  test: {
    environment: "happy-dom",
    globals: true,
    include: ["tests/**/*.spec.ts"],
    css: false,
    // Node 25 内置的 localStorage 占位会盖掉 happy-dom 的实现，
    // 使 `localStorage` 变成没有方法的空对象。见 vitest.setup.ts。
    setupFiles: ["./vitest.setup.ts"],
  },
});
