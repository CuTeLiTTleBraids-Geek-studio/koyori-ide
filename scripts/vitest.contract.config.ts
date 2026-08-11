import vue from "../frontend/node_modules/@vitejs/plugin-vue/dist/index.mjs";
import { defineConfig } from "../frontend/node_modules/vitest/dist/config.js";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(fileURLToPath(new URL("..", import.meta.url)));
const frontend = resolve(root, "frontend");

export default defineConfig({
  root: frontend,
  plugins: [vue()],
  test: {
    environment: "jsdom",
    include: ["../scripts/core-path-smoke.test.ts"],
    pool: "vmThreads",
    maxWorkers: 1,
  },
  resolve: {
    alias: [
      {
        find: /^monaco-editor\/esm\/vs\/.*\.worker(\?.*)?$/,
        replacement: resolve(frontend, "src/test-stubs/monaco-editor.ts"),
      },
      { find: "@", replacement: resolve(frontend, "src") },
      {
        find: "monaco-editor",
        replacement: resolve(frontend, "src/test-stubs/monaco-editor.ts"),
      },
    ],
  },
});
