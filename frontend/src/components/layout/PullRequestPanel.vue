<script setup lang="ts">
// Koyori IDE 组件 · Pull Request Panel。
// 喵，这是 Pull Request Panel，负责 Koyori IDE 的界面呈现喵~
import { computed, reactive, ref, watch } from "vue";
import {
  ArrowLeft,
  ChatDotRound,
  CircleCheck,
  Plus,
  Refresh,
  WarningFilled,
} from "@element-plus/icons-vue";
import {
  addPullRequestComment,
  closePullRequestDetail,
  configurePullRequests,
  createPullRequest,
  filteredPullRequests,
  loadPullRequest,
  loadPullRequests,
  pullRequestState,
  setPullRequestView,
  submitPullRequestReview,
} from "@/stores/pullRequests";
import { useI18n } from "@/lib/i18n";

const props = withDefaults(defineProps<{
  repoPath: string;
  configId: string;
  remoteName?: string;
  gitlabBaseUrl?: string;
}>(), {
  remoteName: "",
  gitlabBaseUrl: "",
});

const { t } = useI18n();

const createForm = reactive({
  title: "",
  body: "",
  sourceBranch: "",
  targetBranch: "main",
  draft: false,
});
const reviewBody = ref("");

const providerName = computed(() => {
  if (pullRequestState.repository?.provider === "github") return "GitHub";
  if (pullRequestState.repository?.provider === "gitlab") return "GitLab";
  return t("pullRequests.name");
});

const accessMessage = computed(() => {
  switch (pullRequestState.access) {
    case "authentication-required":
      return t("pullRequests.authenticationRequired");
    case "permission-denied":
      return t("pullRequests.permissionDenied");
    case "unsupported-provider":
      return t("pullRequests.unsupportedProvider");
    case "error":
      return pullRequestState.error ?? t("pullRequests.loadError");
    default:
      return "";
  }
});

const actionMessage = computed(() => {
  switch (pullRequestState.lastAction) {
    case "created": return t("pullRequests.created");
    case "commented": return t("pullRequests.commented");
    case "approved": return t("pullRequests.approved");
    case "changes_requested": return t("pullRequests.changesRequested");
    default: return pullRequestState.lastAction ?? "";
  }
});

const showBlockingState = computed(() => (
  pullRequestState.access !== "idle" &&
  pullRequestState.access !== "ready" &&
  pullRequestState.repository === null
));

watch(
  () => [props.repoPath, props.configId, props.remoteName, props.gitlabBaseUrl] as const,
  async ([repoPath, configId, remoteName, gitlabBaseUrl]) => {
    configurePullRequests({ repoPath, configId, remoteName, gitlabBaseUrl });
    if (repoPath) await loadPullRequests();
  },
  { immediate: true },
);

async function refreshList(): Promise<void> {
  closePullRequestDetail();
  await loadPullRequests();
}

async function changeState(): Promise<void> {
  closePullRequestDetail();
  await loadPullRequests();
}

async function openDetail(number: number): Promise<void> {
  reviewBody.value = "";
  await loadPullRequest(number);
}

async function submitCreate(): Promise<void> {
  const created = await createPullRequest({
    title: createForm.title,
    body: createForm.body,
    sourceBranch: createForm.sourceBranch,
    targetBranch: createForm.targetBranch,
    draft: createForm.draft,
  });
  if (created) {
    createForm.title = "";
    createForm.body = "";
    createForm.sourceBranch = "";
    createForm.draft = false;
  }
}

async function submitComment(): Promise<void> {
  const body = reviewBody.value.trim();
  if (!body) return;
  if (await addPullRequestComment(body)) reviewBody.value = "";
}

async function submitReview(action: "approve" | "request_changes"): Promise<void> {
  const body = reviewBody.value.trim();
  if (await submitPullRequestReview(action, body)) reviewBody.value = "";
}
</script>

<template>
  <section class="pull-requests" :aria-label="t('pullRequests.panelAria')">
    <header class="pull-requests__toolbar">
      <div class="pull-requests__identity">
        <span data-test="provider-name" class="pull-requests__provider">{{ providerName }}</span>
        <span v-if="pullRequestState.repository" class="pull-requests__repository">
          {{ pullRequestState.repository.projectPath }}
        </span>
      </div>
      <div class="pull-requests__commands">
        <button
          type="button"
          class="pull-requests__icon-button"
          :title="t('pullRequests.refresh')"
          :aria-label="t('pullRequests.refresh')"
          :disabled="pullRequestState.loading"
          @click="refreshList"
        >
          <el-icon><Refresh /></el-icon>
        </button>
        <button
          v-if="pullRequestState.repository?.capabilities.canCreate !== false"
          type="button"
          data-test="new-pull-request"
          class="pull-requests__icon-button"
          :title="t('pullRequests.new')"
          :aria-label="t('pullRequests.new')"
          @click="setPullRequestView('create')"
        >
          <el-icon><Plus /></el-icon>
        </button>
      </div>
    </header>

    <div
      v-if="pullRequestState.error && !showBlockingState"
      class="pull-requests__banner pull-requests__banner--error"
      role="alert"
    >
      <el-icon><WarningFilled /></el-icon>
      <span>{{ pullRequestState.error }}</span>
    </div>
    <div
      v-else-if="pullRequestState.lastAction"
      class="pull-requests__banner pull-requests__banner--success"
      role="status"
    >
      <el-icon><CircleCheck /></el-icon>
      <span>{{ actionMessage }}</span>
    </div>

    <div v-if="pullRequestState.loading && !pullRequestState.repository" class="pull-requests__state" role="status">
      {{ t("pullRequests.loading") }}
    </div>

    <div
      v-else-if="showBlockingState"
      data-test="pull-request-access-state"
      class="pull-requests__state pull-requests__state--error"
      role="alert"
    >
      <el-icon><WarningFilled /></el-icon>
      <p>{{ accessMessage }}</p>
    </div>

    <form
      v-else-if="pullRequestState.activeView === 'create'"
      data-test="create-form"
      class="pull-requests__form"
      @submit.prevent="submitCreate"
    >
      <div class="pull-requests__form-header">
        <button
          type="button"
          class="pull-requests__icon-button"
          :title="t('pullRequests.back')"
          :aria-label="t('pullRequests.back')"
          @click="setPullRequestView('list')"
        >
          <el-icon><ArrowLeft /></el-icon>
        </button>
        <h3>{{ t("pullRequests.new") }}</h3>
      </div>
      <label class="pull-requests__field">
        <span>{{ t("pullRequests.title") }}</span>
        <input
          v-model="createForm.title"
          data-test="create-title"
          type="text"
          maxlength="512"
          required
        />
      </label>
      <div class="pull-requests__branch-grid">
        <label class="pull-requests__field">
          <span>{{ t("pullRequests.sourceBranch") }}</span>
          <input
            v-model="createForm.sourceBranch"
            data-test="create-source"
            type="text"
            maxlength="255"
            required
          />
        </label>
        <label class="pull-requests__field">
          <span>{{ t("pullRequests.targetBranch") }}</span>
          <input
            v-model="createForm.targetBranch"
            data-test="create-target"
            type="text"
            maxlength="255"
            required
          />
        </label>
      </div>
      <label class="pull-requests__field pull-requests__field--grow">
        <span>{{ t("pullRequests.description") }}</span>
        <textarea v-model="createForm.body" maxlength="1048576" />
      </label>
      <label class="pull-requests__checkbox">
        <input v-model="createForm.draft" type="checkbox" />
        <span>{{ t("pullRequests.draft") }}</span>
      </label>
      <button type="submit" class="pull-requests__primary" :disabled="pullRequestState.submitting">
        {{ t("pullRequests.create") }}
      </button>
    </form>

    <div v-else-if="pullRequestState.selected" class="pull-requests__detail">
      <div class="pull-requests__detail-header">
        <button
          type="button"
          class="pull-requests__icon-button"
          :title="t('pullRequests.back')"
          :aria-label="t('pullRequests.back')"
          @click="closePullRequestDetail"
        >
          <el-icon><ArrowLeft /></el-icon>
        </button>
        <div class="pull-requests__detail-heading">
          <span class="pull-requests__number">#{{ pullRequestState.selected.number }}</span>
          <h3>{{ pullRequestState.selected.title }}</h3>
        </div>
      </div>
      <div class="pull-requests__meta">
        <span class="pull-requests__state-tag">{{ pullRequestState.selected.state }}</span>
        <span>{{ pullRequestState.selected.author.login }}</span>
        <span>{{ pullRequestState.selected.sourceBranch }} -> {{ pullRequestState.selected.targetBranch }}</span>
      </div>
      <p v-if="pullRequestState.selected.body" class="pull-requests__body">
        {{ pullRequestState.selected.body }}
      </p>
      <div class="pull-requests__diff-section">
        <div class="pull-requests__section-label">{{ t("pullRequests.changes") }}</div>
        <div v-if="pullRequestState.detailLoading" class="pull-requests__state" role="status">{{ t("pullRequests.loadingChanges") }}</div>
        <pre v-else data-test="pull-request-diff" class="pull-requests__diff">{{ pullRequestState.selected.diff || t("pullRequests.noDiff") }}</pre>
      </div>
      <div class="pull-requests__review">
        <label class="pull-requests__field">
          <span>{{ t("pullRequests.review") }}</span>
          <textarea v-model="reviewBody" data-test="review-body" maxlength="1048576" />
        </label>
        <div class="pull-requests__review-actions">
          <button
            v-if="pullRequestState.repository?.capabilities.canComment"
            type="button"
            data-test="comment-button"
            class="pull-requests__secondary"
            :disabled="pullRequestState.submitting || !reviewBody.trim()"
            @click="submitComment"
          >
            <el-icon><ChatDotRound /></el-icon>
            {{ t("pullRequests.comment") }}
          </button>
          <button
            v-if="pullRequestState.repository?.capabilities.canRequestChanges"
            type="button"
            data-test="request-changes-button"
            class="pull-requests__secondary pull-requests__secondary--danger"
            :disabled="pullRequestState.submitting || !reviewBody.trim()"
            @click="submitReview('request_changes')"
          >
            {{ t("pullRequests.requestChanges") }}
          </button>
          <button
            v-if="pullRequestState.repository?.capabilities.canApprove"
            type="button"
            data-test="approve-button"
            class="pull-requests__primary"
            :disabled="pullRequestState.submitting"
            @click="submitReview('approve')"
          >
            <el-icon><CircleCheck /></el-icon>
            {{ t("pullRequests.approve") }}
          </button>
        </div>
      </div>
    </div>

    <div v-else class="pull-requests__list-view">
      <div class="pull-requests__filters">
        <input
          v-model="pullRequestState.filter"
          data-test="pull-request-filter"
          type="search"
          :placeholder="t('pullRequests.filter')"
          :aria-label="t('pullRequests.filter')"
        />
        <select v-model="pullRequestState.stateFilter" :aria-label="t('pullRequests.state')" @change="changeState">
          <option value="open">{{ t("pullRequests.open") }}</option>
          <option value="closed">{{ t("pullRequests.closed") }}</option>
          <option value="all">{{ t("pullRequests.all") }}</option>
        </select>
      </div>
      <div v-if="pullRequestState.loading" class="pull-requests__state" role="status">{{ t("pullRequests.refreshing") }}</div>
      <button
        v-for="item in filteredPullRequests"
        v-else
        :key="item.number"
        type="button"
        data-test="pull-request-row"
        class="pull-requests__row"
        @click="openDetail(item.number)"
      >
        <span class="pull-requests__row-title">{{ item.title }}</span>
        <span class="pull-requests__row-meta">
          #{{ item.number }} {{ item.author.login }} · {{ item.sourceBranch }} -> {{ item.targetBranch }}
        </span>
      </button>
      <div v-if="!pullRequestState.loading && filteredPullRequests.length === 0" class="pull-requests__state">
        {{ t("pullRequests.none") }}
      </div>
      <div v-if="pullRequestState.truncated" class="pull-requests__limit" role="status">
        {{ t("pullRequests.truncated") }}
      </div>
    </div>
  </section>
</template>

<style scoped>
.pull-requests {
  display: flex;
  flex-direction: column;
  min-width: 0;
  height: 100%;
  color: var(--color-text-primary);
  background: var(--color-sidebar-bg);
  font-size: 12px;
}

.pull-requests__toolbar,
.pull-requests__form-header,
.pull-requests__detail-header,
.pull-requests__filters,
.pull-requests__review-actions,
.pull-requests__meta,
.pull-requests__banner {
  display: flex;
  align-items: center;
}

.pull-requests__toolbar {
  min-height: 38px;
  padding: 0 10px 0 12px;
  border-bottom: 1px solid var(--color-border-subtle);
  justify-content: space-between;
  gap: 8px;
}

.pull-requests__identity,
.pull-requests__detail-heading {
  min-width: 0;
}

.pull-requests__provider {
  display: block;
  font-weight: 600;
  line-height: 16px;
}

.pull-requests__repository {
  display: block;
  max-width: 180px;
  overflow: hidden;
  color: var(--color-text-secondary);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pull-requests__commands {
  display: flex;
  flex-shrink: 0;
  gap: 2px;
}

.pull-requests__icon-button {
  display: inline-flex;
  width: 28px;
  height: 28px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 4px;
  color: var(--color-text-secondary);
  background: transparent;
  cursor: pointer;
}

.pull-requests__icon-button:hover:not(:disabled) {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.pull-requests__icon-button:disabled,
.pull-requests__primary:disabled,
.pull-requests__secondary:disabled {
  opacity: 0.45;
  cursor: default;
}

.pull-requests__banner {
  gap: 7px;
  min-height: 32px;
  padding: 5px 10px;
  border-bottom: 1px solid var(--color-border-subtle);
}

.pull-requests__banner--error,
.pull-requests__state--error {
  color: var(--color-error);
}

.pull-requests__banner--success {
  color: var(--color-success);
}

.pull-requests__state {
  padding: 24px 14px;
  color: var(--color-text-secondary);
  text-align: center;
}

.pull-requests__state--error {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}

.pull-requests__state p {
  margin: 0;
  line-height: 18px;
}

.pull-requests__list-view,
.pull-requests__detail,
.pull-requests__form {
  min-height: 0;
  overflow: auto;
}

.pull-requests__filters {
  position: sticky;
  top: 0;
  z-index: 1;
  gap: 6px;
  padding: 8px;
  border-bottom: 1px solid var(--color-border-subtle);
  background: var(--color-sidebar-bg);
}

.pull-requests input[type="text"],
.pull-requests input[type="search"],
.pull-requests select,
.pull-requests textarea {
  min-width: 0;
  border: 1px solid var(--color-border-default);
  border-radius: 4px;
  color: var(--color-text-primary);
  background: var(--color-input-bg);
  font: inherit;
  outline: none;
}

.pull-requests input[type="text"],
.pull-requests input[type="search"],
.pull-requests select {
  height: 28px;
  padding: 0 8px;
}

.pull-requests input[type="search"] {
  flex: 1;
}

.pull-requests textarea {
  min-height: 82px;
  padding: 7px 8px;
  resize: vertical;
}

.pull-requests input:focus,
.pull-requests select:focus,
.pull-requests textarea:focus {
  border-color: var(--color-primary);
}

.pull-requests__row {
  display: block;
  width: 100%;
  min-height: 50px;
  padding: 7px 12px;
  border: 0;
  border-bottom: 1px solid var(--color-border-subtle);
  color: inherit;
  background: transparent;
  text-align: left;
  cursor: pointer;
}

.pull-requests__row:hover {
  background: var(--color-bg-hover);
}

.pull-requests__row-title,
.pull-requests__row-meta {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pull-requests__row-title {
  margin-bottom: 4px;
  font-weight: 500;
}

.pull-requests__row-meta,
.pull-requests__meta,
.pull-requests__number,
.pull-requests__limit {
  color: var(--color-text-secondary);
}

.pull-requests__limit {
  padding: 7px 12px;
  border-top: 1px solid var(--color-border-subtle);
}

.pull-requests__form,
.pull-requests__detail {
  padding: 10px 12px 14px;
}

.pull-requests__form {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.pull-requests__form-header,
.pull-requests__detail-header {
  min-height: 32px;
  gap: 6px;
}

.pull-requests h3 {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  line-height: 18px;
}

.pull-requests__field {
  display: flex;
  flex-direction: column;
  gap: 5px;
}

.pull-requests__field > span,
.pull-requests__section-label {
  color: var(--color-text-secondary);
  font-weight: 500;
}

.pull-requests__branch-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 8px;
}

.pull-requests__checkbox {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.pull-requests__primary,
.pull-requests__secondary {
  display: inline-flex;
  min-height: 28px;
  align-items: center;
  justify-content: center;
  gap: 5px;
  padding: 4px 10px;
  border: 1px solid transparent;
  border-radius: 4px;
  font: inherit;
  cursor: pointer;
}

.pull-requests__primary {
  color: var(--color-on-primary);
  background: var(--color-primary);
}

.pull-requests__secondary {
  border-color: var(--color-border-default);
  color: var(--color-text-primary);
  background: var(--color-bg-surface);
}

.pull-requests__secondary--danger {
  color: var(--color-error);
}

.pull-requests__detail-heading {
  flex: 1;
}

.pull-requests__meta {
  flex-wrap: wrap;
  gap: 6px 10px;
  margin: 5px 0 12px 34px;
}

.pull-requests__state-tag {
  padding: 1px 5px;
  border-radius: 3px;
  color: var(--color-success);
  background: var(--color-success-bg);
}

.pull-requests__body {
  margin: 0 0 12px;
  line-height: 18px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.pull-requests__diff-section {
  border-top: 1px solid var(--color-border-subtle);
  border-bottom: 1px solid var(--color-border-subtle);
  padding: 10px 0;
}

.pull-requests__diff {
  max-height: 320px;
  margin: 7px 0 0;
  overflow: auto;
  color: var(--color-text-primary);
  background: var(--color-editor-bg);
  font-family: var(--font-mono);
  font-size: 11px;
  line-height: 17px;
  white-space: pre;
}

.pull-requests__review {
  padding-top: 12px;
}

.pull-requests__review-actions {
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 6px;
  margin-top: 8px;
}

@media (max-width: 260px) {
  .pull-requests__branch-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .pull-requests__review-actions {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
