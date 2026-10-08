import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";
import wails from "@wailsio/runtime/plugins/vite";

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue(), tailwindcss(), wails("./bindings")],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    // Wails 使用系统 WebView2 / WebKit，可以放心用较新的目标
    target: "chrome110",
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
});
