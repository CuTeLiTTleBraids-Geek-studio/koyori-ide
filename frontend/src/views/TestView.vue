<script setup lang="ts">
// Koyori IDE 组件 · Test View。
// 喵，这是 Test View，负责 Koyori IDE 的界面呈现喵~
/**
 * F-10 (task-5.md): 测试探索器视图 (/test).
 *
 * 提供独立全屏测试管理体验：
 *   - 顶部工具栏（Discover / Run All / Continuous Testing 开关）
 *   - 左侧：嵌套测试树（suite/test，可展开/折叠）
 *   - 右侧：选中测试的输出 + 测试结果摘要
 *
 * 复用 stores/testExplorer 的全部状态与 action。tree/entries/outputsByTest
 * 等已实现，本视图以全屏布局重新组织。
 *
 * i18n 键前缀：view.test.*
 */
import { computed, onMounted } from "vue";
import {
  testExplorerState,
  discoverTests,
  runDiscoveredTest,
  debugDiscoveredTest,
  coverageDiscoveredTest,
  runGoTestsJSON,
  jumpToTest,
  selectTest,
  selectedTestOutput,
  toggleSuite,
  setContinuousTesting,
  type TestNode,
  type TestEntry,
} from "@/stores/testExplorer";
import { useI18n } from "@/lib/i18n";
import { cancelTestAtCursor, toolchainState } from "@/stores/toolchain";

const { t } = useI18n();

onMounted(() => {
  // 挂载时若已有项目，自动发现一次测试。
  if (testExplorerState.entries.length === 0) {
    void discoverTests();
  }
});

// 选中测试的输出（响应式：selectedTestId 变化时重新计算）。
const currentOutput = computed(() => selectedTestOutput());
const testCursorRunning = computed(() => toolchainState.runningId === "test-cursor");

// 测试结果统计。
const stats = computed(() => {
  const entries = testExplorerState.entries;
  let passed = 0, failed = 0, skipped = 0, running = 0, pending = 0;
  for (const e of entries) {
    switch (e.status) {
      case "pass": passed++; break;
      case "fail": failed++; break;
      case "skip": skipped++; break;
      case "run": running++; break;
      default: pending++; break;
    }
  }
  return { total: entries.length, passed, failed, skipped, running, pending };
});

// 通过 entries id 查找原始 entry（用于点击 test 节点时执行操作）。
function findEntryById(id: string): TestEntry | undefined {
  return testExplorerState.entries.find((e) => e.id === id);
}

// 测试节点状态图标。
function statusIcon(status?: TestNode["status"]): string {
  switch (status) {
    case "passed": return "✓";
    case "failed": return "✗";
    case "skipped": return "→";
    case "running": return "⟳";
    default: return "·";
  }
}

// 测试节点状态颜色 class。
function statusClass(status?: TestNode["status"]): string {
  return status ? `test-view__node--${status}` : "";
}

// 渲染测试树（递归）。
function renderNodes(nodes: TestNode[], depth = 0): Array<{ node: TestNode; depth: number }> {
  const out: Array<{ node: TestNode; depth: number }> = [];
  for (const n of nodes) {
    out.push({ node: n, depth });
    if (n.type === "suite" && n.children && testExplorerState.expanded[n.id]) {
      out.push(...renderNodes(n.children, depth + 1));
    }
  }
  return out;
}

const flatNodes = computed(() => renderNodes(testExplorerState.tree));

function onNodeClick(item: { node: TestNode; depth: number }) {
  const node = item.node;
  if (node.type === "suite") {
    toggleSuite(node.id);
  } else {
    selectTest(node.id);
  }
}

function onRunNode(node: TestNode) {
  if (node.type !== "test") return;
  const entry = findEntryById(node.id);
  if (entry) void runDiscoveredTest(entry);
}

function onDebugNode(node: TestNode) {
  if (node.type !== "test") return;
  const entry = findEntryById(node.id);
  if (entry) void debugDiscoveredTest(entry);
}

function onCoverageNode(node: TestNode) {
  if (node.type !== "test") return;
  const entry = findEntryById(node.id);
  if (entry) void coverageDiscoveredTest(entry);
}

function onJumpToNode(node: TestNode) {
  if (node.type !== "test") return;
  const entry = findEntryById(node.id);
  if (entry) void jumpToTest(entry);
}

async function onRunAll() {
  await runGoTestsJSON();
}

async function onCancelTest() {
  await cancelTestAtCursor();
}

function onToggleContinuous() {
  setContinuousTesting(!testExplorerState.continuousTesting);
}
</script>

<template>
  <div class="test-view" :aria-busy="testExplorerState.loading || testExplorerState.running">
    <header class="test-view__header">
      <h1 class="test-view__title">{{ t("view.test.title") }}</h1>
      <p class="test-view__subtitle">{{ t("view.test.subtitle") }}</p>
    </header>

    <!-- 工具栏 -->
    <div class="test-view__toolbar">
      <button type="button" class="test-view__btn" :disabled="testExplorerState.loading" @click="discoverTests">
        ⟳ {{ t("view.test.discover") }}
      </button>
      <button type="button" class="test-view__btn" :disabled="testExplorerState.running" @click="onRunAll">
        ▶ {{ t("view.test.runAll") }}
      </button>
      <button
        type="button"
        class="test-view__btn"
        :class="{ 'test-view__btn--active': testExplorerState.continuousTesting }"
        @click="onToggleContinuous"
        :title="t('view.test.continuousHint')"
      >
        ⟲ {{ t("view.test.continuous") }}
      </button>
      <button
        v-if="testCursorRunning"
        type="button"
        class="test-view__btn"
        :aria-label="t('view.test.cancel')"
        :title="t('view.test.cancel')"
        @click="onCancelTest"
      >
        ×
      </button>
      <span v-if="testExplorerState.loading" class="test-view__hint" aria-live="polite">{{ t("view.test.loading") }}</span>
      <span v-if="testExplorerState.running" class="test-view__hint" aria-live="polite">{{ t("view.test.runningHint") }}</span>
    </div>

    <!-- 错误条 -->
    <div v-if="testExplorerState.error" class="test-view__error" role="alert">
      ⚠ {{ testExplorerState.error }}
    </div>

    <!-- 统计条 -->
    <div class="test-view__stats">
      <span class="test-view__stat">{{ t("view.test.statTotal") }}: <strong>{{ stats.total }}</strong></span>
      <span class="test-view__stat test-view__stat--passed">✓ {{ stats.passed }}</span>
      <span class="test-view__stat test-view__stat--failed">✗ {{ stats.failed }}</span>
      <span class="test-view__stat test-view__stat--skipped">→ {{ stats.skipped }}</span>
      <span v-if="stats.running" class="test-view__stat test-view__stat--running">⟳ {{ stats.running }}</span>
    </div>

    <!-- 主体：左侧测试树 + 右侧输出 -->
    <div class="test-view__body">
      <section class="test-view__tree-panel">
        <h4 class="test-view__section-title">{{ t("view.test.treeTitle") }}</h4>
        <ul v-if="flatNodes.length" class="test-view__tree" role="tree">
          <li
            v-for="item in flatNodes"
            :key="item.node.id"
            class="test-view__node"
            :class="[statusClass(item.node.status), { 'test-view__node--suite': item.node.type === 'suite' }]"
            :style="{ paddingLeft: 8 + item.depth * 14 + 'px' }"
            role="treeitem"
            tabindex="0"
            :aria-expanded="item.node.type === 'suite' ? !!testExplorerState.expanded[item.node.id] : undefined"
            @click="onNodeClick(item)"
            @keydown.enter.prevent="onNodeClick(item)"
            @keydown.space.prevent="onNodeClick(item)"
          >
            <span class="test-view__node-icon">
              <template v-if="item.node.type === 'suite'">
                {{ testExplorerState.expanded[item.node.id] ? "▾" : "▸" }}
              </template>
              <template v-else>{{ statusIcon(item.node.status) }}</template>
            </span>
            <span class="test-view__node-name">{{ item.node.name }}</span>
            <span v-if="item.node.duration" class="test-view__node-duration">{{ item.node.duration }}ms</span>
            <span v-if="item.node.type === 'test'" class="test-view__node-actions">
              <button type="button" class="test-view__link" @click.stop="onRunNode(item.node)" :aria-label="t('view.test.run')" :title="t('view.test.run')">
                ▶
              </button>
              <button type="button" class="test-view__link" @click.stop="onDebugNode(item.node)" :aria-label="t('view.test.debug')" :title="t('view.test.debug')">
                🐞
              </button>
              <button type="button" class="test-view__link" @click.stop="onCoverageNode(item.node)" :aria-label="t('view.test.coverage')" :title="t('view.test.coverage')">
                ⬚
              </button>
              <button type="button" class="test-view__link" @click.stop="onJumpToNode(item.node)" :aria-label="t('view.test.jump')" :title="t('view.test.jump')">
                ↗
              </button>
            </span>
          </li>
        </ul>
        <p v-else class="test-view__empty">
          {{ testExplorerState.loading ? t("view.test.loading") : t("view.test.empty") }}
        </p>
      </section>

      <section class="test-view__output-panel">
        <h4 class="test-view__section-title">{{ t("view.test.outputTitle") }}</h4>
        <pre v-if="currentOutput" class="test-view__output">{{ currentOutput }}</pre>
        <p v-else class="test-view__empty">{{ t("view.test.outputEmpty") }}</p>
      </section>
    </div>
  </div>
</template>

<style scoped>
.test-view {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  background: var(--color-bg-base, #ffffff);
  color: var(--color-text-primary, #eee);
}

.test-view__header {
  flex-shrink: 0;
  padding: 12px 16px;
  border-bottom: 1px solid var(--color-border, #333);
  background: var(--color-bg-elevated, #f5f5f7);
}

.test-view__title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}

.test-view__subtitle {
  margin: 4px 0 0;
  font-size: 12px;
  opacity: 0.7;
}

.test-view__toolbar {
  flex-shrink: 0;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  padding: 6px 16px;
  border-bottom: 1px solid var(--color-border, #333);
  background: var(--color-bg-elevated, #f5f5f7);
}

.test-view__btn {
  font-size: 11px;
  padding: 3px 10px;
  border-radius: 4px;
  border: 1px solid var(--color-border, #444);
  background: var(--color-bg-surface-container-low, rgba(255, 255, 255, 0.04));
  color: inherit;
  cursor: pointer;
}

.test-view__btn:hover:not(:disabled) {
  background: rgba(255, 255, 255, 0.08);
}

.test-view__btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.test-view__btn--active {
  background: var(--color-success-container);
  color: var(--color-success);
  border-color: var(--color-success);
}

.test-view__hint {
  font-size: 11px;
  opacity: 0.7;
}

.test-view__error {
  padding: 6px 16px;
  background: var(--color-error-container);
  color: var(--color-error);
  border-bottom: 1px solid var(--color-error);
  font-size: 12px;
}

.test-view__stats {
  flex-shrink: 0;
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  padding: 6px 16px;
  border-bottom: 1px solid var(--color-border-default);
  background: var(--color-bg-elevated);
  font-size: 12px;
}

.test-view__stat {
  opacity: 0.85;
}

.test-view__stat--passed { color: var(--color-success); }
.test-view__stat--failed { color: var(--color-error); }
.test-view__stat--skipped { color: var(--color-warning); }
.test-view__stat--running { color: var(--color-primary); }

.test-view__body {
  flex: 1;
  min-height: 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 0;
  overflow: hidden;
}

.test-view__tree-panel,
.test-view__output-panel {
  min-width: 0;
  overflow: auto;
  padding: 8px 12px;
  border-right: 1px solid var(--color-border-default);
}

.test-view__output-panel {
  border-right: none;
  border-left: 1px solid var(--color-border-default);
}

.test-view__section-title {
  margin: 0 0 8px;
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  opacity: 0.7;
}

.test-view__tree {
  list-style: none;
  margin: 0;
  padding: 0;
}

.test-view__node {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 3px 4px;
  cursor: pointer;
  border-radius: 3px;
  font-size: 12px;
  white-space: nowrap;
}

.test-view__node:hover {
  background: var(--chrome-hover-bg);
}

.test-view__node:focus-visible,
.test-view__link:focus-visible,
.test-view__btn:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

.test-view__node--suite {
  font-weight: 600;
  opacity: 0.95;
}

.test-view__node--passed .test-view__node-icon { color: var(--color-success); }
.test-view__node--failed .test-view__node-icon { color: var(--color-error); }
.test-view__node--skipped .test-view__node-icon { color: var(--color-warning); }
.test-view__node--running .test-view__node-icon { color: var(--color-primary); animation: test-view-spin 1s linear infinite; }

.test-view__node-icon {
  display: inline-flex;
  width: 14px;
  justify-content: center;
  flex-shrink: 0;
}

.test-view__node-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.test-view__node-duration {
  font-size: 10px;
  opacity: 0.6;
  font-family: var(--font-mono, ui-monospace, monospace);
}

.test-view__node-actions {
  display: none;
  gap: 2px;
}

.test-view__node:hover .test-view__node-actions {
  display: inline-flex;
}

.test-view__link {
  background: none;
  border: none;
  color: var(--color-text-secondary, #8b949e);
  cursor: pointer;
  font-size: 11px;
  padding: 0 2px;
}

.test-view__link:hover {
  color: var(--color-text-primary, #eee);
}

.test-view__output {
  margin: 0;
  padding: 8px;
  background: rgba(0, 0, 0, 0.3);
  border-radius: 4px;
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 11px;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 100%;
  overflow: auto;
}

.test-view__empty {
  margin: 0;
  padding: 16px;
  opacity: 0.5;
  font-size: 12px;
  text-align: center;
}

@keyframes test-view-spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

@media (prefers-reduced-motion: reduce) {
  .test-view__node--running .test-view__node-icon {
    animation: none;
  }
}

@media (max-width: 720px) {
  .test-view__body {
    grid-template-columns: minmax(0, 1fr);
    grid-template-rows: minmax(160px, 1fr) minmax(120px, 0.7fr);
  }

  .test-view__tree-panel,
  .test-view__output-panel {
    border-right: 0;
    border-left: 0;
  }

  .test-view__output-panel {
    border-top: 1px solid var(--color-border-default);
  }
}
</style>
