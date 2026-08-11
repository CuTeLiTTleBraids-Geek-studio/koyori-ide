// N-128: ESLint flat config. Covers Vue 3 SFCs + TypeScript.
//
// Design notes:
// - Uses `vue3-essential` (catches real bugs like v-for without :key) plus
//   `@typescript-eslint/recommended` (catches type-level bugs). We avoid
//   `vue3-recommended`/`vue3-strongly-recommended` because they enforce
//   style opinions (attribute order, component naming) that would produce
//   a large diff on existing code with limited bug-catching value.
// - Test files (*.test.ts) get `no-console` and `no-explicit-any`
//   relaxed — tests legitimately use both.
// - The `lint` script in package.json runs `eslint src`. CI runs the same.

import js from "@eslint/js";
import tseslint from "typescript-eslint";
import pluginVue from "eslint-plugin-vue";
import globals from "globals";

export default [
  // Global ignores: generated bindings, build output, deps.
  {
    ignores: [
      "dist/**",
      "bindings/**",
      ".bindings-tmp-*/**",
      "node_modules/**",
      "vite.config.ts",
      "vitest.config.ts",
    ],
  },

  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs["flat/essential"],

  // Vue SFCs: parse <script lang="ts"> with the TS parser so type-aware
  // rules work inside Vue files.
  {
    files: ["**/*.vue"],
    languageOptions: {
      parserOptions: {
        parser: tseslint.parser,
        sourceType: "module",
      },
      globals: { ...globals.browser },
    },
  },

  // All TS/JS source files: browser + ES module globals.
  {
    files: ["src/**/*.{ts,js,vue}"],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: "module",
      globals: {
        ...globals.browser,
        ...globals.node,
        // P9-G08: injected by vite define from the repository VERSION file.
        __APP_VERSION__: "readonly",
      },
    },
    rules: {
      // Allow console.* in source — the app intentionally logs to console
      // (it's a desktop app, console output is a useful diagnostic channel).
      "no-console": "off",
      // Prefer const/let over var is already covered; keep unused vars as
      // a warning (not error) to avoid blocking CI on dead code that may
      // be intentional (re-exports, etc.).
      "no-unused-vars": "off",
      "@typescript-eslint/no-unused-vars": ["warn", { argsIgnorePattern: "^_" }],
      // G-QUAL-03 / P1: `any` is now an error. API-compatibility call sites
      // (e.g. the VS Code extension host bridge) that must keep `any` to match
      // the upstream API contract use inline eslint-disable comments.
      "@typescript-eslint/no-explicit-any": "error",
      // G-SEC-11 gate 3: ban v-html to prevent XSS in Vue templates.
      // v-html renders raw HTML, which can execute attacker-controlled
      // scripts if the input is not strictly sanitized.
      "vue/no-v-html": "error",
    },
  },

  // Test files: relax rules that conflict with test patterns.
  {
    files: ["src/**/*.test.ts"],
    rules: {
      "@typescript-eslint/no-explicit-any": "off",
      "@typescript-eslint/no-non-null-assertion": "off",
    },
  },
];
