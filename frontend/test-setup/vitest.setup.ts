import { vi } from "vitest";

// 见 ./wails-runtime-stub.ts 头部说明：默认桩阻断
// @wailsio/runtime → drag.js 的 window 轮询泄漏。测试文件内的局部
// vi.mock("@wailsio/runtime", ...) 优先级高于此处的默认注册。
// 本文件位于 frontend/test-setup/（而非 src/），避免被
// scripts/check-bindings.mjs 的 renderer 绑定审计扫描——桩对运行时的
// 引用只是 vi.mock 字符串，并非真实的 binding 旁路。
vi.mock("@wailsio/runtime", async () => import("./wails-runtime-stub"));
