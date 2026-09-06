# Wails 升级验收门（P20 P1-06）

> 本文是 Wails 运行时/CLI 升级（alpha→beta、beta 小步或大步，例如未来的
> 3.0.0-beta.12，原 PR #39 / #42）的**强制验收门清单**。任何绕过本清单的
> 升级提交一律不得合入。清单一份，不重复记录。

## 背景

`release/v0.2.0` 收敛在 **alpha2.111 线**（`go.mod` 的
`github.com/wailsapp/wails/v3` 与 CLI pin、bindings manifest、surface 测试
四者联动）。main 历史上的 beta.5 线从未跑过产品代码的完整测试证据
（P20 P0-02 依赖线决策）。升级 Wails 会同时影响：

- `go.mod` / `go.sum`（运行时模块版本）
- Wails CLI pin（`scripts/check-wails-pin.mjs` 校验 go.mod 版本与 CLI 安装
  声明文件一致）
- 生成的 TypeScript bindings（`frontend/bindings/**`，untracked，由
  `scripts/generate-bindings.mjs` 按 pinned CLI 生成）
- bindings manifest（`scripts/wails-bindings.manifest.json`，生成器模块版本
  绑定）
- bindings 分层策略（`scripts/lib/wails-bindings.mjs` 的 forbidden/required
  面，`scripts/check-bindings.mjs` / `check-bindings-imports.mjs` 强制）
- 运行时 surface 契约（`wails_runtime_surface_test.go` /
  `TestRegisteredWailsRuntimeSurfaceMatchesManifest`）

## 升级验收门（全部满足才可合并）

1. **CLI pin 联动**：bump `go.mod` 的 `wails/v3` 后，同步更新所有 CLI pin
   声明文件（见 `check-wails-pin.mjs` 的 `pinDeclarationFiles`）；运行
   `node scripts/check-wails-pin.mjs` 必须 exit 0。
2. **bindings 重新生成**：用新版本 CLI 重新生成
   `frontend/bindings/**`（`node scripts/generate-bindings.mjs`），禁止手工
   编辑生成文件；`scripts/wails-bindings.manifest.json` 必须重新生成并与新
   pinned 生成器模块一致。
3. **bindings 策略复核**：若新生成的 binding 面触及
   `scripts/lib/wails-bindings.mjs` 的 forbidden/required 策略（例如新暴露
   危险 setter、方法更名），必须先更新策略并确认 deny-only 面未意外恢复；
   `node scripts/check-bindings.mjs` 与
   `node scripts/check-bindings-imports.mjs` 必须 exit 0。
4. **全量回归重跑**（P20 §2.1 命令在新 HEAD 全绿）：
   - `go vet ./services/... ./internal/... .`
   - `go build ./...`
   - `go test ./services -run 'TestGit' -count=1 -p 1`
   - `go test ./services -run '^TestAIServiceNativeToolStreamingRoundTripHTTP$|^TestAIProviderStreamBoundary|TestMCP|TestG03MCP' -count=1 -p 1`
   - `go test . -run TestRegisteredWailsRuntimeSurfaceMatchesManifest -count=1`
   - `cd frontend && npx vitest run` 与 `npx vue-tsc --noEmit`
5. **surface 契约**：`TestRegisteredWailsRuntimeSurfaceMatchesManifest`
   通过即证明注册面与 manifest 一致；任何 `//wails:ignore` 的移除必须重新
   评审（deny-only 面不得静默恢复）。
6. **packaged 证据**：升级后 dispatch 一次 packaged-e2e（Linux qualification）
   并附绿 run URL。contract-smoke 绿不代表 packaged E2E 绿。
7. **提交纪律**：升级在单独 PR 中进行（不与其它改动混揉）；PR 描述记录
   旧→新版本、生成 diff 摘要与上述各项证据。

## 明确禁止

- 禁止只 bump `go.mod` 不动 CLI pin / bindings / manifest 的"半升级"。
- 禁止在升级 PR 中顺手放宽 bindings 分层策略或守卫脚本。
- 禁止以"binding 已生成"替代第 4、6 项的运行证据。
