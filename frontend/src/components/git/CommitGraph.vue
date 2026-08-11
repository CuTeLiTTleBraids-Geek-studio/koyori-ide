<script setup lang="ts">
// Koyori IDE 组件 · Commit Graph；交互服务：Git 集成（GitService）。
// 喵，这是 Commit Graph，负责 Koyori IDE 的界面呈现喵~
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Refresh } from "@element-plus/icons-vue";
import { gitService, type GitCommitGraphEntry } from "@/api/services";
import EmptyState from "@/components/common/EmptyState.vue";

const props = withDefaults(defineProps<{
  repoPath: string;
  branch?: string;
}>(), {
  branch: "",
});

type GraphScope = "current" | "all";

interface GraphRow {
  commit: GitCommitGraphEntry;
  lane: number;
  laneCount: number;
  lanes: Array<{ id: string; position: number }>;
  parentLanes: Array<{ id: string; position: number }>;
}

const entries = ref<GitCommitGraphEntry[]>([]);
const loading = ref(false);
const error = ref("");
const scope = ref<GraphScope>("current");
const limit = ref(50);
const selectedHash = ref("");
let generation = 0;
let disposed = false;
let laneKeySequence = 0;

function graphRows(commits: GitCommitGraphEntry[]): GraphRow[] {
  let activeLanes: string[] = [];

  return commits.map((commit) => {
    let lane = activeLanes.indexOf(commit.hash);
    if (lane < 0) {
      lane = activeLanes.length;
      activeLanes.push(commit.hash);
    }

    const lanesBefore = [...activeLanes];
    const nextLanes = [...activeLanes];
    // Bindings 生成 string[] | null（Go []string 切片可 nil），运行时 parseCommitGraph
    // 总是返回非 nil 切片，此处 ?? [] 仅用于通过 vue-tsc 严格空检查。
    const parents = commit.parents ?? [];
    if (parents[0]) {
      nextLanes[lane] = parents[0];
    } else {
      nextLanes.splice(lane, 1);
    }

    for (const parent of parents.slice(1)) {
      if (!nextLanes.includes(parent)) {
        nextLanes.splice(lane + 1, 0, parent);
      }
    }

    for (let index = nextLanes.length - 1; index >= 0; index -= 1) {
      if (nextLanes.indexOf(nextLanes[index]) !== index) {
        nextLanes.splice(index, 1);
      }
    }

    const parentLanePositions = parents
      .map((parent) => nextLanes.indexOf(parent))
      .filter((parentLane) => parentLane >= 0);
    const laneCount = Math.max(1, lanesBefore.length, nextLanes.length);
    activeLanes = nextLanes;

    return {
      commit,
      lane,
      laneCount,
      lanes: Array.from({ length: laneCount }, (_, position) => ({
        id: `graph-lane-${++laneKeySequence}`,
        position,
      })),
      parentLanes: parentLanePositions.map((position) => ({
        id: `graph-connector-${++laneKeySequence}`,
        position,
      })),
    };
  });
}

const rows = computed(() => graphRows(entries.value));
const selectedCommit = computed(() =>
  entries.value.find((commit) => commit.hash === selectedHash.value) ?? null,
);

function shortHash(hash: string): string {
  return hash.slice(0, 8);
}

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function connectorStyle(lane: number, parentLane: number): Record<string, string> {
  const start = Math.min(lane, parentLane) * 16 + 7;
  const width = Math.abs(parentLane - lane) * 16;
  return { left: `${start}px`, width: `${width}px` };
}

async function loadGraph(): Promise<void> {
  const requestGeneration = ++generation;
  const repoPath = props.repoPath;
  if (!repoPath) {
    entries.value = [];
    selectedHash.value = "";
    loading.value = false;
    error.value = "";
    return;
  }

  loading.value = true;
  error.value = "";
  try {
    const commits = await gitService.getCommitGraph(
      repoPath,
      Math.min(200, Math.max(1, limit.value)),
      scope.value === "current" ? props.branch : "",
      scope.value === "all",
    );
    if (disposed || requestGeneration !== generation || props.repoPath !== repoPath) return;
    entries.value = commits ?? [];
    if (!entries.value.some((commit) => commit.hash === selectedHash.value)) {
      selectedHash.value = "";
    }
  } catch (loadError) {
    if (disposed || requestGeneration !== generation) return;
    entries.value = [];
    selectedHash.value = "";
    error.value = loadError instanceof Error ? loadError.message : String(loadError);
  } finally {
    if (!disposed && requestGeneration === generation) {
      loading.value = false;
    }
  }
}

function selectScope(nextScope: GraphScope): void {
  scope.value = nextScope;
}

watch(
  () => [props.repoPath, props.branch, scope.value, limit.value] as const,
  () => void loadGraph(),
  { immediate: true },
);

onBeforeUnmount(() => {
  disposed = true;
  generation += 1;
});
</script>

<template>
  <section class="commit-graph" aria-label="Commit graph">
    <header class="commit-graph__header">
      <h3 class="commit-graph__title">Commit graph</h3>
      <div class="commit-graph__controls">
        <div class="commit-graph__scope" role="group" aria-label="Commit scope">
          <button
            type="button"
            data-scope="current"
            :class="{ 'is-active': scope === 'current' }"
            :aria-pressed="scope === 'current'"
            @click="selectScope('current')"
          >
            Current
          </button>
          <button
            type="button"
            data-scope="all"
            :class="{ 'is-active': scope === 'all' }"
            :aria-pressed="scope === 'all'"
            @click="selectScope('all')"
          >
            All
          </button>
        </div>
        <select v-model.number="limit" class="commit-graph__limit" aria-label="Commit limit">
          <option :value="25">25</option>
          <option :value="50">50</option>
          <option :value="100">100</option>
          <option :value="200">200</option>
        </select>
        <button
          type="button"
          class="commit-graph__refresh"
          aria-label="Refresh commit graph"
          title="Refresh commit graph"
          @click="loadGraph"
        >
          <Refresh />
        </button>
      </div>
    </header>

    <div v-if="loading" class="commit-graph__state">Loading commits...</div>
    <div v-else-if="error" class="commit-graph__state commit-graph__error">
      <span>{{ error }}</span>
      <button type="button" @click="loadGraph">Retry</button>
    </div>
    <EmptyState
      v-else-if="rows.length === 0"
      class="commit-graph__empty"
      icon="📭"
      title="No commits yet"
      description="Make your first commit to see the commit graph."
    />
    <div v-else class="commit-graph__list">
      <template v-for="row in rows" :key="row.commit.hash">
        <button
          type="button"
          class="commit-graph__row"
          :class="{ 'is-selected': selectedHash === row.commit.hash }"
          :data-hash="row.commit.hash"
          :data-lane="row.lane"
          @click="selectedHash = row.commit.hash"
        >
          <span
            class="commit-graph__track"
            :style="{ width: `${row.laneCount * 16}px` }"
            aria-hidden="true"
          >
            <span
              v-for="lane in row.lanes"
              :key="lane.id"
              class="commit-graph__lane"
              :style="{ left: `${lane.position * 16 + 7}px` }"
            />
            <span
              v-for="connector in row.parentLanes.filter(({ position }) => position !== row.lane)"
              :key="connector.id"
              class="commit-graph__connector"
              :style="connectorStyle(row.lane, connector.position)"
            />
            <span class="commit-graph__node" :style="{ left: `${row.lane * 16 + 2}px` }" />
          </span>
          <span class="commit-graph__content">
            <span class="commit-graph__subject">{{ row.commit.subject }}</span>
            <span class="commit-graph__meta">
              <code>{{ shortHash(row.commit.hash) }}</code>
              <span>{{ row.commit.author }}</span>
              <time :datetime="row.commit.time">{{ formatTime(row.commit.time) }}</time>
            </span>
            <span v-if="(row.commit.refs ?? []).length" class="commit-graph__refs">
              <span v-for="refName in (row.commit.refs ?? [])" :key="refName">{{ refName }}</span>
            </span>
          </span>
        </button>

        <div v-if="selectedHash === row.commit.hash && selectedCommit" class="commit-graph__details">
          <dl>
            <dt>Commit</dt>
            <dd><code>{{ selectedCommit.hash }}</code></dd>
            <dt>Parents</dt>
            <dd>
              <code v-for="parent in (selectedCommit.parents ?? [])" :key="parent">{{ parent }}</code>
              <span v-if="(selectedCommit.parents ?? []).length === 0">None</span>
            </dd>
            <dt>Author</dt>
            <dd>{{ selectedCommit.author }} &lt;{{ selectedCommit.email }}&gt;</dd>
            <dt>Time</dt>
            <dd><time :datetime="selectedCommit.time">{{ formatTime(selectedCommit.time) }}</time></dd>
            <dt>Refs</dt>
            <dd>{{ (selectedCommit.refs ?? []).join(', ') || 'None' }}</dd>
            <dt>Subject</dt>
            <dd>{{ selectedCommit.subject }}</dd>
          </dl>
        </div>
      </template>
    </div>
  </section>
</template>

<style scoped>
.commit-graph {
  color: var(--color-text-primary);
  font-size: 12px;
}

.commit-graph__header,
.commit-graph__controls,
.commit-graph__scope,
.commit-graph__meta,
.commit-graph__refs {
  display: flex;
  align-items: center;
}

.commit-graph__header {
  min-height: 34px;
  padding: 0 8px;
  border-bottom: 1px solid var(--color-border-default);
  justify-content: space-between;
  gap: 8px;
}

.commit-graph__title {
  margin: 0;
  font-size: 12px;
  font-weight: 600;
}

.commit-graph__controls {
  gap: 5px;
}

.commit-graph__scope {
  overflow: hidden;
  border: 1px solid var(--color-border-default);
  border-radius: 4px;
}

.commit-graph__scope button,
.commit-graph__refresh,
.commit-graph__error button {
  border: 0;
  color: var(--color-text-secondary);
  background: transparent;
  cursor: pointer;
}

.commit-graph__scope button {
  min-width: 42px;
  height: 24px;
  padding: 0 7px;
  font-size: 11px;
}

.commit-graph__scope button + button {
  border-left: 1px solid var(--color-border-default);
}

.commit-graph__scope button.is-active {
  color: var(--color-text-primary);
  background: var(--color-bg-surface-container-high);
}

.commit-graph__limit {
  width: 50px;
  height: 26px;
  border: 1px solid var(--color-border-default);
  border-radius: 4px;
  color: var(--color-text-primary);
  background: var(--color-bg-surface);
  font-size: 11px;
}

.commit-graph__refresh {
  display: grid;
  width: 26px;
  height: 26px;
  padding: 5px;
  place-items: center;
  border-radius: 4px;
}

.commit-graph__refresh:hover,
.commit-graph__error button:hover {
  color: var(--color-primary);
  background: var(--color-bg-surface-container);
}

.commit-graph__state {
  padding: 18px 10px;
  color: var(--color-text-tertiary);
  text-align: center;
}

.commit-graph__error {
  color: var(--color-error);
}

.commit-graph__error button {
  margin-left: 8px;
}

.commit-graph__row {
  display: grid;
  width: 100%;
  min-height: 54px;
  padding: 5px 8px;
  border: 0;
  border-bottom: 1px solid var(--color-border-subtle);
  grid-template-columns: minmax(24px, auto) minmax(0, 1fr);
  gap: 7px;
  color: inherit;
  background: transparent;
  text-align: left;
  cursor: pointer;
}

.commit-graph__row:hover,
.commit-graph__row.is-selected {
  background: var(--color-bg-surface-container);
}

.commit-graph__row.is-selected {
  box-shadow: inset 2px 0 0 var(--color-primary);
}

.commit-graph__track {
  position: relative;
  display: block;
  min-width: 16px;
  height: 100%;
}

.commit-graph__lane,
.commit-graph__connector {
  position: absolute;
  background: var(--color-text-disabled);
}

.commit-graph__lane {
  top: -5px;
  bottom: -6px;
  width: 1px;
}

.commit-graph__connector {
  top: 50%;
  height: 1px;
}

.commit-graph__node {
  position: absolute;
  top: calc(50% - 5px);
  width: 10px;
  height: 10px;
  border: 2px solid var(--color-primary);
  border-radius: 50%;
  background: var(--color-bg-base);
  box-sizing: border-box;
}

.commit-graph__content,
.commit-graph__subject {
  min-width: 0;
}

.commit-graph__content {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.commit-graph__subject {
  overflow: hidden;
  color: var(--color-text-primary);
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.commit-graph__meta {
  min-width: 0;
  gap: 7px;
  color: var(--color-text-tertiary);
  font-size: 11px;
}

.commit-graph__meta span,
.commit-graph__meta time {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.commit-graph__refs {
  flex-wrap: wrap;
  gap: 4px;
}

.commit-graph__refs span {
  max-width: 100%;
  padding: 1px 4px;
  overflow: hidden;
  border: 1px solid var(--color-border-default);
  border-radius: 3px;
  color: var(--color-success);
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.commit-graph__details {
  padding: 8px 10px 10px 38px;
  border-bottom: 1px solid var(--color-border-default);
  background: var(--color-bg-surface-container-low);
}

.commit-graph__details dl {
  display: grid;
  margin: 0;
  grid-template-columns: 52px minmax(0, 1fr);
  gap: 4px 8px;
}

.commit-graph__details dt {
  color: var(--color-text-tertiary);
}

.commit-graph__details dd {
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
}

.commit-graph__details dd code {
  display: block;
}
</style>
