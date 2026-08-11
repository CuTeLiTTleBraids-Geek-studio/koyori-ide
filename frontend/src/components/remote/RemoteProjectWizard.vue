<script setup lang="ts">
// Koyori IDE 组件 · Remote Project Wizard。
// 喵，这是 Remote Project Wizard，负责 Koyori IDE 的界面呈现喵~
// F-9 (prompt-2.md 第 537-586 行) — SSH 远程项目创建向导。
//
// 4 步骤向导（使用 Element Plus 组件）：
//   1. 输入 SSH 连接信息（项目名 + Host/Port/User/KeyPath/Password/KnownHostsPath）
//   2. 测试连接（调用 store.connect，显示成功/失败）
//   3. 选择远程项目目录（连接成功后可执行 `ls` 列出子项辅助选择）
//   4. 确认创建（emit "created" 携带 { name, path, remote }）
//
// 安全（G-SEC-07 / G-SEC-12）：
//   - 密码字段使用 type=password，且绝不通过 console.log/notifyError 输出。
//   - 测试连接失败时仅显示后端返回的错误消息（后端不记录敏感字段）。
//   - 关闭向导时清空本组件持有的 config 副本，避免密码残留在内存。

import { ref, computed, reactive, watch } from "vue";
import { useI18n } from "@/lib/i18n";
import { errorMessage } from "@/lib/errors";
import { notifyError } from "@/lib/notifications";
import {
  connect as storeConnect,
  disconnect as storeDisconnect,
  executeCommand as storeExecuteCommand,
  buildRemoteProject,
  remoteState,
} from "@/stores/remote";
import type { SSHConfig, RemoteConfig } from "@/types";

const props = defineProps<{
  visible: boolean;
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "created", project: { name: string; path: string; remote: RemoteConfig }): void;
}>();

const { t } = useI18n();

type Step = 0 | 1 | 2 | 3;
const step = ref<Step>(0);

// 表单状态
const projectName = ref("");
const config = reactive<SSHConfig>({
  host: "",
  port: 22,
  user: "",
  keyPath: "",
  password: "",
  knownHostsPath: "",
});
const remotePath = ref("/");

// 步骤 2：测试连接
const testing = ref(false);
const testResult = ref<"idle" | "success" | "failed">("idle");
const testError = ref<string>("");
const connectionName = ref<string>(""); // 用于 store 内部索引的会话名

// 步骤 3：浏览目录
const browsing = ref(false);
const browseEntries = ref<string[]>([]);
const browseError = ref<string>("");

// 步骤 4：确认创建
const creating = ref(false);
const createdFlag = ref(false);
const createdProject = ref<{ name: string; path: string; remote: RemoteConfig } | null>(null);

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

const step0Errors = computed(() => {
  const errs: string[] = [];
  if (!projectName.value.trim()) errs.push(t("remote.error.nameRequired"));
  if (!config.host.trim()) errs.push(t("remote.error.hostRequired"));
  if (!config.user.trim()) errs.push(t("remote.error.userRequired"));
  if (!config.keyPath?.trim() && !config.password) {
    errs.push(t("remote.error.authRequired"));
  }
  return errs;
});

const canProceedFromStep0 = computed(() => step0Errors.value.length === 0);

// ---------------------------------------------------------------------------
// 步骤导航
// ---------------------------------------------------------------------------

function resetWizard(): void {
  step.value = 0;
  projectName.value = "";
  config.host = "";
  config.port = 22;
  config.user = "";
  config.keyPath = "";
  config.password = "";
  config.knownHostsPath = "";
  remotePath.value = "/";
  testing.value = false;
  testResult.value = "idle";
  testError.value = "";
  connectionName.value = "";
  browsing.value = false;
  browseEntries.value = [];
  browseError.value = "";
  creating.value = false;
  createdFlag.value = false;
  createdProject.value = null;
}

function goNext(): void {
  if (step.value === 0) {
    if (!canProceedFromStep0.value) return;
    // 进入步骤 2 前重置测试状态
    testResult.value = "idle";
    testError.value = "";
    step.value = 1;
  } else if (step.value === 1) {
    // 必须测试成功才能进入步骤 3
    if (testResult.value !== "success") return;
    step.value = 2;
  } else if (step.value === 2) {
    if (!remotePath.value.trim()) return;
    step.value = 3;
  }
}

function goBack(): void {
  if (step.value > 0) step.value = (step.value - 1) as Step;
}

// ---------------------------------------------------------------------------
// 步骤 2：测试连接
// ---------------------------------------------------------------------------

async function handleTestConnection(): Promise<void> {
  testing.value = true;
  testResult.value = "idle";
  testError.value = "";
  // 会话名使用项目名（已校验非空），保证 store 内唯一索引。
  connectionName.value = projectName.value.trim();
  const ok = await storeConnect(connectionName.value, { ...config });
  testing.value = false;
  if (ok) {
    testResult.value = "success";
  } else {
    testResult.value = "failed";
    // storeConnect 失败时错误写入 remoteState.error；此处取回显示。
    // 重新执行一次 isConnected 以触发 store.error 填充（connect 已填）。
    testError.value = errorMessageFromStore();
  }
}

function errorMessageFromStore(): string {
  // storeConnect 失败时已将 errorMessage 写入 remoteState.error。
  return remoteState.error ?? t("remote.test.unknownError");
}

// ---------------------------------------------------------------------------
// 步骤 3：浏览远程目录
// ---------------------------------------------------------------------------

async function handleBrowse(): Promise<void> {
  if (!connectionName.value) return;
  browsing.value = true;
  browseError.value = "";
  browseEntries.value = [];
  try {
    const out = await storeExecuteCommand(connectionName.value, ["ls", "-1", "--", remotePath.value]);
    if (out === null) {
      browseError.value = t("remote.browse.error", { error: "executeCommand returned null" });
    } else {
      browseEntries.value = out
        .split("\n")
        .map((s) => s.trim())
        .filter((s) => s.length > 0);
      if (browseEntries.value.length === 0) {
        browseError.value = t("remote.browse.empty");
      }
    }
  } catch (e: unknown) {
    browseError.value = t("remote.browse.error", { error: errorMessage(e) });
  } finally {
    browsing.value = false;
  }
}

function selectSubdir(name: string): void {
  // 拼接子目录路径（简单 join，避免末尾斜杠重复）
  const base = remotePath.value.replace(/\/+$/, "");
  remotePath.value = `${base}/${name}`;
  // 选择后清空列表，提示用户可再次列目录
  browseEntries.value = [];
  browseError.value = "";
}

// ---------------------------------------------------------------------------
// 步骤 4：确认创建
// ---------------------------------------------------------------------------

async function handleCreate(): Promise<void> {
  creating.value = true;
  try {
    const project = buildRemoteProject(
      projectName.value.trim(),
      { ...config },
      remotePath.value.trim(),
    );
    createdProject.value = project;
    createdFlag.value = true;
    // 创建成功后不再自动断开，保留连接供后续文件树/终端使用。
  } catch (e: unknown) {
    notifyError(t("remote.test.failed", { error: errorMessage(e) }));
  } finally {
    creating.value = false;
  }
}

function handleOpenCreated(): void {
  if (createdProject.value) {
    emit("created", createdProject.value);
  }
  emit("close");
}

// ---------------------------------------------------------------------------
// 关闭/取消：清理连接（若仅用于测试且未完成创建）
// ---------------------------------------------------------------------------

function handleClose(): void {
  // 若已测试连接但未完成创建，断开会话以避免悬挂连接。
  if (connectionName.value && testResult.value === "success" && !createdFlag.value) {
    void storeDisconnect(connectionName.value);
  }
  resetWizard();
  emit("close");
}

// ---------------------------------------------------------------------------
// visible 变化时重置
// ---------------------------------------------------------------------------

watch(
  () => props.visible,
  (v) => {
    if (v) {
      resetWizard();
    }
  },
  { immediate: true },
);
</script>

<template>
  <el-dialog
    :model-value="visible"
    :title="t('remote.title')"
    width="640px"
    top="8vh"
    :close-on-click-modal="false"
    :close-on-press-escape="true"
    :destroy-on-close="false"
    :aria-label="t('remote.title')"
    @update:model-value="(v: boolean) => { if (!v) handleClose(); }"
  >
    <el-steps :active="step" finish-status="success" align-center class="rpw-steps">
      <el-step :title="t('remote.step.sshInfo')" />
      <el-step :title="t('remote.step.testConnection')" />
      <el-step :title="t('remote.step.selectDir')" />
      <el-step :title="t('remote.step.confirm')" />
    </el-steps>

    <!-- 步骤 1：SSH 连接信息 -->
    <section v-if="step === 0" class="rpw-body">
      <el-form label-position="top" size="default">
        <el-form-item :label="t('remote.field.name')">
          <el-input
            v-model="projectName"
            :placeholder="t('remote.placeholder.name')"
            :aria-label="t('remote.field.name')"
          />
        </el-form-item>
        <el-form-item :label="t('remote.field.host')">
          <el-input
            v-model="config.host"
            :placeholder="t('remote.placeholder.host')"
            :aria-label="t('remote.field.host')"
          />
        </el-form-item>
        <el-form-item :label="t('remote.field.port')">
          <el-input-number
            v-model="config.port"
            :min="1"
            :max="65535"
            :aria-label="t('remote.field.port')"
          />
        </el-form-item>
        <el-form-item :label="t('remote.field.user')">
          <el-input
            v-model="config.user"
            :placeholder="t('remote.placeholder.user')"
            :aria-label="t('remote.field.user')"
          />
        </el-form-item>
        <el-form-item :label="t('remote.field.keyPath')">
          <el-input
            v-model="config.keyPath"
            :placeholder="t('remote.placeholder.keyPath')"
            :aria-label="t('remote.field.keyPath')"
          />
        </el-form-item>
        <el-form-item :label="t('remote.field.password')">
          <el-input
            v-model="config.password"
            type="password"
            show-password
            :placeholder="t('remote.placeholder.password')"
            :aria-label="t('remote.field.password')"
          />
        </el-form-item>
        <el-form-item :label="t('remote.field.knownHostsPath')">
          <el-input
            v-model="config.knownHostsPath"
            :placeholder="t('remote.placeholder.knownHostsPath')"
            :aria-label="t('remote.field.knownHostsPath')"
          />
        </el-form-item>
      </el-form>
      <ul v-if="step0Errors.length > 0" class="rpw-errors" role="alert">
        <li v-for="err in step0Errors" :key="err">{{ err }}</li>
      </ul>
    </section>

    <!-- 步骤 2：测试连接 -->
    <section v-else-if="step === 1" class="rpw-body">
      <p class="rpw-hint">{{ t("remote.test.hint") }}</p>
      <div class="rpw-test-row">
        <el-button
          type="primary"
          :loading="testing"
          :disabled="testing"
          @click="handleTestConnection"
        >
          {{ testing ? t("remote.test.connecting") : t("remote.test.button") }}
        </el-button>
      </div>
      <div v-if="testResult === 'success'" class="rpw-test-success" role="status">
        {{ t("remote.test.success") }}
      </div>
      <div v-else-if="testResult === 'failed'" class="rpw-test-failed" role="alert">
        {{ t("remote.test.failed", { error: testError }) }}
      </div>
    </section>

    <!-- 步骤 3：选择远程项目目录 -->
    <section v-else-if="step === 2" class="rpw-body">
      <el-form label-position="top" size="default">
        <el-form-item :label="t('remote.field.remotePath')">
          <el-input
            v-model="remotePath"
            :placeholder="t('remote.placeholder.remotePath')"
            :aria-label="t('remote.field.remotePath')"
          />
        </el-form-item>
      </el-form>
      <div class="rpw-browse-row">
        <el-button :loading="browsing" :disabled="browsing" @click="handleBrowse">
          {{ t("remote.browse.listButton") }}
        </el-button>
        <span class="rpw-browse-hint">{{ t("remote.browse.hint") }}</span>
      </div>
      <div v-if="browseError" class="rpw-browse-error" role="alert">{{ browseError }}</div>
      <ul v-if="browseEntries.length > 0" class="rpw-browse-list">
        <li v-for="name in browseEntries" :key="name">
          <button type="button" class="rpw-browse-item" @click="selectSubdir(name)">
            {{ name }}
          </button>
        </li>
      </ul>
    </section>

    <!-- 步骤 4：确认创建 -->
    <section v-else class="rpw-body">
      <p v-if="!createdFlag" class="rpw-hint">{{ t("remote.confirm.summary") }}</p>
      <dl class="rpw-summary">
        <div class="rpw-summary-row">
          <dt>{{ t("remote.confirm.name") }}</dt>
          <dd>{{ projectName }}</dd>
        </div>
        <div class="rpw-summary-row">
          <dt>{{ t("remote.confirm.host") }}</dt>
          <dd>{{ config.host }}</dd>
        </div>
        <div class="rpw-summary-row">
          <dt>{{ t("remote.confirm.port") }}</dt>
          <dd>{{ config.port }}</dd>
        </div>
        <div class="rpw-summary-row">
          <dt>{{ t("remote.confirm.user") }}</dt>
          <dd>{{ config.user }}</dd>
        </div>
        <div class="rpw-summary-row">
          <dt>{{ t("remote.confirm.remotePath") }}</dt>
          <dd>{{ remotePath }}</dd>
        </div>
      </dl>
      <!--
        GOAL-P0-07A: state the execution boundary before the user commits.
        Saving this connection does not open a remote workspace — the editor,
        terminal, language servers, git, debugger, and tests all keep running on
        this machine against local files. Showing this at the confirm step (not
        buried in docs) is what stops a user from believing the IDE went remote.
      -->
      <div class="rpw-boundary" role="note">
        <p class="rpw-boundary-title">{{ t("remote.boundary.title") }}</p>
        <p class="rpw-boundary-body">{{ t("remote.boundary.body") }}</p>
        <p class="rpw-boundary-not">{{ t("remote.boundary.notRemote") }}</p>
      </div>
      <div v-if="createdFlag" class="rpw-success">
        <p>{{ t("remote.confirm.created") }}</p>
        <code class="rpw-path">{{ remotePath }}</code>
      </div>
    </section>

    <template #footer>
      <el-button @click="handleClose">{{ t("common.cancel") }}</el-button>
      <el-button v-if="step > 0 && !createdFlag" @click="goBack">
        {{ t("common.back") }}
      </el-button>
      <el-button
        v-if="step < 3 && !createdFlag"
        type="primary"
        :disabled="
          (step === 0 && !canProceedFromStep0) ||
          (step === 1 && testResult !== 'success') ||
          (step === 2 && !remotePath.trim())
        "
        @click="goNext"
      >
        {{ t("common.next") }}
      </el-button>
      <el-button
        v-if="step === 3 && !createdFlag"
        type="primary"
        :loading="creating"
        :disabled="creating"
        @click="handleCreate"
      >
        {{ creating ? t("remote.confirm.creating") : t("common.create") }}
      </el-button>
      <el-button v-if="createdFlag" type="primary" @click="handleOpenCreated">
        {{ t("remote.confirm.open") }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.rpw-steps {
  margin-bottom: 24px;
}

.rpw-body {
  min-height: 240px;
}

.rpw-hint {
  margin: 0 0 16px 0;
  color: var(--color-text-secondary, #aaa);
  font-size: 13px;
}

.rpw-errors {
  margin: 8px 0 0 0;
  padding: 8px 12px;
  list-style: none;
  color: var(--color-danger, #f56c6c);
  font-size: 12px;
  background: var(--color-danger-bg, rgba(245, 108, 108, 0.08));
  border-radius: 4px;
}

.rpw-test-row {
  display: flex;
  gap: 12px;
  align-items: center;
}

.rpw-test-success {
  margin-top: 16px;
  padding: 10px 12px;
  color: var(--color-success, #67c23a);
  font-size: 13px;
  background: var(--color-success-bg, rgba(103, 194, 58, 0.08));
  border-radius: 4px;
}

.rpw-test-failed {
  margin-top: 16px;
  padding: 10px 12px;
  color: var(--color-danger, #f56c6c);
  font-size: 13px;
  background: var(--color-danger-bg, rgba(245, 108, 108, 0.08));
  border-radius: 4px;
  word-break: break-word;
}

.rpw-browse-row {
  display: flex;
  gap: 12px;
  align-items: center;
  margin-bottom: 12px;
}

.rpw-browse-hint {
  font-size: 12px;
  color: var(--color-text-tertiary, #888);
}

.rpw-browse-error {
  margin: 8px 0;
  padding: 8px 12px;
  color: var(--color-danger, #f56c6c);
  font-size: 12px;
  background: var(--color-danger-bg, rgba(245, 108, 108, 0.08));
  border-radius: 4px;
  word-break: break-word;
}

.rpw-browse-list {
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 200px;
  overflow-y: auto;
  border: 1px solid var(--color-border, #333);
  border-radius: 4px;
}

.rpw-browse-list li {
  border-bottom: 1px solid var(--color-border, #333);
}

.rpw-browse-list li:last-child {
  border-bottom: none;
}

.rpw-browse-item {
  display: block;
  width: 100%;
  padding: 6px 12px;
  background: transparent;
  border: none;
  color: var(--color-text-primary, #d4d4d4);
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}

.rpw-browse-item:hover {
  background: var(--color-bg-surface-container, #2a2a2a);
}

.rpw-summary {
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.rpw-summary-row {
  display: flex;
  gap: 12px;
  font-size: 13px;
}

.rpw-summary-row dt {
  width: 120px;
  color: var(--color-text-tertiary, #888);
  flex-shrink: 0;
}

.rpw-summary-row dd {
  margin: 0;
  color: var(--color-text-primary, #d4d4d4);
  word-break: break-word;
}

.rpw-success {
  margin-top: 16px;
  padding: 12px;
  background: var(--color-success-bg, rgba(103, 194, 58, 0.08));
  border-radius: 4px;
}

.rpw-path {
  display: block;
  margin-top: 6px;
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  color: var(--color-text-secondary, #aaa);
  word-break: break-all;
}

/*
 * GOAL-P0-07A: capability-boundary notice.
 *
 * Styled as a warning rather than an informational hint on purpose. A user who
 * reaches this step is about to assume the editor will run remotely; the notice
 * has to interrupt that assumption, not blend into the summary above it.
 */
.rpw-boundary {
  margin-top: 16px;
  padding: 12px;
  background: var(--color-warning-bg, rgba(230, 162, 60, 0.08));
  border-left: 3px solid var(--color-warning, #e6a23c);
  border-radius: 4px;
}

.rpw-boundary-title {
  margin: 0 0 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--color-warning, #e6a23c);
}

.rpw-boundary-body {
  margin: 0 0 6px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-text-primary, #d4d4d4);
}

.rpw-boundary-not {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-text-secondary, #aaa);
}
</style>
