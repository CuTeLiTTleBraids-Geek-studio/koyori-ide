<script setup lang="ts">
// Koyori IDE 组件 · Database Tool Window。
// 喵，这是 Database Tool Window，负责 Koyori IDE 的界面呈现喵~
import { computed, onMounted, ref } from "vue";
import {
  ArrowLeft,
  ArrowRight,
  Close,
  Connection,
  Delete,
  Key,
  VideoPlay,
} from "@element-plus/icons-vue";
import { useI18n } from "@/lib/i18n";
import {
  cancelDatabaseQuery,
  connectDatabase,
  databaseState,
  disconnectDatabase,
  loadDatabaseConnections,
  runDatabaseQuery,
  selectDatabaseConnection,
  selectDatabaseSchema,
  selectDatabaseTable,
  type DatabaseProviderName,
} from "@/stores/database";

const { t } = useI18n();
const provider = ref<DatabaseProviderName>("sqlite");
const connectionName = ref("");
const databasePath = ref("");
const credentialConfigId = ref("");
const defaultSchema = ref("");
const resultRowKeys = new WeakMap<unknown[], string>();
let resultRowKeySequence = 0;

function resultRowKey(row: unknown[]): string {
  const existing = resultRowKeys.get(row);
  if (existing) return existing;
  const key = `database-row-${++resultRowKeySequence}`;
  resultRowKeys.set(row, key);
  return key;
}

const activeConnection = computed(() => (
  databaseState.connections.find(({ id }) => id === databaseState.activeConnectionId) ?? null
));
const canConnect = computed(() => (
  provider.value === "sqlite" ? databasePath.value.trim() !== "" : credentialConfigId.value.trim() !== ""
));

onMounted(() => void loadDatabaseConnections());

async function connect(): Promise<void> {
  if (await connectDatabase({
    provider: provider.value,
    name: connectionName.value,
    databasePath: provider.value === "sqlite" ? databasePath.value : undefined,
    credentialConfigId: provider.value === "sqlite" ? undefined : credentialConfigId.value,
    defaultSchema: provider.value === "sqlite" ? "main" : defaultSchema.value,
  })) {
    connectionName.value = "";
    databasePath.value = "";
    credentialConfigId.value = "";
    defaultSchema.value = "";
  }
}

function formatCell(value: unknown): string {
  if (value === null || value === undefined) return t("database.null");
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") {
    return String(value);
  }
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}
</script>

<template>
  <section class="database-tool" :aria-label="t('database.panelAria')">
    <form class="database-tool__connect" @submit.prevent="connect">
      <select
        v-model="provider"
        data-test="database-provider"
        :aria-label="t('database.provider')"
      >
        <option value="sqlite">SQLite</option>
        <option value="postgres">PostgreSQL</option>
        <option value="mysql">MySQL</option>
      </select>
      <input
        v-model="connectionName"
        data-test="database-name"
        type="text"
        :placeholder="t('database.connectionName')"
        :aria-label="t('database.connectionName')"
      />
      <input
        v-model="databasePath"
        v-if="provider === 'sqlite'"
        data-test="database-path"
        class="database-tool__path"
        type="text"
        :placeholder="t('database.path')"
        :aria-label="t('database.path')"
        autocomplete="off"
      />
      <template v-else>
        <input
          v-model="credentialConfigId"
          data-test="database-credential-config"
          class="database-tool__path"
          type="text"
          :placeholder="t('database.credentialConfigId')"
          :aria-label="t('database.credentialConfigId')"
          autocomplete="off"
        />
        <input
          v-model="defaultSchema"
          data-test="database-default-schema"
          class="database-tool__schema-input"
          type="text"
          :placeholder="t('database.defaultSchema')"
          :aria-label="t('database.defaultSchema')"
          autocomplete="off"
        />
      </template>
      <button
        data-test="database-connect"
        class="database-tool__primary"
        type="submit"
        :disabled="databaseState.connecting || !canConnect"
      >
        <Connection aria-hidden="true" />
        <span>{{ databaseState.connecting ? t("database.connecting") : t("database.connect") }}</span>
      </button>
    </form>

    <div class="database-tool__connection-bar">
      <span class="database-tool__readonly">{{ t("database.readOnly") }}</span>
      <select
        data-test="database-connection"
        :value="databaseState.activeConnectionId ?? ''"
        :aria-label="t('database.connection')"
        @change="selectDatabaseConnection(($event.target as HTMLSelectElement).value)"
      >
        <option value="">{{ t("database.noConnection") }}</option>
        <option v-for="item in databaseState.connections" :key="item.id" :value="item.id">
          {{ item.name }} ({{ item.provider }})
        </option>
      </select>
      <select
        v-if="databaseState.activeConnectionId && databaseState.schemas.length > 0"
        data-test="database-schema"
        :value="databaseState.activeSchema ?? ''"
        :aria-label="t('database.schemaSelector')"
        @change="selectDatabaseSchema(($event.target as HTMLSelectElement).value)"
      >
        <option v-for="schema in databaseState.schemas" :key="schema.name" :value="schema.name">
          {{ schema.name }}
        </option>
      </select>
      <button
        class="database-tool__icon"
        data-test="database-disconnect"
        type="button"
        :title="t('database.disconnect')"
        :aria-label="t('database.disconnect')"
        :disabled="!activeConnection"
        @click="activeConnection && disconnectDatabase(activeConnection.id)"
      >
        <Delete aria-hidden="true" />
      </button>
    </div>

    <div v-if="databaseState.error" class="database-tool__error" role="alert">
      {{ databaseState.error }}
    </div>

    <div class="database-tool__workspace">
      <aside class="database-tool__schema">
        <h2>{{ t("database.schema") }}</h2>
        <div v-if="databaseState.schemaLoading" class="database-tool__empty" role="status">
          {{ t("database.loadingSchema") }}
        </div>
        <div v-else-if="databaseState.tables.length === 0" class="database-tool__empty">
          {{ t("database.emptySchema") }}
        </div>
        <button
          v-for="table in databaseState.tables"
          v-else
          :key="table.name"
          type="button"
          class="database-tool__table"
          :class="{ 'database-tool__table--active': databaseState.selectedTable === table.name }"
          :data-table="table.name"
          @click="selectDatabaseTable(table.name)"
        >
          <span class="database-tool__table-dot" aria-hidden="true" />
          <span class="database-tool__table-name">{{ table.name }}</span>
          <span class="database-tool__table-type">{{ table.type }}</span>
        </button>

        <section v-if="databaseState.selectedTable" class="database-tool__columns">
          <h3>{{ t("database.columns") }}</h3>
          <div v-for="column in databaseState.columns" :key="column.name" class="database-tool__column">
            <Key v-if="column.primaryKey" :aria-label="t('database.primaryKey')" />
            <span v-else class="database-tool__column-spacer" />
            <span class="database-tool__column-name">{{ column.name }}</span>
            <span class="database-tool__column-type">{{ column.dataType || "-" }}</span>
          </div>
        </section>
      </aside>

      <main class="database-tool__query">
        <div class="database-tool__query-head">
          <h2>{{ t("database.query") }}</h2>
          <label>
            <span>{{ t("database.pageSize") }}</span>
            <select v-model.number="databaseState.pageSize" data-test="database-page-size">
              <option :value="50">50</option>
              <option :value="100">100</option>
              <option :value="250">250</option>
              <option :value="500">500</option>
            </select>
          </label>
          <button
            v-if="databaseState.queryRunning"
            class="database-tool__icon database-tool__stop"
            data-test="database-cancel"
            type="button"
            :title="t('database.cancel')"
            :aria-label="t('database.cancel')"
            @click="cancelDatabaseQuery"
          >
            <Close aria-hidden="true" />
          </button>
          <button
            v-else
            class="database-tool__primary"
            data-test="database-run"
            type="button"
            :disabled="!databaseState.activeConnectionId || !databaseState.sql.trim()"
            @click="runDatabaseQuery(0)"
          >
            <VideoPlay aria-hidden="true" />
            <span>{{ t("database.run") }}</span>
          </button>
        </div>

        <textarea
          v-model="databaseState.sql"
          data-test="database-sql"
          class="database-tool__editor"
          spellcheck="false"
          :aria-label="t('database.query')"
        />

        <div class="database-tool__result-head">
          <span>{{ t("database.results") }}</span>
          <template v-if="databaseState.result">
            <span class="database-tool__meta">
              {{ databaseState.result.rows.length }} {{ t("database.rows") }}
            </span>
            <span class="database-tool__meta">{{ databaseState.result.durationMs }} ms</span>
            <span class="database-tool__spacer" />
            <button
              class="database-tool__icon"
              data-test="database-previous"
              type="button"
              :disabled="databaseState.queryRunning || databaseState.result.page === 0"
              :title="t('database.previousPage')"
              :aria-label="t('database.previousPage')"
              @click="runDatabaseQuery(databaseState.result.page - 1)"
            >
              <ArrowLeft aria-hidden="true" />
            </button>
            <span class="database-tool__page">
              {{ t("database.page", { page: databaseState.result.page + 1 }) }}
            </span>
            <button
              class="database-tool__icon"
              data-test="database-next"
              type="button"
              :disabled="databaseState.queryRunning || !databaseState.result.hasMore"
              :title="t('database.nextPage')"
              :aria-label="t('database.nextPage')"
              @click="runDatabaseQuery(databaseState.result.page + 1)"
            >
              <ArrowRight aria-hidden="true" />
            </button>
          </template>
        </div>

        <div v-if="databaseState.result" class="database-tool__result" data-test="database-result">
          <table>
            <thead>
              <tr>
                <th v-for="column in databaseState.result.columns" :key="column.name">
                  <span>{{ column.name }}</span>
                  <small>{{ column.databaseType }}</small>
                </th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in databaseState.result.rows" :key="resultRowKey(row)">
                <td
                  v-for="(column, columnIndex) in databaseState.result.columns"
                  :key="column.name"
                  :title="formatCell(row[columnIndex])"
                >
                  {{ formatCell(row[columnIndex]) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="database-tool__empty database-tool__result-empty">
          {{ t("database.noResults") }}
        </div>
      </main>
    </div>
  </section>
</template>

<style scoped>
.database-tool {
  display: flex;
  min-width: 0;
  height: 100%;
  color: var(--color-text-primary, #d7d7d7);
  background: var(--color-bg-base, #181818);
  flex-direction: column;
  font-size: 12px;
  container-type: inline-size;
}

.database-tool__connect,
.database-tool__connection-bar,
.database-tool__query-head,
.database-tool__result-head {
  display: flex;
  min-height: 38px;
  padding: 5px 10px;
  border-bottom: 1px solid var(--color-border-default, #343434);
  align-items: center;
  gap: 8px;
}

input,
select,
textarea {
  color: inherit;
  border: 1px solid var(--color-border-default, #424242);
  border-radius: 3px;
  outline: none;
  background: var(--color-bg-surface-container, #242424);
}

input:focus-visible,
select:focus-visible,
textarea:focus-visible {
  border-color: var(--color-primary);
  outline: 2px solid var(--color-primary-focus);
  outline-offset: -2px;
}

input,
select {
  height: 28px;
  padding: 0 8px;
}

.database-tool__connect [data-test="database-provider"] {
  width: 112px;
}

.database-tool__connect [data-test="database-name"] {
  width: min(180px, 22%);
}

.database-tool__path {
  min-width: 120px;
  flex: 1;
}

.database-tool__schema-input {
  width: min(150px, 20%);
}

button {
  color: inherit;
  border: 0;
  background: transparent;
  cursor: pointer;
}

button:disabled {
  cursor: default;
  opacity: 0.42;
}

.database-tool__primary {
  display: inline-flex;
  min-width: 78px;
  height: 28px;
  padding: 0 10px;
  color: var(--color-on-primary);
  border-radius: 3px;
  background: var(--color-primary);
  align-items: center;
  justify-content: center;
  gap: 6px;
  transition: background var(--transition-fast);
}

.database-tool__primary:hover:not(:disabled) {
  background: var(--color-primary-focus);
}

.database-tool__primary svg,
.database-tool__icon svg {
  width: 15px;
  height: 15px;
}

.database-tool__connection-bar select {
  min-width: 180px;
  max-width: 360px;
}

.database-tool__readonly {
  padding: 2px 6px;
  color: var(--color-success);
  border: 1px solid var(--color-success);
  border-radius: 3px;
  background: var(--color-success-container);
  font-size: 11px;
}

.database-tool__icon {
  display: inline-grid;
  width: 28px;
  height: 28px;
  border-radius: 3px;
  flex: 0 0 28px;
  place-items: center;
  transition: background var(--transition-fast);
}

.database-tool__icon:hover:not(:disabled) {
  background: var(--chrome-hover-bg, #303030);
}

.database-tool__stop {
  color: var(--color-error);
}

.database-tool__error {
  padding: 7px 10px;
  color: var(--color-error);
  border-bottom: 1px solid var(--color-error);
  background: var(--color-error-container);
}

.database-tool__workspace {
  display: grid;
  min-height: 0;
  flex: 1;
  grid-template-columns: minmax(190px, 24%) minmax(0, 1fr);
}

.database-tool__schema,
.database-tool__query {
  min-width: 0;
  min-height: 0;
}

.database-tool__schema {
  border-right: 1px solid var(--color-border-default, #343434);
  overflow: auto;
}

h2,
h3 {
  margin: 0;
  color: var(--color-text-secondary, #aaa);
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
}

.database-tool__schema > h2 {
  padding: 11px 10px 7px;
}

.database-tool__table {
  display: grid;
  width: 100%;
  min-height: 30px;
  padding: 0 9px;
  align-items: center;
  grid-template-columns: 8px minmax(0, 1fr) auto;
  gap: 7px;
  text-align: left;
  transition: background var(--transition-fast);
}

.database-tool__table:hover,
.database-tool__table--active {
  background: var(--chrome-hover-bg, #303030);
}

.database-tool__table--active {
  box-shadow: inset 2px 0 var(--color-primary);
}

.database-tool__table-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-warning);
}

.database-tool__table-name,
.database-tool__column-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.database-tool__table-type,
.database-tool__column-type,
.database-tool__meta {
  color: var(--color-text-tertiary, #8d8d8d);
  font-size: 11px;
}

.database-tool__columns {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--color-border-default, #343434);
}

.database-tool__columns h3 {
  padding: 0 10px 6px;
}

.database-tool__column {
  display: grid;
  min-height: 27px;
  padding: 0 9px;
  align-items: center;
  grid-template-columns: 13px minmax(0, 1fr) auto;
  gap: 6px;
}

.database-tool__column svg {
  width: 12px;
  height: 12px;
  color: var(--color-warning);
}

.database-tool__column-spacer {
  width: 12px;
}

.database-tool__query {
  display: grid;
  grid-template-rows: auto 116px auto minmax(0, 1fr);
}

.database-tool__query-head h2 {
  margin-right: auto;
}

.database-tool__query-head label {
  display: inline-flex;
  color: var(--color-text-secondary, #aaa);
  align-items: center;
  gap: 6px;
}

.database-tool__query-head select {
  width: 70px;
}

.database-tool__editor {
  width: 100%;
  min-width: 0;
  padding: 9px 11px;
  border: 0;
  border-bottom: 1px solid var(--color-border-default, #343434);
  border-radius: 0;
  resize: none;
  font-family: var(--font-mono, "Cascadia Code", Consolas, monospace);
  font-size: 12px;
  line-height: 1.5;
}

.database-tool__result-head {
  min-height: 34px;
}

.database-tool__spacer {
  flex: 1;
}

.database-tool__page {
  min-width: 52px;
  text-align: center;
}

.database-tool__result {
  min-width: 0;
  min-height: 0;
  overflow: auto;
}

table {
  width: max-content;
  min-width: 100%;
  border-collapse: collapse;
  font-family: var(--font-mono, "Cascadia Code", Consolas, monospace);
}

th,
td {
  min-width: 120px;
  max-width: 320px;
  height: 30px;
  padding: 0 9px;
  border-right: 1px solid var(--color-border-default, #343434);
  border-bottom: 1px solid var(--color-border-subtle, #2e2e2e);
  overflow: hidden;
  text-align: left;
  text-overflow: ellipsis;
  white-space: nowrap;
}

th {
  position: sticky;
  z-index: 1;
  top: 0;
  background: var(--color-bg-surface-container, #242424);
  font-family: inherit;
  font-weight: 600;
}

th small {
  margin-left: 7px;
  color: var(--color-text-tertiary, #888);
  font-size: 10px;
  font-weight: 400;
}

tbody tr:hover {
  background: var(--chrome-hover-bg, #292929);
}

.database-tool__empty {
  padding: 18px 10px;
  color: var(--color-text-tertiary, #888);
  text-align: center;
}

.database-tool__result-empty {
  display: grid;
  min-height: 120px;
  place-items: center;
}

@container (max-width: 620px) {
  .database-tool__connect {
    flex-wrap: wrap;
  }

  .database-tool__connect input:first-child,
  .database-tool__connect [data-test="database-name"],
  .database-tool__path,
  .database-tool__schema-input {
    width: 100%;
  }

  .database-tool__workspace {
    grid-template-columns: 150px minmax(0, 1fr);
  }

  .database-tool__query-head label span {
    display: none;
  }
}
</style>
