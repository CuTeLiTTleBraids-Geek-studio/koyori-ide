import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import wails from "@wailsio/runtime/plugins/vite";
import tailwindcss from "@tailwindcss/vite";
import { readFileSync } from "fs";
import { resolve } from "path";

// P9-G08: VERSION is the single source of truth. The build injects it into
// every renderer as __APP_VERSION__ so Welcome/About always shows the same
// version that ships in the packaged metadata, regardless of Go build-info
// availability.
const appVersion = readFileSync(resolve(__dirname, "..", "VERSION"), "utf8").trim();

// https://vitejs.dev/config/
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  resolve: {
    alias: {
      "@": resolve(__dirname, "src"),
    },
  },
  plugins: [vue(), tailwindcss(), wails("./bindings")],
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
    // P9-G10: Vite 8 no longer injects process.env VITE_* into
    // import.meta.env, so the packaged e2e probe opt-ins are exposed here
    // explicitly. Values are "1" during e2e builds and "0" otherwise.
    __KOYORI_IDE_E2E_WORKSPACE__: JSON.stringify(process.env.VITE_KOYORI_IDE_E2E_WORKSPACE ?? "0"),
    __KOYORI_IDE_E2E_RUNTIME_ROLE__: JSON.stringify(process.env.VITE_KOYORI_IDE_E2E_RUNTIME_ROLE ?? "0"),
    __KOYORI_IDE_E2E_MONACO__: JSON.stringify(process.env.VITE_KOYORI_IDE_E2E_MONACO ?? "0"),
    __KOYORI_IDE_E2E_HTTP_CLIENT__: JSON.stringify(process.env.VITE_KOYORI_IDE_E2E_HTTP_CLIENT ?? "0"),
    __KOYORI_IDE_E2E_RECOVERY__: JSON.stringify(process.env.VITE_KOYORI_IDE_E2E_RECOVERY ?? "0"),
  },
  build: {
    rollupOptions: {
      output: {
        // N-145: Split large vendor dependencies into separate chunks
        // to improve caching and reduce the main bundle size.
        // Vite 8 (Rolldown) requires manualChunks as a function.
        manualChunks(id: string): string | undefined {
          if (id.includes("node_modules")) {
            if (id.includes("monaco-editor") || id.includes("@guolao/vue-monaco-editor")) {
              return "vendor-monaco";
            }
            if (id.includes("element-plus") || id.includes("@element-plus/icons-vue")) {
              return "vendor-element";
            }
            if (id.includes("@xterm")) {
              return "vendor-terminal";
            }
            if (id.includes("marked") || id.includes("dompurify") || id.includes("highlight.js")) {
              return "vendor-markdown";
            }
            if (id.includes("vue-router") || /[\\/]node_modules[\\/]vue[\\/]/.test(id)) {
              return "vendor-vue";
            }
          }
          return undefined;
        },
      },
    },
  },
});
