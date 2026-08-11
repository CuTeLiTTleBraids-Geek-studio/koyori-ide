<script lang="ts">
// Koyori IDE 组件 · Focus Trap Dialog。
// 喵，这是 Focus Trap Dialog，负责 Koyori IDE 的界面呈现喵~
import type { Ref } from "vue";

interface DialogController {
  root: Ref<HTMLElement | null>;
  focusBoundary: (reverse?: boolean) => void;
}

const dialogStack: DialogController[] = [];
</script>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useAttrs } from "vue";

defineOptions({ inheritAttrs: false });

const props = withDefaults(defineProps<{
  tag?: string;
  dialogRole?: "dialog" | "alertdialog";
}>(), {
  tag: "div",
  dialogRole: "dialog",
});

const emit = defineEmits<{
  close: [];
}>();

const attrs = useAttrs();
const root = ref<HTMLElement | null>(null);
let previouslyFocused: HTMLElement | null = document.activeElement instanceof HTMLElement
  ? document.activeElement
  : null;
let registeredWithDocument = false;

const controller: DialogController = {
  root,
  focusBoundary,
};

function isTopmostDialog(): boolean {
  return !registeredWithDocument || dialogStack[dialogStack.length - 1] === controller;
}

function focusableElements(): HTMLElement[] {
  if (!root.value) return [];
  return Array.from(
    root.value.querySelectorAll<HTMLElement>(
      'button, [href], input, select, textarea, [contenteditable="true"], [tabindex]:not([tabindex="-1"])',
    ),
  ).filter((element) =>
    !element.hasAttribute("disabled")
    && element.getAttribute("aria-disabled") !== "true"
    && element.getAttribute("aria-hidden") !== "true"
    && !element.hasAttribute("hidden")
    && !element.closest("[inert]")
    && element.tabIndex >= 0
    && !(element instanceof HTMLInputElement && element.type === "hidden")
  );
}

function focusBoundary(reverse = false): void {
  const focusable = focusableElements();
  const target = reverse
    ? focusable[focusable.length - 1]
    : focusable.find((element) => element.hasAttribute("autofocus")) ?? focusable[0];
  (target ?? root.value)?.focus();
}

function trapKeydown(event: KeyboardEvent): void {
  if (event.key === "Escape") {
    event.preventDefault();
    event.stopPropagation();
    emit("close");
    return;
  }
  if (event.key !== "Tab") return;

  const focusable = focusableElements();
  if (focusable.length === 0) {
    event.preventDefault();
    root.value?.focus();
    return;
  }

  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  const activeElement = document.activeElement;
  const focusIsOutside = !root.value?.contains(activeElement);
  if (event.shiftKey && (focusIsOutside || activeElement === first || activeElement === root.value)) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && (focusIsOutside || activeElement === last)) {
    event.preventDefault();
    first.focus();
  }
}

function handleKeydown(event: KeyboardEvent): void {
  if (!isTopmostDialog()) return;
  trapKeydown(event);
}

function handleDocumentKeydown(event: KeyboardEvent): void {
  if (!isTopmostDialog() || event.defaultPrevented) return;
  trapKeydown(event);
}

onMounted(() => {
  if (root.value?.isConnected) {
    dialogStack.push(controller);
    document.addEventListener("keydown", handleDocumentKeydown);
    registeredWithDocument = true;
  }
  void nextTick(() => {
    if (isTopmostDialog()) focusBoundary();
  });
});

onBeforeUnmount(() => {
  if (!registeredWithDocument) {
    if (previouslyFocused?.isConnected) previouslyFocused.focus();
    previouslyFocused = null;
    return;
  }

  document.removeEventListener("keydown", handleDocumentKeydown);
  const wasTopmost = isTopmostDialog();
  const index = dialogStack.lastIndexOf(controller);
  if (index >= 0) dialogStack.splice(index, 1);
  registeredWithDocument = false;

  if (wasTopmost) {
    const nextTopmost = dialogStack[dialogStack.length - 1];
    if (
      previouslyFocused?.isConnected
      && (!nextTopmost || nextTopmost.root.value?.contains(previouslyFocused))
    ) {
      previouslyFocused.focus();
    } else {
      nextTopmost?.focusBoundary();
    }
  }
  previouslyFocused = null;
});
</script>

<template>
  <component
    :is="tag"
    ref="root"
    v-bind="attrs"
    :role="props.dialogRole"
    aria-modal="true"
    tabindex="-1"
    @keydown="handleKeydown"
  >
    <slot />
  </component>
</template>
