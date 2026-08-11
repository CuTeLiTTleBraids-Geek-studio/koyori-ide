# Vue 3 + TypeScript + Vite（前端）

## 中文

本目录为 **koyori-ide** 的前端工程：Vue 3 + TypeScript + Vite，使用 `<script setup>` 单文件组件。

### 推荐 IDE
- [VS Code](https://code.visualstudio.com/) + [Volar](https://marketplace.visualstudio.com/items?itemName=Vue.volar)（请禁用 Vetur）
- 可选 [TypeScript Vue Plugin (Volar)](https://marketplace.visualstudio.com/items?itemName=Vue.vscode-typescript-vue-plugin)

### `.vue` 在 TypeScript 中的类型支持
TypeScript 默认无法处理 `.vue` 的类型信息，类型检查请使用 `vue-tsc`（而非 `tsc`）。编辑器侧需要 Volar / TS Vue 插件，才能让 TS 语言服务识别 `.vue` 类型。

若独立 TS 插件偏慢，可启用 Volar 的 [Take Over Mode](https://github.com/johnsoncodehk/volar/discussions/471#discussioncomment-1361669)：
1. 命令面板 → `Extensions: Show Built-in Extensions`
2. 找到 **TypeScript and JavaScript Language Features** → 右键 → **Disable (Workspace)**
3. 命令面板 → `Developer: Reload Window`

### 常用命令
```bash
npm ci
npm run bindings:generate
npm run dev
npm run build
npm test
npx vue-tsc --noEmit
```

---

## English

This is the **koyori-ide** frontend: Vue 3 + TypeScript + Vite with `<script setup>` SFCs.

### Recommended IDE
- [VS Code](https://code.visualstudio.com/) + [Volar](https://marketplace.visualstudio.com/items?itemName=Vue.volar) (disable Vetur)
- Optional [TypeScript Vue Plugin (Volar)](https://marketplace.visualstudio.com/items?itemName=Vue.vscode-typescript-vue-plugin)

### Type Support for `.vue` Imports
TypeScript cannot type-check `.vue` imports by default; use `vue-tsc` instead of `tsc`. In the editor, Volar (or the TS Vue plugin) makes the language service aware of `.vue` types.

For better performance, enable Volar [Take Over Mode](https://github.com/johnsoncodehk/volar/discussions/471#discussioncomment-1361669): disable the built-in TypeScript extension for the workspace, then reload the window.

### Scripts
```bash
npm ci
npm run bindings:generate
npm run dev
npm run build
npm test
npx vue-tsc --noEmit
```
