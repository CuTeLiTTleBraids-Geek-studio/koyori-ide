<script setup lang="ts">
// Koyori IDE 组件 · Httpclient Panel。
// 喵，这是 Httpclient Panel，负责 Koyori IDE 的界面呈现喵~
import { computed, onMounted, ref, watch } from "vue";
import { Close, Delete, VideoPlay } from "@element-plus/icons-vue";
import {
  authorizeSelectedPrivateNetwork,
  cancelActiveHTTPRequest,
  clearPrivateNetworkAuthorization,
  clearHTTPHistory,
  formatHTTPResponseBody,
  httpClientState,
  loadHTTPHistory,
  parseHTTPDocument,
  selectedHTTPRequest,
  selectHTTPRequest,
  selectHTTPRequestAtLine,
  sendHTTPRequest,
  type HTTPEnvironment,
} from "@/stores/httpClient";
import { useI18n } from "@/lib/i18n";

const { t } = useI18n();

const props = withDefaults(defineProps<{
  source: string;
  cursorLine?: number;
  environment?: HTTPEnvironment;
}>(), {
  cursorLine: 1,
  environment: () => ({ values: {}, secretRefs: {} }),
});

const timeoutMs = ref(30_000);
const maxResponseBytes = ref(5 * 1024 * 1024);
const activeView = ref<"response" | "history">("response");

const formattedBody = computed(() => (
  httpClientState.response ? formatHTTPResponseBody(httpClientState.response) : ""
));

const responseHeaders = computed(() => (
  Object.entries(httpClientState.response?.headers ?? {}).sort(([a], [b]) => a.localeCompare(b))
));

const responseBodyBytes = computed(() => (
  new Blob([httpClientState.response?.body ?? ""]).size
));

watch(
  () => [props.source, props.environment] as const,
  () => void parseHTTPDocument(props.source, props.environment, props.cursorLine),
  { immediate: true, deep: true },
);

watch(() => props.cursorLine, (line) => selectHTTPRequestAtLine(line));

onMounted(() => void loadHTTPHistory());

async function runSelected(): Promise<void> {
  activeView.value = "response";
  await sendHTTPRequest({
    timeoutMs: timeoutMs.value,
    maxResponseBytes: maxResponseBytes.value,
  });
}

async function togglePrivateNetwork(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement;
  const enabled = input.checked;
  if (!enabled) {
    clearPrivateNetworkAuthorization();
    return;
  }
  const approved = await authorizeSelectedPrivateNetwork();
  if (!approved) input.checked = false;
}
</script>

<template>
  <section class="http-client-panel" :aria-label="t('activity.httpClient')">
    <div class="http-toolbar">
      <select
        class="request-select"
        :value="httpClientState.selectedIndex"
        :aria-label="t('a11y.httpRequest')"
        @change="selectHTTPRequest(Number(($event.target as HTMLSelectElement).value))"
      >
        <option
          v-for="(request, index) in httpClientState.requests"
          :key="`${request.startLine}-${request.name}-${request.url}`"
          :value="index"
        >
          {{ request.name || `${request.method} ${request.url}` }}
        </option>
      </select>

      <label class="numeric-option">
        <span>Timeout</span>
        <input v-model.number="timeoutMs" type="number" min="100" max="120000" step="100" />
        <span>ms</span>
      </label>

      <label class="private-option">
        <input
          :checked="httpClientState.privateNetworkApproval !== null"
          data-testid="http-private-network"
          type="checkbox"
          :disabled="httpClientState.authorizingPrivateNetwork || !selectedHTTPRequest"
          @change="togglePrivateNetwork"
        />
        <span>{{ httpClientState.authorizingPrivateNetwork
          ? t("httpClient.authorizingPrivateNetwork")
          : t("httpClient.privateNetwork") }}</span>
      </label>

      <button
        v-if="!httpClientState.loading"
        data-testid="http-run"
        class="primary-command"
        type="button"
        :disabled="!selectedHTTPRequest || httpClientState.parsing || httpClientState.authorizingPrivateNetwork"
        @click="runSelected"
      >
        <VideoPlay aria-hidden="true" />
        <span>Run</span>
      </button>
      <button
        v-else
        class="icon-command"
        type="button"
        :title="t('a11y.cancelHttpRequest')"
        :aria-label="t('a11y.cancelHttpRequest')"
        @click="cancelActiveHTTPRequest"
      >
        <Close aria-hidden="true" />
      </button>
    </div>

    <div v-if="selectedHTTPRequest" class="request-line">
      <strong>{{ selectedHTTPRequest.method }}</strong>
      <span>{{ selectedHTTPRequest.url }}</span>
    </div>

    <div class="view-tabs" role="tablist" :aria-label="t('a11y.httpClientResults')">
      <button
        type="button"
        role="tab"
        :aria-selected="activeView === 'response'"
        :class="{ active: activeView === 'response' }"
        @click="activeView = 'response'"
      >
        Response
      </button>
      <button
        type="button"
        role="tab"
        :aria-selected="activeView === 'history'"
        :class="{ active: activeView === 'history' }"
        @click="activeView = 'history'"
      >
        History
      </button>
    </div>

    <div v-if="httpClientState.error" class="http-error" role="alert">
      {{ httpClientState.error }}
    </div>

    <div v-if="activeView === 'response'" class="response-view">
      <div v-if="httpClientState.response" class="response-summary">
        <span data-testid="http-status" :class="{ failed: httpClientState.response.status >= 400 }">
          {{ httpClientState.response.status }} {{ httpClientState.response.statusText }}
        </span>
        <span data-testid="http-duration">{{ httpClientState.response.durationMs }} ms</span>
        <span>{{ responseBodyBytes }} B</span>
      </div>

      <div v-if="httpClientState.response" class="response-grid">
        <section class="headers-section">
          <h3>Headers</h3>
          <dl data-testid="http-response-headers">
            <template v-for="([name, value]) in responseHeaders" :key="name">
              <dt>{{ name }}</dt>
              <dd>{{ value }}</dd>
            </template>
          </dl>
        </section>
        <section class="body-section">
          <h3>Body</h3>
          <pre data-testid="http-response-body">{{ formattedBody }}</pre>
        </section>
      </div>

      <div v-else-if="httpClientState.loading" class="empty-state">Sending...</div>
      <div v-else class="empty-state">No response</div>
    </div>

    <div v-else class="history-view">
      <div class="history-actions">
        <span>{{ httpClientState.history.length }} requests</span>
        <button
          class="icon-command"
          type="button"
          :title="t('a11y.clearHttpHistory')"
          :aria-label="t('a11y.clearHttpHistory')"
          :disabled="httpClientState.history.length === 0"
          @click="clearHTTPHistory"
        >
          <Delete aria-hidden="true" />
        </button>
      </div>
      <ol class="history-list">
        <li v-for="entry in httpClientState.history" :key="entry.id">
          <span class="history-method">{{ entry.method }}</span>
          <span class="history-name">{{ entry.name || entry.url }}</span>
          <span :class="{ failed: (entry.status ?? 0) >= 400 || entry.error }">
            {{ entry.error ? "Error" : entry.status }}
          </span>
          <span>{{ entry.durationMs }} ms</span>
        </li>
      </ol>
    </div>
  </section>
</template>

<style scoped>
.http-client-panel {
  display: flex;
  min-width: 0;
  height: 100%;
  color: var(--color-text-primary, #d7d7d7);
  background: var(--color-bg-base, #181818);
  flex-direction: column;
  font-size: 12px;
  container-type: inline-size;
}

.http-toolbar {
  display: flex;
  min-height: 40px;
  padding: 6px 10px;
  border-bottom: 1px solid var(--color-border-default, #343434);
  align-items: center;
  gap: 10px;
}

.request-select {
  min-width: 180px;
  max-width: 360px;
  height: 28px;
  padding: 0 8px;
  color: inherit;
  border: 1px solid var(--color-border-default, #424242);
  border-radius: 3px;
  background: var(--color-bg-surface-container, #242424);
}

.numeric-option,
.private-option {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  white-space: nowrap;
}

.numeric-option input {
  width: 78px;
  height: 26px;
  padding: 0 6px;
  color: inherit;
  border: 1px solid var(--color-border-default, #424242);
  border-radius: 3px;
  background: var(--color-bg-surface-container, #242424);
}

button {
  color: inherit;
  border: 0;
  background: transparent;
  cursor: pointer;
}

button:disabled {
  cursor: default;
  opacity: 0.45;
}

.primary-command {
  display: inline-flex;
  height: 28px;
  margin-left: auto;
  padding: 0 11px;
  color: var(--color-on-primary);
  border-radius: 3px;
  background: var(--color-primary);
  align-items: center;
  gap: 6px;
  transition: background var(--transition-fast);
}

.primary-command:hover:not(:disabled) {
  background: var(--color-primary-focus);
}

.primary-command svg,
.icon-command svg {
  width: 15px;
  height: 15px;
}

.icon-command {
  display: inline-grid;
  width: 28px;
  height: 28px;
  border-radius: 3px;
  place-items: center;
  transition: background var(--transition-fast);
}

.icon-command:hover:not(:disabled) {
  background: var(--chrome-hover-bg, #303030);
}

.request-line {
  display: flex;
  min-height: 34px;
  padding: 0 12px;
  border-bottom: 1px solid var(--color-border-default, #343434);
  align-items: center;
  gap: 10px;
  overflow: hidden;
}

.request-line strong {
  color: var(--color-success);
}

.request-line span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.view-tabs {
  display: flex;
  min-height: 34px;
  padding: 0 10px;
  border-bottom: 1px solid var(--color-border-default, #343434);
  gap: 16px;
}

.view-tabs button {
  padding: 0 2px;
  border-bottom: 2px solid transparent;
}

.view-tabs button.active {
  color: var(--color-text-primary);
  border-bottom-color: var(--color-primary);
}

.http-error {
  padding: 8px 12px;
  color: var(--color-error);
  border-bottom: 1px solid var(--color-error);
  background: var(--color-error-container);
}

.response-view,
.history-view {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
}

.response-summary,
.history-actions {
  display: flex;
  min-height: 34px;
  padding: 0 12px;
  border-bottom: 1px solid var(--color-border-default, #343434);
  align-items: center;
  gap: 16px;
}

.response-summary [data-testid="http-status"] {
  color: var(--color-success);
  font-weight: 600;
}

.failed {
  color: var(--color-error) !important;
}

.response-grid {
  display: grid;
  min-height: 0;
  flex: 1;
  grid-template-columns: minmax(220px, 30%) minmax(0, 1fr);
}

.headers-section,
.body-section {
  min-width: 0;
  min-height: 0;
  padding: 10px 12px;
  overflow: auto;
}

.headers-section {
  border-right: 1px solid var(--color-border-default, #343434);
}

h3 {
  margin: 0 0 9px;
  color: var(--color-text-secondary, #aaa);
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
}

dl {
  display: grid;
  margin: 0;
  grid-template-columns: minmax(90px, auto) minmax(0, 1fr);
  gap: 5px 10px;
}

dt {
  color: var(--color-primary);
}

dd {
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
}

pre {
  margin: 0;
  font-family: var(--font-mono, "Cascadia Code", Consolas, monospace);
  line-height: 1.55;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.empty-state {
  display: grid;
  min-height: 160px;
  color: var(--color-text-tertiary, #888);
  place-items: center;
}

.history-actions {
  justify-content: space-between;
}

.history-list {
  margin: 0;
  padding: 0;
  list-style: none;
  overflow: auto;
}

.history-list li {
  display: grid;
  min-height: 34px;
  padding: 0 12px;
  border-bottom: 1px solid var(--color-border-subtle, #2e2e2e);
  align-items: center;
  grid-template-columns: 60px minmax(0, 1fr) 60px 70px;
  gap: 8px;
}

.history-method {
  color: var(--color-primary);
  font-weight: 600;
}

.history-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 720px) {
  .http-toolbar {
    flex-wrap: wrap;
  }

  .request-select {
    width: 100%;
    max-width: none;
  }

  .primary-command {
    margin-left: 0;
  }

  .response-grid {
    display: block;
    overflow: auto;
  }

  .headers-section {
    border-right: 0;
    border-bottom: 1px solid var(--border-color, #343434);
  }
}

@container (max-width: 480px) {
  .http-toolbar {
    flex-wrap: wrap;
  }

  .request-select {
    width: 100%;
    max-width: none;
  }

  .primary-command {
    margin-left: auto;
  }

  .response-grid {
    display: block;
    overflow: auto;
  }

  .headers-section {
    border-right: 0;
    border-bottom: 1px solid var(--border-color, #343434);
  }
}
</style>
