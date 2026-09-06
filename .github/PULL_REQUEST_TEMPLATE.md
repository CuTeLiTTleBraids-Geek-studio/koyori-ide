## 变更摘要 / Summary

<!-- 用一两句话说明这个 PR 做什么 / One or two sentences on what this PR does -->

## 关联 Issue / Related issues

<!-- Closes #123 -->

## 变更类型 / Type of change

- [ ] Bug 修复 / Bug fix
- [ ] 新功能 / Feature
- [ ] 重构 / Refactor
- [ ] 文档 / Documentation
- [ ] 构建 / 发布 / Build & release
- [ ] 其他 / Other

## 测试 / Testing

- [ ] `node scripts/backend-gate.mjs` 通过（或在下方说明等价的定向命令）
- [ ] `npm.cmd ci --ignore-scripts --registry=https://registry.npmjs.org` 与 `node scripts/npm-audit-gate.mjs` 通过（依赖变更）
- [ ] `task bindings:check` 通过（绑定变更）
- [ ] 发布/工作流变更已运行 `go test ./internal/repo -run 'TestReleaseWorkflow' -count=1`
- [ ] 新增/修改了对应测试

## 检查清单 / Checklist

- [ ] 提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)
- [ ] 已更新 [CHANGELOG](../docs/CHANGELOG.md)（如适用）/ Changelog updated if applicable
- [ ] 已更新相关 [docs](../docs/)（如适用）/ Docs updated if applicable
- [ ] 不包含任何密钥、凭据或本地绝对路径 / No secrets or local absolute paths
- [ ] 未把 `S`/`T`/`I`/`U` 源码证据写成真实 CI、发布或跨平台运行证据 / Evidence levels remain honest
- [ ] 工作流、依赖、Docker 或安全策略变更需要 Code Owner review / High-impact changes have Code Owner review
- [ ] 行为符合 [行为准则](CODE_OF_CONDUCT.md) / Behavior complies with the Code of Conduct

## 屏幕截图 / Screenshots（可选）

<!-- 视觉变更请附截图 / Attach screenshots for visual changes -->
