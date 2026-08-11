<script setup lang="ts">
// Koyori IDE 组件 · Command Palette。
// 喵，这是 Command Palette，负责 Koyori IDE 的界面呈现喵~
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from "vue";
import type { Command } from "@/types";
import { useI18n } from "@/lib/i18n";
import { commandRegistry } from "@/lib/commands";
import { matchIndices, scoreMatch } from "@/lib/fuzzy";

const props = defineProps<{
  visible: boolean;
  commands: Command[];
}>();

const emit = defineEmits<{
  (e: "close"): void;
  (e: "run", command: Command): void;
}>();

const { t } = useI18n();

const query = ref("");
const debouncedQuery = ref("");
const selectedIndex = ref(0);
const inputRef = ref<HTMLInputElement | null>(null);
const dialogRef = ref<HTMLElement | null>(null);
// N-126: remember the element that had focus before opening so we can
// restore it when the dialog closes.
let previouslyFocused: HTMLElement | null = null;
let searchTimer: ReturnType<typeof setTimeout> | null = null;
let unsubscribeRegistry: (() => void) | null = null;
const registryVersion = ref(0);

// G-VSC-04: source priority for the unified palette. Built-in commands
// (undefined source) rank highest, then native plugins, then VS Code
// extensions. Lower number = higher priority. Used for a stable sort so
// native commands always appear before VS Code extension commands in the
// filtered results, regardless of input order.
function sourcePriority(source: Command["source"]): number {
  if (source === "native") return 1;
  if (source === "vscode") return 2;
  return 0; // built-in
}

const availableCommands = computed<Command[]>(() => {
  void registryVersion.value;
  const ids = new Set(props.commands.map((command) => command.id));
  const registeredOnly = commandRegistry.list()
    .filter((command) => !ids.has(command.id))
    .map<Command>((command) => ({
      id: command.id,
      label: command.title,
      shortcut: command.keybinding,
      disabled: command.disabled,
      disabledReason: command.disabledReason,
      action: () => { void commandRegistry.execute(command.id); },
    }));
  return [...props.commands, ...registeredOnly];
});

const filtered = computed(() => {
  const q = debouncedQuery.value.trim();
  const list = q
    ? availableCommands.value
      .map((command) => {
        const registered = commandRegistry.get(command.id);
        const searchText = [
          command.label,
          command.shortcut ?? "",
          registered?.category ?? "",
          ...(registered?.keywords ?? []),
        ].join(" ");
        const indices = matchIndices(searchText, q);
        return indices === null
          ? null
          : { command, score: scoreMatch(searchText, indices) };
      })
      .filter((entry): entry is { command: Command; score: number } => entry !== null)
      .sort((left, right) => right.score - left.score)
      .map(({ command }) => command)
    : availableCommands.value;
  // Stable sort by source priority so native commands come before VS Code
  // extension commands. Array.prototype.sort is stable in modern engines.
  return [...list].sort(
    (a, b) => sourcePriority(a.source) - sourcePriority(b.source),
  );
});

// G-VSC-04: labels that appear more than once in the filtered set. When a
// label collides, the source badge is always shown so the user can
// disambiguate (e.g. a native plugin and a VS Code extension offering the
// same command). Non-colliding native/vscode commands also show a badge.
const duplicateLabels = computed(() => {
  const counts = new Map<string, number>();
  for (const c of filtered.value) {
    counts.set(c.label, (counts.get(c.label) ?? 0) + 1);
  }
  const dupes = new Set<string>();
  for (const [label, count] of counts) {
    if (count > 1) dupes.add(label);
  }
  return dupes;
});

// G-VSC-04: decide whether to show the source badge for a command. Always
// show for native/vscode commands; for built-ins only when their label
// collides with another command (rare, but keeps disambiguation consistent).
function shouldShowSource(cmd: Command): boolean {
  if (cmd.source === "native" || cmd.source === "vscode") return true;
  return duplicateLabels.value.has(cmd.label);
}

// G-VSC-04: short badge text for a command source.
function sourceBadge(cmd: Command): string {
  if (cmd.source === "native") return t("commandPalette.sourceNative");
  if (cmd.source === "vscode") return t("commandPalette.sourceVscode");
  return t("commandPalette.sourceBuiltin");
}

watch(
  () => props.visible,
  (v) => {
    if (v) {
      // N-126: capture the trigger element so we can restore focus on close.
      previouslyFocused = document.activeElement as HTMLElement | null;
      query.value = "";
      debouncedQuery.value = "";
      selectedIndex.value = 0;
      nextTick(() => inputRef.value?.focus());
    } else {
      // N-126: restore focus to the trigger element on close.
      previouslyFocused?.focus?.();
      previouslyFocused = null;
    }
  },
);

watch(query, (value) => {
  if (searchTimer) clearTimeout(searchTimer);
  if (!value.trim()) {
    debouncedQuery.value = "";
    return;
  }
  searchTimer = setTimeout(() => {
    searchTimer = null;
    debouncedQuery.value = value;
  }, 300);
});

watch(filtered, () => {
  selectedIndex.value = 0;
});

function handleKeydown(e: KeyboardEvent) {
  if (e.key === "ArrowDown") {
    e.preventDefault();
    if (filtered.value.length === 0) return;
    selectedIndex.value = Math.min(
      selectedIndex.value + 1,
      filtered.value.length - 1,
    );
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    if (filtered.value.length === 0) return;
    selectedIndex.value = Math.max(selectedIndex.value - 1, 0);
  } else if (e.key === "Enter") {
    e.preventDefault();
    if (searchTimer) {
      clearTimeout(searchTimer);
      searchTimer = null;
      debouncedQuery.value = query.value;
    }
    const cmd = filtered.value[selectedIndex.value];
    if (cmd && !cmd.disabled) emit("run", cmd);
  } else if (e.key === "Escape") {
    emit("close");
  }
}

// N-126: focus trap when Tab/Shift+Tab is pressed inside the dialog,
// cycle focus among the dialog's focusable elements instead of letting
// it escape to the underlying UI.
function handleTab(e: KeyboardEvent) {
  const root = dialogRef.value;
  if (!root) return;
  const focusable = Array.from(
    root.querySelectorAll<HTMLElement>(
      'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
    ),
  ).filter((element) =>
    !element.hasAttribute("disabled")
    && element.getAttribute("aria-hidden") !== "true"
    && !element.hasAttribute("hidden")
    && !element.closest("[inert]")
    && element.tabIndex >= 0
  );
  if (focusable.length === 0) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (e.shiftKey) {
    if (document.activeElement === first || document.activeElement === root) {
      e.preventDefault();
      last.focus();
    }
  } else {
    if (document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }
}

onMounted(() => {
  unsubscribeRegistry = commandRegistry.subscribe(() => {
    registryVersion.value += 1;
  });
});

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer);
  unsubscribeRegistry?.();
  unsubscribeRegistry = null;
});
</script>

<template>
  <transition name="cmd-fade">
    <div
      v-if="visible"
      class="command-palette-overlay"
    >
      <button
        type="button"
        class="dialog-backdrop-button"
        tabindex="-1"
        :aria-label="t('a11y.closeDialog')"
        @click="emit('close')"
      />
      <div
        ref="dialogRef"
        class="command-palette"
        role="dialog"
        aria-modal="true"
        :aria-label="t('commandPalette.title')"
        tabindex="-1"
        @keydown.tab="handleTab"
      >
        <input
          ref="inputRef"
          v-model="query"
          class="command-palette__input"
          :placeholder="t('commandPalette.placeholder')"
          :aria-label="t('commandPalette.inputAria')"
          role="combobox"
          aria-expanded="true"
          :aria-activedescendant="
            selectedIndex >= 0 && filtered[selectedIndex]
              ? `cmd-item-${selectedIndex}`
              : undefined
          "
          @keydown="handleKeydown"
        />
        <div
          class="command-palette__list"
          role="listbox"
          :aria-label="t('commandPalette.title')"
          aria-live="polite"
        >
          <div v-if="filtered.length === 0" class="command-palette__empty">
            {{ t("commandPalette.noMatches") }}
          </div>
          <button
            v-for="(cmd, i) in filtered"
            :id="`cmd-item-${i}`"
            :key="cmd.id"
            type="button"
            class="command-palette__item"
            :class="{ 'command-palette__item--active': i === selectedIndex }"
            role="option"
            tabindex="-1"
            :aria-selected="i === selectedIndex"
            :aria-disabled="cmd.disabled || undefined"
            :disabled="cmd.disabled"
            :title="cmd.disabledReason"
            @click="emit('run', cmd)"
            @mouseenter="selectedIndex = i"
          >
            <span class="command-palette__label">
              {{ cmd.label }}
              <!-- G-VSC-04: source badge for disambiguation. Native plugins
                   and VS Code extensions always show their source; built-in
                   commands show a badge only when their label collides. -->
              <span
                v-if="shouldShowSource(cmd)"
                class="command-palette__source-badge"
                :class="`command-palette__source-badge--${cmd.source ?? 'builtin'}`"
                >{{ sourceBadge(cmd) }}</span
              >
            </span>
            <span v-if="cmd.shortcut" class="command-palette__shortcut">{{
              cmd.shortcut
            }}</span>
          </button>
        </div>
      </div>
    </div>
  </transition>
</template>

<style scoped>
.command-palette-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background-color: rgba(0, 0, 0, 0.4);
  z-index: 1000;
  display: flex;
  justify-content: center;
  align-items: flex-start;
  padding-top: 80px;
}

.dialog-backdrop-button {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  margin: 0;
  padding: 0;
  border: 0;
  background: transparent;
  cursor: default;
}

.command-palette {
  position: relative;
  z-index: 1;
  width: 520px;
  max-width: 90vw;
  background-color: var(--color-bg-surface);
  border: 1px solid var(--color-border-default);
  border-radius: var(--radius-md);
  overflow: hidden;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
}

.command-palette__input {
  width: 100%;
  padding: 12px 16px;
  font-size: 14px;
  font-family: var(--font-sans);
  color: var(--color-text-primary);
  background-color: transparent;
  border: none;
  border-bottom: 1px solid var(--color-border-subtle);
  outline: none;
}

.command-palette__input::placeholder {
  color: var(--color-text-tertiary);
}

.command-palette__list {
  max-height: 320px;
  overflow-y: auto;
  padding: 4px;
}

.command-palette__empty {
  padding: 16px;
  font-size: 12px;
  color: var(--color-text-tertiary);
  text-align: center;
}

.command-palette__item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 8px 12px;
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  cursor: pointer;
  text-align: left;
  color: var(--color-text-primary);
  font-size: 13px;
  transition: background-color 80ms ease;
}

.command-palette__item--active {
  background-color: color-mix(in srgb, var(--color-primary) 12%, transparent);
}

.command-palette__item:disabled {
  cursor: default;
  color: var(--color-text-disabled);
  opacity: 0.65;
}

.command-palette__label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: 1;
  min-width: 0;
}

/* G-VSC-04: source badge for disambiguating native plugins vs VS Code
   extensions in the unified command palette. */
.command-palette__source-badge {
  flex-shrink: 0;
  display: inline-block;
  padding: 1px 5px;
  font-size: 10px;
  font-weight: 600;
  line-height: 1.4;
  letter-spacing: 0.02em;
  text-transform: uppercase;
  border-radius: var(--radius-sm);
  border: 1px solid var(--color-border-subtle, rgba(255, 255, 255, 0.12));
  color: var(--color-text-tertiary);
  background-color: var(
    --color-bg-surface-container,
    rgba(255, 255, 255, 0.04)
  );
}

.command-palette__source-badge--native {
  color: var(--color-success, #4caf50);
  border-color: color-mix(
    in srgb,
    var(--color-success, #4caf50) 40%,
    transparent
  );
}

.command-palette__source-badge--vscode {
  color: var(--color-primary, #4285f4);
  border-color: color-mix(
    in srgb,
    var(--color-primary, #4285f4) 40%,
    transparent
  );
}

.command-palette__source-badge--builtin {
  color: var(--color-text-tertiary);
}

.command-palette__shortcut {
  font-size: 11px;
  color: var(--color-text-tertiary);
  font-family: var(--font-mono);
}

.cmd-fade-enter-active,
.cmd-fade-leave-active {
  transition: opacity 120ms ease;
}

.cmd-fade-enter-from,
.cmd-fade-leave-to {
  opacity: 0;
}
</style>
