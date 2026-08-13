import { readFileSync } from "fs";
import { defineConfig } from "vitest/config";
import vue from "@vitejs/plugin-vue";
import { resolve } from "path";

// P9-G08: keep the renderer-visible version constant aligned with the
// repository VERSION file under vitest too (vite.config.ts does this for
// production builds).
const appVersion = readFileSync(resolve(__dirname, "..", "VERSION"), "utf8").trim();

export default defineConfig({
  plugins: [vue()],
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
    __KOYORI_IDE_E2E_WORKSPACE__: JSON.stringify("0"),
    __KOYORI_IDE_E2E_RUNTIME_ROLE__: JSON.stringify("0"),
    __KOYORI_IDE_E2E_MONACO__: JSON.stringify("0"),
    __KOYORI_IDE_E2E_HTTP_CLIENT__: JSON.stringify("0"),
    __KOYORI_IDE_E2E_RECOVERY__: JSON.stringify("0"),
  },
  test: {
    environment: "jsdom",
    globals: true,
    // G-CI-15: the threads pool occasionally tears down while a worker console
    // message is still in flight on busy runners ("Closing rpc while
    // onUserConsoleLog was pending"), failing green runs. forks is the
    // recommended stable pool for this scenario.
    pool: "forks",
    // G-CI-11: clear timers registered by @wailsio/runtime module side
    // effects (drag polling) so nothing fires after jsdom teardown.
    setupFiles: ["src/test-setup.ts"],
    // N-130: coverage configuration. Run with `npm run test:coverage`.
    // Reports go to frontend/coverage/. v8 provider requires no extra deps.
    coverage: {
      provider: "v8",
      reporter: ["text", "html", "lcov"],
      reportsDirectory: "coverage",
      // Exclude non-source files from coverage to keep reports focused.
      exclude: [
        "node_modules/**",
        "dist/**",
        "bindings/**",
        ".bindings-tmp-*/**",
        "src/**/*.test.ts",
        "src/**/*.spec.ts",
        "src/**/*.d.ts",
        "src/main.ts",
        "src/vite-env.d.ts",
        "vite.config.ts",
        "vitest.config.ts",
        "eslint.config.js",
      ],
      // Thresholds enforced in CI to prevent coverage regression. Current
      // baseline reflects the existing codebase; raise as tests improve.
      thresholds: {
        statements: 50,
        branches: 50,
        functions: 50,
        lines: 50,
      },
    },
  },
  resolve: {
    alias: [
      // L-6: main.ts imports monaco-editor web workers via Vite's ?worker
      // suffix (e.g. "monaco-editor/esm/vs/editor/editor.worker?worker").
      // The base "monaco-editor" string alias below only matches the exact
      // specifier "monaco-editor", not subpaths — so without this regex
      // alias, main.test.ts fails with "Failed to resolve import ... ?worker".
      // We redirect all worker subpaths to the same stub; the ?worker query
      // is consumed by the regex so Vite does not try to bundle a real worker.
      {
        find: /^monaco-editor\/esm\/vs\/.*\.worker(\?.*)?$/,
        replacement: resolve(__dirname, "src/test-stubs/monaco-editor.ts"),
      },
      { find: "@", replacement: resolve(__dirname, "src") },
      // prompt-5 Task D / BUG-M3: monaco-editor cannot resolve fully under
      // vitest/jsdom; stub it so suites that transitively import
      // monaco-themes (e.g. ExtensionPermissionDialog via app store) load.
      { find: "monaco-editor", replacement: resolve(__dirname, "src/test-stubs/monaco-editor.ts") },
    ],
  },
});
