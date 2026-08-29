<script setup lang="ts">
// Koyori IDE 组件 · Status Bar。
// 喵，这是 Status Bar，负责 Koyori IDE 的界面呈现喵~
import { appState, toggleTerminal } from "@/stores/app";
import { editorState, activeFile } from "@/stores/editor";
import { toggleInlineCompletion, inlineCompletionUnavailable } from "@/stores/inlineCompletion";
import { connectivityState } from "@/lib/connectivity";
import {
  lspState,
  lspStatusLabel,
  lspStatusDetail,
  restartLSPServer,
  setLSPEnabled,
} from "@/stores/lsp";
import { runtimeVersions, refreshRuntimeVersions } from "@/stores/toolchain";
import { goTargetState, refreshGoTarget, restoreHostGoTarget, selectGoTarget } from "@/stores/goTarget";
import { workspaceModulesState } from "@/stores/workspaceModules";
import { computed, onMounted, watch } from "vue";
import { ArrowDown, Connection, Monitor, Refresh, SwitchButton } from "@element-plus/icons-vue";
import { extensionStatusBarEntries, extensionProgress } from "@/stores/extensionHostUi";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

const branchName = computed(() => appState.branchName || "—");
const errors = computed(() => appState.errors);
const warnings = computed(() => appState.warnings);
const cursorLine = computed(() => appState.cursorLine);
const cursorColumn = computed(() => appState.cursorColumn);
const encoding = computed(() => appState.encoding);
const languageMode = computed(() => activeFile.value?.language ?? appState.languageMode);
const hasProblems = computed(() => errors.value > 0 || warnings.value > 0);
const hasOpenFile = computed(() => editorState.openFiles.length > 0);
const inlineCompletionLabel = computed(() =>
  appState.inlineCompletionEnabled ? t("statusBar.aiCompletionOn") : t("statusBar.aiCompletionOff"),
);
// G-FEAT-02: when the network is offline, AI completion is unavailable but
// LSP-based offline completion keeps working. Show a badge to make this
// state visible so the user understands why AI is disabled.
const isOffline = computed(() => !connectivityState.online || inlineCompletionUnavailable.value);
// prompt-8 Task 8-D: LSP status for gopls / typescript-language-server.
const lspLabel = computed(() => lspStatusLabel.value);
const lspDetail = computed(() => lspStatusDetail.value);
const lspServers = computed(() => Object.values(lspState.statuses));
// prompt-9 9-I: Go / Node versions
const goVer = computed(() => runtimeVersions.goVersion || "");
const nodeVer = computed(() => runtimeVersions.nodeVersion || "");
const goWork = computed(() => runtimeVersions.hasGoWork);
const activeWorkspaceRoot = computed(() => workspaceModulesState.activeRoot || appState.currentProject || "");
const currentGoTarget = computed(() => {
  const target = goTargetState.current;
  return target.goos && target.goarch ? `${target.goos}/${target.goarch}` : "";
});
const hostGoTarget = computed(() => `${goTargetState.host.goos}/${goTargetState.host.goarch}`);

async function handleGoTargetCommand(command: string): Promise<void> {
  const root = activeWorkspaceRoot.value;
  if (!root) return;
  if (command === "__host__") {
    await restoreHostGoTarget(root);
    return;
  }
  const target = goTargetState.targets.find(({ goos, goarch }) => `${goos}/${goarch}` === command);
  if (target) await selectGoTarget(root, target);
}

async function handleLSPCommand(command: string): Promise<void> {
  if (command === "toggle") {
    await setLSPEnabled(!lspState.enabled);
  } else if (command.startsWith("restart:")) {
    await restartLSPServer(command.slice("restart:".length));
  }
}

onMounted(() => {
  void refreshRuntimeVersions();
  if (activeWorkspaceRoot.value) void refreshGoTarget(activeWorkspaceRoot.value);
});

watch(activeWorkspaceRoot, (root, previous) => {
  if (root && root !== previous) void refreshGoTarget(root);
});
</script>

<template>
  <footer
    class="statusbar"
    role="status"
    :aria-label="t('statusBar.statusBar')"
  >
    <!-- Left side -->
    <div class="statusbar__left">
      <span
        class="statusbar__item statusbar__item--branch"
        role="status"
        :aria-label="t('statusBar.currentBranchAria', { name: branchName })"
        :title="t('statusBar.currentBranch')"
      >
        <el-icon class="statusbar__branch-symbol" :size="13" aria-hidden="true"><Connection /></el-icon>
        <span>{{ branchName }}</span>
      </span>

      <span
        v-if="hasProblems"
        class="statusbar__item"
        role="status"
        :aria-label="t('statusBar.problemsAria', { errors, warnings })"
        :title="t('statusBar.errorsAndWarnings')"
      >
        <span v-if="errors > 0" class="statusbar__problem statusbar__problem--error" aria-hidden="true">
          <span class="statusbar__dot statusbar__dot--error" />
          {{ errors }}
        </span>
        <span v-if="warnings > 0" class="statusbar__problem statusbar__problem--warning" aria-hidden="true">
          <span class="statusbar__dot statusbar__dot--warning" />
          {{ warnings }}
        </span>
      </span>

      <!-- G-FEAT-02: offline badge — shown when the network is offline.
           LSP-based offline completion still works in this state, but AI
           completion is unavailable, so we surface the state explicitly. -->
      <span
        v-for="entry in extensionStatusBarEntries"
        :key="entry.id"
        v-show="entry.visible"
        class="statusbar__item statusbar__item--extension"
        role="status"
        :title="entry.tooltip"
      >
        {{ entry.text }}
      </span>
      <span
        v-if="extensionProgress"
        class="statusbar__item statusbar__item--extension-progress"
        role="progressbar"
        :aria-valuenow="extensionProgress.increment"
        :title="extensionProgress.message"
      >
        {{ extensionProgress.title || "Extension" }}<span v-if="extensionProgress.message">: {{ extensionProgress.message }}</span>
      </span>
      <span
        v-if="isOffline"
        class="statusbar__item statusbar__item--offline"
        role="status"
        :aria-label="t('statusBar.offlineBadgeAria')"
        :title="t('statusBar.offlineBadgeHint')"
      >
        <span class="statusbar__dot statusbar__dot--warning" aria-hidden="true" />
        {{ t("statusBar.offlineBadge") }}
      </span>
    </div>

    <!-- Right side -->
    <div class="statusbar__right">
      <button
        v-if="hasOpenFile"
        type="button"
        class="statusbar__item statusbar__item--interactive"
        :aria-label="t('statusBar.lineColumnAria', { line: cursorLine, column: cursorColumn })"
      >
        {{ t("statusBar.lineColumn", { line: cursorLine, column: cursorColumn }) }}
      </button>
      <button
        v-if="hasOpenFile"
        type="button"
        class="statusbar__item statusbar__item--interactive"
        :aria-label="t('statusBar.encodingAria', { encoding })"
      >
        {{ encoding }}
      </button>
      <button
        type="button"
        class="statusbar__item statusbar__item--interactive"
        :aria-label="t('statusBar.languageAria', { language: languageMode })"
      >
        {{ languageMode }}
      </button>
      <span
        v-if="goVer"
        class="statusbar__item"
        role="status"
        :title="goWork ? `${goVer} (go.work)` : goVer"
      >
        {{ goVer }}{{ goWork ? " · work" : "" }}
      </span>
      <el-dropdown
        v-if="currentGoTarget"
        trigger="click"
        @command="handleGoTargetCommand"
      >
        <button
          type="button"
          class="statusbar__item statusbar__item--interactive"
          :class="{ 'statusbar__item--target-override': goTargetState.overridden }"
          :title="t('statusBar.goTargetHint')"
          :aria-label="`Go target: ${currentGoTarget}`"
        >
          <el-icon :size="13" aria-hidden="true"><Monitor /></el-icon>
          GO {{ currentGoTarget }}
        </button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item
              v-for="target in goTargetState.targets"
              :key="`${target.goos}/${target.goarch}`"
              :command="`${target.goos}/${target.goarch}`"
              :disabled="target.goos === goTargetState.current.goos && target.goarch === goTargetState.current.goarch"
            >
              {{ target.goos }}/{{ target.goarch }}
            </el-dropdown-item>
            <el-dropdown-item command="__host__" divided :disabled="!goTargetState.overridden">
              {{ t("statusBar.restoreHostTarget", { target: hostGoTarget }) }}
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <span
        v-if="nodeVer"
        class="statusbar__item"
        role="status"
        :title="nodeVer"
      >
        {{ nodeVer }}
      </span>
      <el-dropdown data-test="lsp-menu" trigger="click" @command="handleLSPCommand">
        <button
          type="button"
          class="statusbar__item statusbar__item--interactive"
          :disabled="lspState.busy"
          :aria-label="lspDetail || lspLabel"
          :title="lspDetail || t('statusBar.lspHint')"
        >
          {{ lspLabel }}
          <el-icon :size="11" aria-hidden="true"><ArrowDown /></el-icon>
        </button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="toggle">
              <el-icon aria-hidden="true"><SwitchButton /></el-icon>
              {{ lspState.enabled ? t("statusBar.lspDisable") : t("statusBar.lspEnable") }}
            </el-dropdown-item>
            <el-dropdown-item
              v-for="server in lspServers"
              :key="server.language"
              :command="`restart:${server.language}`"
              :disabled="!lspState.enabled || !server.available"
            >
              <el-icon aria-hidden="true"><Refresh /></el-icon>
              {{ t("statusBar.lspRestart", { server: server.serverKind || server.language }) }}
            </el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
      <button
        type="button"
        class="statusbar__item statusbar__item--interactive"
        :class="{ 'statusbar__item--active': appState.terminalVisible }"
        :aria-label="t('statusBar.toggleTerminalAria', { state: appState.terminalVisible ? t('statusBar.on') : t('statusBar.off') })"
        :title="t('statusBar.toggleTerminal')"
        @click="toggleTerminal"
      >
        <span
          class="statusbar__dot"
          :class="appState.terminalVisible ? 'statusbar__dot--success' : 'statusbar__dot--muted'"
          aria-hidden="true"
        />
        {{ t("statusBar.terminal") }}
      </button>
      <button
        type="button"
        class="statusbar__item statusbar__item--interactive"
        :class="{ 'statusbar__item--active': appState.inlineCompletionEnabled }"
        :aria-label="t('statusBar.toggleHint', { label: inlineCompletionLabel })"
        :title="inlineCompletionLabel"
        @click="toggleInlineCompletion"
      >
        <span
          class="statusbar__dot"
          :class="appState.inlineCompletionEnabled ? 'statusbar__dot--success' : 'statusbar__dot--muted'"
          aria-hidden="true"
        />
        {{ t("statusBar.aiLabel") }}
      </button>
    </div>
  </footer>
</template>

<style scoped>
/* Apple 风格 StatusBar：与全局导航同色（纯黑），形成视觉框架 */
.statusbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 28px;
  min-height: 28px;
  padding: 0 12px;
  background-color: var(--color-statusbar-bg);
  /* Apple 风格：无装饰边框，色块本身就是分割 */
  border-top: none;
  z-index: 15;
  user-select: none;
}

.statusbar__left,
.statusbar__right {
  display: flex;
  align-items: center;
  gap: 4px;
  /* BUG3: 窗口缩小时左右两侧内容溢出会重叠。min-width:0 + overflow:hidden
     让子项可以正确截断/隐藏。 */
  min-width: 0;
  overflow: hidden;
}

.statusbar__item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  /* Apple nav-link 12px / 400 / -0.12px */
  font-size: 12px;
  font-family: var(--font-sans);
  font-weight: 400;
  letter-spacing: -0.12px;
  color: var(--chrome-text-secondary);
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  cursor: default;
  white-space: nowrap;
  line-height: 1;
  transition: background-color var(--transition-fast),
              color var(--transition-fast),
              transform var(--transition-fast);
}

.statusbar__item--branch {
  cursor: pointer;
  color: var(--chrome-text-primary);
}

.statusbar__item--branch:hover {
  background-color: var(--chrome-hover-bg);
  color: var(--chrome-text-primary);
}

.statusbar__item--branch:active {
  transform: scale(0.95);
}

/* P2-17: 区分可点击与纯展示 —— --interactive 提供 cursor/hover/active */
.statusbar__item--interactive {
  cursor: pointer;
}

.statusbar__item--interactive:hover {
  background-color: var(--chrome-hover-bg);
  color: var(--chrome-text-primary);
}

.statusbar__item--interactive:active {
  transform: scale(0.95);
}

/* Branch symbol — chrome-text-active（深/浅模式自适应） */
.statusbar__branch-symbol {
  font-size: 13px;
  line-height: 1;
  color: var(--chrome-text-active);
}

.statusbar__problem {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.statusbar__problem--error {
  color: var(--color-error);
}

.statusbar__problem--warning {
  color: var(--color-warning);
}

.statusbar__dot {
  display: inline-block;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}

.statusbar__dot--error {
  background-color: var(--color-error);
}

.statusbar__dot--warning {
  background-color: var(--color-warning);
}

.statusbar__dot--success {
  background-color: var(--color-success);
}

.statusbar__dot--muted {
  background-color: var(--chrome-text-muted);
}

.statusbar__item--active {
  cursor: pointer;
}

.statusbar__item--active:hover {
  background-color: var(--chrome-hover-bg);
  color: var(--chrome-text-primary);
}

.statusbar__item--active:active {
  transform: scale(0.95);
}

/* G-FEAT-02: offline badge — warning-tinted to draw attention. */
.statusbar__item--offline {
  color: var(--color-warning);
  cursor: default;
}

.statusbar__item--target-override {
  color: var(--color-primary);
}

button.statusbar__item:focus-visible {
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

/* BUG3: 窗口缩小时隐藏非关键状态栏项，避免溢出重叠。
   优先级：branch > problems > language/encoding > goVer/nodeVer > LSP > inlineCompletion。
   窄屏下按需隐藏低优先级项。 */
@media (max-width: 600px) {
  /* 隐藏 Go 版本、Node 版本、编码、GO target — 这些在窄屏下不是关键信息。 */
  .statusbar__right > :nth-last-child(-n+5) {
    display: none;
  }
}

@media (max-width: 480px) {
  /* 极窄屏下仅保留 branch + problems + line/col + language。 */
  .statusbar__right > :nth-last-child(-n+8) {
    display: none;
  }
}
</style>
