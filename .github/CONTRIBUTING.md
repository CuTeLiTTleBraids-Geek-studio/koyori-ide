# 贡献指南 / Contributing to koyori-ide

感谢你愿意为 koyori-ide 贡献力量！本文说明开发环境与协作约定。  
Thank you for contributing! This document covers setup and project conventions.

## 反馈问题 / Reporting Issues

**中文：**
- **Bug：** 提交带 `bug` 标签的 Issue，写明系统、版本、复现步骤、期望与实际结果。
- **功能建议：** 使用 `enhancement` 标签，说明场景与方案。
- **安全漏洞：** **不要**公开 Issue，请私下按 [SECURITY.md](SECURITY.md)（本目录）报告。

**English:**
- **Bugs:** open a GitHub issue labeled `bug` (OS, version, repro, expected vs actual).
- **Features:** label `enhancement` with use case and proposal.
- **Security:** do **not** open a public issue — see [SECURITY.md](SECURITY.md) in this folder.

## 开发环境 / Development Setup

### 前置条件 / Prerequisites

- **Go** 1.25+
- **Node.js** 20+（含 npm）
- **Wails3 CLI** `v3.0.0-alpha2.111`（用于 `wails3 dev` / `wails3 build`；不要使用 `@latest`）

### 获取代码 / Clone

```bash
git clone https://github.com/CuTeLiTTleBraids-Geek-studio/koyori-ide.git
cd koyori-ide
```

### 一键初始化 / One-shot setup

```bash
# Unix
bash scripts/dev-setup.sh

# Windows PowerShell
pwsh -File scripts/dev-setup.ps1
```

脚本会：`go mod download`、`npm ci`、跑基础测试，并可选安装 `gopls` / `dlv`。  
The scripts download modules, install frontend deps, run basic tests, and optionally install language tools.

### 安装依赖 / Dependencies

```bash
go mod download
cd frontend && npm ci
```

### 开发运行 / Running in Development

```bash
# 推荐 / recommended
wails3 dev -config ./build/config.yml -port 9245
```

安装与 `go.mod`、CI 一致的 alpha CLI：

```bash
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-alpha2.111
```

或分终端手动启动 / or two terminals:

```bash
cd frontend && npm run dev   # 终端 1
go run .                     # 终端 2
```

### 语言服务 / Language servers（可选）

```bash
go install golang.org/x/tools/gopls@latest
npm i -D typescript-language-server typescript
```

### 测试 / Tests

```bash
node scripts/generate-bindings.mjs
go test ./services/... -v
cd frontend && npm test
cd frontend && npx vue-tsc --noEmit
```

前端 coverage 是实际门禁，四项最低值均为当前基线 50%，不是目标宣传值：

```bash
cd frontend && npm run test:coverage
```

不要为通过门禁而删除测试或扩大 source exclusions；提高阈值前先记录可复现的新基线。发布维护者可用 `bash scripts/release-evidence.sh <artifact...>` 收集 checksum、可选 SBOM 与 `govulncheck` 证据；该脚本不验证签名。

### Wails bindings

修改导出的 Go service 方法或 DTO 后，必须重新生成 TypeScript binding，并运行仓库的漂移检查：

```bash
node scripts/generate-bindings.mjs
node scripts/check-bindings.mjs
```

`frontend/bindings/` 是不跟踪的生成代码，不要手工修补。导出面由 `scripts/wails-bindings.manifest.json` 审核；有意修改 Go 导出后，维护者必须显式运行 `node scripts/update-bindings-manifest.mjs --accept-export-surface`。生产构建也会在前端打包前生成并校验 binding。固定构建输入与发布检查见 [docs/RELEASING.md](../docs/RELEASING.md)。

合并 PR 前测试必须通过。 / All tests must pass before merge.

## 代码风格 / Code Style

### Go
- 遵循 Effective Go，使用 `gofmt` / `goimports`
- 提交前 `go vet .` 无告警
- 服务方法使用指针接收者；导出符号写文档注释
- 边界处处理错误，禁止静默吞掉

### TypeScript / Vue
- 新组件使用 `<script setup lang="ts">`
- 结构：`stores/` / `components/` / `views/` / `composables/` / `lib/`
- 优先 Composition API；有对应组件时用 Element Plus
- 提交前 `npx vue-tsc --noEmit` 无错误

### CSS
- BEM 命名；颜色用 `var(--color-...)`；`<style scoped>`

## 提交信息 / Commit Messages

遵循 [Conventional Commits](https://www.conventionalcommits.org/)：

```
<type>(<scope>): <description>
```

**类型 / Types：** `feat` · `fix` · `docs` · `style` · `refactor` · `test` · `chore` · `ci`

**示例 / Examples：**
```
feat(ai): add conversation history sidebar
fix(terminal): clear output buffer after read
docs: update README with AI configuration guide
```

## Pull Request 流程 / PR Process

1. Fork 并从 `main` 建分支：`git checkout -b feat/my-feature`
2. 单次提交保持单一逻辑变更
3. 补充/更新测试
4. 跑通 Go + 前端测试
5. 更新 `docs/CHANGELOG.md` 的 Unreleased（若面向用户）
6. 开 PR，关联 Issue（如 `Closes #123`）
7. 根据 Review 修改

### 检查清单 / Checklist

- [ ] `node scripts/generate-bindings.mjs` 后，`go test ./services/...` 与 `npm test` 通过
- [ ] `go vet .` 干净
- [ ] `npx vue-tsc --noEmit` 干净
- [ ] `node scripts/check-bindings.mjs` 通过；导出 Go API 有变化时已审查并显式接受 manifest
- [ ] `node scripts/check-doc-numbers.mjs` 通过
- [ ] `node scripts/check-doc-links.mjs` 通过
- [ ] 用户可见变更已写 `docs/CHANGELOG.md`
- [ ] Conventional Commits
- [ ] 无新增 lint 警告

## VSIX 扩展作者须知 / VSIX Extension Authoring

- 发布格式是标准 VSIX：根内包含 `extension/package.json`、`engines.vscode`（或等价兼容声明）、`contributes` 和 `browser`/`main` 入口。固定 SHA 的安装语料必须同时记录版本、入口与 SHA-256。
- `koyoriIde.permissions` 是可选元数据，不是安装硬阻。缺失时宿主从 `activationEvents`、`contributes` 和入口中静态出现的 `vscode.*` 引用推导权限；shell、网络、未知来源或推导失败按 Restricted，并默认保持禁用。
- 扩展应只依赖兼容矩阵中的 API。未知命名空间或 Monaco 缺少对应 provider 方法时，宿主以 `KOYORI_IDE_EXT_API_UNSUPPORTED` fail-closed，不提供空实现冒充成功。
- `window.showInputBox` / `showQuickPick` 使用真实宿主 UI；取消返回 `undefined`，不能依赖默认值或第一项。写入、shell、网络、debug、tasks 等能力仍受权限与审批约束。
- 提交扩展兼容改动时，至少运行 `frontend` 的 `vue-tsc`、ExtensionHost/VSIX real-Worker 测试，并更新 [docs/EXTENSION-COMPATIBILITY.md](../docs/EXTENSION-COMPATIBILITY.md) 的证据等级。

## 项目结构 / Project Structure

```
koyori-ide/
├── main.go
├── services/
├── frontend/
├── build/
└── scripts/
```

## 许可证 / License

贡献内容默认以 **MIT** 协议授权。  
By contributing, you agree your contributions are licensed under the MIT License.
